package gates

import (
	"reflect"
	"sort"
	"testing"
)

func names(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestManifestParsersFindEveryDeclaredDependency pins what "adds a
// dependency" can see in each ecosystem. A parser that misses a form — a
// single-line go require, a devDependency, a Cargo table dependency — is a
// dependency that joins the build with no policy applied to it.
func TestManifestParsersFindEveryDeclaredDependency(t *testing.T) {
	cases := []struct {
		name    string
		parse   func([]byte) (map[string]bool, error)
		content string
		want    []string
	}{
		{"go.mod", parseGoMod,
			"module x\n\ngo 1.22\n\nrequire example.com/one v1.0.0\n\nrequire (\n\texample.com/two v0.1.0 // indirect\n\t// example.com/commented v1\n)\n",
			[]string{"example.com/one", "example.com/two"}},
		{"package.json", parsePackageJSON,
			`{"name":"x","dependencies":{"left-pad":"1"},"devDependencies":{"jest":"29"},"scripts":{"test":"jest"}}`,
			[]string{"jest", "left-pad"}},
		{"requirements.txt", parseRequirements,
			"# pinned\nRequests==2.31\nflask_cors>=4 ; python_version > '3'\n-r other.txt\nnumpy[extra]\n",
			[]string{"flask-cors", "numpy", "requests"}},
		{"Cargo.toml", parseCargoToml,
			"[package]\nname = \"x\"\n\n[dependencies]\nserde = \"1\"\n\n[dev-dependencies]\ntokio = { version = \"1\" }\n\n[dependencies.rand]\nversion = \"0.8\"\n\n[target.'cfg(unix)'.dependencies]\nlibc = \"0.2\"\n",
			[]string{"libc", "rand", "serde", "tokio"}},
	}
	for _, c := range cases {
		got, err := c.parse([]byte(c.content))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(names(got), c.want) {
			t.Errorf("%s: got %v, want %v", c.name, names(got), c.want)
		}
	}
}

// TestSchemaPaths pins which paths count as the database schema.
func TestSchemaPaths(t *testing.T) {
	for p, want := range map[string]bool{
		"migrations/000002_x.up.sql":           true,
		"internal/ci/migrations/embed.go":      true,
		"db/migrate/20240101_add.rb":           true,
		"prisma/schema.prisma":                 true,
		"app/alembic/versions/abc_add.py":      true,
		"queries/report.sql":                   true,
		"internal/ci/scheduler.go":             false,
		"docs/migrating-from-github.md":        false,
		".novaforge/gates/tests.yaml":          false,
		"web/src/screens/MigrationsScreen.tsx": false,
	} {
		if got := isSchemaPath(p); got != want {
			t.Errorf("isSchemaPath(%q) = %v, want %v", p, got, want)
		}
	}
}
