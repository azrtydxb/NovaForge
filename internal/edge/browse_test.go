package edge_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/platformtest"
)

// TestRepoBrowseAPI: branches, tags, commit history, diffs, and tree and blob
// reads are retrievable through the REST edge for a repository pushed by an
// unmodified git client, with the real git-platform behind the real edge.
// Until this test, tree, blob and diff were asserted only at gitops.Repo and
// tags at no layer at all.
func TestRepoBrowseAPI(t *testing.T) {
	p := platformtest.Start(t)
	owner := p.NewUser(t, "browse")
	org := p.NewOrg(t, owner, "browseorg")
	repo := p.NewRepo(t, owner, org, "browse", map[string]string{
		"README.md":        "# browse\n",
		"src/main.go":      "package main\n\nfunc main() {}\n",
		"src/util/help.go": "package util\n",
	})

	work := t.TempDir() + "/w"
	platformtest.Git(t, "", nil, "clone", "-q", p.CloneURL(org, repo.Name, owner.Session), work)
	platformtest.Git(t, work, nil, "tag", "v1.0.0-light")
	platformtest.Git(t, work, nil, "-c", "user.email=t@example.com", "-c", "user.name=T", "tag", "-a", "v1.0.0", "-m", "first release")
	second := platformtest.Commit(t, work, map[string]string{"README.md": "# browse\n\nNow with a second line.\n"}, "describe it")
	platformtest.Git(t, work, nil, "checkout", "-q", "-b", "agents/NF-1/work")
	third := platformtest.Commit(t, work, map[string]string{"src/new.go": "package src\n"}, "agent work")
	platformtest.Git(t, work, nil, "push", "-q", "origin", "main:main", "agents/NF-1/work", "--tags")

	base := p.EdgeURL + "/api/v1/orgs/" + org.Name + "/repos/" + repo.Name
	get := func(path string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		req.Header.Set("Authorization", "Bearer "+owner.Session)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, resp.StatusCode, body)
		}
		return resp.StatusCode, body
	}
	type ref struct{ Name, Sha, Kind string }
	refs := func(path string) map[string]ref {
		_, body := get(path)
		var out struct{ Refs []ref }
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		m := map[string]ref{}
		for _, r := range out.Refs {
			m[r.Name] = r
		}
		return m
	}

	branches := refs("/branches")
	if b := branches["main"]; b.Sha != second || b.Kind != "branch" {
		t.Errorf("branch main = %+v, want %s as a branch", b, second)
	}
	if b := branches["agents/NF-1/work"]; b.Sha != third || b.Kind != "branch" {
		t.Errorf("branch agents/NF-1/work = %+v, want %s as a branch", b, third)
	}

	// A tag names the commit it marks. An annotated tag's own object is not a
	// commit, and handing its sha out as "the tag" gives a browser a ref it
	// cannot list a tree or history for.
	tags := refs("/tags")
	for _, name := range []string{"v1.0.0", "v1.0.0-light"} {
		if tg := tags[name]; tg.Sha != repo.Head || tg.Kind != "tag" {
			t.Errorf("tag %s = %+v, want commit %s as a tag", name, tg, repo.Head)
		}
	}

	_, body := get("/commits/main")
	var commits struct {
		Commits []struct{ Sha, Message string }
	}
	_ = json.Unmarshal(body, &commits)
	if len(commits.Commits) != 2 || commits.Commits[0].Sha != second || commits.Commits[1].Sha != repo.Head {
		t.Errorf("history of main = %+v", commits.Commits)
	}
	_, body = get("/commits/" + url.PathEscape("agents/NF-1/work"))
	if !strings.Contains(string(body), third) {
		t.Errorf("history of the agent branch lacks %s: %s", third, body)
	}
	_, body = get("/commits/v1.0.0")
	_ = json.Unmarshal(body, &commits)
	if len(commits.Commits) != 1 || commits.Commits[0].Sha != repo.Head {
		t.Errorf("history at tag v1.0.0 = %+v", commits.Commits)
	}

	_, body = get("/tree/main/")
	for _, want := range []string{`"README.md"`, `"src"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("root tree lacks %s: %s", want, body)
		}
	}
	_, body = get("/tree/main/src/util")
	if !strings.Contains(string(body), `"help.go"`) {
		t.Errorf("tree of src/util lacks help.go: %s", body)
	}
	_, body = get("/tree/v1.0.0/src")
	if !strings.Contains(string(body), `"main.go"`) {
		t.Errorf("tree at tag lacks main.go: %s", body)
	}

	if _, body = get("/blob/main/README.md"); string(body) != "# browse\n\nNow with a second line.\n" {
		t.Errorf("blob README.md at main = %q", body)
	}
	if _, body = get("/blob/v1.0.0/README.md"); string(body) != "# browse\n" {
		t.Errorf("blob README.md at v1.0.0 = %q", body)
	}

	_, body = get("/diff?from=v1.0.0&to=main")
	var diff struct{ Unified string }
	_ = json.Unmarshal(body, &diff)
	if !strings.Contains(diff.Unified, "+Now with a second line.") || !strings.Contains(diff.Unified, "README.md") {
		t.Errorf("diff v1.0.0..main = %q", diff.Unified)
	}
	_, body = get("/diff?from=main&to=" + url.QueryEscape("agents/NF-1/work"))
	_ = json.Unmarshal(body, &diff)
	if !strings.Contains(diff.Unified, "src/new.go") {
		t.Errorf("diff main..agents/NF-1/work = %q", diff.Unified)
	}
}
