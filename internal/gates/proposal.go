package gates

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// Proposer turns a request to change a gate into an Engineering Run.
//
// A gate toggle is a policy change, and the obvious implementation — write
// the file on the default branch — would let anyone with a settings screen
// switch off the check that was about to stop their change, with no record
// anyone looked at it. So nothing here writes to the default branch. The
// modified file is committed to a branch of its own and opened as a run,
// which is reviewed and merged like any other change. The run's gates are
// then resolved from the default branch (see Resolve), where the gate being
// switched off is still on: a proposal cannot waive its own review.
type Proposer struct {
	Git     gitv1.GitServiceClient
	Reviews reviewsv1.ReviewsServiceClient
}

// GateState is one known gate as a ref declares it.
type GateState struct {
	Name     string
	Declared bool
	Enabled  bool
	Path     string
	Params   map[string]any
}

// GateChange is what a caller asks to change about one gate. A nil Enabled
// leaves the required flag alone; a nil Params leaves the params alone.
type GateChange struct {
	Repo    string
	Gate    string
	Enabled *bool
	Params  map[string]any
}

// Proposal is the run a GateChange became.
type Proposal struct {
	RunID     string
	RunNumber int32
	Branch    string
	CommitSHA string
	Title     string
}

// declaredFile is one parsed file under .novaforge/gates at a commit.
type declaredFile struct {
	path    string
	content []byte
	parsed  gateFile
}

// configAt reads every gate file at the repository's default branch, pinned
// to one commit so that the file a proposal modifies and the commit its
// branch starts from are the same snapshot.
func (p *Proposer) configAt(ctx context.Context, repo string) (r *gitv1.Repo, sha string, files map[string]declaredFile, err error) {
	resp, err := p.Git.GetRepo(ctx, &gitv1.GetRepoRequest{Name: repo})
	if err != nil {
		return nil, "", nil, statusFromErr(err, codes.Internal, "look up repository")
	}
	r = resp.GetRepo()
	defaultBranch := r.GetDefaultBranch()

	branches, err := p.Git.ListBranches(ctx, &gitv1.ListBranchesRequest{Repo: repo})
	if err != nil {
		return nil, "", nil, statusFromErr(err, codes.Internal, "list branches")
	}
	for _, b := range branches.GetRefs() {
		if b.GetName() == defaultBranch {
			sha = b.GetSha()
		}
	}
	files = map[string]declaredFile{}
	// A repository with no commits declares no gates. That is a true answer
	// for List; Propose refuses it, since there is nothing to branch from.
	if sha == "" {
		return r, "", files, nil
	}
	tree, err := p.Git.GetTree(ctx, &gitv1.GetTreeRequest{Repo: repo, Ref: sha, Path: gatesDir})
	if status.Code(err) == codes.NotFound {
		return r, sha, files, nil
	}
	if err != nil {
		return nil, "", nil, statusFromErr(err, codes.Internal, "list "+gatesDir)
	}
	for _, entry := range tree.GetEntries() {
		// The same filter Resolve applies: a file Resolve ignores is not
		// configuration, and presenting it as a gate would misstate policy.
		if entry.GetKind() != "blob" || !(strings.HasSuffix(entry.GetName(), ".yaml") || strings.HasSuffix(entry.GetName(), ".yml")) {
			continue
		}
		path := gatesDir + "/" + entry.GetName()
		blob, err := p.Git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repo, Ref: sha, Path: path})
		if err != nil {
			return nil, "", nil, statusFromErr(err, codes.Internal, "read "+path)
		}
		var gf gateFile
		if err := yaml.Unmarshal(blob.GetContent(), &gf); err != nil {
			return nil, "", nil, status.Errorf(codes.FailedPrecondition, "malformed gate config %s: %v", path, err)
		}
		if !knownGates[gf.Name] {
			return nil, "", nil, status.Errorf(codes.FailedPrecondition, "%s names unknown gate %q", path, gf.Name)
		}
		// Resolve would silently let the later file win. Editing one of two
		// files that both claim a gate would change nothing that is enforced,
		// so the ambiguity is reported instead of guessed through.
		if prev, dup := files[gf.Name]; dup {
			return nil, "", nil, status.Errorf(codes.FailedPrecondition,
				"gate %q is declared by both %s and %s", gf.Name, prev.path, path)
		}
		files[gf.Name] = declaredFile{path: path, content: blob.GetContent(), parsed: gf}
	}
	return r, sha, files, nil
}

// List reports every gate the platform knows, as the default branch
// declares it.
func (p *Proposer) List(ctx context.Context, repo string) (ref, sha string, out []GateState, err error) {
	// Reading configuration needs only an org scope: it is the same content
	// anyone who can read the repository already sees.
	if scope, err := authz.FromContext(ctx); err != nil || scope.OrgID == uuid.Nil {
		return "", "", nil, status.Error(codes.PermissionDenied, "no authorization scope for this call")
	}
	r, sha, files, err := p.configAt(ctx, repo)
	if err != nil {
		return "", "", nil, err
	}
	ref = r.GetDefaultBranch()
	for name := range knownGates {
		st := GateState{Name: name}
		if f, ok := files[name]; ok {
			st.Declared, st.Enabled, st.Path, st.Params = true, f.parsed.Required, f.path, f.parsed.Params
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return ref, sha, out, nil
}

// requireMember admits a person acting in an organization. Membership was
// verified when identity resolved the credential against the org; what is
// refused here is everything else. An agent is not offered this: an agent
// proposing to relax the gates that judge agents is the thing gates exist to
// prevent, and a service token names no person to review against.
func requireMember(ctx context.Context) (authz.Scope, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return authz.Scope{}, status.Error(codes.PermissionDenied, "no authorization scope for this call")
	}
	if scope.ActorKind != "user" || scope.ActorID == uuid.Nil {
		return authz.Scope{}, status.Errorf(codes.PermissionDenied, "only an organization member may propose a gate change, not a %q caller", scope.ActorKind)
	}
	return scope, nil
}

// Propose commits change to a new branch and opens a run from it to the
// default branch.
func (p *Proposer) Propose(ctx context.Context, change GateChange) (Proposal, error) {
	scope, err := requireMember(ctx)
	if err != nil {
		return Proposal{}, err
	}
	if !knownGates[change.Gate] {
		return Proposal{}, status.Errorf(codes.InvalidArgument, "unknown gate %q", change.Gate)
	}
	if change.Enabled == nil && change.Params == nil {
		return Proposal{}, status.Error(codes.InvalidArgument, "a proposal must change enabled, params, or both")
	}

	repo, sha, files, err := p.configAt(ctx, change.Repo)
	if err != nil {
		return Proposal{}, err
	}
	if sha == "" {
		return Proposal{}, status.Errorf(codes.FailedPrecondition,
			"repository %q has no commits on %q yet, so there is no branch to propose a gate change against", change.Repo, repo.GetDefaultBranch())
	}

	current, declared := files[change.Gate]
	path := gatesDir + "/" + change.Gate + ".yaml"
	if declared {
		path = current.path
	}
	updated, err := rewriteGateFile(current.content, declared, change)
	if err != nil {
		return Proposal{}, status.Errorf(codes.FailedPrecondition, "rewrite %s: %v", path, err)
	}

	// The rewritten file is read back through the same parser Resolve uses,
	// so what is proposed is exactly what would be enforced after merge.
	var check gateFile
	if err := yaml.Unmarshal(updated, &check); err != nil || check.Name != change.Gate {
		return Proposal{}, status.Errorf(codes.Internal, "rewritten %s does not parse back to gate %q: %v", path, change.Gate, err)
	}
	// Compared as enforced state, not bytes: an undeclared gate is simply
	// not required, so "disable" on it is as much a no-op as re-saving an
	// unchanged file, and neither may become a run someone has to review.
	wasEnabled := declared && current.parsed.Required
	if wasEnabled == check.Required && paramsEqual(current.parsed.Params, check.Params) {
		return Proposal{}, status.Errorf(codes.FailedPrecondition, "the %s gate is already configured that way", change.Gate)
	}

	title := proposalTitle(change.Gate, wasEnabled, check.Required)

	suffix, err := randomSuffix()
	if err != nil {
		return Proposal{}, status.Errorf(codes.Internal, "name proposal branch: %v", err)
	}
	branch := "gates/" + change.Gate + "-" + suffix

	// The branch starts at the commit the file was read from, not at
	// whatever the default branch has moved to since: the diff under review
	// is then exactly the one-file change, never someone else's commit.
	if _, err := p.Git.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: change.Repo, Name: branch, FromRef: sha}); err != nil {
		return Proposal{}, statusFromErr(err, codes.Internal, "create branch "+branch)
	}
	commit, err := p.Git.CreateCommit(ctx, &gitv1.CreateCommitRequest{
		Repo:    change.Repo,
		Branch:  branch,
		Message: title + "\n\nProposed through NovaForge. The default branch is unchanged until this run\nis reviewed and merged, and the run's own gates are resolved from the\ndefault branch, so this change cannot waive them for itself.\n",
		Files:   []*gitv1.FileChange{{Path: path, Content: updated}},
	})
	if err != nil {
		return Proposal{}, statusFromErr(err, codes.Internal, "commit gate change to "+branch)
	}

	run, err := p.Reviews.CreateRun(ctx, &reviewsv1.CreateRunRequest{
		RepoId:     repo.GetId(),
		Title:      title,
		SourceRef:  branch,
		TargetRef:  repo.GetDefaultBranch(),
		AuthorId:   scope.ActorID.String(),
		AuthorKind: "user",
	})
	if err != nil {
		return Proposal{}, statusFromErr(err, codes.Internal, "open run for "+branch)
	}
	return Proposal{
		RunID:     run.GetRun().GetId(),
		RunNumber: run.GetRun().GetNumber(),
		Branch:    branch,
		CommitSHA: commit.GetSha(),
		Title:     title,
	}, nil
}

// rewriteGateFile applies change to a gate file's YAML. An existing file is
// edited as a node tree rather than decoded and re-marshalled, so its
// comments and any keys this code does not know survive: a toggle that
// silently dropped the author's explanation of why a gate is configured the
// way it is would make the reviewed diff lie about its own size.
func rewriteGateFile(content []byte, declared bool, change GateChange) ([]byte, error) {
	var doc yaml.Node
	if declared {
		if err := yaml.Unmarshal(content, &doc); err != nil {
			return nil, err
		}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
		setKey(doc.Content[0], "name", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: change.Gate})
		setKey(doc.Content[0], "required", boolNode(false))
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("gate file is not a YAML mapping")
	}
	root := doc.Content[0]

	if change.Enabled != nil {
		setKey(root, "required", boolNode(*change.Enabled))
	}
	if change.Params != nil {
		var params yaml.Node
		if err := params.Encode(change.Params); err != nil {
			return nil, fmt.Errorf("encode params: %w", err)
		}
		setKey(root, "params", &params)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func setKey(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			// Keep a comment written on the value's line with the new value.
			value.LineComment = mapping.Content[i+1].LineComment
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func boolNode(v bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprintf("%t", v)}
}

func paramsEqual(a, b map[string]any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

func proposalTitle(gate string, wasEnabled, enabled bool) string {
	switch {
	case wasEnabled && !enabled:
		return "Disable the " + gate + " gate"
	case !wasEnabled && enabled:
		return "Enable the " + gate + " gate"
	default:
		return "Change the " + gate + " gate's parameters"
	}
}

func randomSuffix() (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
