-- An approval raised for a change is about one head of that change: a push
-- after the decision is a different change and needs its own. The author is
-- kept so the person who made a change can never approve it, and the reason
-- and paths say, in words and files, why the platform asked.
ALTER TABLE approvals.approval_requests
  ADD COLUMN IF NOT EXISTS head_sha text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS reason text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS paths text[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS comment text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS author_id uuid,
  ADD COLUMN IF NOT EXISTS author_kind text NOT NULL DEFAULT '';

-- One request per action per head: the controller raises requests whenever
-- it is asked, and concurrent asks must converge on the same row.
CREATE UNIQUE INDEX IF NOT EXISTS approval_requests_run_action_head
  ON approvals.approval_requests (run_id, action, head_sha)
  WHERE head_sha <> '';
