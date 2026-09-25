package semanticindex

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLSPRealGoplsExactReceiver(t *testing.T) {
	requireTool(t, "gopls")
	s, dir := toolSnapshot(t, map[string]string{
		"go.mod":    "module fixture.test/semantic\n\ngo 1.26\n",
		"types.go":  "package fixture\ntype One struct{}\nfunc (One) Value() int { return 1 }\ntype Two struct{}\nfunc (Two) Value() int { return 2 }\n",
		"caller.go": "package fixture\nfunc Caller() int { return (One{}).Value() }\n",
	})
	result := goplsDefinitions(t, s, dir, toolEnvironment(dir), Query{Path: "caller.go", Language: "go", Position: Position{1, strings.Index(strings.Split(string(s.Files["caller.go"]), "\n")[1], "Value")}})
	if result.AbsenceSafe || len(result.Resolutions) != 1 || len(result.Resolutions[0].Targets) != 1 {
		t.Fatalf("unexpected LSP result: %+v", result)
	}
	target := result.Resolutions[0].Targets[0]
	if target.Path != "types.go" || target.Range.Start.Line != 2 || target.ContentHash != digest(s.Files["types.go"]) {
		t.Fatalf("gopls did not resolve exact One.Value receiver: %+v", target)
	}
	if result.SnapshotDigest == "" || result.Revision != s.Revision {
		t.Fatal("LSP evidence lost snapshot binding")
	}
	t.Logf("gopls resolved One.Value only, revision %s", result.Revision)
}

func TestLSPRealGoWorkspaceAndLocalReplacement(t *testing.T) {
	requireTool(t, "gopls")
	for _, workspace := range []bool{false, true} {
		name := "local-replace"
		if workspace {
			name = "go-work"
		}
		t.Run(name, func(t *testing.T) {
			files := map[string]string{
				"go.mod":            "module fixture.test/app\n\ngo 1.26\nrequire fixture.test/lib v0.0.0\nreplace fixture.test/lib => ./nested\n",
				"caller.go":         "package app\nimport \"fixture.test/lib\"\nfunc Caller() int { return lib.Hello() }\n",
				"nested/go.mod":     "module fixture.test/lib\n\ngo 1.26\n",
				"nested/library.go": "package lib\nfunc Hello() int { return 1 }\n",
			}
			if workspace {
				files["go.mod"] = "module fixture.test/app\n\ngo 1.26\n"
				files["go.work"] = "go 1.26\nuse (\n .\n ./nested\n)\n"
			}
			s, dir := toolSnapshot(t, files)
			env := toolEnvironment(dir)
			if workspace {
				for i, value := range env {
					if strings.HasPrefix(value, "GOWORK=") {
						env[i] = "GOWORK=" + filepath.Join(dir, "go.work")
					}
				}
			}
			result := goplsDefinitions(t, s, dir, env, Query{Path: "caller.go", Language: "go", Position: Position{2, strings.Index(strings.Split(files["caller.go"], "\n")[2], "Hello")}})
			if len(result.Resolutions) != 1 || len(result.Resolutions[0].Targets) != 1 || result.Resolutions[0].Targets[0].Path != "nested/library.go" || result.Resolutions[0].Targets[0].Range.Start.Line != 1 {
				t.Fatalf("compiler did not resolve exact nested-module target: %+v", result)
			}
			if result.AbsenceSafe {
				t.Fatal("positive cross-module resolution is not absence evidence")
			}
			t.Logf("%s exact nested module definition verified at %s", name, result.Revision)
		})
	}
}

func goplsDefinitions(t *testing.T, s Snapshot, dir string, env []string, query Query) DefinitionResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	version := strings.TrimSpace(string(runTool(t, dir, "gopls", "version")))
	manifest, err := json.Marshal(struct {
		Version   string
		Args, Env []string
	}{version, []string{"gopls", "serve"}, env})
	if err != nil {
		t.Fatal(err)
	}
	s.ExecutionDigest = digest(manifest)
	cmd := exec.CommandContext(ctx, "gopls", "serve")
	cmd.Dir, cmd.Env = dir, env
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	e := fixtureEvidence(t, s)
	e.Tool, e.ToolVersion = "gopls", version
	result, err := ResolveDefinitions(ctx, &pipeTransport{out, in}, s, e, []Query{query})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExecutionDigest != s.ExecutionDigest {
		t.Fatal("execution context evidence lost")
	}
	return result
}

type pipeTransport struct {
	io.ReadCloser
	io.WriteCloser
}

func (p *pipeTransport) Close() error {
	err := p.ReadCloser.Close()
	_ = p.WriteCloser.Close()
	return err
}

func TestLSPCancellationClosesTransport(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	s := fixtureSnapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ResolveDefinitions(ctx, client, s, fixtureEvidence(t, s), []Query{{Path: "a.ts", Language: "typescript"}})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("blocked LSP transport did not cancel: %v", err)
	}
}
