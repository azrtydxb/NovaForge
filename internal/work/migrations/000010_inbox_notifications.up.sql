-- inbox_notifications is the unified Inbox: one row per recipient per
-- observable action worth a person's attention (a review requested, an
-- approval awaiting a decision, a gate that failed, a maintenance proposal).
-- Notifications are addressed to a user id and rows are per-recipient: the
-- same event fans out into as many rows as it has addressees, because done,
-- saved and snoozed are each recipient's own state and must never leak from
-- one reader to another.
--
-- Reasons map onto the design's REASON filters. Every reason is written by a
-- publisher that observed the action; none is derived. Two of the design's
-- reasons have no publisher on this platform yet and are declared here so the
-- schema is honest about what may appear:
--   * "mention" needs comment mentions, which the platform's comments do not
--     have — a comment is a body of text with no recipient parsing.
--   * "agent_question" needs an agent asking a person a structured question,
--     which has no RPC anywhere. The GUI renders the filter statically and
--     shows an honest empty state under it rather than inventing rows.
-- Both are inserted by exactly one path (Store.PublishInbox), so when a
-- publisher for them exists it needs no migration.
CREATE TABLE IF NOT EXISTS inbox_notifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL,
    recipient_id uuid NOT NULL,
    repo_id uuid NOT NULL,
    reason text NOT NULL CHECK (reason IN (
        'review_requested', 'approval', 'gate_failure',
        'maintenance', 'mention', 'agent_question')),
    -- ref is how a person says the subject out loud: "run #212", "NF-318",
    -- "maint-88". It is display text, not a foreign key — the subject lives in
    -- another service's schema and this table does not read across.
    ref text NOT NULL CHECK (ref <> ''),
    title text NOT NULL CHECK (title <> ''),
    body text NOT NULL DEFAULT '',
    actor_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    actor_kind text NOT NULL DEFAULT '',
    actor_name text NOT NULL DEFAULT '',
    -- dedupe_key makes a publisher idempotent. A gate re-evaluated at the same
    -- head, or a review request retried while its row still exists, must not
    -- re-notify: the event happened once. Empty means the publisher chose to
    -- allow repeats.
    dedupe_key text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'inbox' CHECK (state IN ('inbox', 'saved', 'done')),
    -- A snoozed row stays in state 'inbox' but is hidden until snoozed_until,
    -- then returns by itself. Done and saved are states, not timestamps.
    snoozed_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX inbox_notifications_recipient_idx
    ON inbox_notifications (org_id, recipient_id, state, created_at DESC);
-- One notification per (org, recipient, dedupe key): the partial index keeps
-- repeats of the same observable event from stacking up in anyone's inbox.
CREATE UNIQUE INDEX inbox_notifications_dedupe_idx
    ON inbox_notifications (org_id, recipient_id, dedupe_key)
    WHERE dedupe_key <> '';
