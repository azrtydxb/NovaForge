DROP INDEX IF EXISTS approvals.approval_requests_run_action_head;
ALTER TABLE approvals.approval_requests
  DROP COLUMN IF EXISTS author_kind,
  DROP COLUMN IF EXISTS author_id,
  DROP COLUMN IF EXISTS comment,
  DROP COLUMN IF EXISTS paths,
  DROP COLUMN IF EXISTS reason,
  DROP COLUMN IF EXISTS head_sha;
