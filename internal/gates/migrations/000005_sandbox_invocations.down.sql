-- Removing even settled evidence or admission tombstones changes authority.
-- Rollback requires an explicit, separately reviewed evidence-preserving plan.
DO $$ BEGIN
    RAISE EXCEPTION 'sandbox journal rollback refused: retain obligations, evidence and deletion fences';
END $$;
