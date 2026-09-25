-- Never infer legacy identity from whoever presents a token first.
ALTER TABLE secrets.secret_leases
 ADD COLUMN actor_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
 ADD COLUMN actor_kind text NOT NULL DEFAULT '',
 ADD COLUMN service_name text NOT NULL DEFAULT '';
