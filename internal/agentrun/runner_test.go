package agentrun_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/ctxasm"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/knowledge"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/novaforge/novaforge/internal/tools"
	"github.com/novaforge/novaforge/internal/work"
)

const runnerTestSecret = "agentrun-runner-test-secret"

// recordingModel is the model double for these tests: it plays a script and
// keeps every request, so a test can read exactly what the model was told
// and offered — the brief is only evidence if it reached the model.
type recordingModel struct {
	mu     sync.Mutex
	script []stubResponse
	calls  []provider.Call
}

func (m *recordingModel) Generate(_ context.Context, call provider.Call) (*provider.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call)
	if len(m.calls) > len(m.script) {
		return nil, fmt.Errorf("recording model script exhausted after %d calls", len(m.script))
	}
	r := m.script[len(m.calls)-1]
	var content []provider.ContentPart
	finish := provider.FinishStop
	if r.text != "" {
		content = append(content, provider.TextPart{Text: r.text})
	}
	for _, tc := range r.toolCalls {
		content = append(content, tc)
		finish = provider.FinishToolCalls
	}
	if r.totalTokens == 0 {
		r.totalTokens = 1
	}
	return &provider.Response{Content: content, FinishReason: finish, Usage: provider.Usage{TotalTokens: r.totalTokens}}, nil
}

func (m *recordingModel) Stream(context.Context, provider.Call) (provider.StreamResponse, error) {
	return nil, fmt.Errorf("streaming is not used")
}
func (m *recordingModel) ModelID() string                     { return "recording" }
func (m *recordingModel) ProviderName() string                { return "test" }
func (m *recordingModel) Capabilities() provider.Capabilities { return provider.Capabilities{} }

// firstBrief is the text of the user message the model was opened with.
func (m *recordingModel) firstBrief(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		t.Fatal("the model was never called")
	}
	var b strings.Builder
	for _, msg := range m.calls[0].Messages {
		if msg.Role != provider.RoleUser {
			continue
		}
		for _, part := range msg.Content {
			if tp, ok := part.(provider.TextPart); ok {
				b.WriteString(tp.Text)
			}
		}
	}
	return b.String()
}

func (m *recordingModel) offeredTools(t *testing.T) []string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		t.Fatal("the model was never called")
	}
	var names []string
	for _, d := range m.calls[0].Tools {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	return names
}

// platform is the three services an agent run reads before its first model
// turn — git, work and the engineering graph — each the real implementation
// on real PostgreSQL, served over a real gRPC connection behind the platform's
// own credential interceptor, so a call that carries no credential is refused
// here exactly as on the cluster.
type platform struct {
	pool   *pgxpool.Pool
	gitSrv *gitops.Server
	root   string
	git    gitv1.GitServiceClient
	work   workv1.WorkServiceClient
	graph  graphv1.GraphServiceClient
	agents *agents.Store
	audit  *agents.AuditLog
}

func newPlatform(t *testing.T) *platform {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := database.MigrateAs(url, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform: %v", err)
	}
	if err := database.Migrate(url, "work", work.MigrationsFS); err != nil {
		t.Fatalf("migrate work: %v", err)
	}
	if err := database.Migrate(url, "graph", graph.MigrationsFS); err != nil {
		t.Fatalf("migrate graph: %v", err)
	}
	if err := database.Migrate(url, "knowledge", knowledge.MigrationsFS); err != nil {
		t.Fatalf("migrate knowledge: %v", err)
	}
	if err := database.Migrate(url, "agents", agents.MigrationsFS); err != nil {
		t.Fatalf("migrate agents: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	root := t.TempDir()
	gitSrv := gitops.NewGRPCServer(pool, root)
	workStore := work.NewStore(pool)
	graphStore := graph.NewStore(pool)
	vectors := graph.NewVectorStore(pool)
	knowledgeStore := knowledge.NewStore(pool)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	gitClient := gitv1.NewGitServiceClient(conn)

	srv := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, runnerTestSecret)))
	gitv1.RegisterGitServiceServer(srv, gitSrv)
	workv1.RegisterWorkServiceServer(srv, work.NewGRPCServer(workStore))
	graphv1.RegisterGraphServiceServer(srv, graph.NewGRPCServer(graphStore, vectors, knowledgeStore, workStore, nil,
		ctxasm.NewAssembleFunc(gitClient, graphStore, vectors, knowledgeStore, workStore, nil, runnerTestSecret)))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return &platform{
		pool:   pool,
		gitSrv: gitSrv,
		root:   root,
		git:    gitClient,
		work:   workv1.NewWorkServiceClient(conn),
		graph:  graphv1.NewGraphServiceClient(conn),
		agents: agents.NewStore(pool),
		audit:  agents.NewAuditLog(pool),
	}
}

// org is one organization on the platform, with the run identity an agent
// run in it presents.
type org struct {
	id  uuid.UUID
	ctx context.Context
}

func (p *platform) newOrg(t *testing.T) org {
	t.Helper()
	id := uuid.New()
	agentID := uuid.New()
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: id, ActorID: agentID, ActorKind: "agent"})
	ctx, err := agentrun.WithRunIdentity(ctx, runnerTestSecret, id, agentID, time.Hour)
	if err != nil {
		t.Fatalf("WithRunIdentity: %v", err)
	}
	return org{id: id, ctx: ctx}
}

// repo creates a repository and pushes files to its default branch with an
// unmodified git client.
func (p *platform) repo(t *testing.T, o org, files map[string]string) uuid.UUID {
	t.Helper()
	scoped := authz.WithScope(context.Background(), authz.Scope{OrgID: o.id, ActorID: uuid.New(), ActorKind: "user"})
	name := "run-" + uuid.NewString()[:8]
	created, err := p.gitSrv.CreateRepo(scoped, &gitv1.CreateRepoRequest{Name: name})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	p.push(t, o, name, files, "seed")
	return uuid.MustParse(created.GetRepo().GetId())
}

func (p *platform) push(t *testing.T, o org, name string, files map[string]string, message string) {
	t.Helper()
	bare := filepath.Join(p.root, o.id.String(), name+".git")
	work := t.TempDir()
	runGit(t, "", "clone", "-q", bare, work)
	for path, body := range files {
		full := filepath.Join(work, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, work, "add", "-A")
	runGit(t, work, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-q", "-m", message)
	runGit(t, work, "push", "-q", "origin", "HEAD:main")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// startRun creates a Work Item for repo, an agent, and a run of that agent on
// that item, the way StartRun does.
func (p *platform) startRun(t *testing.T, o org, repoID uuid.UUID, agentName, goal string, acceptance []string) agents.Run {
	t.Helper()
	item, err := p.work.CreateItem(o.ctx, &workv1.CreateItemRequest{RepoId: repoID.String(), Type: "feature", Goal: goal, Acceptance: acceptance})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	agent, err := p.agents.CreateAgent(o.ctx, agents.Agent{OrgID: o.id, Name: agentName, Role: "engineer", ModelRef: "deployment-default", Enabled: true})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	run, err := p.agents.CreateRun(o.ctx, agents.Run{
		OrgID: o.id, RepoID: repoID, AgentID: agent.ID, WorkItemID: uuid.MustParse(item.GetItem().GetId()),
		SponsorID: uuid.New(), GrantID: uuid.Nil, Branch: "agents/" + item.GetItem().GetKey() + "/work",
		WallclockLimit: time.Hour, TokenLimit: 1_000_000,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return run
}

// runner is agentrun.Runner as agent-runtime builds it, with the model double
// standing in for the gateway.
func (p *platform) runner(model *recordingModel, requested *[]string) *agentrun.Runner {
	return &agentrun.Runner{
		Git:   p.git,
		Work:  p.work,
		Graph: p.graph,
		Runs:  p.agents,
		Audit: p.audit,
		NewModel: func(name string) (provider.LanguageModel, error) {
			*requested = append(*requested, name)
			return model, nil
		},
	}
}

func (p *platform) runtimeFor(run agents.Run) tools.Runtime {
	return tools.Runtime{
		Work:      tools.NewWorkClient(p.work),
		Knowledge: tools.NewKnowledgeClient(p.graph, run.RepoID.String(), run.ID.String()),
	}
}

func toolCall(id, name string, args any) provider.ToolCallPart {
	raw, _ := json.Marshal(args)
	return provider.ToolCallPart{ID: id, Name: name, Args: raw}
}

// TestKnowledgeRecall is S-17: a decision an agent records in one run is in
// the context the model receives at the start of a later, related run.
//
// Runs never assembled context. The opening brief carried the run, work item
// and repository ids and nothing else, AssembleContext had no caller, and no
// tool let an agent record a decision at all — so nothing a run concluded
// could ever reach the next one.
func TestKnowledgeRecall(t *testing.T) {
	p := newPlatform(t)
	o := p.newOrg(t)
	repoID := p.repo(t, o, map[string]string{
		"billing/vat.go": "package billing\n\n// LineVAT returns the VAT on one invoice line, in cents.\nfunc LineVAT(cents int64) int64 { return cents * 21 / 100 }\n",
	})

	const decision = "Round VAT on every invoice line to whole cents before summing"
	first := p.startRun(t, o, repoID, "engineer-"+uuid.NewString()[:6], "Decide how VAT rounding works on invoices", nil)
	recorder := &recordingModel{script: []stubResponse{
		{toolCalls: []provider.ToolCallPart{toolCall("k1", "knowledge.record", map[string]string{
			"kind":  "decision",
			"title": "VAT is rounded per invoice line",
			"body":  decision + "; rounding only the invoice total drifted by a cent on multi-line invoices.",
		})}},
		{text: "Recorded the rounding decision."},
	}}
	var requested []string
	result := p.runner(recorder, &requested).Run(o.ctx, first, p.runtimeFor(first))
	if result.State != "succeeded" {
		t.Fatalf("first run ended %q (%s), want succeeded", result.State, result.Summary)
	}
	entries, err := p.audit.List(o.ctx, first.ID)
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	recorded := false
	for _, e := range entries {
		if e.Tool == "knowledge.record" && e.Outcome == "ok" {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("knowledge.record did not succeed in the first run: %+v", entries)
	}

	later := p.startRun(t, o, repoID, "engineer-"+uuid.NewString()[:6], "Add VAT lines to credit notes for refunded invoices", []string{"credit notes show VAT per line"})
	reader := &recordingModel{script: []stubResponse{{text: "nothing to do in this test"}}}
	p.runner(reader, &requested).Run(o.ctx, later, p.runtimeFor(later))

	brief := reader.firstBrief(t)
	if !strings.Contains(brief, decision) {
		t.Fatalf("the later run's opening brief does not carry the recorded decision %q:\n%s", decision, brief)
	}
	for _, want := range []string{"Add VAT lines to credit notes", "credit notes show VAT per line"} {
		if !strings.Contains(brief, want) {
			t.Fatalf("the brief does not carry the work item's %q:\n%s", want, brief)
		}
	}
	if len(brief) > agentrun.MaxBriefBytes {
		t.Fatalf("the brief is %d bytes, over its %d-byte bound", len(brief), agentrun.MaxBriefBytes)
	}

	// An unrelated run is not handed the decision just because it exists.
	unrelated := p.startRun(t, o, repoID, "engineer-"+uuid.NewString()[:6], "Rename the deployment health endpoint", nil)
	other := &recordingModel{script: []stubResponse{{text: "ok"}}}
	p.runner(other, &requested).Run(o.ctx, unrelated, p.runtimeFor(unrelated))
	if strings.Contains(other.firstBrief(t), decision) {
		t.Fatalf("an unrelated run's brief carries the VAT decision:\n%s", other.firstBrief(t))
	}
}

// TestRepoConfigGoverns is S-18's agent half: a repository's .novaforge
// configuration, read at the run's base ref, decides which tools the model is
// offered, which model runs and how much it may spend, and what project
// context it starts with — and a malformed configuration fails the run with
// the parse error recorded, rather than running with no configuration.
//
// repoconfig.Load had no production caller, so none of this was true: every
// run was offered every tool whatever the repository declared.
func TestRepoConfigGoverns(t *testing.T) {
	p := newPlatform(t)
	o := p.newOrg(t)
	agentName := "engineer-" + uuid.NewString()[:6]
	repoID := p.repo(t, o, map[string]string{
		".novaforge/project.yaml": "name: shop\ndescription: A shop whose money amounts are integer cents.\n",
		".novaforge/agents/engineer.yaml": "name: " + agentName + "\nrole: engineer\nmodel: repo-pinned-model\n" +
			"tools: [work.get, knowledge.record]\nbudget:\n  tokens: 5000\n",
		".novaforge/context/money.md": "# Money\n\nNever use float64 for money; amounts are int64 cents.\n",
		"README.md":                   "# shop\n",
	})

	run := p.startRun(t, o, repoID, agentName, "Add a discount to invoices", nil)
	model := &recordingModel{script: []stubResponse{
		// git.commit is not in the repository's tool list: the call must be
		// refused as a tool this run does not have.
		{toolCalls: []provider.ToolCallPart{toolCall("c1", "git.commit", map[string]any{"repo": repoID.String(), "branch": run.Branch, "message": "x", "files": map[string]string{"a": "b"}})}, totalTokens: 10},
		{text: "stopping", totalTokens: 10},
	}}
	var requested []string
	result := p.runner(model, &requested).Run(o.ctx, run, p.runtimeFor(run))
	if result.Model != model.ModelID() {
		t.Fatalf("effective model missing from result: %+v", result)
	}

	if got := model.offeredTools(t); strings.Join(got, ",") != "knowledge.record,work.get" {
		t.Fatalf("the model was offered %v, want exactly the repository's [knowledge.record work.get]", got)
	}
	if len(requested) != 1 || requested[0] != "repo-pinned-model" {
		t.Fatalf("the run asked for model %v, want the repository's repo-pinned-model", requested)
	}
	brief := model.firstBrief(t)
	for _, want := range []string{"integer cents", "Never use float64 for money", ".novaforge/context/money.md"} {
		if !strings.Contains(brief, want) {
			t.Fatalf("the brief does not carry the repository's context %q:\n%s", want, brief)
		}
	}
	entries, err := p.audit.List(o.ctx, run.ID)
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	refused := false
	for _, e := range entries {
		// The refusal is audited, as every refused call is; it must never
		// have been dispatched.
		if e.Tool == "git.commit" && (e.Outcome != "refused" || e.Error != "tool refused") {
			t.Fatalf("git.commit was audited as %q (%s), want refused as a tool this run does not have", e.Outcome, e.Error)
		}
	}
	for _, m := range model.calls[1].Messages {
		for _, part := range m.Content {
			if tr, ok := part.(provider.ToolResultPart); ok && tr.IsError && strings.Contains(fmt.Sprint(tr.Result), "git.commit") {
				refused = true
			}
		}
	}
	if !refused {
		t.Fatal("the model's git.commit call was not refused")
	}
	if result.State == "over_budget" {
		t.Fatalf("a 20-token run ended over budget under a 5000-token repository limit")
	}

	plan, err := agentrun.Prepare(o.ctx, agentrun.Services{Git: p.git, Work: p.work, Graph: p.graph}, run, agents.Agent{Name: agentName, Role: "engineer"})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if plan.TokenLimit != 5000 {
		t.Fatalf("plan token limit = %d, want the repository's 5000 (stricter than the run's 1000000)", plan.TokenLimit)
	}

	// A malformed agent definition fails the run loudly, and the reason is
	// on the record: a typo must never quietly hand an agent every tool.
	p.push(t, o, repoName(t, p, o, repoID), map[string]string{
		".novaforge/agents/engineer.yaml": "name: " + agentName + "\ntools: [work.get\n",
	}, "break the agent definition")
	broken := p.startRun(t, o, repoID, agentName+"-b", "Add a discount to credit notes", nil)
	never := &recordingModel{}
	failed := p.runner(never, &requested).Run(o.ctx, broken, p.runtimeFor(broken))
	if failed.State != "failed" {
		t.Fatalf("a run on a malformed configuration ended %q, want failed", failed.State)
	}
	if len(never.calls) != 0 {
		t.Fatalf("the model was called %d times for a run whose configuration does not parse", len(never.calls))
	}
	summaries, err := p.audit.List(o.ctx, broken.ID)
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	var summary string
	for _, e := range summaries {
		if e.Tool == "run.summary" {
			summary = string(e.ArgsJSON)
		}
	}
	if !strings.Contains(summary, "novaforge config") || !strings.Contains(summary, "engineer.yaml") {
		t.Fatalf("the failed run's recorded summary does not carry the parse error: %q", summary)
	}
}

func repoName(t *testing.T, p *platform, o org, repoID uuid.UUID) string {
	t.Helper()
	resp, err := p.git.GetRepo(o.ctx, &gitv1.GetRepoRequest{Name: repoID.String()})
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	return resp.GetRepo().GetName()
}

func TestRepositoryCostLimitRequiresEffectiveModelPrice(t *testing.T) {
	p := newPlatform(t)
	o := p.newOrg(t)
	name := "engineer-" + uuid.NewString()[:6]
	repo := p.repo(t, o, map[string]string{".novaforge/agents/engineer.yaml": "name: " + name + "\nmodel: unpriced-override\nbudget:\n  cost_micros: 1\n"})
	run := p.startRun(t, o, repo, name, "bounded run", nil)
	model := &recordingModel{script: []stubResponse{{text: "done"}}}
	var requested []string
	runner := p.runner(model, &requested)
	runner.Price = &agents.TokenPrice{InputMicrosPerMillion: 1000000, OutputMicrosPerMillion: 1000000}
	result := runner.Run(o.ctx, run, p.runtimeFor(run))
	if result.State != "failed" || !strings.Contains(result.Summary, "price") || len(model.calls) != 0 {
		t.Fatalf("unpriced override was executed: %+v calls=%d", result, len(model.calls))
	}
}

func TestRepositoryModelPriceIsResolved(t *testing.T) {
	p := newPlatform(t)
	o := p.newOrg(t)
	name := "engineer-" + uuid.NewString()[:6]
	repo := p.repo(t, o, map[string]string{".novaforge/agents/engineer.yaml": "name: " + name + "\nmodel: priced-override\nbudget:\n  cost_micros: 1\n"})
	run := p.startRun(t, o, repo, name, "bounded run", nil)
	model := &hangingModel{stubModel: &stubModel{script: []stubResponse{{text: "done"}}}, usage: provider.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}
	runner := &agentrun.Runner{Git: p.git, Work: p.work, Runs: p.agents, Audit: p.audit,
		NewModel: func(string) (provider.LanguageModel, error) { return model, nil },
		PriceForModel: func(model string) *agents.TokenPrice {
			if model != "priced-override" {
				t.Fatalf("price for %q", model)
			}
			return &agents.TokenPrice{InputMicrosPerMillion: 1000000, OutputMicrosPerMillion: 2000000}
		}}
	result := runner.Run(o.ctx, run, p.runtimeFor(run))
	if result.State != "over_budget" || result.CostMicros != 4 {
		t.Fatalf("wrong effective price: %+v", result)
	}
}
