package webhooks_test

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestGitPlatformStartsTheDeliveryWorker asserts the seam, not the component.
//
// This repository's most common defect by a wide margin is a worker that is
// written, unit-tested and started by nothing: ClaimJob, Dispatch, the swarm
// scheduler, the maintenance scanners and the auto-merger were all like this.
// The failure is silent — a push is consumed, acked, and no hook ever fires,
// which is indistinguishable from "nobody registered a hook".
//
// So this type-checks cmd/git-platform and insists on a call to
// (*webhooks.Worker).Run somewhere in it. Type information is what makes the
// assertion worth anything: a grep for "Worker" would be satisfied by a comment,
// and a grep for ".Run(" by any other type's Run.
func TestGitPlatformStartsTheDeliveryWorker(t *testing.T) {
	root := repoRoot(t)
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:  root,
	}
	pkgs, err := packages.Load(cfg, "github.com/novaforge/novaforge/cmd/git-platform")
	if err != nil {
		t.Fatalf("load cmd/git-platform: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("cmd/git-platform did not load")
	}

	started := false
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			t.Fatalf("load cmd/git-platform: %v", pkg.Errors[0])
		}
		for _, f := range pkg.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Run" {
					return true
				}
				s := pkg.TypesInfo.Selections[sel]
				if s == nil {
					return true
				}
				recv := s.Recv()
				if p, ok := recv.(*types.Pointer); ok {
					recv = p.Elem()
				}
				named, ok := types.Unalias(recv).(*types.Named)
				if !ok || named.Obj().Pkg() == nil {
					return true
				}
				if named.Obj().Name() == "Worker" &&
					named.Obj().Pkg().Path() == "github.com/novaforge/novaforge/internal/webhooks" {
					started = true
				}
				return true
			})
		}
	}
	if !started {
		t.Fatal("cmd/git-platform never calls (*webhooks.Worker).Run: no push would ever reach a registered hook")
	}
}

// repoRoot locates the repository from this test file's own path, so the test
// does not depend on where `go test` was invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}
