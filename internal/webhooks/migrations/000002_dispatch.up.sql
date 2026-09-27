-- Reserve attempts before network I/O. Redelivery and process restarts share
-- this budget, including attempts whose HTTP acknowledgement was lost.
CREATE TABLE hook_dispatch (
    hook_id uuid NOT NULL REFERENCES hooks(id) ON DELETE CASCADE,
    delivery text NOT NULL,
    event text NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    done boolean NOT NULL DEFAULT false,
    lease_until timestamptz NOT NULL DEFAULT '-infinity',
    claim uuid,
    PRIMARY KEY (hook_id, delivery)
);
