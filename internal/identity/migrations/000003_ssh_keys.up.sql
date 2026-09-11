CREATE TABLE IF NOT EXISTS ssh_keys (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title text NOT NULL,
    fingerprint text NOT NULL UNIQUE,
    public_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
