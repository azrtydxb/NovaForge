-- work_item_comments is the discussion thread on one Work Item. Comments
-- live here rather than on reviews.Run because a Work Item outlives any
-- single review: an agent recording what it decided, or a person answering
-- it, is commenting on the intent, not on one attempt at satisfying it.
--
-- author_kind distinguishes a person from an agent, because a reader must
-- always be able to tell which of the two wrote a line.
CREATE TABLE IF NOT EXISTS work_item_comments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL,
    work_item_id uuid NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    author_id uuid NOT NULL,
    author_kind text NOT NULL CHECK (author_kind IN ('user', 'agent')),
    body text NOT NULL CHECK (body <> ''),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ON work_item_comments (org_id, work_item_id, created_at);
