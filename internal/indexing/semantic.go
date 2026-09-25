package indexing

import (
	"context"
	"fmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/semanticindex"
)

// The controller reads the entire bounded pinned tree, including manifests and
// local replacements. A changed source file can change another file's resolution;
// semantic updates therefore never reuse partial per-file execution contexts.
func (idx *Indexer) indexSemantic(ctx context.Context, org, repo uuid.UUID, sha string) error {
	digest, err := idx.Semantic.ExecutionDigest()
	if err != nil {
		return err
	}
	delta, err := idx.diff(ctx, events.PushEvent{OrgID: org, RepoID: repo, OldSHA: emptyTreeSHA, NewSHA: sha})
	if err != nil {
		return err
	}
	if len(delta.GetChangedPaths()) > semanticindex.MaxFiles {
		return fmt.Errorf("semantic tree exceeds file limit")
	}
	s := semanticindex.Snapshot{OrgID: org, RepoID: repo, Revision: sha, RootURI: semanticindex.ProducerRoot, ExecutionDigest: digest, Files: map[string][]byte{}}
	total := 0
	for _, path := range delta.GetChangedPaths() {
		blob, err := idx.Git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repo.String(), Ref: sha, Path: path})
		if err != nil {
			return err
		}
		total += len(blob.GetContent())
		if total > semanticindex.MaxSourceBytes || len(blob.GetContent()) > semanticindex.MaxFileBytes {
			return fmt.Errorf("semantic tree exceeds source bound")
		}
		s.Files[path] = blob.GetContent()
	}
	if len(s.Files) == 0 {
		return fmt.Errorf("semantic snapshot has no source")
	}
	produced, err := idx.Semantic.Produce(ctx, s)
	if err != nil {
		return fmt.Errorf("semantic production: %w", err)
	}
	return idx.Graph.ReplaceSemanticIndex(ctx, produced)
}

func (idx *Indexer) indexDeclarations(ctx context.Context, org, repo uuid.UUID, sha string) error {
	blob, err := idx.Git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repo.String(), Ref: sha, Path: ".novaforge/graph.json"})
	if status.Code(err) == codes.NotFound {
		return idx.Graph.ReplaceDeclaredRelationships(ctx, org, repo, sha, nil)
	}
	if err != nil {
		return err
	}
	return idx.Graph.ReplaceDeclaredRelationships(ctx, org, repo, sha, blob.GetContent())
}
