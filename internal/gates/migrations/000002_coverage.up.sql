-- Preserve absence for old measurements; never reconstruct from display text.
ALTER TABLE gates.gate_evaluations
    ADD COLUMN repo_id UUID NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    ADD COLUMN coverage_percent DOUBLE PRECISION CHECK (coverage_percent >= 0 AND coverage_percent <= 100);
CREATE INDEX gate_coverage_history ON gates.gate_evaluations (org_id, repo_id, evaluated_at DESC, id DESC) WHERE gate = 'tests';
