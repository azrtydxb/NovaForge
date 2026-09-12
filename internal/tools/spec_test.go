package tools_test

import (
	"encoding/json"
	"testing"

	"github.com/novaforge/novaforge/internal/tools"
)

// TestSpecsCoverEveryTool pins that every registered tool is described to
// the model. A tool with no spec is offered under its own name and an open
// object schema, which is what every tool used to get: the model then
// guesses its arguments, and guesses wrong. git.commit's "files" is an
// object mapping path to content; the model sent a string every time, and
// every commit an agent attempted failed to unmarshal.
func TestSpecsCoverEveryTool(t *testing.T) {
	for _, name := range tools.KnownToolNames() {
		spec, ok := tools.Specs[name]
		if !ok {
			t.Errorf("tool %q is registered but never described to the model", name)
			continue
		}
		if spec.Description == "" || spec.Description == name {
			t.Errorf("tool %q has no description beyond its own name", name)
		}
		var schema map[string]any
		if err := json.Unmarshal(spec.Schema, &schema); err != nil {
			t.Errorf("tool %q has an unparseable schema: %v", name, err)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("tool %q's schema is not an object schema", name)
		}
		if _, ok := schema["properties"]; !ok {
			t.Errorf("tool %q's schema declares no properties, so it says nothing about its arguments", name)
		}
	}
}

// TestSpecsDescribeNoToolThatDoesNotExist pins the other direction: a spec
// for a tool nobody registers is a promise to the model that cannot be kept.
func TestSpecsDescribeNoToolThatDoesNotExist(t *testing.T) {
	known := map[string]bool{}
	for _, n := range tools.KnownToolNames() {
		known[n] = true
	}
	for name := range tools.Specs {
		if !known[name] {
			t.Errorf("spec describes %q, but no such tool is registered", name)
		}
	}
}

// TestGitCommitSchemaDeclaresFilesAsAnObject pins the specific shape the
// model kept getting wrong.
func TestGitCommitSchemaDeclaresFilesAsAnObject(t *testing.T) {
	var schema struct {
		Properties struct {
			Files struct {
				Type                 string `json:"type"`
				AdditionalProperties struct {
					Type string `json:"type"`
				} `json:"additionalProperties"`
			} `json:"files"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tools.Specs["git.commit"].Schema, &schema); err != nil {
		t.Fatalf("parse git.commit schema: %v", err)
	}
	if schema.Properties.Files.Type != "object" {
		t.Fatalf("git.commit's files is declared %q, not an object", schema.Properties.Files.Type)
	}
	if schema.Properties.Files.AdditionalProperties.Type != "string" {
		t.Fatal("git.commit's files does not say its values are file contents")
	}
}
