-- No FK: deleting a job must not delete its external revocation obligation.
CREATE TABLE ci.credential_cleanup (
 org_id uuid NOT NULL,
 job_id uuid NOT NULL,
 attempt_id uuid NOT NULL,
 phase text NOT NULL CHECK (phase IN ('preparing','dispatched','cleanup')),
 ready_at timestamptz NOT NULL DEFAULT now(),
 lease_ids uuid[] NOT NULL DEFAULT '{}',
 PRIMARY KEY (org_id,job_id,attempt_id)
);
CREATE INDEX ON ci.credential_cleanup (phase,ready_at);

CREATE FUNCTION ci.fence_job_credentials() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE organization uuid;
BEGIN
 IF TG_OP = 'UPDATE' THEN
  IF OLD.status IN ('success','failure','cancelled') AND NEW.status NOT IN ('success','failure','cancelled') THEN
   RAISE EXCEPTION 'terminal job cannot resume';
  END IF;
  IF NEW.status NOT IN ('success','failure','cancelled') THEN RETURN NEW; END IF;
 END IF;
 IF cardinality(OLD.secrets)>0 THEN
  SELECT org_id INTO organization FROM ci.workflow_runs WHERE id=OLD.run_id;
  IF organization IS NOT NULL THEN
   INSERT INTO ci.credential_cleanup(org_id,job_id,attempt_id,phase)
    VALUES(organization,OLD.id,'00000000-0000-0000-0000-000000000000','cleanup')
    ON CONFLICT(org_id,job_id,attempt_id) DO UPDATE SET phase='cleanup',ready_at=now();
   UPDATE ci.credential_cleanup SET phase='cleanup',ready_at=now()
    WHERE org_id=organization AND job_id=OLD.id;
  END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER fence_job_credentials BEFORE UPDATE OF status OR DELETE ON ci.workflow_jobs
 FOR EACH ROW EXECUTE FUNCTION ci.fence_job_credentials();

-- A cascading child DELETE can no longer see the parent; capture identities
-- before the parent disappears, in the same transaction as that deletion.
CREATE FUNCTION ci.fence_run_credentials() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO ci.credential_cleanup(org_id,job_id,attempt_id,phase)
  SELECT OLD.org_id,id,'00000000-0000-0000-0000-000000000000','cleanup'
  FROM ci.workflow_jobs WHERE run_id=OLD.id AND cardinality(secrets)>0
  ON CONFLICT(org_id,job_id,attempt_id) DO UPDATE SET phase='cleanup',ready_at=now();
 UPDATE ci.credential_cleanup SET phase='cleanup',ready_at=now()
  WHERE org_id=OLD.org_id AND job_id IN (SELECT id FROM ci.workflow_jobs WHERE run_id=OLD.id);
 RETURN OLD;
END $$;
CREATE TRIGGER fence_run_credentials BEFORE DELETE ON ci.workflow_runs
 FOR EACH ROW EXECUTE FUNCTION ci.fence_run_credentials();

-- Previously finished jobs still need a provider fence after upgrade.
INSERT INTO ci.credential_cleanup(org_id,job_id,attempt_id,phase)
 SELECT r.org_id,j.id,'00000000-0000-0000-0000-000000000000','cleanup'
 FROM ci.workflow_jobs j JOIN ci.workflow_runs r ON r.id=j.run_id
 WHERE cardinality(j.secrets)>0 AND j.status IN ('success','failure','cancelled');
