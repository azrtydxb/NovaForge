package semanticindex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProducerRealTools(t *testing.T) {
	for _, tc := range []struct {
		lang  string
		files map[string]string
	}{
		{"go", map[string]string{"go.mod": "module fixture.test/semantic\n\ngo 1.26\n", "library.go": "package fixture\nfunc Hello() int {return 1}\n", "caller.go": "package fixture\nfunc Caller() int {return Hello()}\n"}},
		{"typescript", map[string]string{"package.json": `{"name":"fixture","version":"1.0.0"}`, "tsconfig.json": `{"compilerOptions":{"strict":true},"include":["*.ts"]}`, "library.ts": "export function hello(){return 1;}\n", "caller.ts": "import {hello} from './library';\nexport const value=hello();\n"}},
		{"python", map[string]string{"library.py": "def hello():\n    return 1\n", "caller.py": "from library import hello\nvalue=hello()\n"}},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			requireTool(t, producerTools[tc.lang].Name)
			requireTool(t, "scip")
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "source")
			s := fixtureSnapshot()
			s.RootURI = "file://" + dir
			s.Files = map[string][]byte{}
			for p, b := range tc.files {
				s.Files[p] = []byte(b)
			}
			env := toolEnvironment(root)
			artifacts, err := produceArtifacts(context.Background(), s, dir, env)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(artifacts)
			if err != nil {
				t.Fatal(err)
			}
			results, err := ImportArtifacts(context.Background(), s, raw)
			if err != nil {
				t.Fatal(err)
			}
			definitions := map[string]bool{}
			found := false
			for _, d := range results[0].Documents {
				if strings.HasPrefix(d.Path, "library.") {
					for _, o := range d.Occurrences {
						if o.Definition && strings.Contains(strings.ToLower(o.Symbol), "hello") {
							definitions[o.SymbolID] = true
						}
					}
				}
			}
			for _, d := range results[0].Documents {
				if strings.HasPrefix(d.Path, "caller.") {
					for _, o := range d.Occurrences {
						if !o.Definition && definitions[o.SymbolID] {
							found = true
						}
					}
				}
			}
			if !found || results[0].AbsenceSafe {
				t.Fatalf("exact cross-file producer evidence missing: %+v", results)
			}
			for p, b := range s.Files {
				got, err := os.ReadFile(filepath.Join(dir, p))
				if err != nil || string(got) != string(b) {
					t.Fatal("tool changed source")
				}
			}
		})
	}
}
