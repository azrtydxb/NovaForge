-- External identities: the durable link between an account at an OpenID
-- Connect provider and a NovaForge user, made at that account's first single
-- sign-on (just-in-time provisioning).
--
-- The link is (issuer, subject) and nothing else. A subject is the provider's
-- stable, never-reassigned id for an account, and it is only unique within its
-- issuer, so both form the key. Email and username are deliberately NOT link
-- keys: whoever controls a claim at the provider would otherwise be handed the
-- NovaForge account that happens to share it.
CREATE TABLE IF NOT EXISTS external_identities (
    issuer text NOT NULL,
    subject text NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The profile as the provider last asserted it. Recorded for the operator,
    -- never consulted to decide who someone is.
    email citext,
    email_verified boolean NOT NULL DEFAULT false,
    preferred_username text NOT NULL DEFAULT '',
    display_name text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    last_sign_in_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (issuer, subject)
);
CREATE INDEX IF NOT EXISTS external_identities_by_user ON external_identities (user_id);

-- A user provisioned by single sign-on whose provider email is unverified, or
-- already belongs to another account, holds no email rather than an invented
-- one. Password accounts still always register with one (Register requires it).
ALTER TABLE users ALTER COLUMN email DROP NOT NULL;
