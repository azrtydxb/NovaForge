DROP INDEX gates.gate_coverage_history;
ALTER TABLE gates.gate_evaluations DROP COLUMN coverage_percent, DROP COLUMN repo_id;
