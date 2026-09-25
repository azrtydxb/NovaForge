package semanticindex

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These fixtures invoke installed compilers/indexers, not a semantic-output
// double. They contain only test-owned source, never an untrusted repository.
func TestSCIPRealTools(t *testing.T) {
	for _, tc := range []struct {
		name, tool, definition, caller string
		files                          map[string]string
		args                           []string
	}{
		{"typescript", "scip-typescript", "a space.ts", "caller.ts", map[string]string{
			"package.json":  `{"name":"semantic-fixture","version":"1.0.0"}`,
			"tsconfig.json": `{"compilerOptions":{"strict":true},"include":["*.ts"]}`,
			"a space.ts":    "export function hello() { return 1; }\n",
			"caller.ts":     "import {hello as renamed} from './a space';\nexport const value = renamed();\n",
		}, []string{"index", "--no-progress-bar"}},
		{"python", "scip-python", "library.py", "caller.py", map[string]string{
			"library.py": "def hello():\n    return 1\n",
			"caller.py":  "from library import hello as renamed\nvalue = renamed()\n",
		}, []string{"index", "--project-name", "semantic-fixture", "--project-version", "1", "--quiet"}},
		{"go", "scip-go", "library.go", "caller.go", map[string]string{
			"go.mod":     "module fixture.test/semantic\n\ngo 1.26\n",
			"library.go": "package fixture\nfunc Hello() int { return 1 }\n",
			"caller.go":  "package fixture\nfunc Caller() int { return Hello() }\n",
		}, []string{"index"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireTool(t, tc.tool)
			requireTool(t, "scip")
			s, dir := toolSnapshot(t, tc.files)
			runTool(t, dir, tc.tool, tc.args...)
			data := runTool(t, dir, "scip", "print", "--json", filepath.Join(dir, "index.scip"))
			var raw scipIndex
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			e := fixtureEvidence(t, s)
			e.Tool, e.ToolVersion = raw.Metadata.ToolInfo.Name, raw.Metadata.ToolInfo.Version
			result, err := ImportSCIP(context.Background(), s, e, bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			definitions := map[string]bool{}
			for _, d := range result.Documents {
				if d.Path == tc.definition {
					for _, o := range d.Occurrences {
						if o.Definition && strings.Contains(strings.ToLower(o.Symbol), "hello") {
							definitions[o.SymbolID] = true
						}
					}
				}
			}
			found := false
			for _, d := range result.Documents {
				if d.Path == tc.caller {
					for _, o := range d.Occurrences {
						found = found || (!o.Definition && definitions[o.SymbolID])
					}
				}
			}
			if !found {
				t.Fatalf("real %s index lost cross-file exact definition reference: %+v", tc.tool, result)
			}
			t.Logf("%s %s: %d documents, revision %s, exact cross-file reference verified", e.Tool, e.ToolVersion, len(result.Documents), result.Revision)
		})
	}
}

func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		if os.Getenv("NOVAFORGE_REQUIRE_SEMANTIC_TOOLS") == "1" {
			t.Fatalf("required semantic test tool %s unavailable: %v", name, err)
		}
		t.Skipf("real-tool regression unavailable: %s: %v", name, err)
	}
}

func runTool(t *testing.T, dir, tool string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Dir = dir
	cmd.Env = toolEnvironment(dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s\n%s", tool, args, err, data, stderr.String())
	}
	return data
}

func toolEnvironment(home string) []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + os.TempDir(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
}

func toolSnapshot(t *testing.T, files map[string]string) (Snapshot, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := fixtureSnapshot()
	s.RootURI = (&url.URL{Scheme: "file", Path: dir}).String()
	s.Files = map[string][]byte{}
	for p, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		s.Files[p] = []byte(content)
	}
	runTool(t, dir, "git", "init", "-q")
	runTool(t, dir, "git", "add", "--all")
	runTool(t, dir, "git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "semantic fixture")
	s.Revision = strings.TrimSpace(string(runTool(t, dir, "git", "rev-parse", "HEAD")))
	return s, dir
}
