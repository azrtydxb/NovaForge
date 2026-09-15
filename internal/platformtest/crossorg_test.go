package platformtest_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/platformtest"
)

// crossOrg is two organizations, each with a person, a repository, a Work
// Item, an agent and an Agent Run — and a caller from A who knows every id and
// name in B, because ids leak (URLs, logs, screenshots) and names are guessable.
type crossOrg struct {
	p        *platformtest.Platform
	a, b     platformtest.User
	orgA     platformtest.Org
	orgB     platformtest.Org
	repoB    platformtest.Repo
	itemB    *workv1.WorkItem
	agentB   string
	runB     *agentsv1.Run
	patA     string
	keyA     string
	asAInA   context.Context
	asAInB   context.Context
	asBInB   context.Context
	agentA   string
	repoA    platformtest.Repo
	itemA    *workv1.WorkItem
	refusals []string
}

func newCrossOrg(t *testing.T) *crossOrg {
	t.Helper()
	p := platformtest.Start(t)
	c := &crossOrg{p: p}
	c.a, c.b = p.NewUser(t, "xa"), p.NewUser(t, "xb")
	c.orgA, c.orgB = p.NewOrg(t, c.a, "xorga"), p.NewOrg(t, c.b, "xorgb")
	c.repoA = p.NewRepo(t, c.a, c.orgA, "shared", nil)
	c.repoB = p.NewRepo(t, c.b, c.orgB, "secret", map[string]string{"SECRET.md": "org B only\n"})
	c.itemA = p.NewWorkItem(t, c.a, c.orgA, c.repoA)
	c.itemB = p.NewWorkItem(t, c.b, c.orgB, c.repoB)
	c.agentA = p.NewAgent(t, c.a, c.orgA)
	c.agentB = p.NewAgent(t, c.b, c.orgB)
	c.runB = p.StartAgentRun(t, c.b, c.orgB, c.repoB, c.agentB, c.itemB)
	_, c.patA = p.NewPAT(t, c.a)
	c.keyA = p.AddSSHKey(t, c.a)
	c.asAInA, c.asAInB, c.asBInB = p.AsUser(c.a, c.orgA), p.AsUser(c.a, c.orgB), p.AsUser(c.b, c.orgB)
	return c
}

// refused fails t unless err is a refusal: an error whose code says the caller
// may not have it, or that it does not exist for them.
func refused(t *testing.T, what string, err error) {
	t.Helper()
	switch status.Code(err) {
	case codes.PermissionDenied, codes.NotFound, codes.Unauthenticated:
		return
	case codes.OK:
		t.Errorf("LEAK: %s succeeded across organizations", what)
	default:
		t.Errorf("%s: want a refusal, got %v", what, err)
	}
}

// TestCrossOrgAccessDenied: a request carrying org A's credentials — a
// session, a personal access token, an SSH key — cannot read or write org B's
// repositories, Work Items, CI runs or Agent Runs, whether it names
// organization B (identity refuses the membership) or names its own
// organization with B's ids (every service scopes the lookup).
func TestCrossOrgAccessDenied(t *testing.T) {
	c := newCrossOrg(t)
	p := c.p

	t.Run("repositories over gRPC", func(t *testing.T) {
		for _, ctx := range []context.Context{c.asAInB, c.asAInA} {
			_, err := p.Git.GetRepo(ctx, &gitv1.GetRepoRequest{Name: c.repoB.Name})
			refused(t, "GetRepo by name", err)
			_, err = p.Git.GetRepo(ctx, &gitv1.GetRepoRequest{Name: c.repoB.ID})
			refused(t, "GetRepo by id", err)
			_, err = p.Git.GetTree(ctx, &gitv1.GetTreeRequest{Repo: c.repoB.ID, Ref: "main"})
			refused(t, "GetTree", err)
			_, err = p.Git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: c.repoB.ID, Ref: "main", Path: "SECRET.md"})
			refused(t, "GetBlob", err)
			_, err = p.Git.GetDiff(ctx, &gitv1.GetDiffRequest{Repo: c.repoB.ID, From: c.repoB.Head, To: "main"})
			refused(t, "GetDiff", err)
			_, err = p.Git.ListTags(ctx, &gitv1.ListTagsRequest{Repo: c.repoB.ID})
			refused(t, "ListTags", err)
			_, err = p.Git.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: c.repoB.ID, Name: "a-was-here"})
			refused(t, "CreateBranch", err)
			_, err = p.Git.CreateCommit(ctx, &gitv1.CreateCommitRequest{Repo: c.repoB.ID, Branch: "main", Message: "x",
				Files: []*gitv1.FileChange{{Path: "PWNED.md", Content: []byte("x")}}})
			refused(t, "CreateCommit", err)
			_, err = p.Git.DeleteRepo(ctx, &gitv1.DeleteRepoRequest{Name: c.repoB.ID})
			refused(t, "DeleteRepo", err)
		}
		list, err := p.Git.ListRepos(c.asAInA, &gitv1.ListReposRequest{})
		if err != nil {
			t.Fatalf("ListRepos in A: %v", err)
		}
		for _, r := range list.GetRepos() {
			if r.GetId() == c.repoB.ID {
				t.Error("LEAK: ListRepos in A lists B's repository")
			}
		}
	})

	t.Run("repositories over smart-HTTP and SSH", func(t *testing.T) {
		for name, cred := range map[string]string{"session": c.a.Session, "personal access token": c.patA} {
			out, err := platformtest.TryGit("", nil, "clone", "-q", p.CloneURL(c.orgB, c.repoB.Name, cred), t.TempDir()+"/x")
			if err == nil {
				t.Errorf("LEAK: A's %s cloned B's repository over HTTP:\n%s", name, out)
			}
			// Naming A's organization in the URL resolves B's repository name
			// inside A, where it does not exist.
			out, err = platformtest.TryGit("", nil, "ls-remote", p.CloneURL(c.orgA, c.repoB.Name, cred))
			if err == nil {
				t.Errorf("LEAK: A's %s listed B's refs through A's organization path:\n%s", name, out)
			}
		}
		// A push: clone A's own repository, then push it at B's.
		work := t.TempDir() + "/own"
		platformtest.Git(t, "", nil, "clone", "-q", p.CloneURL(c.orgA, c.repoA.Name, c.a.Session), work)
		platformtest.Commit(t, work, map[string]string{"PWNED.md": "x\n"}, "cross-org push")
		out, err := platformtest.TryGit(work, nil, "push", p.CloneURL(c.orgB, c.repoB.Name, c.patA), "HEAD:refs/heads/main")
		if err == nil {
			t.Errorf("LEAK: A pushed to B's repository over HTTP:\n%s", out)
		}

		sshCmd := p.SSHCommand(c.keyA)
		out, err = platformtest.TryGit("", nil, "-c", "core.sshCommand="+sshCmd, "clone", "-q", p.SSHURL(c.orgB, c.repoB.Name), t.TempDir()+"/ssh")
		if err == nil {
			t.Errorf("LEAK: A's SSH key cloned B's repository:\n%s", out)
		}
		out, err = platformtest.TryGit(work, nil, "-c", "core.sshCommand="+sshCmd, "push", p.SSHURL(c.orgB, c.repoB.Name), "HEAD:refs/heads/main")
		if err == nil {
			t.Errorf("LEAK: A's SSH key pushed to B's repository:\n%s", out)
		}
		// A's key does work where A belongs, so the refusals above are about
		// the organization, not a broken key.
		platformtest.Git(t, "", nil, "-c", "core.sshCommand="+sshCmd, "ls-remote", p.SSHURL(c.orgA, c.repoA.Name))

		// An agent credential for A cannot reach B either: its organization is
		// the one it names, whatever the URL says.
		agentCred := p.AgentCredential(t, c.orgA, c.agentA)
		out, err = platformtest.TryGit("", nil, "ls-remote", p.CloneURL(c.orgB, c.repoB.Name, agentCred))
		if err == nil {
			t.Errorf("LEAK: A's agent credential listed B's refs:\n%s", out)
		}

		branches, err := p.Git.ListBranches(c.asBInB, &gitv1.ListBranchesRequest{Repo: c.repoB.Name})
		if err != nil {
			t.Fatalf("ListBranches as B: %v", err)
		}
		for _, br := range branches.GetRefs() {
			if br.GetName() == "main" && br.GetSha() != c.repoB.Head {
				t.Errorf("LEAK: B's main moved to %s", br.GetSha())
			}
			if br.GetName() == "a-was-here" {
				t.Error("LEAK: A created a branch in B's repository")
			}
		}
	})

	t.Run("work items", func(t *testing.T) {
		for _, ctx := range []context.Context{c.asAInB, c.asAInA} {
			_, err := p.Work.GetItem(ctx, &workv1.GetItemRequest{Key: c.itemB.GetKey()})
			if err == nil {
				// Keys are allocated per organization, so A may well have an
				// item with B's key; what must never come back is B's item.
				got, _ := p.Work.GetItem(ctx, &workv1.GetItemRequest{Key: c.itemB.GetKey()})
				if got.GetItem().GetId() == c.itemB.GetId() {
					t.Error("LEAK: GetItem by key returned B's item")
				}
			}
			_, err = p.Work.GetItem(ctx, &workv1.GetItemRequest{Id: c.itemB.GetId()})
			refused(t, "GetItem by id", err)
			_, err = p.Work.AssignItem(ctx, &workv1.AssignItemRequest{Id: c.itemB.GetId(), AssigneeId: c.a.ID, AssigneeKind: "user"})
			refused(t, "AssignItem", err)
			_, err = p.Work.AddComment(ctx, &workv1.AddCommentRequest{WorkItemId: c.itemB.GetId(), Body: "from A"})
			refused(t, "AddComment", err)
			_, err = p.Work.ListComments(ctx, &workv1.ListCommentsRequest{WorkItemId: c.itemB.GetId()})
			refused(t, "ListComments", err)
			_, err = p.Work.ListItems(ctx, &workv1.ListItemsRequest{RepoId: c.repoB.ID})
			if err == nil {
				items, _ := p.Work.ListItems(ctx, &workv1.ListItemsRequest{RepoId: c.repoB.ID})
				for _, it := range items.GetItems() {
					if it.GetId() == c.itemB.GetId() {
						t.Error("LEAK: ListItems returned B's item")
					}
				}
			}
		}
		// Creating an item that names B's repository must not put anything
		// into B.
		_, _ = p.Work.CreateItem(c.asAInA, &workv1.CreateItemRequest{RepoId: c.repoB.ID, Type: "feature", Goal: "planted by A"})
		items, err := p.Work.ListItems(c.asBInB, &workv1.ListItemsRequest{RepoId: c.repoB.ID})
		if err != nil {
			t.Fatalf("ListItems as B: %v", err)
		}
		for _, it := range items.GetItems() {
			if it.GetGoal() == "planted by A" {
				t.Error("LEAK: A created a Work Item in B's repository")
			}
			if it.GetAssigneeId() == c.a.ID {
				t.Error("LEAK: A assigned B's Work Item")
			}
		}
		comments, err := p.Work.ListComments(c.asBInB, &workv1.ListCommentsRequest{WorkItemId: c.itemB.GetId()})
		if err != nil {
			t.Fatalf("ListComments as B: %v", err)
		}
		if len(comments.GetComments()) != 0 {
			t.Errorf("LEAK: B's Work Item carries comments after A's attempts: %v", comments.GetComments())
		}
	})

	t.Run("agent runs", func(t *testing.T) {
		for _, ctx := range []context.Context{c.asAInB, c.asAInA} {
			_, err := p.Agents.GetRun(ctx, &agentsv1.GetRunRequest{Id: c.runB.GetId()})
			refused(t, "GetRun", err)
			_, err = p.Agents.CancelRun(ctx, &agentsv1.CancelRunRequest{Id: c.runB.GetId()})
			refused(t, "CancelRun", err)
		}
		// Starting a run in A with B's agent would issue a capability grant in
		// A to an agent A does not own.
		_, err := p.Agents.StartRun(c.asAInA, &agentsv1.StartRunRequest{
			AgentId: c.agentB, RepoId: c.repoA.ID, WorkItemKey: c.itemA.GetKey(), SponsorId: c.a.ID,
		})
		refused(t, "StartRun with B's agent", err)
		// Nor may A name B's person as the one answerable for A's run.
		_, err = p.Agents.StartRun(c.asAInA, &agentsv1.StartRunRequest{
			AgentId: c.agentA, RepoId: c.repoA.ID, WorkItemKey: c.itemA.GetKey(), SponsorId: c.b.ID,
		})
		refused(t, "StartRun sponsored by B's person", err)

		run, err := p.Agents.GetRun(c.asBInB, &agentsv1.GetRunRequest{Id: c.runB.GetId()})
		if err != nil {
			t.Fatalf("GetRun as B: %v", err)
		}
		if run.GetRun().GetState() == "cancelled" {
			t.Error("LEAK: A cancelled B's Agent Run")
		}

		stream, err := p.Agents.StreamRunEvents(c.asAInA, &agentsv1.StreamRunEventsRequest{RunId: c.runB.GetId()})
		if err == nil {
			_, err = stream.Recv()
		}
		refused(t, "StreamRunEvents", err)
	})

	t.Run("mcp register", func(t *testing.T) {
		_, err := p.MCP.ListApprovedServers(c.asAInB, &mcpv1.ListApprovedServersRequest{})
		refused(t, "ListApprovedServers naming B", err)
	})

	t.Run("REST edge", func(t *testing.T) {
		get := func(path string) int {
			req, _ := http.NewRequest(http.MethodGet, p.EdgeURL+path, nil)
			req.Header.Set("Authorization", "Bearer "+c.a.Session)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			return resp.StatusCode
		}
		for _, path := range []string{
			"/api/v1/orgs/" + c.orgB.Name + "/repos",
			"/api/v1/orgs/" + c.orgB.Name + "/repos/" + c.repoB.Name + "/blob/main/SECRET.md",
			"/api/v1/orgs/" + c.orgA.Name + "/repos/" + c.repoB.Name + "/blob/main/SECRET.md",
			"/api/v1/orgs/" + c.orgA.Name + "/repos/" + c.repoB.ID + "/tree/main/",
			"/api/v1/orgs/" + c.orgB.Name + "/repos/" + c.repoB.Name + "/work/" + c.itemB.GetKey(),
			"/api/v1/orgs/" + c.orgA.Name + "/agent-runs/" + c.runB.GetId(),
			"/api/v1/orgs/" + c.orgB.Name + "/agent-runs/" + c.runB.GetId(),
		} {
			if code := get(path); code < 400 {
				t.Errorf("LEAK: GET %s answered %d for A", path, code)
			}
		}
	})
}

// TestCrossOrgCIDenied covers CI runs, logs and artifacts, and the runner
// protocol that feeds them: a person in A cannot read B's run, its job log or
// its artifacts, and nobody can register a runner into B, or report on, log
// to or upload for B's job, without B's own authority.
func TestCrossOrgCIDenied(t *testing.T) {
	c := newCrossOrg(t)
	p := c.p
	p.RequireCI(t)
	bg := context.Background()

	orgB := uuid.MustParse(c.orgB.ID)
	run, created, err := p.CIStore.CreateRun(bg, ci.Run{
		OrgID: orgB, RepoID: uuid.MustParse(c.repoB.ID), RepoName: c.repoB.Name,
		CommitSHA: c.repoB.Head, Ref: "refs/heads/main", Status: "queued",
	})
	if err != nil || !created {
		t.Fatalf("create B's CI run: %v (created %v)", err, created)
	}
	job, err := p.CIStore.CreateJob(bg, ci.WorkflowJob{RunID: run.ID, Name: "build", RunCmd: "make", Status: "pending"})
	if err != nil {
		t.Fatalf("create B's CI job: %v", err)
	}

	t.Run("runs, logs and artifacts", func(t *testing.T) {
		for _, ctx := range []context.Context{c.asAInB, c.asAInA} {
			_, err := p.CI.GetRun(ctx, &civ1.GetRunRequest{Id: run.ID.String()})
			refused(t, "CI GetRun", err)
			_, err = p.CI.GetJobLogs(ctx, &civ1.GetJobLogsRequest{JobId: job.ID.String()})
			refused(t, "CI GetJobLogs by job", err)
			_, err = p.CI.GetJobLogs(ctx, &civ1.GetJobLogsRequest{RunId: run.ID.String()})
			refused(t, "CI GetJobLogs by run", err)
			_, err = p.CI.ListArtifacts(ctx, &civ1.ListArtifactsRequest{RunId: run.ID.String()})
			refused(t, "CI ListArtifacts", err)
			_, err = p.CI.TriggerRun(ctx, &civ1.TriggerRunRequest{RepoId: c.repoB.ID, Ref: "main"})
			refused(t, "CI TriggerRun on B's repository", err)
		}
		if _, err := p.CI.GetRun(c.asBInB, &civ1.GetRunRequest{Id: run.ID.String()}); err != nil {
			t.Fatalf("B cannot read its own run, so the refusals above prove nothing: %v", err)
		}
	})

	t.Run("runner protocol", func(t *testing.T) {
		runners := civ1.NewRunnerServiceClient(p.DialCI(t))

		// Registering a runner into an organization is an owner's or admin's
		// act. With no credential, with a credential for another
		// organization, or as a plain member, it is refused.
		_, err := runners.Register(bg, &civ1.RegisterRequest{OrgId: c.orgB.ID, Name: "rogue", Labels: []string{"linux"}})
		refused(t, "Register into B with no credential", err)
		_, err = runners.Register(c.asAInA, &civ1.RegisterRequest{OrgId: c.orgB.ID, Name: "rogue", Labels: []string{"linux"}})
		refused(t, "Register into B with A's credential", err)
		_, err = runners.Register(c.asAInB, &civ1.RegisterRequest{OrgId: c.orgB.ID, Name: "rogue", Labels: []string{"linux"}})
		refused(t, "Register naming B with A's credential", err)
		member := p.NewUser(t, "xmember")
		p.AddMember(t, c.b, c.orgB, member, "member")
		_, err = runners.Register(p.AsUser(member, c.orgB), &civ1.RegisterRequest{Name: "member-runner", Labels: []string{"linux"}})
		refused(t, "Register as a plain member of B", err)

		// A's owner may register a runner into A — and that runner, with its
		// own valid token, still cannot touch B's job.
		reg, err := runners.Register(c.asAInA, &civ1.RegisterRequest{Name: "a-runner", Labels: []string{"linux"}})
		if err != nil {
			t.Fatalf("A's owner registers a runner into A: %v", err)
		}
		_, err = runners.ReportStatus(bg, &civ1.ReportStatusRequest{
			RunnerId: reg.GetRunnerId(), Token: reg.GetToken(), JobId: job.ID.String(), Status: "success",
		})
		refused(t, "ReportStatus on B's job by A's runner", err)
		_, err = runners.ReportStatus(bg, &civ1.ReportStatusRequest{
			RunnerId: reg.GetRunnerId(), Token: randomToken(t), JobId: job.ID.String(), Status: "success",
		})
		refused(t, "ReportStatus with a forged runner token", err)
		_, err = runners.UploadArtifact(bg, &civ1.UploadArtifactRequest{
			RunnerId: reg.GetRunnerId(), Token: reg.GetToken(), JobId: job.ID.String(), Name: "planted.txt", Content: []byte("x"),
		})
		refused(t, "UploadArtifact to B's job by A's runner", err)

		// A stream that presents a runner id without that runner's token is
		// closed before anything is dispatched to it or accepted from it.
		ctx, cancel := context.WithTimeout(bg, 5*time.Second)
		defer cancel()
		stream, err := runners.Connect(ctx)
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		if err := stream.Send(&civ1.ConnectRequest{RunnerId: reg.GetRunnerId(), Token: randomToken(t),
			Payload: &civ1.ConnectRequest_LogChunk{LogChunk: &civ1.LogChunk{JobId: job.ID.String(), Line: "planted"}}}); err != nil {
			t.Fatalf("send: %v", err)
		}
		_, err = stream.Recv()
		refused(t, "Connect with a forged runner token", err)

		got, err := p.CIStore.GetJob(bg, job.ID)
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		if got.Status != "pending" {
			t.Errorf("LEAK: B's job is %q after A's attempts", got.Status)
		}
		logs, err := p.CI.GetJobLogs(c.asBInB, &civ1.GetJobLogsRequest{JobId: job.ID.String()})
		if err != nil {
			t.Fatalf("GetJobLogs as B: %v", err)
		}
		for _, line := range logs.GetLines() {
			if strings.Contains(line, "planted") {
				t.Error("LEAK: a line was written into B's job log without B's runner token")
			}
		}
		arts, err := p.CI.ListArtifacts(c.asBInB, &civ1.ListArtifactsRequest{RunId: run.ID.String()})
		if err != nil {
			t.Fatalf("ListArtifacts as B: %v", err)
		}
		if len(arts.GetArtifacts()) != 0 {
			t.Errorf("LEAK: B's run carries artifacts after A's attempts: %v", arts.GetArtifacts())
		}
	})
}

func randomToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
