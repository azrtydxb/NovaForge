package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/tools"
)

// fakeGitClient is an in-process test double for the git service adapter;
// Commit records whether it was ever invoked, so a capability refusal that
// still reached the handler would be visible.
type fakeGitClient struct {
	committed bool
}

func (f *fakeGitClient) Search(ctx context.Context, repo, query string) ([]tools.SearchHit, error) {
	return nil, nil
}
func (f *fakeGitClient) ReadFile(ctx context.Context, repo, ref, path string) ([]byte, error) {
	return nil, nil
}
func (f *fakeGitClient) GetSymbol(ctx context.Context, repo, ref, symbol string) (tools.SymbolInfo, error) {
	return tools.SymbolInfo{}, nil
}
func (f *fakeGitClient) GetDependencies(ctx context.Context, repo, ref, path string) ([]string, error) {
	return nil, nil
}
func (f *fakeGitClient) Diff(ctx context.Context, repo, from, to string) (string, error) {
	return "", nil
}
func (f *fakeGitClient) Commit(ctx context.Context, repo, branch, message string, files map[string]string) (string, error) {
	f.committed = true
	return "deadbeef", nil
}

func TestGitCommitRefusesOutOfScopeBranch(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool := auditPool(t)
	audit := agents.NewAuditLog(pool)
	store := agents.NewStore(pool)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := newTestRun(t, ctx, store, orgID)

	git := &fakeGitClient{}
	rt := tools.Runtime{
		Budget: agents.NewBudget(time.Hour, 1000, 1000),
		Grant:  capability.Grant{WriteBranch: "agents/NF-1/"},
		Git:    git,
	}
	reg := tools.NewRegistry(rt, audit)

	args, _ := json.Marshal(map[string]any{
		"repo":    "example",
		"branch":  "main",
		"message": "hack the mainline",
		"files":   map[string]string{"a.txt": "x"},
	})

	_, err := reg.Call(ctx, run.ID, "git.commit", args)
	if err == nil {
		t.Fatal("expected error committing to an out-of-scope branch")
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), "not permitted")
	}
	if git.committed {
		t.Fatal("handler executed a commit despite the capability refusal")
	}
}

func TestWorkspaceWriteFileRejectsTraversal(t *testing.T) {
	pool := auditPool(t)
	audit := agents.NewAuditLog(pool)
	store := agents.NewStore(pool)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := newTestRun(t, ctx, store, orgID)

	root := t.TempDir()
	rt := tools.Runtime{
		Budget:        agents.NewBudget(time.Hour, 1000, 1000),
		WorkspaceRoot: root,
	}
	reg := tools.NewRegistry(rt, audit)

	args, _ := json.Marshal(map[string]string{
		"path":    "../../etc/passwd",
		"content": "pwned",
	})

	_, callErr := reg.Call(ctx, run.ID, "workspace.write_file", args)
	if callErr == nil {
		t.Fatal("expected error for a path escaping the workspace root")
	}
	if !strings.Contains(callErr.Error(), "outside workspace") {
		t.Fatalf("error = %q, want it to contain %q", callErr.Error(), "outside workspace")
	}
}
