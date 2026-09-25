package repoconfig_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/repoconfig"
)

// Exercise Load, not just a decoder: malformed governance must discard the
// entire configuration, including an otherwise valid project read earlier.
func TestLoadRejectsAmbiguousDocuments(t *testing.T) {
	for _, tc := range []struct{ name, path, text string }{
		{"project empty document", ".novaforge/project.yaml", "# present but no policy mapping\n"},
		{"project null document", ".novaforge/project.yaml", "null\n"},
		{"agent null document", ".novaforge/agents/test.yaml", "null\n"},
		{"project trailing policy", ".novaforge/project.yaml", "name: example\n---\ngates: [security]\n"},
		{"project empty second document", ".novaforge/project.yaml", "name: example\n---\n"},
		{"project unknown gate", ".novaforge/project.yaml", "name: example\ngates: [testz]\n"},
		{"project empty gate", ".novaforge/project.yaml", "name: example\ngates: ['']\n"},
		{"project duplicate gate", ".novaforge/project.yaml", "name: example\ngates: [tests, tests]\n"},
		{"agent trailing restriction", ".novaforge/agents/test.yaml", "name: test\n---\ntools: []\n"},
		{"agent malformed trailing document", ".novaforge/agents/test.yaml", "name: test\n---\ntools: [\n"},
		{"gate unknown parameter", ".novaforge/gates/test.yaml", "name: architecture\nrequired: true\nparams:\n  forbidden_dependences: [frontend -> database]\n"},
		{"gate wrong parameter type", ".novaforge/gates/test.yaml", "name: architecture\nrequired: true\nparams:\n  forbidden_dependencies: [12]\n"},
		{"gate invalid coverage", ".novaforge/gates/test.yaml", "name: tests\nrequired: true\nparams:\n  minimum_coverage: .nan\n"},
		{"gate fractional documentation", ".novaforge/gates/test.yaml", "name: documentation\nrequired: true\nparams:\n  max_undocumented: 1.5\n"},
		{"gate unknown field", ".novaforge/gates/test.yaml", "name: tests\nrequired: true\nrequird: false\n"},
		{"gate trailing policy", ".novaforge/gates/test.yaml", "name: tests\nrequired: false\n---\nrequired: true\n"},
		{"gate duplicate key", ".novaforge/gates/test.yaml", "name: tests\nrequired: false\nrequired: true\n"},
		{"gate missing required", ".novaforge/gates/test.yaml", "name: tests\n"},
		{"gate null required", ".novaforge/gates/test.yaml", "name: tests\nrequired: null\n"},
		{"gate unknown name", ".novaforge/gates/test.yaml", "name: nonexistent\nrequired: true\n"},
		{"gate missing name", ".novaforge/gates/test.yaml", "required: true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &stubGitClient{blobs: map[string][]byte{".novaforge/project.yaml": []byte("name: valid\n"), tc.path: []byte(tc.text)}, trees: map[string][]*gitv1.TreeEntry{}}
			if tc.path != ".novaforge/project.yaml" {
				dir := tc.path[:strings.LastIndex(tc.path, "/")]
				client.trees[dir] = []*gitv1.TreeEntry{{Kind: "blob", Name: "test.yaml"}}
			}
			cfg, err := repoconfig.Load(context.Background(), client, uuid.New(), uuid.New(), "main")
			if err == nil || !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("expected path-qualified rejection, got config=%+v error=%v", cfg, err)
			}
			if !reflect.DeepEqual(cfg, repoconfig.Config{}) {
				t.Fatalf("malformed governance returned partial configuration: %+v", cfg)
			}
		})
	}
}

func TestLoadRejectsDuplicateGateDefinitions(t *testing.T) {
	client := &stubGitClient{blobs: map[string][]byte{
		".novaforge/gates/first.yaml":  []byte("name: tests\nrequired: true\n"),
		".novaforge/gates/second.yaml": []byte("name: tests\nrequired: false\n"),
	}, trees: map[string][]*gitv1.TreeEntry{".novaforge/gates": {
		{Kind: "blob", Name: "first.yaml"}, {Kind: "blob", Name: "second.yaml"},
	}}}
	cfg, err := repoconfig.Load(context.Background(), client, uuid.New(), uuid.New(), "main")
	if err == nil || !strings.Contains(err.Error(), "duplicate gate") || !reflect.DeepEqual(cfg, repoconfig.Config{}) {
		t.Fatalf("duplicate policy accepted: %+v, %v", cfg, err)
	}
}

func TestLoadAcceptsExplicitGatePolicy(t *testing.T) {
	client := &stubGitClient{blobs: map[string][]byte{
		".novaforge/gates/tests.yaml": []byte("name: tests\nrequired: true\nparams: {}\n# trailing comments are not another document\n"),
	}, trees: map[string][]*gitv1.TreeEntry{".novaforge/gates": {{Kind: "blob", Name: "tests.yaml"}}}}
	cfg, err := repoconfig.Load(context.Background(), client, uuid.New(), uuid.New(), "main")
	if err != nil || len(cfg.Gates) != 1 || cfg.Gates[0].Name != "tests" || !cfg.Gates[0].Required {
		t.Fatalf("explicit gate policy rejected: %+v, %v", cfg, err)
	}
}
