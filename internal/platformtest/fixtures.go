package platformtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
)

// Repo is a repository a test created, with a first commit on main.
type Repo struct {
	ID, Name string
	// Head is main's commit.
	Head string
}

// NewRepo creates a repository in org as u and pushes a first commit to main
// with an unmodified git client over smart-HTTP, using u's session.
func (p *Platform) NewRepo(t testing.TB, u User, org Org, prefix string, files map[string]string) Repo {
	t.Helper()
	name := prefix + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	resp, err := p.Git.CreateRepo(p.AsUser(u, org), &gitv1.CreateRepoRequest{Name: name})
	if err != nil {
		t.Fatalf("platformtest: create repo %s: %v", name, err)
	}
	if files == nil {
		files = map[string]string{"README.md": "# " + name + "\n"}
	}
	work := t.TempDir()
	Git(t, "", nil, "clone", "-q", p.CloneURL(org, name, u.Session), work)
	Commit(t, work, files, "first commit")
	Git(t, work, nil, "push", "-q", "origin", "HEAD:refs/heads/main")
	return Repo{ID: resp.GetRepo().GetId(), Name: name, Head: Git(t, work, nil, "rev-parse", "HEAD")}
}

// Git runs an unmodified git client in dir with extra environment, failing t
// on error, and returns its trimmed stdout.
func Git(t testing.TB, dir string, env []string, args ...string) string {
	t.Helper()
	out, err := TryGit(dir, env, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(out)
}

// TryGit runs git and returns its combined output and error, for a test that
// expects a refusal.
func TryGit(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Never prompt: a refused credential must fail, not wait on a terminal.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Commit writes files into the working tree at work and commits them.
func Commit(t testing.TB, work string, files map[string]string, message string) string {
	t.Helper()
	for path, body := range files {
		full := filepath.Join(work, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("platformtest: mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("platformtest: write %s: %v", path, err)
		}
	}
	Git(t, work, nil, "add", "-A")
	Git(t, work, nil, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-q", "-m", message)
	return Git(t, work, nil, "rev-parse", "HEAD")
}

// NewAgent defines an enabled agent in org as u.
func (p *Platform) NewAgent(t testing.TB, u User, org Org) string {
	t.Helper()
	resp, err := p.Agents.CreateAgent(p.AsUser(u, org), &agentsv1.CreateAgentRequest{
		Name: "agent-" + uuid.NewString()[:8], Role: "implementer", Enabled: true,
	})
	if err != nil {
		t.Fatalf("platformtest: create agent: %v", err)
	}
	return resp.GetAgent().GetId()
}

// NewWorkItem creates a Work Item in repo as u.
func (p *Platform) NewWorkItem(t testing.TB, u User, org Org, repo Repo) *workv1.WorkItem {
	t.Helper()
	resp, err := p.Work.CreateItem(p.AsUser(u, org), &workv1.CreateItemRequest{
		RepoId: repo.ID, Type: "feature", Goal: "describe the repository", Acceptance: []string{"a README exists"},
	})
	if err != nil {
		t.Fatalf("platformtest: create work item: %v", err)
	}
	return resp.GetItem()
}

// StartAgentRun starts agent on item as u, which issues the run's capability
// grant, and returns the run.
func (p *Platform) StartAgentRun(t testing.TB, u User, org Org, repo Repo, agentID string, item *workv1.WorkItem) *agentsv1.Run {
	t.Helper()
	resp, err := p.Agents.StartRun(p.AsUser(u, org), &agentsv1.StartRunRequest{
		AgentId: agentID, RepoId: repo.ID, WorkItemKey: item.GetKey(), SponsorId: u.ID,
	})
	if err != nil {
		t.Fatalf("platformtest: start agent run: %v", err)
	}
	return resp.GetRun()
}

// SSHCommand is a core.sshCommand for git against this platform's SSH
// transport, authenticating with the private key at keyPath.
func (p *Platform) SSHCommand(keyPath string) string {
	_, port, _ := strings.Cut(p.SSHAddr, ":")
	return "ssh -i " + keyPath + " -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes -p " + port
}

// SSHPasswordEnv returns a core.sshCommand and environment that make ssh
// authenticate to this platform's SSH transport with password, and nothing
// else, non-interactively.
func (p *Platform) SSHPasswordEnv(t testing.TB, password string) (sshCommand string, env []string) {
	t.Helper()
	_, port, _ := strings.Cut(p.SSHAddr, ":")
	dir := t.TempDir()
	askpass := filepath.Join(dir, "askpass.sh")
	secret := filepath.Join(dir, "password")
	if err := os.WriteFile(secret, []byte(password), 0o600); err != nil {
		t.Fatalf("platformtest: write password: %v", err)
	}
	if err := os.WriteFile(askpass, []byte("#!/bin/sh\ncat '"+secret+"'\n"), 0o700); err != nil {
		t.Fatalf("platformtest: write askpass: %v", err)
	}
	cmd := "ssh -o PreferredAuthentications=password -o PubkeyAuthentication=no -o NumberOfPasswordPrompts=1" +
		" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p " + port
	return cmd, []string{"SSH_ASKPASS=" + askpass, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=none"}
}

// SSHURL is the SSH URL of org/repo.
func (p *Platform) SSHURL(org Org, repo string) string {
	return "ssh://git@127.0.0.1/" + org.Name + "/" + repo + ".git"
}
