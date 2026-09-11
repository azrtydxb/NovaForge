package maintenance

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// scanDocs detects the documentation_drift kind: a context document (from
// a repository's .novaforge/context directory) that references a symbol
// no longer present in the engineering graph — the document has drifted
// out of sync with the code it describes.
//
// A nil Graph or an empty ContextDocs means there is nothing to check —
// reported as no findings, not an error.
func scanDocs(ctx context.Context, in ScanInput) ([]Finding, error) {
	if in.Graph == nil || len(in.ContextDocs) == 0 {
		return nil, nil
	}

	var findings []Finding
	for _, doc := range in.ContextDocs {
		missing, err := missingSymbols(ctx, in, doc.ReferencedSymbols)
		if err != nil {
			return nil, fmt.Errorf("maintenance: documentation drift scan: %s: %w", doc.Path, err)
		}
		if len(missing) == 0 {
			continue
		}
		sort.Strings(missing)
		findings = append(findings, Finding{
			Kind:         "documentation_drift",
			Title:        fmt.Sprintf("%s references symbols that no longer exist", doc.Path),
			Detail:       fmt.Sprintf("missing: %s", strings.Join(missing, ", ")),
			Severity:     "low",
			Paths:        []string{doc.Path},
			ProposedType: "documentation",
		})
	}
	return findings, nil
}

// missingSymbols returns the subset of symbols that have no matching
// "symbol" node in the engineering graph for in.OrgID.
func missingSymbols(ctx context.Context, in ScanInput, symbols []string) ([]string, error) {
	var missing []string
	for _, sym := range symbols {
		var exists bool
		err := in.Graph.Pool().QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM graph.graph_nodes
				WHERE org_id = $1 AND kind = 'symbol' AND key = $2
			)`,
			in.OrgID, sym,
		).Scan(&exists)
		if err != nil {
			return nil, fmt.Errorf("check symbol %q: %w", sym, err)
		}
		if !exists {
			missing = append(missing, sym)
		}
	}
	return missing, nil
}
