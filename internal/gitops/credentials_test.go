package gitops_test

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/platformtest"
)

// These tests start from credentials identity issued and go through
// git-platform's real credential adapter — NewCredentialAuthFunc,
// NewFingerprintFunc, NewAgentPasswordFunc, NewGrantCapFunc — which is what
// the service runs. Every other transport test injects its own AuthFunc and
// CapFunc, so none of them could see a defect in the functions that actually
// decide who may clone and push.

// TestPATGitClone: a personal access token authenticates an unmodified git
// client's clone over HTTP, and once the token is revoked the same clone is
// refused by the transport itself — not merely by a token lookup somewhere
// else.
func TestPATGitClone(t *testing.T) {
	p := platformtest.Start(t)
	owner := p.NewUser(t, "patowner")
	org := p.NewOrg(t, owner, "patorg")
	repo := p.NewRepo(t, owner, org, "patrepo", map[string]string{"README.md": "cloned with a token\n"})

	tokenID, pat := p.NewPAT(t, owner)
	work := t.TempDir() + "/clone"
	platformtest.Git(t, "", nil, "clone", "-q", p.CloneURL(org, repo.Name, pat), work)
	if head := platformtest.Git(t, work, nil, "rev-parse", "HEAD"); head != repo.Head {
		t.Fatalf("cloned HEAD %s, want %s", head, repo.Head)
	}
	// The token writes too: it is the person's credential, not a read key.
	platformtest.Commit(t, work, map[string]string{"NOTES.md": "pushed with a token\n"}, "token push")
	platformtest.Git(t, work, nil, "push", "-q", "origin", "HEAD:refs/heads/main")

	p.RevokePAT(t, owner, tokenID)

	out, err := platformtest.TryGit("", nil, "clone", "-q", p.CloneURL(org, repo.Name, pat), t.TempDir()+"/again")
	if err == nil {
		t.Fatalf("a revoked token cloned the repository:\n%s", out)
	}
	if !strings.Contains(out, "Authentication failed") && !strings.Contains(out, "401") {
		t.Fatalf("a revoked token was refused, but not as an authentication failure:\n%s", out)
	}
	out, err = platformtest.TryGit("", nil, "ls-remote", p.CloneURL(org, repo.Name, pat))
	if err == nil {
		t.Fatalf("a revoked token listed refs:\n%s", out)
	}
	// The session the token was minted from is untouched by revoking it.
	platformtest.Git(t, "", nil, "ls-remote", p.CloneURL(org, repo.Name, owner.Session))
}

// TestAgentBranchScopeEnforced: an agent whose run was granted
// agents/<key>/ writes inside it and is refused the default branch, with the
// same reason, through smart-HTTP, through SSH, and through the API the
// git.commit tool calls. A person's push to the default branch is not
// constrained by any grant.
func TestAgentBranchScopeEnforced(t *testing.T) {
	p, control := platformtest.StartWithControlledRunner(t)
	owner := p.NewUser(t, "scopeowner")
	org := p.NewOrg(t, owner, "scopeorg")
	repo := p.NewRepo(t, owner, org, "scoperepo", nil)
	agentID := p.NewAgent(t, owner, org)
	item := p.NewWorkItem(t, owner, org, repo)
	run := p.StartAgentRun(t, owner, org, repo, agentID, item)
	// Keep a real admitted Runner alive at inference while exercising Git.
	// A nil executor must never manufacture a live capability for this test.
	control.Wait(t, run.GetId())
	defer control.Stop(t, run.GetId())
	agentCred := p.AgentCredential(t, org, agentID)
	granted := "refs/heads/" + run.GetBranch()
	if !strings.HasPrefix(run.GetBranch(), "agents/"+item.GetKey()+"/") {
		t.Fatalf("run branch %q is not inside the grant agents/%s/", run.GetBranch(), item.GetKey())
	}

	// A denial names the ref and what the grant allows; that text is the
	// "identical" the criterion asks for.
	wantReason := "not permitted; grant allows \"agents/" + item.GetKey() + "/\""

	// --- smart-HTTP ---
	httpWork := t.TempDir() + "/http"
	platformtest.Git(t, "", nil, "clone", "-q", p.CloneURL(org, repo.Name, agentCred), httpWork)
	platformtest.Commit(t, httpWork, map[string]string{"HTTP.md": "from an agent over http\n"}, "agent over http")
	out, err := platformtest.TryGit(httpWork, nil, "push", "origin", "HEAD:refs/heads/main")
	if err == nil {
		t.Fatalf("an agent pushed main over HTTP:\n%s", out)
	}
	if !strings.Contains(out, wantReason) {
		t.Fatalf("HTTP refusal does not carry the grant's reason %q:\n%s", wantReason, out)
	}
	platformtest.Git(t, httpWork, nil, "push", "-q", "origin", "HEAD:"+granted)

	// --- SSH, with the same agent credential as the password ---
	sshCmd, sshEnv := p.SSHPasswordEnv(t, agentCred)
	sshWork := t.TempDir() + "/ssh"
	platformtest.Git(t, "", sshEnv, "-c", "core.sshCommand="+sshCmd, "clone", "-q", p.SSHURL(org, repo.Name), sshWork)
	platformtest.Commit(t, sshWork, map[string]string{"SSH.md": "from an agent over ssh\n"}, "agent over ssh")
	out, err = platformtest.TryGit(sshWork, sshEnv, "-c", "core.sshCommand="+sshCmd, "push", "origin", "HEAD:refs/heads/main")
	if err == nil {
		t.Fatalf("an agent pushed main over SSH:\n%s", out)
	}
	if !strings.Contains(out, wantReason) {
		t.Fatalf("SSH refusal does not carry the grant's reason %q:\n%s", wantReason, out)
	}
	platformtest.Git(t, sshWork, sshEnv, "-c", "core.sshCommand="+sshCmd, "push", "-q", "-f", "origin", "HEAD:"+granted+"-ssh")

	// --- the API git.commit calls ---
	agentCtx := platformtest.WithCredential(t.Context(), agentCred, org.ID)
	_, err = p.Git.CreateCommit(agentCtx, &gitv1.CreateCommitRequest{
		Repo: repo.Name, Branch: "main", Message: "agent via the api",
		Files: []*gitv1.FileChange{{Path: "API.md", Content: []byte("x\n")}},
	})
	if status.Code(err) != codes.PermissionDenied || !strings.Contains(err.Error(), wantReason) {
		t.Fatalf("CreateCommit to main as the agent: %v, want PermissionDenied carrying %q", err, wantReason)
	}
	if _, err := p.Git.CreateCommit(agentCtx, &gitv1.CreateCommitRequest{
		Repo: repo.Name, Branch: run.GetBranch(), Message: "agent via the api",
		Files: []*gitv1.FileChange{{Path: "API.md", Content: []byte("x\n")}},
	}); err != nil {
		t.Fatalf("CreateCommit inside the grant as the agent: %v", err)
	}
	if _, err := p.Git.Merge(agentCtx, &gitv1.MergeRequest{Repo: repo.Name, SourceRef: run.GetBranch(), TargetRef: "main"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Merge into main as the agent: %v, want PermissionDenied", err)
	}

	// main is exactly as the owner left it.
	branches, err := p.Git.ListBranches(p.AsUser(owner, org), &gitv1.ListBranchesRequest{Repo: repo.Name})
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	for _, b := range branches.GetRefs() {
		if b.GetName() == "main" && b.GetSha() != repo.Head {
			t.Fatalf("main moved to %s under the agent's credential, want %s", b.GetSha(), repo.Head)
		}
	}

	// An agent of the same organization holding no grant writes nothing,
	// even where the first agent may.
	other := p.AgentCredential(t, org, p.NewAgent(t, owner, org))
	out, err = platformtest.TryGit(httpWork, nil, "push", p.CloneURL(org, repo.Name, other), "HEAD:"+granted+"-other")
	if err == nil {
		t.Fatalf("an agent with no grant pushed:\n%s", out)
	}

	// A person is not held to an agent's grant.
	personWork := t.TempDir() + "/person"
	platformtest.Git(t, "", nil, "clone", "-q", p.CloneURL(org, repo.Name, owner.Session), personWork)
	platformtest.Commit(t, personWork, map[string]string{"PERSON.md": "a member's own push\n"}, "member push")
	platformtest.Git(t, personWork, nil, "push", "-q", "origin", "HEAD:refs/heads/main")
}

// TestSSHPasswordIsOnlyForAgents: the SSH transport's password method accepts
// an agent run's credential and nothing else — not a person's session, not a
// personal access token, not a platform worker's token.
func TestSSHPasswordIsOnlyForAgents(t *testing.T) {
	p := platformtest.Start(t)
	owner := p.NewUser(t, "sshpwowner")
	org := p.NewOrg(t, owner, "sshpworg")
	repo := p.NewRepo(t, owner, org, "sshpwrepo", nil)
	_, pat := p.NewPAT(t, owner)

	for name, password := range map[string]string{
		"session":               owner.Session,
		"personal access token": pat,
		"password":              owner.Password,
	} {
		sshCmd, env := p.SSHPasswordEnv(t, password)
		out, err := platformtest.TryGit("", env, "-c", "core.sshCommand="+sshCmd, "ls-remote", p.SSHURL(org, repo.Name))
		if err == nil {
			t.Errorf("a %s was accepted as an SSH password:\n%s", name, out)
		}
	}
}
