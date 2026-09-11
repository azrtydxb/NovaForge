package maintenance

import (
	"context"
	"fmt"
)

// scanDeadCode detects the dead_code kind: symbol nodes in the engineering
// graph with no inbound "called_by" edge (nothing calls them) and no
// outbound "tested_by" edge (nothing exercises them either) — the
// conjunction the spec asks for, since a symbol with test coverage but no
// caller might still be intentional (an exported API awaiting a
// consumer), while a symbol with neither is a strong dead-code signal.
//
// A nil Graph means the scan has no graph to query — reported as no
// findings, not an error, per Scanner's failure-isolation contract.
func scanDeadCode(ctx context.Context, in ScanInput) ([]Finding, error) {
	if in.Graph == nil {
		return nil, nil
	}

	rows, err := in.Graph.Pool().Query(ctx, `
		SELECT n.key
		FROM graph.graph_nodes n
		WHERE n.org_id = $1
		  AND n.kind = 'symbol'
		  AND ($2::uuid IS NULL OR n.repo_id = $2)
		  AND NOT EXISTS (
		    SELECT 1 FROM graph.graph_edges e
		    WHERE e.to_id = n.id AND e.kind = 'called_by'
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM graph.graph_edges e
		    WHERE e.from_id = n.id AND e.kind = 'tested_by'
		  )
		ORDER BY n.key`,
		in.OrgID, nullableUUID(in.RepoID),
	)
	if err != nil {
		return nil, fmt.Errorf("maintenance: dead code scan: %w", err)
	}
	defer rows.Close()

	var findings []Finding
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("maintenance: dead code scan: scan symbol: %w", err)
		}
		findings = append(findings, Finding{
			Kind:         "dead_code",
			Title:        fmt.Sprintf("%s appears unused", key),
			Detail:       fmt.Sprintf("%s has no caller and no test coverage in the engineering graph", key),
			Severity:     "low",
			Paths:        []string{key},
			ProposedType: "tech_debt",
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("maintenance: dead code scan: %w", err)
	}
	return findings, nil
}
