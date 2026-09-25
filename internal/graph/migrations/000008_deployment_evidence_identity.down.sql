-- Refuse to discard an identity fence on rollback once any event was observed.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM deployment_evidence_identities) THEN
        RAISE EXCEPTION 'deployment evidence identities must survive rollback';
    END IF;
END $$;
DROP TABLE deployment_evidence_identities;
