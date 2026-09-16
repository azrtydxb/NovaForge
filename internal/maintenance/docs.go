package maintenance

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// scanDocs detects the documentation_drift kind: a context document (from
// a repository's .novaforge/context directory) that references a symbol
// absent from this repository's engineering graph. This is a drift candidate,
// not proof of deletion: an incomplete or stale index can also omit a symbol.
//
// A nil Graph or an empty ContextDocs means there is nothing to check —
// reported as no findings, not an error.
func scanDocs(ctx context.Context, in ScanInput) ([]Finding, error) {
	if in.Graph == nil || len(in.ContextDocs) == 0 {
		return nil, nil
	}

	var findings []Finding
	for _, doc := range in.ContextDocs {
		missing, err := in.Graph.MissingSymbols(ctx, in.OrgID, in.RepoID, doc.ReferencedSymbols)
		if err != nil {
			return nil, fmt.Errorf("maintenance: documentation drift scan: %s: %w", doc.Path, err)
		}
		if len(missing) == 0 {
			continue
		}
		sort.Strings(missing)
		findings = append(findings, Finding{
			Kind:         "documentation_drift",
			Title:        fmt.Sprintf("%s references symbols absent from this repository's graph", doc.Path),
			Detail:       fmt.Sprintf("missing: %s", strings.Join(missing, ", ")),
			Severity:     "low",
			Paths:        []string{doc.Path},
			ProposedType: "documentation",
		})
	}
	return findings, nil
}
