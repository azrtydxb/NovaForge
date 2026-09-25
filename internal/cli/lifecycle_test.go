package cli_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/cli"
	"github.com/novaforge/novaforge/internal/platformtest"
)

// nf runs the CLI as whoever's config directory is active and returns stdout,
// failing t on a non-zero exit.
func nf(t *testing.T, args ...string) string {
	t.Helper()
	out, errOut, code := nfTry(args...)
	if code != 0 {
		t.Fatalf("nf %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}

func nfTry(args ...string) (string, string, int) {
	var out, errOut bytes.Buffer
	code := cli.Execute(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

// TestCLIFullLifecycle: nf creates a repository, clones it, and after a plain
// git push opens a Work Item, starts an Agent Run on it, opens an Engineering
// Run, inspects its gates, records an independent review and merges — through
// the real REST edge and the real services, with no GUI. `nf run gates` and
// `nf run merge` existed and no test or e2e step had ever called them.
func TestCLIFullLifecycle(t *testing.T) {
	p, control := platformtest.StartWithControlledRunner(t)
	author := p.NewUser(t, "cliauthor")
	reviewer := p.NewUser(t, "clireviewer")
	authorHome, reviewerHome := t.TempDir(), t.TempDir()
	org := "cliorg" + strings.ToLower(author.ID[:8])

	t.Setenv("XDG_CONFIG_HOME", authorHome)
	nf(t, "login", "--server", p.EdgeURL, "--username", author.Username, "--password", author.Password)
	nf(t, "org", "create", org)
	nf(t, "org", "use", org)
	nf(t, "org", "add-member", reviewer.Username, "--role", "member")
	nf(t, "repo", "create", "ledger")
	if !strings.Contains(nf(t, "repo", "list"), "ledger") {
		t.Fatal("nf repo list does not show the new repository")
	}

	work := t.TempDir() + "/ledger"
	nf(t, "repo", "clone", "ledger", work, "--git", p.GitHTTPURL)
	if url := nf(t, "repo", "clone", "ledger", "--print"); !strings.Contains(url, "/"+org+"/ledger.git") {
		t.Fatalf("nf repo clone --print = %q, and the git host was not remembered", url)
	}
	platformtest.Commit(t, work, map[string]string{
		"go.mod":                              "module example.com/ledger\n\ngo 1.22\n",
		"ledger.go":                           "// Package ledger keeps balances.\npackage ledger\n\n// Balance is an account's balance in cents.\ntype Balance int64\n",
		".novaforge/gates/documentation.yaml": "name: documentation\nrequired: true\n",
	}, "start the ledger")
	platformtest.Git(t, work, nil, "push", "-q", "origin", "HEAD:refs/heads/main")
	platformtest.Git(t, work, nil, "checkout", "-q", "-b", "feature/credit")
	platformtest.Commit(t, work, map[string]string{
		"credit.go": "package ledger\n\n// Credit adds cents to a balance.\nfunc Credit(b Balance, cents int64) Balance { return b + Balance(cents) }\n",
	}, "credit a balance")
	platformtest.Git(t, work, nil, "push", "-q", "origin", "feature/credit")

	created := nf(t, "work", "create", "ledger", "--type", "feature", "--goal", "credit a balance",
		"--acceptance", "Credit adds to a balance", "--gate", "documentation")
	key := regexp.MustCompile(`[A-Z]+-\d+`).FindString(created)
	if key == "" {
		t.Fatalf("nf work create printed no key: %q", created)
	}
	if got := nf(t, "work", "get", "ledger", key); !strings.Contains(got, "Credit adds to a balance") || !strings.Contains(got, "documentation") {
		t.Fatalf("the Work Item lost its acceptance criteria or gates: %s", got)
	}

	nf(t, "agent", "create", "implementer-bot")
	started := nf(t, "agent", "start", "ledger", key)
	if !strings.Contains(started, "agents/"+key+"/") {
		t.Fatalf("nf agent start did not report the run's granted branch: %q", started)
	}
	runID := strings.Fields(started)[0]
	id, err := uuid.Parse(runID)
	if err != nil {
		t.Fatalf("nf agent start printed invalid run ID: %v", err)
	}
	control.Wait(t, id.String())
	defer control.Stop(t, id.String())
	if got := nf(t, "agent", "get", runID); !strings.Contains(got, runID) {
		t.Fatalf("nf agent get %s = %q", runID, got)
	}

	opened := nf(t, "run", "create", "ledger", "--title", "Credit a balance", "--source", "feature/credit", "--work-item", key)
	number := strings.TrimPrefix(strings.Fields(opened)[0], "#")
	if number == "" {
		t.Fatalf("nf run create printed no number: %q", opened)
	}

	if _, errOut, code := nfTry("run", "merge", "ledger", number); code == 0 || !strings.Contains(errOut, "independent") {
		t.Fatalf("an unreviewed run merged or was refused for another reason: exit %d, %s", code, errOut)
	}
	if _, errOut, code := nfTry("run", "review", "ledger", number, "--verdict", "approve"); code == 0 {
		t.Fatalf("the author approved their own run: %s", errOut)
	}

	gates := nf(t, "run", "gates", "ledger", number)
	if !strings.Contains(gates, "documentation\tpass") {
		t.Fatalf("nf run gates = %q, want the documentation gate passing", gates)
	}
	if proof := nf(t, "run", "proof", "ledger", number); !strings.Contains(proof, "documentation\tpass") {
		t.Fatalf("nf run proof = %q, want the gate's proof recorded", proof)
	}

	t.Setenv("XDG_CONFIG_HOME", reviewerHome)
	nf(t, "login", "--server", p.EdgeURL, "--username", reviewer.Username, "--password", reviewer.Password)
	nf(t, "org", "use", org)
	nf(t, "run", "review", "ledger", number, "--verdict", "approve", "--summary", "documented and small")

	t.Setenv("XDG_CONFIG_HOME", authorHome)
	merged := nf(t, "run", "merge", "ledger", number)
	if !strings.Contains(merged, "merged #"+number) {
		t.Fatalf("nf run merge = %q", merged)
	}
	if log := nf(t, "repo", "log", "ledger", "main"); !strings.Contains(log, "credit a balance") {
		t.Fatalf("main does not carry the merged change:\n%s", log)
	}
	if list := nf(t, "run", "list", "ledger"); !strings.Contains(list, "#"+number+"\tmerged") {
		t.Fatalf("nf run list = %q, want run #%s merged", list, number)
	}
}
