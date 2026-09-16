package maintenance

import (
	"context"
	"fmt"
)

// scanDeadCode reports graph candidates with no recorded dependent or test.
// Absence of a graph edge is not proof that deleting the symbol is safe:
// dynamic consumers and external callers may not be represented.
func scanDeadCode(ctx context.Context, in ScanInput) ([]Finding, error) {
	if in.Graph == nil {
		return nil, nil
	}
	nodes, err := in.Graph.UnreferencedSymbols(ctx, in.OrgID, in.RepoID)
	if err != nil {
		return nil, fmt.Errorf("maintenance: dead code scan: %w", err)
	}
	var findings []Finding
	for _, n := range nodes {
		findings = append(findings, Finding{
			Kind: "dead_code", Title: fmt.Sprintf("%s appears unused", n.Key),
			Detail:   fmt.Sprintf("%s has no recorded dependent or test reference in the engineering graph; verify dynamic and external consumers before removal", n.Key),
			Severity: "low", Paths: []string{n.Key}, ProposedType: "tech_debt",
		})
	}
	return findings, nil
}
