package indexing_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/graph"
)

func TestNestedModuleEvidenceAndInternalImports(t *testing.T) {
	git := newFakeGitClient()
	org, repo := uuid.New(), uuid.New()
	root := "module example.com/root\n"
	nested := "module example.com/nested\n"
	files := map[string]string{"go.mod": root, "nested/go.mod": nested, "nested/lib/lib.go": "package lib\nfunc Local() {}\n", "nested/main.go": "package main\nimport \"example.com/nested/lib\"\nfunc main(){lib.Local()}\n"}
	git.head = "first"
	for p, content := range files {
		git.blobs[p+"@first"] = []byte(content)
		git.diffs["4b825dc642cb6eb9a060e54bf8d69288fbee4904..first"] += "diff --git a/" + p + " b/" + p + "\n"
	}
	idx := newIndexer(t, git)
	idx.HMACSecret = testHMACSecret
	evt := events.PushEvent{OrgID: org, RepoID: repo, Ref: "refs/heads/main", NewSHA: "first"}
	if err := idx.HandlePush(context.Background(), evt); err != nil {
		t.Fatal(err)
	}
	var digest string
	if err := idx.Graph.Pool().QueryRow(context.Background(), `SELECT attrs->>'module_hash' FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND kind='file' AND attrs->>'path'='nested/main.go'`, org, repo).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != graph.SourceDigest([]byte(nested)) {
		t.Errorf("nested file attributed to root module: %s", digest)
	}
	var refs int
	if err := idx.Graph.Pool().QueryRow(context.Background(), `SELECT count(*) FROM graph.file_references WHERE org_id=$1 AND repo_id=$2 AND from_path='nested/main.go' AND target_dir='nested/lib' AND target_name='Local'`, org, repo).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if refs != 1 {
		t.Error("nested import did not resolve relative to nested module directory")
	}
	// Changing only nested go.mod must refresh previously indexed Go files.
	git.head = "second"
	nested = "module example.com/renamed\n"
	files["nested/go.mod"] = nested
	for p, content := range files {
		git.blobs[p+"@second"] = []byte(content)
		git.diffs["4b825dc642cb6eb9a060e54bf8d69288fbee4904..second"] += "diff --git a/" + p + " b/" + p + "\n"
	}
	git.diffs["first..second"] = "diff --git a/nested/go.mod b/nested/go.mod\n"
	evt.OldSHA, evt.NewSHA = "first", "second"
	if err := idx.HandlePush(context.Background(), evt); err != nil {
		t.Fatal(err)
	}
	if err := idx.Graph.Pool().QueryRow(context.Background(), `SELECT attrs->>'module_hash' FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND kind='file' AND attrs->>'path'='nested/main.go'`, org, repo).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != graph.SourceDigest([]byte(nested)) {
		t.Error("nested module-only change left unchanged file evidence stale")
	}
}

func TestUnresolvedModuleContextsDoNotClaimCompleteness(t *testing.T) {
	for _, fixture := range []struct {
		name  string
		files map[string]string
	}{
		{"workspace", map[string]string{"go.mod": "module example.com/root\n", "go.work": "go 1.26\nuse .\n", "a.go": goSrc}},
		{"replace", map[string]string{"go.mod": "module example.com/root\nreplace example.com/dep => ./dep\n", "a.go": goSrc}},
		{"boundary", map[string]string{"go.mod": "module example.com/root\n", "a.go": "package p\nimport \"example.com/root/sub\"\nfunc F(){sub.Used()}\n", "sub/go.mod": "module example.com/sub\n", "sub/a.go": "package sub\nfunc Used(){}\n"}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			git := newFakeGitClient()
			git.head = "head"
			org, repo := uuid.New(), uuid.New()
			for p, content := range fixture.files {
				git.blobs[p+"@head"] = []byte(content)
				git.diffs["4b825dc642cb6eb9a060e54bf8d69288fbee4904..head"] += "diff --git a/" + p + " b/" + p + "\n"
			}
			idx := newIndexer(t, git)
			idx.HMACSecret = testHMACSecret
			if err := idx.HandlePush(context.Background(), events.PushEvent{OrgID: org, RepoID: repo, Ref: "refs/heads/main", NewSHA: "head"}); err != nil {
				t.Fatal(err)
			}
			var complete bool
			if err := idx.Graph.Pool().QueryRow(context.Background(), `SELECT coalesce(attrs->>'parse_complete','')='true' FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND kind='file' AND attrs->>'path'='a.go'`, org, repo).Scan(&complete); err != nil {
				t.Fatal(err)
			}
			if complete {
				t.Error("unresolved module selection claimed complete absence evidence")
			}
		})
	}
}

// An import-free source file must still validate its owning main-module identity.
// Main modules may be dotless: remote dependency path rules are too restrictive.
func TestMainModuleIdentityControlsCompleteness(t *testing.T) {
	for _, dir := range []string{"", "nested/"} {
		for _, fixture := range []struct {
			name, module string
			complete     bool
		}{
			{"empty", "module \"\"\n", false},
			{"space", "module \"example.com/bad path\"\n", false},
			{"relative", "module ../bad\n", false},
			{"empty-component", "module \"example.com//bad\"\n", false},
			{"dotless", "module local\n", true},
			{"dotless-path", "module local/project\n", true},
			{"domain", "module example.com/good\n", true},
		} {
			t.Run(dir+fixture.name, func(t *testing.T) {
				git := newFakeGitClient()
				git.head = "head"
				org, repo := uuid.New(), uuid.New()
				modulePath, sourcePath := dir+"go.mod", dir+"a.go"
				files := map[string]string{"go.mod": "module example.com/root\n", modulePath: fixture.module, sourcePath: goSrc}
				for p, content := range files {
					git.blobs[p+"@head"] = []byte(content)
					git.diffs["4b825dc642cb6eb9a060e54bf8d69288fbee4904..head"] += "diff --git a/" + p + " b/" + p + "\n"
				}
				idx := newIndexer(t, git)
				idx.HMACSecret = testHMACSecret
				ctx := context.Background()
				if err := idx.HandlePush(ctx, events.PushEvent{OrgID: org, RepoID: repo, Ref: "refs/heads/main", NewSHA: "head"}); err != nil {
					t.Fatal(err)
				}
				var attrs map[string]string
				if err := idx.Graph.Pool().QueryRow(ctx, `SELECT attrs FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND kind='file' AND attrs->>'path'=$3`, org, repo, sourcePath).Scan(&attrs); err != nil {
					t.Fatal(err)
				}
				complete, present := attrs["parse_complete"]
				if fixture.complete && complete != "true" || !fixture.complete && present {
					t.Errorf("parse_complete = %q (present=%v), want completeness %v", complete, present, fixture.complete)
				}
				if attrs["source_hash"] != graph.SourceDigest([]byte(goSrc)) || attrs["module_hash"] != graph.SourceDigest([]byte(fixture.module)) || attrs["module_path"] != modulePath || attrs["path"] != sourcePath {
					t.Errorf("ordinary file evidence lost: %v", attrs)
				}
				var symbols int
				if err := idx.Graph.Pool().QueryRow(ctx, `SELECT count(*) FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND kind='symbol' AND attrs->>'path'=$3 AND attrs->>'name'='F'`, org, repo, sourcePath).Scan(&symbols); err != nil {
					t.Fatal(err)
				}
				if symbols != 1 || chunkCount(t, idx.Graph.Pool(), ctx, org, sourcePath) == 0 {
					t.Error("ordinary symbol or chunk indexing lost")
				}
			})
		}
	}
}

func TestMovingUnchangedModuleChangesEvidenceAndEdges(t *testing.T) {
	git := newFakeGitClient()
	git.head = "before"
	org, repo := uuid.New(), uuid.New()
	module := "module example.com/move\n"
	files := map[string]string{"go.mod": module, "nested/main.go": "package main\nimport \"example.com/move/lib\"\nfunc main(){lib.Used()}\n", "lib/lib.go": "package lib\nfunc Used() {}\n", "nested/lib/lib.go": "package lib\nfunc Used() {}\n"}
	for p, src := range files {
		git.blobs[p+"@before"] = []byte(src)
		git.diffs["4b825dc642cb6eb9a060e54bf8d69288fbee4904..before"] += "diff --git a/" + p + " b/" + p + "\n"
	}
	idx := newIndexer(t, git)
	idx.HMACSecret = testHMACSecret
	evt := events.PushEvent{OrgID: org, RepoID: repo, Ref: "refs/heads/main", NewSHA: "before"}
	if err := idx.HandlePush(context.Background(), evt); err != nil {
		t.Fatal(err)
	}
	check := func(wantModulePath, wantTarget string) {
		t.Helper()
		var modulePath, hash, source string
		if err := idx.Graph.Pool().QueryRow(context.Background(), `SELECT coalesce(attrs->>'module_path','<missing>'),attrs->>'module_hash',attrs->>'source_hash' FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND kind='file' AND attrs->>'path'='nested/main.go'`, org, repo).Scan(&modulePath, &hash, &source); err != nil {
			t.Fatal(err)
		}
		if modulePath != wantModulePath {
			t.Errorf("module path evidence %q, want %q", modulePath, wantModulePath)
		}
		if hash != graph.SourceDigest([]byte(module)) || source != graph.SourceDigest([]byte(files["nested/main.go"])) {
			t.Error("fixture changed source/module bytes")
		}
		var target string
		if err := idx.Graph.Pool().QueryRow(context.Background(), `SELECT target_dir FROM graph.file_references WHERE org_id=$1 AND repo_id=$2 AND from_path='nested/main.go' AND target_name='Used'`, org, repo).Scan(&target); err != nil {
			t.Fatal(err)
		}
		if target != wantTarget {
			t.Errorf("resolved module target %q, want %q", target, wantTarget)
		}
		var edges int
		if err := idx.Graph.Pool().QueryRow(context.Background(), `SELECT count(*) FROM graph.graph_edges e JOIN graph.graph_nodes src ON src.id=e.from_id JOIN graph.graph_nodes dst ON dst.id=e.to_id WHERE src.org_id=$1 AND src.repo_id=$2 AND src.kind='symbol' AND src.attrs->>'path'='nested/main.go' AND e.kind='depends_on' AND dst.attrs->>'dir'<>$3`, org, repo, wantTarget).Scan(&edges); err != nil {
			t.Fatal(err)
		}
		if edges != 0 {
			t.Error("stale materialized dependency survived module move")
		}
	}
	check("go.mod", "lib")
	delete(files, "go.mod")
	files["nested/go.mod"] = module
	git.head = "after"
	for p, src := range files {
		git.blobs[p+"@after"] = []byte(src)
		git.diffs["4b825dc642cb6eb9a060e54bf8d69288fbee4904..after"] += "diff --git a/" + p + " b/" + p + "\n"
	}
	git.diffs["before..after"] = "diff --git a/go.mod b/go.mod\ndiff --git a/nested/go.mod b/nested/go.mod\n"
	evt.OldSHA, evt.NewSHA = "before", "after"
	if err := idx.HandlePush(context.Background(), evt); err != nil {
		t.Fatal(err)
	}
	check("nested/go.mod", "nested/lib")
}
