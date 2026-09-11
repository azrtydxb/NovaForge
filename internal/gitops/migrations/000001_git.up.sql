-- This migration is applied via database.MigrateAs(url, "gitplatform",
-- "gitplatform_git", gitops.MigrationsFS) (see cmd/git-platform/main.go),
-- tracked in its own schema_migrations_gitplatform_git table rather than
-- the schema-named one. "gitplatform" is also written to by
-- internal/capability's migration (capability_grants, applied by the
-- identity service via the default tracking table). golang-migrate requires
-- a single migration source to recognize every version ever recorded in its
-- tracking table, even ones it has nothing left to apply — so two
-- independently versioned migration sets cannot safely share one tracking
-- table while applying to the same schema. Giving this migration its own
-- tracking table lets it apply its DDL into the shared "gitplatform" schema
-- without colliding with capability's version bookkeeping, in either start
-- order.
--
-- name is plain text, not citext: repository names map 1:1 onto case-
-- sensitive on-disk bare repository paths (see resolvePath in repo.go), so
-- case-sensitive uniqueness is what the filesystem already enforces. This
-- also avoids depending on the citext extension having been installed into
-- a schema on this connection's search_path, which is not guaranteed here
-- since Migrate/MigrateAs scope search_path to exactly the "gitplatform"
-- schema.
CREATE TABLE IF NOT EXISTS repositories (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    name text NOT NULL,
    default_branch text NOT NULL DEFAULT 'main',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);
