package swarm_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/novaforge/novaforge/internal/swarm"
	"github.com/novaforge/novaforge/internal/work"
)

type schemaModel struct {
	stubPlannerModel
	schema json.RawMessage
}

func (m *schemaModel) Generate(ctx context.Context, call provider.Call) (*provider.Response, error) {
	if call.ResponseFormat != nil {
		m.schema = call.ResponseFormat.Schema
	}
	return m.stubPlannerModel.Generate(ctx, call)
}

// Prose alone allowed the live model to put the agent role "test" in type,
// twice. The structured output contract must carry the actual closed set.
func TestDecompositionSchemaConstrainsWorkTypes(t *testing.T) {
	m := &schemaModel{stubPlannerModel: stubPlannerModel{body: ssoDecomposition}}
	p := swarm.NewPlanner(m, nil, ssoRoles)
	if _, err := p.Decompose(context.Background(), work.Item{Key: "NF-1"}, swarm.Bundle{}); err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Elements struct {
				Items struct {
					Properties struct {
						Type struct {
							Enum []string `json:"enum"`
						} `json:"type"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"elements"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(m.schema, &schema); err != nil {
		t.Fatal(err)
	}
	got := schema.Properties.Elements.Items.Properties.Type.Enum
	sort.Strings(got)
	if !reflect.DeepEqual(got, work.SortedTypes()) {
		t.Fatalf("schema types = %v, want %v", got, work.SortedTypes())
	}
}
