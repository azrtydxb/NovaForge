-- Organization deletion must fence even runs not yet admitted. These minimal
-- tombstones outlive deletion so delayed issuance cannot resurrect credentials.
CREATE TABLE secrets.credential_organizations (
 org_id uuid PRIMARY KEY,
 closed boolean NOT NULL DEFAULT false
);
