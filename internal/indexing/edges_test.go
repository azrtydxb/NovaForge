package indexing_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/indexing"
)

// edgeFixture is a real repository behind the real git service, indexed by
// the real indexer into the real graph schema, and queried through the graph
// service's own RPCs — the path a person's Graph screen and an agent's
// repo.get_dependencies both take.
type edgeFixture struct {
	t      *testing.T
	orgID  uuid.UUID
	repoID uuid.UUID
	work   string
	idx    *indexing.Indexer
	graph  *graph.GRPCServer
	head   string
}

func newEdgeFixture(t *testing.T) *edgeFixture {
	t.Helper()
	gitClient, impl, root := realGitService(t)
	orgID := uuid.New()
	created, err := impl.CreateRepo(scopedCtx(orgID), &gitv1.CreateRepoRequest{Name: "edges-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	bare, err := gitops.Open(root, orgID, created.GetRepo().GetName())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	work := t.TempDir()
	gitCmd(t, "", "clone", "-q", bare.Path(), work)

	pool := graphPool(t)
	store := graph.NewStore(pool)
	return &edgeFixture{
		t:      t,
		orgID:  orgID,
		repoID: uuid.MustParse(created.GetRepo().GetId()),
		work:   work,
		head:   zeroSHA,
		idx: &indexing.Indexer{
			Git:        gitClient,
			Graph:      store,
			Vectors:    graph.NewVectorStore(pool),
			Embedder:   stubEmbedder{},
			HMACSecret: testHMACSecret,
		},
		graph: graph.NewGRPCServer(store, nil, nil, nil, nil, nil),
	}
}

// push commits files to main, pushes, and hands the resulting push event to
// the indexer exactly as the push stream would.
func (f *edgeFixture) push(files map[string]string, message string) string {
	f.t.Helper()
	sha := commitFiles(f.t, f.work, files, message)
	f.deliver("refs/heads/main", f.head, sha)
	f.head = sha
	return sha
}

func (f *edgeFixture) deliver(ref, old, sha string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := f.idx.HandlePush(ctx, events.PushEvent{
		OrgID: f.orgID, RepoID: f.repoID, Ref: ref, OldSHA: old, NewSHA: sha,
	}); err != nil {
		f.t.Fatalf("HandlePush %s %s: %v", ref, sha, err)
	}
}

func (f *edgeFixture) dependents(symbol string) []string {
	f.t.Helper()
	resp, err := f.graph.Dependents(scopedCtx(f.orgID), &graphv1.DependentsRequest{RepoId: f.repoID.String(), Symbol: symbol})
	if err != nil {
		f.t.Fatalf("Dependents(%s): %v", symbol, err)
	}
	return nodeLabels(resp.GetNodes())
}

func (f *edgeFixture) dependencies(symbol string) []string {
	f.t.Helper()
	resp, err := f.graph.Dependencies(scopedCtx(f.orgID), &graphv1.DependenciesRequest{RepoId: f.repoID.String(), Symbol: symbol})
	if err != nil {
		f.t.Fatalf("Dependencies(%s): %v", symbol, err)
	}
	return nodeLabels(resp.GetNodes())
}

func (f *edgeFixture) tests(symbol string) []string {
	f.t.Helper()
	resp, err := f.graph.TestsCovering(scopedCtx(f.orgID), &graphv1.TestsCoveringRequest{RepoId: f.repoID.String(), Symbol: symbol})
	if err != nil {
		f.t.Fatalf("TestsCovering(%s): %v", symbol, err)
	}
	return nodeLabels(resp.GetNodes())
}

func (f *edgeFixture) lastChanged(symbol string) *graphv1.LastChangedByResponse {
	f.t.Helper()
	resp, err := f.graph.LastChangedBy(scopedCtx(f.orgID), &graphv1.LastChangedByRequest{RepoId: f.repoID.String(), Symbol: symbol})
	if err != nil {
		f.t.Fatalf("LastChangedBy(%s): %v", symbol, err)
	}
	return resp
}

// nodeLabels renders nodes as "path#name" (or "path" for a file), sorted, so
// an assertion reads as the relation it checks.
func nodeLabels(nodes []*graphv1.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		label := n.GetAttrs()["path"]
		if name := n.GetAttrs()["name"]; name != "" {
			label += "#" + name
		}
		if label == "" {
			label = n.GetAttrs()["import_path"]
		}
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const (
	goMod      = "module example.com/shop\n\ngo 1.26\n"
	invoiceGo  = "package invoicing\n\n// Total sums the lines.\nfunc Total(lines []int) int {\n\tsum := 0\n\tfor _, l := range lines {\n\t\tsum += l\n\t}\n\treturn sum\n}\n\n// Subtotal is untouched by later commits.\nfunc Subtotal(a, b int) int { return a + b }\n"
	invoiceFix = "package invoicing\n\n// Total sums the lines, ignoring negatives.\nfunc Total(lines []int) int {\n\tsum := 0\n\tfor _, l := range lines {\n\t\tif l > 0 {\n\t\t\tsum += l\n\t\t}\n\t}\n\treturn sum\n}\n\n// Subtotal is untouched by later commits.\nfunc Subtotal(a, b int) int { return a + b }\n"
	invoiceTst = "package invoicing\n\nimport \"testing\"\n\nfunc TestTotal(t *testing.T) {\n\tif Total([]int{1, 2}) != 3 {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n"
	handlerGo  = "package api\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/shop/invoicing\"\n)\n\n// Handle renders an invoice total.\nfunc Handle(lines []int) string {\n\treturn fmt.Sprint(invoicing.Total(lines))\n}\n"
	handlerOff = "package api\n\nimport \"fmt\"\n\n// Handle no longer uses invoicing.\nfunc Handle(lines []int) string {\n\treturn fmt.Sprint(len(lines))\n}\n"
)

// TestPushWritesDependencyTestAndChangeEdges is S-14 and S-15 on a real
// repository: the graph answers, for a symbol, what depends on it, which tests
// cover it and which Work Item last changed it — because a push wrote those
// edges, not because a test inserted them.
//
// The indexer used to discard every reference Parse found and pass nil edges,
// so each of these queries answered empty for every repository ever pushed.
func TestPushWritesDependencyTestAndChangeEdges(t *testing.T) {
	f := newEdgeFixture(t)

	first := f.push(map[string]string{
		"go.mod":                    goMod,
		"invoicing/invoice.go":      invoiceGo,
		"invoicing/invoice_test.go": invoiceTst,
		"api/handler.go":            handlerGo,
		"README.md":                 "# shop\n",
	}, "NF-7: invoicing and its handler")

	if got := f.dependents("Total"); !equal(got, []string{"api/handler.go#Handle"}) {
		t.Fatalf("Dependents(Total) = %v, want [api/handler.go#Handle]", got)
	}
	if got := f.dependencies("Handle"); !equal(got, []string{"invoicing/invoice.go#Total"}) {
		t.Fatalf("Dependencies(Handle) = %v, want [invoicing/invoice.go#Total]", got)
	}
	if got := f.tests("Total"); !equal(got, []string{"invoicing/invoice_test.go#TestTotal"}) {
		t.Fatalf("TestsCovering(Total) = %v, want [invoicing/invoice_test.go#TestTotal]", got)
	}
	last := f.lastChanged("Total")
	if last.GetWorkItemKey() != "NF-7" || last.GetCommitSha() != first {
		t.Fatalf("LastChangedBy(Total) = %q at %s, want NF-7 at %s", last.GetWorkItemKey(), last.GetCommitSha(), first)
	}

	// A file importing a package depends on that package, whether or not the
	// graph can see into it (fmt is outside the repository).
	files, err := f.graph.FileRelations(scopedCtx(f.orgID), &graphv1.FileRelationsRequest{RepoId: f.repoID.String(), Path: "api/handler.go"})
	if err != nil {
		t.Fatalf("FileRelations(api/handler.go): %v", err)
	}
	if got := nodeLabels(files.GetImports()); !equal(got, []string{"example.com/shop/invoicing", "fmt"}) {
		t.Fatalf("imports of api/handler.go = %v, want [example.com/shop/invoicing fmt]", got)
	}
	importers, err := f.graph.FileRelations(scopedCtx(f.orgID), &graphv1.FileRelationsRequest{RepoId: f.repoID.String(), Path: "invoicing/invoice.go"})
	if err != nil {
		t.Fatalf("FileRelations(invoicing/invoice.go): %v", err)
	}
	if got := nodeLabels(importers.GetImportedBy()); !equal(got, []string{"api/handler.go"}) {
		t.Fatalf("importers of invoicing = %v, want [api/handler.go]", got)
	}
	if got := nodeLabels(importers.GetTests()); !equal(got, []string{"invoicing/invoice_test.go#TestTotal"}) {
		t.Fatalf("tests of invoicing/invoice.go = %v, want [invoicing/invoice_test.go#TestTotal]", got)
	}

	// Re-indexing the target file must not lose the edges that point into it
	// from files this push did not touch — its symbols are replaced, and the
	// old ones took their inbound edges with them.
	second := f.push(map[string]string{"invoicing/invoice.go": invoiceFix}, "NF-9: ignore negative lines")
	if got := f.dependents("Total"); !equal(got, []string{"api/handler.go#Handle"}) {
		t.Fatalf("after re-indexing invoice.go, Dependents(Total) = %v, want [api/handler.go#Handle]", got)
	}
	if got := f.tests("Total"); !equal(got, []string{"invoicing/invoice_test.go#TestTotal"}) {
		t.Fatalf("after re-indexing invoice.go, TestsCovering(Total) = %v, want [invoicing/invoice_test.go#TestTotal]", got)
	}
	if last := f.lastChanged("Total"); last.GetWorkItemKey() != "NF-9" || last.GetCommitSha() != second {
		t.Fatalf("LastChangedBy(Total) after the fix = %q at %s, want NF-9 at %s", last.GetWorkItemKey(), last.GetCommitSha(), second)
	}
	// Subtotal sits in the same file but not in the changed lines: it was
	// last changed by NF-7, and saying NF-9 would be inventing history.
	if last := f.lastChanged("Subtotal"); last.GetWorkItemKey() != "NF-7" {
		t.Fatalf("LastChangedBy(Subtotal) = %q, want NF-7 — only Total's lines changed in NF-9", last.GetWorkItemKey())
	}

	// A stale edge is worse than none: once the handler stops calling Total,
	// nothing may still claim it depends on it.
	f.push(map[string]string{"api/handler.go": handlerOff}, "NF-10: decouple the handler")
	if got := f.dependents("Total"); len(got) != 0 {
		t.Fatalf("after the handler stopped calling Total, Dependents(Total) = %v, want none", got)
	}
	gone, err := f.graph.FileRelations(scopedCtx(f.orgID), &graphv1.FileRelationsRequest{RepoId: f.repoID.String(), Path: "invoicing/invoice.go"})
	if err != nil {
		t.Fatalf("FileRelations after decoupling: %v", err)
	}
	if got := nodeLabels(gone.GetImportedBy()); len(got) != 0 {
		t.Fatalf("after the handler dropped its import, importers of invoicing = %v, want none", got)
	}
}

// TestIndexFollowsTheDefaultBranchOnly pins the index to what the default
// branch says. A push to any branch used to replace the indexed content of
// the files it touched, so a feature branch that deleted a function removed it
// from search, from the graph and from every agent's context, while main still
// had it.
func TestIndexFollowsTheDefaultBranchOnly(t *testing.T) {
	f := newEdgeFixture(t)
	f.push(map[string]string{"go.mod": goMod, "invoicing/invoice.go": invoiceGo}, "NF-7: invoicing")

	gitCmd(t, f.work, "checkout", "-q", "-b", "feature")
	for _, p := range []string{"invoicing/invoice.go"} {
		gitCmd(t, f.work, "rm", "-q", p)
	}
	gitCmd(t, f.work, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-q", "-m", "drop invoicing")
	gitCmd(t, f.work, "push", "-q", "origin", "HEAD:feature")
	featureSHA := gitCmd(t, f.work, "rev-parse", "HEAD")
	f.deliver("refs/heads/feature", f.head, featureSHA)

	if _, err := f.graph.GetSymbol(scopedCtx(f.orgID), &graphv1.GetSymbolRequest{RepoId: f.repoID.String(), Name: "Total"}); err != nil {
		t.Fatalf("after a push to a feature branch deleted invoice.go, GetSymbol(Total) = %v; main still defines it", err)
	}
}
