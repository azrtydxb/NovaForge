// Package indexing (this file) turns push events into engineering-graph
// updates: it consumes events.StreamGitPush, fetches only the paths that
// changed, parses them with Parse, and replaces their symbol nodes and code
// chunks so the graph never drifts from what is actually on disk.
package indexing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// consumerGroup is the Redis Streams consumer group the indexer reads
// events.StreamGitPush under.
const consumerGroup = "indexer"

// claimInterval is how often Run reclaims messages that a previous consumer
// picked up but never acknowledged, so a crashed indexer pod cannot strand
// a push event forever.
const claimInterval = 30 * time.Second

// minIdle is how long a delivered-but-unacked message must sit before
// autoClaim will reclaim it from whichever consumer last had it.
const minIdle = 30 * time.Second

// gitClient is the subset of gitv1.GitServiceClient the indexer needs: the
// diff between two SHAs (to discover changed paths) and the blob content of
// a path at a SHA (to parse it, or to discover it no longer exists there).
//
// GetRepo names the repository's default branch, the only branch the index
// describes; ListCommits finds the commits a push brought, so each changed
// file is attributed to the commit that changed it.
type gitClient interface {
	GetDiff(ctx context.Context, in *gitv1.GetDiffRequest, opts ...grpc.CallOption) (*gitv1.GetDiffResponse, error)
	GetBlob(ctx context.Context, in *gitv1.GetBlobRequest, opts ...grpc.CallOption) (*gitv1.GetBlobResponse, error)
	GetRepo(ctx context.Context, in *gitv1.GetRepoRequest, opts ...grpc.CallOption) (*gitv1.GetRepoResponse, error)
	ListCommits(ctx context.Context, in *gitv1.ListCommitsRequest, opts ...grpc.CallOption) (*gitv1.ListCommitsResponse, error)
}

// Indexer keeps the engineering graph's symbol nodes and code chunks in
// sync with what was actually pushed.
type Indexer struct {
	RDB      *redis.Client
	Git      gitClient
	Graph    *graph.Store
	Vectors  *graph.VectorStore
	Embedder graph.Embedder

	// HMACSecret signs the service token the indexer presents to the git
	// service for the organization whose push it is indexing.
	HMACSecret string

	// Consumer names this indexer's Redis Streams consumer identity. A
	// process that leaves it empty gets a random one, which is fine for a
	// single replica but a production deployment with several replicas
	// should set one consumer name per pod.
	Consumer string
}

// Run consumes events.StreamGitPush in the "indexer" consumer group until
// ctx is cancelled, indexing each push's changed paths and acknowledging
// the message only once the handler returns nil — so a crash between
// indexing and XACK simply redelivers the same push, which IndexCommit
// handles idempotently.
func (idx *Indexer) Run(ctx context.Context) error {
	if err := events.EnsureGroup(ctx, idx.RDB, events.StreamGitPush, consumerGroup); err != nil {
		return fmt.Errorf("ensure consumer group: %w", err)
	}

	consumer := idx.Consumer
	if consumer == "" {
		consumer = "indexer-" + uuid.NewString()
	}

	ticker := time.NewTicker(claimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			idx.autoClaim(ctx, consumer)
			continue
		default:
		}

		res, err := idx.RDB.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    consumerGroup,
			Consumer: consumer,
			Streams:  []string{events.StreamGitPush, ">"},
			Count:    10,
			Block:    2 * time.Second,
		}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("indexing: XReadGroup: %v", err)
			continue
		}

		for _, stream := range res {
			for _, msg := range stream.Messages {
				if err := idx.handleMessage(ctx, msg); err != nil {
					log.Printf("indexing: handle message %s: %v", msg.ID, err)
					continue
				}
				if err := idx.RDB.XAck(ctx, events.StreamGitPush, consumerGroup, msg.ID).Err(); err != nil {
					log.Printf("indexing: XAck %s: %v", msg.ID, err)
				}
			}
		}
	}
}

// autoClaim reclaims messages idle for more than minIdle so a consumer that
// died mid-handler does not strand its push event forever, then processes
// and acknowledges each one exactly as the main loop does.
func (idx *Indexer) autoClaim(ctx context.Context, consumer string) {
	start := "0"
	for {
		msgs, next, err := idx.RDB.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   events.StreamGitPush,
			Group:    consumerGroup,
			Consumer: consumer,
			MinIdle:  minIdle,
			Start:    start,
			Count:    50,
		}).Result()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("indexing: XAutoClaim: %v", err)
			}
			return
		}
		for _, msg := range msgs {
			if err := idx.handleMessage(ctx, msg); err != nil {
				log.Printf("indexing: handle reclaimed message %s: %v", msg.ID, err)
				continue
			}
			if err := idx.RDB.XAck(ctx, events.StreamGitPush, consumerGroup, msg.ID).Err(); err != nil {
				log.Printf("indexing: XAck reclaimed %s: %v", msg.ID, err)
			}
		}
		if next == "0" || len(msgs) == 0 {
			return
		}
		start = next
	}
}

// handleMessage decodes one stream message as an events.PushEvent, resolves
// the paths that changed between its old and new SHA, and indexes them.
func (idx *Indexer) handleMessage(ctx context.Context, msg redis.XMessage) error {
	raw, ok := msg.Values["data"].(string)
	if !ok {
		return fmt.Errorf("message %s: missing string field \"data\"", msg.ID)
	}
	var evt events.PushEvent
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		return fmt.Errorf("unmarshal push event: %w", err)
	}
	return idx.HandlePush(ctx, evt)
}

// HandlePush indexes the paths one push changed. Run calls it for every
// message on the push stream; it is exported so the whole path from a push to
// a stored chunk can be driven against a real git service.
func (idx *Indexer) HandlePush(ctx context.Context, evt events.PushEvent) error {
	// A deleted ref has no commit to index. Returning an error would leave
	// the event unacknowledged and redelivered forever.
	if isZeroSHA(evt.NewSHA) {
		return nil
	}

	// The indexer reads the repository through the git service, which refuses
	// an anonymous caller — and must, or anyone could read any organization's
	// code. It called anonymously, so on the cluster every push failed here
	// and nothing was ever indexed; the in-process fake it was tested against
	// checked no credential at all. It acts for exactly the pushed-to org.
	tok, err := svcauth.Mint(idx.HMACSecret, "indexer", evt.OrgID, svcauth.DefaultTTL)
	if err != nil {
		return fmt.Errorf("mint service token for org %s: %w", evt.OrgID, err)
	}
	ctx = metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer "+tok,
		"x-novaforge-org", evt.OrgID.String(),
	)

	// The index describes one branch: the repository's default branch. It
	// used to follow every push, so a push to a feature branch replaced the
	// indexed content of the files it touched — a branch that deleted a
	// function removed it from search, from the graph and from every agent's
	// context while the default branch still had it. Indexing per ref instead
	// would multiply the index by every short-lived branch an agent creates,
	// for questions ("what depends on this", "what does this project look
	// like") that are asked of the default branch. Merges into it arrive as
	// pushes of it, so nothing that reaches the default branch is missed.
	repo, err := idx.Git.GetRepo(ctx, &gitv1.GetRepoRequest{Name: evt.RepoID.String()})
	if status.Code(err) == codes.NotFound {
		// A repository deleted since the push has nothing left to index.
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve default branch of %s: %w", evt.RepoID, err)
	}
	if evt.Ref != "refs/heads/"+repo.GetRepo().GetDefaultBranch() {
		return nil
	}

	lock, err := idx.lockRepository(ctx, evt.OrgID, evt.RepoID)
	if err != nil {
		return fmt.Errorf("lock index repository: %w", err)
	}
	defer releaseIndexLock(ctx, lock)
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: evt.OrgID, ActorKind: "service"})
	evt, full, done, err := idx.currentPush(ctx, evt)
	if err != nil || done {
		return err
	}
	unified, err := idx.diff(ctx, evt)
	if err != nil {
		return fmt.Errorf("compute changed paths for %s: %w", evt.NewSHA, err)
	}
	paths := parseDiffPaths(unified)
	if full {
		existing, err := idx.existingPaths(ctx, evt.OrgID, evt.RepoID)
		if err != nil {
			return fmt.Errorf("read paths for reconciliation: %w", err)
		}
		seen := make(map[string]bool, len(paths))
		for _, path := range paths {
			seen[path] = true
		}
		for _, path := range existing {
			if !seen[path] {
				paths = append(paths, path)
				seen[path] = true
			}
		}
	}
	info := pushInfo{
		module:  idx.goModule(ctx, evt.RepoID, evt.NewSHA),
		changed: changedLines(unified),
		commits: idx.attribute(ctx, evt.RepoID, evt.OldSHA, evt.NewSHA, paths),
	}
	// Files are replaced individually. Once any replacement starts, the old
	// checkpoint no longer describes the whole index. Invalidate it first so
	// a crash or a force-push back to that old SHA cannot make partial state
	// look complete. The next attempt then reconciles all current/known paths.
	if _, err := idx.Graph.Pool().Exec(ctx, `
		UPDATE graph.graph_nodes SET attrs = attrs - 'sha'
		WHERE org_id = $1 AND kind = 'commit' AND key = $2
	`, evt.OrgID, evt.RepoID.String()); err != nil {
		return fmt.Errorf("invalidate index checkpoint: %w", err)
	}
	_, err = idx.indexCommit(ctx, evt.OrgID, evt.RepoID, evt.NewSHA, paths, info)
	return err
}

// emptyTreeSHA is git's well-known id for the empty tree, which every
// repository can resolve whether or not it stores the object.
const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// isZeroSHA reports whether sha is git's "no commit" value (or absent).
func isZeroSHA(sha string) bool {
	return strings.Trim(sha, "0") == ""
}

var diffGitLineRe = regexp.MustCompile(`(?m)^diff --git a/(\S+) b/(\S+)$`)

// diff asks the git service for the unified diff between evt's old and new
// SHA.
func (idx *Indexer) diff(ctx context.Context, evt events.PushEvent) (string, error) {
	// A ref that did not exist before the push reports an all-zero old SHA,
	// which git cannot diff from: every repository's first push failed here,
	// unacknowledged, and was retried forever. Everything in such a commit is
	// new, which is exactly its diff against the empty tree.
	from := evt.OldSHA
	if isZeroSHA(from) {
		from = emptyTreeSHA
	}
	resp, err := idx.Git.GetDiff(ctx, &gitv1.GetDiffRequest{
		Repo: evt.RepoID.String(),
		From: from,
		To:   evt.NewSHA,
	})
	if err != nil {
		return "", err
	}
	return resp.GetUnified(), nil
}

// parseDiffPaths extracts every path named on a "diff --git a/X b/Y" header
// line of a unified diff, de-duplicated and in first-seen order.
func parseDiffPaths(unified string) []string {
	matches := diffGitLineRe.FindAllStringSubmatch(unified, -1)
	seen := make(map[string]bool, len(matches))
	paths := make([]string, 0, len(matches))
	for _, m := range matches {
		p := m[2]
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths
}

// IndexCommit indexes changedPaths as they exist at sha: each path's
// content is fetched through the git service and parsed, and its symbol
// nodes and code chunks replace whatever was previously indexed for that
// path. A path the git service reports missing at sha is treated as
// deleted: its symbols and chunks are removed rather than parsed. A path
// that fails to fetch or parse is skipped, with the error logged, so one
// bad file never blocks the rest of the commit from indexing. A partial
// attempt returns an error and does not advance the completed checkpoint,
// so the stream consumer retains it for retry.
//
// The SHA last indexed for repoID is recorded in the graph itself (as a
// "commit" node keyed by repoID), so a redelivery of an already-indexed SHA
// is detected up front and returns immediately without touching git, the
// graph, or the vector store again — making IndexCommit idempotent under
// the at-least-once delivery Redis Streams gives every consumer.
func (idx *Indexer) IndexCommit(ctx context.Context, orgID, repoID uuid.UUID, sha string, changedPaths []string) (indexed int, err error) {
	return idx.indexCommit(ctx, orgID, repoID, sha, changedPaths, pushInfo{})
}

func (idx *Indexer) indexCommit(ctx context.Context, orgID, repoID uuid.UUID, sha string, changedPaths []string, info pushInfo) (indexed int, err error) {
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorKind: "service"})

	last, err := idx.lastIndexedSHA(ctx, orgID, repoID)
	if err != nil {
		return 0, fmt.Errorf("read last indexed sha for repo %s: %w", repoID, err)
	}
	if last != "" && last == sha {
		return 0, nil
	}

	var failed []string
	for _, path := range changedPaths {
		if path == "" {
			continue
		}
		if idx.indexPath(ctx, orgID, repoID, sha, path, info) {
			indexed++
		} else {
			failed = append(failed, path)
		}
	}
	// Success here causes XACK. Recording the SHA despite a model, fetch
	// or storage failure both acknowledged lost work and made redelivery
	// skip it permanently. Keep healthy-file progress without claiming a
	// complete index until every requested path has succeeded.
	if len(failed) > 0 {
		return indexed, fmt.Errorf("index incomplete at %s: failed paths %s", sha, strings.Join(failed, ", "))
	}

	if err := idx.recordIndexedSHA(ctx, orgID, repoID, sha); err != nil {
		return indexed, fmt.Errorf("record indexed sha %s for repo %s: %w", sha, repoID, err)
	}
	return indexed, nil
}

// indexPath indexes (or, for a deleted file, de-indexes) a single path,
// logging and returning false on any failure so the caller can move on to
// the next path rather than aborting the whole commit.
func (idx *Indexer) indexPath(ctx context.Context, orgID, repoID uuid.UUID, sha, path string, info pushInfo) bool {
	blob, err := idx.Git.GetBlob(ctx, &gitv1.GetBlobRequest{
		Repo: repoID.String(),
		Ref:  sha,
		Path: path,
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return idx.deindexPath(ctx, orgID, repoID, path)
		}
		log.Printf("indexing: fetch blob %s@%s: %v", path, sha, err)
		return false
	}

	file, perr := ParseFile(path, blob.GetContent())
	if perr != nil {
		log.Printf("indexing: parse %s: %v", path, perr)
		return false
	}
	symbols := file.Symbols

	// Parse's references used to be discarded here and the file written
	// with nil edges, so every dependency, test and history query the graph
	// offers answered empty on every real repository.
	keys := make(map[*Symbol]string, len(symbols))
	nodes := make([]graph.Node, 0, len(symbols))
	for i := range symbols {
		sym := &symbols[i]
		keys[sym] = symbolKey(repoID, path, *sym)
		nodes = append(nodes, graph.Node{
			ID:    uuid.New(),
			OrgID: orgID,
			Kind:  "symbol",
			Key:   keys[sym],
			Attrs: map[string]string{
				"path":       path,
				"dir":        graphDir(path),
				"name":       sym.Name,
				"kind":       sym.Kind,
				"signature":  sym.Signature,
				"start_line": strconv.Itoa(sym.StartLine),
				"end_line":   strconv.Itoa(sym.EndLine),
			},
		})
	}
	fi := graph.FileIndex{OrgID: orgID, RepoID: repoID, Path: path, Symbols: nodes}
	if strings.HasSuffix(path, ".go") {
		fi.Imports, fi.References = goEdges(path, info.module, file, keys)
	}
	if commit, ok := info.commits[path]; ok {
		fi.Commit = &commit
		fi.Changed = symbolsTouched(symbols, keys, info.changed[path])
	}
	if err := idx.Graph.ReplaceFileIndex(ctx, fi); err != nil {
		log.Printf("indexing: replace graph for %s: %v", path, err)
		return false
	}

	chunks := chunksForFile(path, blob.GetContent(), symbols)
	if len(chunks) > 0 && idx.Embedder != nil {
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Text
		}
		vecs, err := idx.Embedder.Embed(ctx, texts)
		if err != nil {
			log.Printf("indexing: embed %s: %v", path, err)
			return false
		}
		for i := range chunks {
			if i < len(vecs) {
				chunks[i].Embedding = vecs[i]
			}
		}
	}
	if err := idx.Vectors.Upsert(ctx, orgID, repoID, path, chunks); err != nil {
		log.Printf("indexing: upsert chunks for %s: %v", path, err)
		return false
	}
	return true
}

// deindexPath removes path's symbol nodes and code chunks, for a path the
// git service reports as no longer existing at the SHA being indexed.
func (idx *Indexer) deindexPath(ctx context.Context, orgID, repoID uuid.UUID, path string) bool {
	if err := idx.Graph.RemoveFileIndex(ctx, orgID, repoID, path); err != nil {
		log.Printf("indexing: remove subgraph for deleted %s: %v", path, err)
		return false
	}
	if err := idx.Vectors.Upsert(ctx, orgID, repoID, path, nil); err != nil {
		log.Printf("indexing: remove chunks for deleted %s: %v", path, err)
		return false
	}
	return true
}

func symbolKey(repoID uuid.UUID, path string, sym Symbol) string {
	return repoID.String() + ":" + path + "#" + sym.Name + ":" + strconv.Itoa(sym.StartLine)
}

const (
	chunkWindowLines  = 60
	chunkOverlapLines = 10
)

// chunksForFile splits content into the Chunks that will be embedded and
// stored for path: one chunk per symbol span when symbols were found,
// otherwise overlapping fixed-size line windows, so a file with no
// recognised symbols is still searchable semantically.
func chunksForFile(path string, content []byte, symbols []Symbol) []graph.Chunk {
	text := string(content)
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}

	if len(symbols) > 0 {
		chunks := make([]graph.Chunk, 0, len(symbols))
		for _, sym := range symbols {
			start, end := sym.StartLine, sym.EndLine
			if start < 1 {
				start = 1
			}
			if end > len(lines) {
				end = len(lines)
			}
			if start > end {
				continue
			}
			chunks = append(chunks, graph.Chunk{
				ID:        uuid.New(),
				Path:      path,
				StartLine: start,
				EndLine:   end,
				Text:      strings.Join(lines[start-1:end], "\n"),
			})
		}
		return chunks
	}

	step := chunkWindowLines - chunkOverlapLines
	var chunks []graph.Chunk
	for start := 1; start <= len(lines); start += step {
		end := start + chunkWindowLines - 1
		if end > len(lines) {
			end = len(lines)
		}
		chunks = append(chunks, graph.Chunk{
			ID:        uuid.New(),
			Path:      path,
			StartLine: start,
			EndLine:   end,
			Text:      strings.Join(lines[start-1:end], "\n"),
		})
		if end == len(lines) {
			break
		}
	}
	return chunks
}

// lastIndexedSHA reads the SHA recorded for repoID's "commit" node, or ""
// if none has been indexed yet.
func (idx *Indexer) lastIndexedSHA(ctx context.Context, orgID, repoID uuid.UUID) (string, error) {
	var sha *string
	err := idx.Graph.Pool().QueryRow(ctx, `
		SELECT attrs->>'sha' FROM graph.graph_nodes
		WHERE org_id = $1 AND kind = 'commit' AND key = $2
	`, orgID, repoID.String()).Scan(&sha)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	if sha == nil {
		return "", nil
	}
	return *sha, nil
}

// recordIndexedSHA upserts repoID's "commit" node with sha, so a later
// redelivery of the same push can be detected and skipped immediately.
func (idx *Indexer) recordIndexedSHA(ctx context.Context, orgID, repoID uuid.UUID, sha string) error {
	_, err := idx.Graph.UpsertNode(ctx, graph.Node{
		ID:    uuid.New(),
		OrgID: orgID,
		Kind:  "commit",
		Key:   repoID.String(),
		Attrs: map[string]string{"sha": sha},
	})
	return err
}
