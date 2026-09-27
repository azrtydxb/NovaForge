package agentrun

import (
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/repoconfig"
	"strings"
	"testing"
)

func TestOpeningKnowledgeRetainsProvenance(t *testing.T) {
	entry := &graphv1.KnowledgeEntry{Id: "entry-id", Key: "DEC-1", Kind: "decision", Title: "VAT rounding", Body: "Round per line", CreatedAt: "2026-09-27T00:00:00Z", SourceRunId: "prior-run-id"}
	brief := renderBrief(agents.Run{}, Plan{}, nil, repoconfig.Project{}, nil, &graphv1.ContextBundle{Knowledge: []*graphv1.KnowledgeEntry{entry}}, nil)
	for _, want := range []string{entry.Id, entry.Key, entry.CreatedAt, entry.SourceRunId, entry.Body} {
		if !strings.Contains(brief, want) {
			t.Errorf("opening context lost provenance %q", want)
		}
	}
}
