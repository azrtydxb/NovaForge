-- Intent is committed with the owner transition, including worker/recovery paths.
CREATE TABLE event_outbox (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX event_outbox_pending ON event_outbox(created_at,id);
CREATE FUNCTION enqueue_repository_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE event_id uuid := gen_random_uuid(); previous text := '';
BEGIN
    IF NEW.status NOT IN ('success','failure','cancelled') THEN RETURN NEW; END IF;
    IF TG_OP = 'UPDATE' AND NEW.status IS NOT DISTINCT FROM OLD.status THEN RETURN NEW; END IF;
    IF TG_OP = 'UPDATE' THEN previous := OLD.status; END IF;
    INSERT INTO ci.event_outbox(id,org_id,payload) VALUES(event_id,NEW.org_id,
      jsonb_build_object('version',1,'event_id',event_id,'event','ci_result',
        'org_id',NEW.org_id,'repo_id',NEW.repo_id,'run_id',NEW.id,
        'state',NEW.status,'previous_state',previous,'at',clock_timestamp()));
    RETURN NEW;
END;
$$;
CREATE TRIGGER repository_event AFTER INSERT OR UPDATE OF status ON workflow_runs
FOR EACH ROW EXECUTE FUNCTION enqueue_repository_event();
