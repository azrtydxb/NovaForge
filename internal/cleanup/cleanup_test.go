package cleanup_test

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/approvals"
	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/cleanup"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/gates"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/knowledge"
	"github.com/novaforge/novaforge/internal/mcp"
	"github.com/novaforge/novaforge/internal/retention"
	"github.com/novaforge/novaforge/internal/reviews"
	"github.com/novaforge/novaforge/internal/secrets"
	"github.com/novaforge/novaforge/internal/work"
)

func dbURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return u
}

// migrate applies a schema's migrations. The dev database is shared by every
// worktree, so it can be ahead of this checkout — another branch has added a
// migration this one does not have — and golang-migrate then reports the
// database's version as missing. The tables this test uses exist either way;
// that one condition is reported and tolerated, and every other error fails.
func migrate(t *testing.T, schema string, fsys fs.FS) {
	t.Helper()
	if err := database.Migrate(dbURL(t), schema, fsys); err != nil {
		if strings.Contains(err.Error(), "no migration found for version") {
			t.Logf("schema %s is ahead of this checkout in the shared dev database: %v", schema, err)
			return
		}
		t.Fatalf("migrate %s: %v", schema, err)
	}
}

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	p, err := database.Connect(context.Background(), dbURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func rdb(t *testing.T) *redis.Client {
	t.Helper()
	u := os.Getenv("TEST_REDIS_URL")
	if u == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(u)
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opts)
	t.Cleanup(func() { c.Close() })
	return c
}

func exec(t *testing.T, p *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := p.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func count(t *testing.T, p *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func seedWork(t *testing.T, p *pgxpool.Pool, org, repo uuid.UUID) (uuid.UUID, uuid.UUID) {
	t.Helper()
	parent, child := uuid.New(), uuid.New()
	seq := time.Now().UnixNano() % 1_000_000_000
	exec(t, p, `INSERT INTO work.work_items (id, org_id, repo_id, seq, key, type, goal) VALUES ($1,$2,$3,$4,$5,'feature','epic')`,
		parent, org, repo, seq, "NF-"+uuid.NewString()[:8])
	exec(t, p, `INSERT INTO work.work_items (id, org_id, repo_id, seq, key, type, goal, parent_id) VALUES ($1,$2,$3,$4,$5,'feature','sub',$6)`,
		child, org, repo, seq+1, "NF-"+uuid.NewString()[:8], parent)
	exec(t, p, `INSERT INTO work.work_item_comments (org_id, work_item_id, author_id, author_kind, body) VALUES ($1,$2,$3,'user','hello')`,
		org, child, uuid.New())
	exec(t, p, `INSERT INTO work.maintenance_proposals (org_id, repo_id, fingerprint, work_item_id) VALUES ($1,$2,$3,$4)`,
		org, repo, uuid.NewString(), parent)
	run := uuid.New()
	exec(t, p, `INSERT INTO reviews.runs (id, org_id, repo_id, number, title, source_ref, target_ref, author_id, author_kind)
		VALUES ($1,$2,$3,1,'t','feature','main',$4,'user')`, run, org, repo, uuid.New())
	exec(t, p, `INSERT INTO reviews.run_plan_steps (run_id, ordinal, text) VALUES ($1, 1, 'step')`, run)
	return parent, run
}

// TestWorkReviewsPurgesADeletedRepository covers work-reviews' consumer, run
// through the real stream: a deleted repository's Work Items (an epic and its
// subtask, a comment, a maintenance proposal) and its Engineering Runs go; the
// runs are named on the runs stream first, and the gates consumer removes
// their evaluations from that. The same organization's other repository and
// another organization are untouched.
func TestWorkReviewsPurgesADeletedRepository(t *testing.T) {
	migrate(t, "work", work.MigrationsFS)
	migrate(t, "reviews", reviews.MigrationsFS)
	migrate(t, "gates", gates.MigrationsFS)
	migrate(t, "approvals", approvals.MigrationsFS)
	migrate(t, "secrets", secrets.MigrationsFS)
	p := pool(t)
	r := rdb(t)
	org, other := uuid.New(), uuid.New()
	doomedRepo, keptRepo, otherRepo := uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() {
		for _, o := range []uuid.UUID{org, other} {
			_, _ = p.Exec(context.Background(), `UPDATE work.work_items SET parent_id = NULL WHERE org_id = $1`, o)
			_, _ = p.Exec(context.Background(), `DELETE FROM work.work_items WHERE org_id = $1`, o)
			_, _ = p.Exec(context.Background(), `DELETE FROM reviews.runs WHERE org_id = $1`, o)
			_, _ = p.Exec(context.Background(), `DELETE FROM gates.gate_evaluations WHERE org_id = $1`, o)
		}
	})
	_, doomedRun := seedWork(t, p, org, doomedRepo)
	_, keptRun := seedWork(t, p, org, keptRepo)
	seedWork(t, p, other, otherRepo)
	for _, run := range []uuid.UUID{doomedRun, keptRun} {
		exec(t, p, `INSERT INTO gates.gate_evaluations (id, org_id, run_id, gate, status, target_sha) VALUES ($1,$2,$3,'tests','pass','abc')`,
			uuid.New(), org, run)
	}

	suffix := uuid.NewString()
	repoStream, runsStream := "stream:test:repo-deleted:"+suffix, "stream:test:runs-deleted:"+suffix
	t.Cleanup(func() { r.Del(context.Background(), repoStream, runsStream) })
	publishRuns := func(ctx context.Context, e events.RunsDeletedEvent) error {
		return events.Publish(ctx, r, runsStream, e)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	wr := cleanup.WorkReviews(work.NewStore(p), reviews.NewStore(p), publishRuns)
	wr.RepoStream = repoStream
	wr.Run(ctx, r, "test")
	g := cleanup.Gates(&gates.Purger{Pool: p})
	g.RunsStream = runsStream
	g.OrgStream = "stream:test:unused:" + suffix
	g.Run(ctx, r, "test")

	if err := events.Publish(ctx, r, repoStream, events.RepoDeletedEvent{OrgID: org, RepoID: doomedRepo, RepoName: "gone", At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, ctx, "the deleted repository's Work Items, runs and gate evaluations to go", func() bool {
		return count(t, p, `SELECT count(*) FROM work.work_items WHERE org_id = $1 AND repo_id = $2`, org, doomedRepo) == 0 &&
			count(t, p, `SELECT count(*) FROM reviews.runs WHERE org_id = $1 AND repo_id = $2`, org, doomedRepo) == 0 &&
			count(t, p, `SELECT count(*) FROM gates.gate_evaluations WHERE run_id = $1`, doomedRun) == 0
	})
	if n := count(t, p, `SELECT count(*) FROM work.maintenance_proposals WHERE org_id = $1 AND repo_id = $2`, org, doomedRepo); n != 0 {
		t.Fatalf("%d maintenance proposal(s) of the deleted repository left", n)
	}
	if n := count(t, p, `SELECT count(*) FROM work.work_items WHERE org_id = $1 AND repo_id = $2`, org, keptRepo); n != 2 {
		t.Fatalf("the organization's other repository has %d Work Items, want 2", n)
	}
	if n := count(t, p, `SELECT count(*) FROM gates.gate_evaluations WHERE run_id = $1`, keptRun); n != 1 {
		t.Fatalf("the other repository's gate evaluation was removed (%d left)", n)
	}
	if n := count(t, p, `SELECT count(*) FROM work.work_items WHERE org_id = $1`, other); n != 2 {
		t.Fatalf("another organization lost Work Items (%d left, want 2)", n)
	}
}

func waitFor(t *testing.T, ctx context.Context, what string, cond func() bool) {
	t.Helper()
	for !cond() {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func blobs(t *testing.T) *blobstore.Client {
	t.Helper()
	ep := os.Getenv("TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("TEST_S3_ENDPOINT not set")
	}
	c, err := blobstore.New(context.Background(), blobstore.Options{
		Endpoint: ep, AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("TEST_S3_SECRET_KEY"),
		Bucket: "novaforge-test-cleanup",
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func seedCI(t *testing.T, p *pgxpool.Pool, sink *ci.LogSink, store *blobstore.Client, org, repo uuid.UUID) (uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	run, job := uuid.New(), uuid.New()
	exec(t, p, `INSERT INTO ci.workflow_runs (id, org_id, repo_id, commit_sha, ref) VALUES ($1,$2,$3,$4,'refs/heads/main')`, run, org, repo, uuid.NewString())
	exec(t, p, `INSERT INTO ci.workflow_jobs (id, run_id, name) VALUES ($1,$2,'build')`, job, run)
	key := "artifacts/" + org.String() + "/" + job.String() + "/report.txt"
	if err := store.Put(ctx, key, bytes.NewReader([]byte("body")), 4, "text/plain"); err != nil {
		t.Fatal(err)
	}
	exec(t, p, `INSERT INTO ci.artifacts (id, org_id, job_id, name, size_bytes, object_key) VALUES ($1,$2,$3,'report.txt',4,$4)`, uuid.New(), org, job, key)
	if err := sink.Append(ctx, job, "a line"); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Seal(ctx, job); err != nil {
		t.Fatal(err)
	}
	return job, key
}

// TestCIPurgesObjectsNotOnlyRows covers ci-runner's consumer: a deleted
// repository's runs, jobs and artifact rows go, and so do the artifact objects
// and sealed logs in object storage — which a row delete alone would have left
// to be stored forever. The organization's deletion also removes its runners.
func TestCIPurgesObjectsNotOnlyRows(t *testing.T) {
	migrate(t, "ci", ci.MigrationsFS)
	migrate(t, "retention", retention.MigrationsFS)
	p := pool(t)
	r := rdb(t)
	b := blobs(t)
	sink := ci.NewLogSink(r, b)
	h := cleanup.CI(&ci.Purger{Pool: p, Logs: sink, Blobs: b})

	org := uuid.New()
	doomedRepo, keptRepo := uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = p.Exec(context.Background(), `DELETE FROM ci.workflow_runs WHERE org_id = $1`, org)
		_, _ = p.Exec(context.Background(), `DELETE FROM ci.runners WHERE org_id = $1`, org)
	})
	doomedJob, doomedKey := seedCI(t, p, sink, b, org, doomedRepo)
	keptJob, keptKey := seedCI(t, p, sink, b, org, keptRepo)
	exec(t, p, `INSERT INTO ci.runners (id, org_id, name, token_hash) VALUES ($1,$2,'r',$3)`, uuid.New(), org, []byte(uuid.NewString()))

	ctx := context.Background()
	if err := h.RepoDeleted(ctx, events.RepoDeletedEvent{OrgID: org, RepoID: doomedRepo}); err != nil {
		t.Fatalf("RepoDeleted: %v", err)
	}
	if n := count(t, p, `SELECT count(*) FROM ci.workflow_runs WHERE org_id = $1 AND repo_id = $2`, org, doomedRepo); n != 0 {
		t.Fatalf("%d run(s) of the deleted repository left", n)
	}
	if _, err := b.Get(ctx, doomedKey); err == nil {
		t.Fatal("the deleted repository's artifact object is still in object storage")
	}
	if _, err := b.Get(ctx, "logs/"+doomedJob.String()+".txt"); err == nil {
		t.Fatal("the deleted repository's sealed log is still in object storage")
	}
	if _, err := b.Get(ctx, keptKey); err != nil {
		t.Fatalf("the other repository's artifact was removed: %v", err)
	}
	if _, err := b.Get(ctx, "logs/"+keptJob.String()+".txt"); err != nil {
		t.Fatalf("the other repository's log was removed: %v", err)
	}
	// Repeating a purge is harmless: an event can be delivered twice.
	if err := h.RepoDeleted(ctx, events.RepoDeletedEvent{OrgID: org, RepoID: doomedRepo}); err != nil {
		t.Fatalf("repeated RepoDeleted: %v", err)
	}

	if err := h.OrgDeleted(ctx, events.OrgDeletedEvent{OrgID: org}); err != nil {
		t.Fatalf("OrgDeleted: %v", err)
	}
	if n := count(t, p, `SELECT count(*) FROM ci.workflow_runs WHERE org_id = $1`, org); n != 0 {
		t.Fatalf("%d run(s) of the deleted organization left", n)
	}
	if n := count(t, p, `SELECT count(*) FROM ci.runners WHERE org_id = $1`, org); n != 0 {
		t.Fatalf("%d runner(s) of the deleted organization left", n)
	}
	if _, err := b.Get(ctx, keptKey); err == nil {
		t.Fatal("the deleted organization's artifact object is still in object storage")
	}
}

// TestEngineeringGraphPurgesIndexAndKnowledge covers engineering-graph's
// consumer: a deleted repository's graph nodes, code chunks and knowledge
// entries go — a deleted repository's code otherwise went on answering
// searches — and nothing of the organization's other repository.
func TestEngineeringGraphPurgesIndexAndKnowledge(t *testing.T) {
	migrate(t, "graph", graph.MigrationsFS)
	migrate(t, "knowledge", knowledge.MigrationsFS)
	p := pool(t)
	h := cleanup.EngineeringGraph(graph.NewStore(p), knowledge.NewStore(p))
	org := uuid.New()
	doomed, kept := uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = p.Exec(context.Background(), `DELETE FROM graph.graph_nodes WHERE org_id = $1`, org)
		_, _ = p.Exec(context.Background(), `DELETE FROM graph.code_chunks WHERE org_id = $1`, org)
		_, _ = p.Exec(context.Background(), `UPDATE knowledge.knowledge_entries SET superseded_by = NULL WHERE org_id = $1`, org)
		_, _ = p.Exec(context.Background(), `DELETE FROM knowledge.knowledge_entries WHERE org_id = $1`, org)
	})
	for _, repo := range []uuid.UUID{doomed, kept} {
		a, b := uuid.New(), uuid.New()
		exec(t, p, `INSERT INTO graph.graph_nodes (id, org_id, repo_id, kind, key) VALUES ($1,$2,$3,'symbol',$4)`, a, org, repo, uuid.NewString())
		exec(t, p, `INSERT INTO graph.graph_nodes (id, org_id, repo_id, kind, key) VALUES ($1,$2,$3,'file',$4)`, b, org, repo, uuid.NewString())
		exec(t, p, `INSERT INTO graph.graph_edges (from_id, to_id, kind) VALUES ($1,$2,'depends_on')`, a, b)
		exec(t, p, `INSERT INTO graph.code_chunks (id, org_id, repo_id, path, start_line, end_line, text, embedding)
			VALUES ($1,$2,$3,'a.go',1,2,'func A()', array_fill(0, ARRAY[1024])::vector)`, uuid.New(), org, repo)
		old, newer := uuid.New(), uuid.New()
		exec(t, p, `INSERT INTO knowledge.knowledge_entries (id, org_id, repo_id, key, kind, title, body) VALUES ($1,$2,$3,$4,'decision','t','b')`, newer, org, repo, uuid.NewString())
		exec(t, p, `INSERT INTO knowledge.knowledge_entries (id, org_id, repo_id, key, kind, title, body, superseded_by) VALUES ($1,$2,$3,$4,'decision','t','b',$5)`, old, org, repo, uuid.NewString(), newer)
	}

	if err := h.RepoDeleted(context.Background(), events.RepoDeletedEvent{OrgID: org, RepoID: doomed}); err != nil {
		t.Fatalf("RepoDeleted: %v", err)
	}
	for _, table := range []string{"graph.graph_nodes", "graph.code_chunks", "knowledge.knowledge_entries"} {
		if n := count(t, p, `SELECT count(*) FROM `+table+` WHERE org_id = $1 AND repo_id = $2`, org, doomed); n != 0 {
			t.Fatalf("%d row(s) of the deleted repository left in %s", n, table)
		}
		if n := count(t, p, `SELECT count(*) FROM `+table+` WHERE org_id = $1 AND repo_id = $2`, org, kept); n == 0 {
			t.Fatalf("the other repository's rows in %s were removed", table)
		}
	}
	if err := h.OrgDeleted(context.Background(), events.OrgDeletedEvent{OrgID: org}); err != nil {
		t.Fatalf("OrgDeleted: %v", err)
	}
	for _, table := range []string{"graph.graph_nodes", "graph.code_chunks", "knowledge.knowledge_entries"} {
		if n := count(t, p, `SELECT count(*) FROM `+table+` WHERE org_id = $1`, org); n != 0 {
			t.Fatalf("%d row(s) of the deleted organization left in %s", n, table)
		}
	}
}

// TestAgentRuntimeCancelsThenPurgesRuns covers agent-runtime's consumer: a
// deleted repository's Agent Runs — one of them still running — and their tool
// calls go, their ids are announced for the gates service first, and an
// organization's deletion removes its agents too.
func TestAgentRuntimeCancelsThenPurgesRuns(t *testing.T) {
	migrate(t, "agents", agents.MigrationsFS)
	p := pool(t)
	var announced []events.RunsDeletedEvent
	h := cleanup.AgentRuntime(agents.NewStore(p), func(_ context.Context, e events.RunsDeletedEvent) error {
		announced = append(announced, e)
		return nil
	})
	org := uuid.New()
	doomed, kept := uuid.New(), uuid.New()
	agent := uuid.New()
	t.Cleanup(func() {
		_, _ = p.Exec(context.Background(), `DELETE FROM agents.agent_runs WHERE org_id = $1`, org)
		_, _ = p.Exec(context.Background(), `DELETE FROM agents.agents WHERE org_id = $1`, org)
	})
	exec(t, p, `INSERT INTO agents.agents (id, org_id, name, role, model_ref) VALUES ($1,$2,$3,'coder','m')`, agent, org, "a-"+uuid.NewString()[:8])
	var doomedRun uuid.UUID
	for _, repo := range []uuid.UUID{doomed, kept} {
		run := uuid.New()
		exec(t, p, `INSERT INTO agents.agent_runs (id, org_id, agent_id, sponsor_id, grant_id, branch, repo_id, state)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'running')`, run, org, agent, uuid.New(), uuid.New(), "agents/"+uuid.NewString()[:6]+"/work", repo)
		exec(t, p, `INSERT INTO agents.tool_calls (id, run_id, org_id, tool, args_json) VALUES ($1,$2,$3,'git.commit','{}')`, uuid.New(), run, org)
		if repo == doomed {
			doomedRun = run
		}
	}

	if err := h.RepoDeleted(context.Background(), events.RepoDeletedEvent{OrgID: org, RepoID: doomed}); err != nil {
		t.Fatalf("RepoDeleted: %v", err)
	}
	if len(announced) != 1 || announced[0].Kind != "agent" || len(announced[0].RunIDs) != 1 || announced[0].RunIDs[0] != doomedRun {
		t.Fatalf("announced = %+v, want the deleted repository's one run", announced)
	}
	if n := count(t, p, `SELECT count(*) FROM agents.agent_runs WHERE org_id = $1 AND repo_id = $2`, org, doomed); n != 0 {
		t.Fatalf("%d run(s) of the deleted repository left", n)
	}
	if n := count(t, p, `SELECT count(*) FROM agents.tool_calls WHERE run_id = $1`, doomedRun); n != 0 {
		t.Fatalf("%d tool call(s) of a deleted run left", n)
	}
	if n := count(t, p, `SELECT count(*) FROM agents.agent_runs WHERE org_id = $1 AND repo_id = $2 AND state = 'running'`, org, kept); n != 1 {
		t.Fatalf("the other repository's running run was touched (%d still running)", n)
	}
	if err := h.OrgDeleted(context.Background(), events.OrgDeletedEvent{OrgID: org}); err != nil {
		t.Fatalf("OrgDeleted: %v", err)
	}
	if n := count(t, p, `SELECT count(*) FROM agents.agents WHERE org_id = $1`, org); n != 0 {
		t.Fatalf("%d agent(s) of the deleted organization left", n)
	}
}

// TestGatesAndMCPPurgeAnOrganization covers the two consumers whose share is
// organization-wide only: gate evaluations, approvals, leases and secret
// values; and registered MCP servers. Another organization keeps its own.
func TestGatesAndMCPPurgeAnOrganization(t *testing.T) {
	migrate(t, "gates", gates.MigrationsFS)
	migrate(t, "approvals", approvals.MigrationsFS)
	migrate(t, "secrets", secrets.MigrationsFS)
	migrate(t, "mcp", mcp.MigrationsFS)
	migrate(t, "gitplatform", capability.MigrationsFS)
	p := pool(t)
	org, other := uuid.New(), uuid.New()
	t.Cleanup(func() {
		for _, o := range []uuid.UUID{org, other} {
			for _, q := range []string{
				`DELETE FROM gates.gate_evaluations WHERE org_id = $1`, `DELETE FROM approvals.approval_requests WHERE org_id = $1`,
				`DELETE FROM secrets.secret_leases WHERE org_id = $1`, `DELETE FROM secrets.secret_values WHERE org_id = $1`,
				`DELETE FROM mcp.mcp_servers WHERE org_id = $1`,
			} {
				_, _ = p.Exec(context.Background(), q, o)
			}
		}
	})
	for _, o := range []uuid.UUID{org, other} {
		run := uuid.New()
		exec(t, p, `INSERT INTO gates.gate_evaluations (id, org_id, run_id, gate, status, target_sha) VALUES ($1,$2,$3,'tests','pass','abc')`, uuid.New(), o, run)
		exec(t, p, `INSERT INTO approvals.approval_requests (id, org_id, run_id, action, detail) VALUES ($1,$2,$3,'deploy','{}')`, uuid.New(), o, run)
		exec(t, p, `INSERT INTO secrets.secret_leases (id, org_id, run_id, name, token_hash, expires_at) VALUES ($1,$2,$3,'k',$4, now())`, uuid.New(), o, run, []byte(uuid.NewString()))
		exec(t, p, `INSERT INTO secrets.secret_values (org_id, name, environment, ciphertext) VALUES ($1,'k','staging','x')`, o)
		exec(t, p, `INSERT INTO mcp.mcp_servers (id, org_id, name, url, transport, requested_by) VALUES ($1,$2,$3,'http://x','streamable_http',$4)`, uuid.New(), o, "s-"+uuid.NewString()[:6], uuid.New())
	}
	ctx := context.Background()
	if err := cleanup.Gates(&gates.Purger{Pool: p}).OrgDeleted(ctx, events.OrgDeletedEvent{OrgID: org}); err != nil {
		t.Fatalf("gates OrgDeleted: %v", err)
	}
	if err := cleanup.MCPServer(mcp.NewRegistry(p, nil)).OrgDeleted(ctx, events.OrgDeletedEvent{OrgID: org}); err != nil {
		t.Fatalf("mcp OrgDeleted: %v", err)
	}
	for _, table := range []string{"gates.gate_evaluations", "approvals.approval_requests", "secrets.secret_leases", "secrets.secret_values", "mcp.mcp_servers"} {
		if n := count(t, p, `SELECT count(*) FROM `+table+` WHERE org_id = $1`, org); n != 0 {
			t.Fatalf("%d row(s) of the deleted organization left in %s", n, table)
		}
		if n := count(t, p, `SELECT count(*) FROM `+table+` WHERE org_id = $1`, other); n != 1 {
			t.Fatalf("another organization lost its row in %s", table)
		}
	}
}
