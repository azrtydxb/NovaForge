package gates_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/gates"
)

func TestResolveRejectsAmbiguousPolicy(t *testing.T) {
	for name, content := range map[string]string{
		"unknown-parameter":        "name: architecture\nrequired: true\nparams:\n  forbidden_dependences: [frontend -> database]\n",
		"wrong-parameter-type":     "name: architecture\nrequired: true\nparams:\n  forbidden_dependencies: [12]\n",
		"invalid-coverage":         "name: tests\nrequired: true\nparams:\n  minimum_coverage: .nan\n",
		"fractional-documentation": "name: documentation\nrequired: true\nparams:\n  max_undocumented: 1.5\n",
		"unknown-field":            "name: tests\nrequired: true\nrequred: false\n",
		"missing-required":         "name: tests\n",
		"null-required":            "name: tests\nrequired: null\n",
		"multiple-documents":       "name: tests\nrequired: true\n---\nname: security\nrequired: true\n",
		"duplicate-name":           "name: tests\nrequired: true\nname: security\n",
	} {
		t.Run(name, func(t *testing.T) {
			client := &stubGitClient{tree: map[string][]*gitv1.TreeEntry{"main": {{Kind: "blob", Name: "tests.yaml"}}}, blobs: map[string][]byte{"main|.novaforge/gates/tests.yaml": []byte(content)}}
			if _, err := gates.Resolve(context.Background(), client, uuid.New(), uuid.New(), "main", nil); err == nil {
				t.Fatal("ambiguous governance policy accepted")
			}
		})
	}
}

func TestResolveRejectsDuplicateGateDefinitions(t *testing.T) {
	client := &stubGitClient{tree: map[string][]*gitv1.TreeEntry{"main": {{Kind: "blob", Name: "first.yaml"}, {Kind: "blob", Name: "second.yaml"}}}, blobs: map[string][]byte{
		"main|.novaforge/gates/first.yaml":  []byte("name: tests\nrequired: true\n"),
		"main|.novaforge/gates/second.yaml": []byte("name: tests\nrequired: false\n"),
	}}
	if _, err := gates.Resolve(context.Background(), client, uuid.New(), uuid.New(), "main", nil); err == nil {
		t.Fatal("later file weakened earlier required gate")
	}
}

func TestResolveExplicitOptionalGate(t *testing.T) {
	client := &stubGitClient{tree: map[string][]*gitv1.TreeEntry{"main": {{Kind: "blob", Name: "tests.yaml"}}}, blobs: map[string][]byte{"main|.novaforge/gates/tests.yaml": []byte("name: tests\nrequired: false\n")}}
	defs, err := gates.Resolve(context.Background(), client, uuid.New(), uuid.New(), "main", nil)
	if err != nil || len(defs) != 1 || defs[0].Required {
		t.Fatalf("explicit optional policy rejected: %+v %v", defs, err)
	}
}
