-- When a Work Item completed, so "how much finished today" is an answer the
-- platform has rather than one a client guesses. The row carried created_at and
-- a state, and nothing recorded the transition.
--
-- A trigger maintains it instead of each writer, because five separate
-- statements move an item's state (SetState, TransitionState, the editor, the
-- dependency resolver and admission) and a sixth added later would silently stop
-- counting. This way the invariant belongs to the table, not to whoever
-- remembers it.
ALTER TABLE work_items ADD COLUMN done_at timestamptz;

CREATE FUNCTION work.work_items_done_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state = 'done' THEN
  -- Reaching done records now; staying done keeps the completion it already has,
  -- so an unrelated update does not look like a fresh completion.
  IF TG_OP = 'INSERT' OR OLD.state IS DISTINCT FROM 'done' THEN
   NEW.done_at := clock_timestamp();
  END IF;
 ELSE
  -- A reopened item has not completed. Leaving the old timestamp would count it
  -- again the next time it finished, and count it while it was still open.
  NEW.done_at := NULL;
 END IF;
 RETURN NEW;
END $$;

CREATE TRIGGER work_items_done_at BEFORE INSERT OR UPDATE ON work_items
 FOR EACH ROW EXECUTE FUNCTION work.work_items_done_at();

-- Items already done before this migration have no recorded completion; they are
-- left NULL rather than back-dated to now, which would report a day's work that
-- did not happen.
CREATE INDEX work_items_done_at_idx ON work_items(org_id, done_at) WHERE done_at IS NOT NULL;
