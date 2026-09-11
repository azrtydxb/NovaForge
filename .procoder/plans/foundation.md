# foundation — implementation plan

Status: complete
Spec: .procoder/specs/backend-platform.md

## Goal

Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git
client clones and pushes over HTTPS and SSH against org-isolated, capability-checked
repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

## Architecture

Two Go services — `identity` and `git-platform` — plus an `edge` REST gateway, all in one
module, each with its own PostgreSQL schema and its own gRPC API. The edge terminates REST
(contract-first from `api/openapi.yaml`) and calls services over gRPC; services never read
each other's tables. Bare repositories live on a read-write-many volume so any `git-platform`
replica serves any repository, and every authorization decision is org-scoped before it is
role-scoped.

## Constraints

Taken verbatim from the spec; every task inherits these.

- Go 1.26 or later, PostgreSQL 16 or later with pgvector, Redis 7 or later with Streams and
  consumer groups, Kubernetes 1.29 or later.
- Standard Git compatibility is absolute: clone, fetch, pull, push, SSH, HTTPS, branches, and
  tags must work with an unmodified git client.
- Git is implemented by shelling out to the git binary, not a pure-Go library. Every image
  touching repositories must contain git, and its version is asserted at startup.
- SSH is served by an embedded Go SSH server built on `golang.org/x/crypto/ssh`, authenticating
  against stored public keys. No host sshd dependency.
- Internal transport is gRPC; the client edge is REST described by OpenAPI; events are Redis
  Streams.
- One PostgreSQL cluster, one schema per service. No service reads another service's tables;
  cross-service reads go through that service's gRPC API.
- Organizations are a hard security boundary: per-org namespaces, per-org credentials, and no
  data path between orgs. Every authorization check is org-scoped, and no query may be
  satisfiable without an org predicate.
- Air-gapped operation is a first-class deployment model. No feature may hard-depend on
  reaching a hosted model provider or the public internet.
- Redis durability is AOF persistence with consumer-group redelivery, giving at-least-once
  delivery. Every stream handler must be idempotent; exactly-once is not assumed anywhere.
- Kubernetes and Helm are the only supported deployment path; docker-compose is out of scope.
- Module path is `github.com/novaforge/novaforge`. Service binaries live under `cmd/<service>`,
  shared packages under `internal/<domain>`.

## Task 1: Go module, tooling, and the check command

Files: `go.mod`, `Makefile`, `.gitignore`, `internal/version/version.go`,
`internal/version/version_test.go`

Interfaces: produces `version.Require(bin string, min string) error` and
`version.Current() string`, used by every service's startup path in later tasks.

- [ ] Write the failing test `internal/version/version_test.go`:
      `func TestRequireRejectsOldGit(t *testing.T) { err := version.Require("git", "2.40.0"); if err != nil && !strings.Contains(err.Error(), "git") { t.Fatalf("want git in error, got %v", err) } }`
      and `func TestRequireMissingBinary(t *testing.T) { if err := version.Require("definitely-not-a-binary", "1.0.0"); err == nil { t.Fatal("want error for missing binary") } }`.
      Run `go test ./internal/version/` — expect FAIL with "no required module provides package".
- [ ] Create `go.mod` with `module github.com/novaforge/novaforge` and `go 1.26`.
- [ ] Implement `internal/version/version.go`: `Require` runs `exec.Command(bin, "--version")`,
      parses the first three-part version number with the regexp pattern
      (\d+)\.(\d+)\.(\d+), compares numerically against `min`, and returns
      `fmt.Errorf("%s %s is older than required %s", bin, got, min)` when short, or
      `fmt.Errorf("%s not found: %w", bin, err)` when the binary is absent.
- [ ] Run `go test ./internal/version/` — expect PASS.
- [ ] Write `Makefile` with targets `test: go test ./...`, `build: go build ./...`,
      `lint: go vet ./...`, and `generate`.
- [ ] Run `make test` and `make lint` — expect both to exit 0.
- [ ] Commit as `feat: add go module, makefile, and binary version guard`.

## Task 2: PostgreSQL connection and per-service schema migrations

Files: `internal/database/connect.go`, `internal/database/migrate.go`,
`internal/database/migrate_test.go`

Interfaces: produces `database.Connect(ctx context.Context, url string) (*pgxpool.Pool, error)`
and `database.Migrate(url string, schema string, fsys fs.FS) error`. Every later service calls
`Migrate` with its own schema name and its own embedded migrations directory.

- [ ] Write the failing test `internal/database/migrate_test.go`:
      `func TestMigrateCreatesSchema(t *testing.T)` which skips when `TEST_DATABASE_URL` is
      unset, calls `database.Migrate(url, "testschema", os.DirFS("testdata/migrations"))`, then
      asserts `SELECT 1 FROM information_schema.schemata WHERE schema_name='testschema'`
      returns a row. Run `go test ./internal/database/` — expect FAIL with "undefined:
      database.Migrate".
- [ ] Add dependencies: `go get github.com/jackc/pgx/v5` and
      `go get github.com/golang-migrate/migrate/v4`.
- [ ] Implement `Connect` returning a `*pgxpool.Pool` with `MaxConns` 25 and a 30s connect
      timeout, pinging before returning.
- [ ] Implement `Migrate`: open the URL with `search_path=<schema>`, issue
      `CREATE SCHEMA IF NOT EXISTS <schema>` after validating `schema` matches
      `^[a-z][a-z0-9_]{0,62}$` and rejecting anything else with
      `fmt.Errorf("invalid schema name %q", schema)`, then run golang-migrate over `fsys` with
      `x-migrations-table=schema_migrations_<schema>`.
- [ ] Create `internal/database/testdata/migrations/000001_init.up.sql` containing
      `CREATE TABLE IF NOT EXISTS probe (id int primary key);` and a matching `.down.sql` with
      `DROP TABLE IF EXISTS probe;`.
- [ ] Run `TEST_DATABASE_URL=postgres://novaforge:novaforge@localhost:5432/novaforge?sslmode=disable go test ./internal/database/`
      — expect PASS.
- [ ] Commit as `feat: add database connection pool and per-schema migrations`.

## Task 3: Org-scoped authorization primitive

Files: `internal/authz/scope.go`, `internal/authz/scope_test.go`

Interfaces: produces `authz.Scope{OrgID uuid.UUID, ActorID uuid.UUID, ActorKind string}`,
`authz.FromContext(ctx context.Context) (Scope, error)`, `authz.WithScope(ctx, Scope) context.Context`,
and `authz.RequireOrg(ctx context.Context, orgID uuid.UUID) error`. Every query in every later
task derives its org predicate from this and never accepts an org id from the request body.

- [ ] Write the failing test `internal/authz/scope_test.go`:
      `func TestRequireOrgRejectsCrossOrg(t *testing.T)` builds
      `ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: orgA, ActorID: user, ActorKind: "user"})`
      and asserts `authz.RequireOrg(ctx, orgB)` returns an error whose message contains
      "cross-org"; plus `func TestFromContextEmpty(t *testing.T)` asserting
      `authz.FromContext(context.Background())` errors rather than returning a zero Scope.
      Run `go test ./internal/authz/` — expect FAIL with "undefined: authz.WithScope".
- [ ] Implement `scope.go` with an unexported context key type
      (`type ctxKey struct{}`) so no other package can collide or forge the value.
- [ ] Implement `RequireOrg` returning
      `fmt.Errorf("cross-org access denied: scope org %s, requested %s", s.OrgID, orgID)` on
      mismatch and `nil` on match.
- [ ] Run `go test ./internal/authz/` — expect PASS.
- [ ] Commit as `feat: add org-scoped authorization primitive`.

## Task 4: Identity schema and user records

Files: `internal/identity/migrations/000001_identity.up.sql`,
`internal/identity/migrations/000001_identity.down.sql`, `internal/identity/store.go`,
`internal/identity/store_test.go`

Interfaces: produces `identity.Store` with
`CreateUser(ctx, email, username, passwordHash string) (User, error)`,
`UserByUsername(ctx, username string) (User, error)`,
`CreateOrg(ctx, name string, ownerID uuid.UUID) (Org, error)`, and
`AddOrgMember(ctx, orgID, userID uuid.UUID, role string) error`. `User` has fields
`ID uuid.UUID, Email, Username, PasswordHash string, TOTPSecret sql.NullString, CreatedAt time.Time`.

- [ ] Write the failing test `internal/identity/store_test.go`:
      `func TestCreateUserAndLookup(t *testing.T)` creates a user and asserts
      `UserByUsername` returns the same ID; `func TestDuplicateUsernameRejected(t *testing.T)`
      asserts a second create with the same username returns an error containing "username".
      Run `go test ./internal/identity/` — expect FAIL with "undefined: identity.Store".
- [ ] Write the up migration creating `users` (id uuid pk, email citext unique not null,
      username citext unique not null, password_hash text not null, totp_secret text,
      created_at timestamptz not null default now()), `organizations` (id uuid pk, name citext
      unique not null, created_at timestamptz not null default now()), and `org_members`
      (org_id uuid not null references organizations(id) on delete cascade, user_id uuid not
      null references users(id) on delete cascade, role text not null check (role in
      ('owner','admin','member')), primary key (org_id, user_id)). Enable `citext` first with
      `CREATE EXTENSION IF NOT EXISTS citext;`.
- [ ] Write the matching down migration dropping the three tables in reverse dependency order.
- [ ] Implement `Store` over `*pgxpool.Pool`, mapping the unique-violation SQLSTATE `23505` to
      `fmt.Errorf("username %q already taken", username)`.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/identity/` — expect PASS.
- [ ] Commit as `feat: add identity schema with users, orgs, and memberships`.

## Task 5: Password hashing, login, and sessions

Files: `internal/identity/password.go`, `internal/identity/password_test.go`,
`internal/identity/session.go`, `internal/identity/session_test.go`

Interfaces: produces `identity.HashPassword(plain string) (string, error)`,
`identity.VerifyPassword(hash, plain string) bool`,
`identity.SessionStore.Create(ctx, userID uuid.UUID, ttl time.Duration) (token string, err error)`,
and `identity.SessionStore.Resolve(ctx, token string) (uuid.UUID, error)`. Sessions live in
Redis, not PostgreSQL.

- [ ] Write the failing test `internal/identity/password_test.go`:
      `func TestHashVerifyRoundTrip(t *testing.T)` asserts
      `VerifyPassword(mustHash("correct horse"), "correct horse")` is true and
      `VerifyPassword(hash, "wrong")` is false; `func TestHashIsSalted(t *testing.T)` asserts
      two hashes of the same input differ. Run `go test ./internal/identity/` — expect FAIL
      with "undefined: identity.HashPassword".
- [ ] Add `go get golang.org/x/crypto` and implement hashing with
      `argon2.IDKey` using time=1, memory=64*1024, threads=4, keyLen=32 and a 16-byte
      crypto/rand salt, encoded as `$argon2id$v=19$m=65536,t=1,p=4$<b64salt>$<b64hash>`.
      `VerifyPassword` parses those parameters back out and compares with
      `subtle.ConstantTimeCompare`.
- [ ] Run `go test -run TestHash ./internal/identity/` — expect PASS.
- [ ] Write the failing test `internal/identity/session_test.go`:
      `func TestSessionResolve(t *testing.T)` creates a session and asserts `Resolve` returns
      the same user id; `func TestSessionExpired(t *testing.T)` creates with `ttl` of 1ms,
      sleeps 20ms, and asserts `Resolve` errors. Skip both when `TEST_REDIS_URL` is unset.
      Run `go test ./internal/identity/` — expect FAIL with "undefined: identity.SessionStore".
- [ ] Implement `SessionStore` over `*redis.Client`: `Create` generates 32 bytes from
      `crypto/rand`, encodes base64url as the token, and `SET session:<sha256(token)> <userID> EX <ttl>`.
      Store the hash, never the token itself.
- [ ] Run `TEST_REDIS_URL=redis://localhost:6379 go test ./internal/identity/` — expect PASS.
- [ ] Commit as `feat: add argon2id password hashing and redis-backed sessions`.

## Task 6: Personal access tokens

Files: `internal/identity/migrations/000002_tokens.up.sql`,
`internal/identity/migrations/000002_tokens.down.sql`, `internal/identity/token.go`,
`internal/identity/token_test.go`

Interfaces: produces
`identity.TokenStore.Create(ctx, userID uuid.UUID, name string, scopes []string, expiresAt *time.Time) (plaintext string, t Token, err error)`,
`identity.TokenStore.Resolve(ctx, plaintext string) (Token, error)`, and
`identity.TokenStore.Revoke(ctx, id uuid.UUID) error`. `Token` carries
`ID, UserID uuid.UUID, Name string, Scopes []string, ExpiresAt *time.Time, RevokedAt *time.Time`.

- [ ] Write the failing test `internal/identity/token_test.go`:
      `func TestTokenResolve(t *testing.T)` creates a token and asserts `Resolve(plaintext)`
      returns the matching user id; `func TestRevokedTokenRejected(t *testing.T)` revokes then
      asserts `Resolve` returns an error containing "revoked";
      `func TestExpiredTokenRejected(t *testing.T)` creates with an `expiresAt` one hour in the
      past and asserts the error contains "expired". Run `go test ./internal/identity/` —
      expect FAIL with "undefined: identity.TokenStore".
- [ ] Write the up migration creating `access_tokens` (id uuid pk, user_id uuid not null
      references users(id) on delete cascade, name text not null, token_hash bytea not null
      unique, scopes text[] not null default '{}', expires_at timestamptz, revoked_at
      timestamptz, created_at timestamptz not null default now()) plus
      `CREATE INDEX ON access_tokens (token_hash);` and the matching down migration.
- [ ] Implement `Create` generating 32 random bytes rendered as `nf_<base64url>`, storing only
      `sha256(plaintext)` in `token_hash`, and returning the plaintext exactly once.
- [ ] Implement `Resolve` looking up by `sha256(plaintext)`, returning
      `errors.New("token revoked")` when `revoked_at` is set and
      `errors.New("token expired")` when `expires_at` is in the past.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/identity/` — expect PASS.
- [ ] Commit as `feat: add personal access tokens with hashed storage`.

## Task 7: TOTP two-factor

Files: `internal/identity/totp.go`, `internal/identity/totp_test.go`

Interfaces: produces `identity.GenerateTOTPSecret() (secret string, uri string, err error)`,
`identity.ValidateTOTP(secret, code string, at time.Time) bool`, and
`identity.Store.SetTOTPSecret(ctx, userID uuid.UUID, secret string) error`. The login path in
Task 12 calls `ValidateTOTP` whenever `User.TOTPSecret` is non-null.

- [ ] Write the failing test `internal/identity/totp_test.go`:
      `func TestTOTPKnownVector(t *testing.T)` asserts
      `ValidateTOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "no-such-code", time.Unix(59,0))` is
      false, and that the code produced for `time.Unix(59,0)` validates at that instant;
      `func TestTOTPWindowTolerance(t *testing.T)` asserts a code generated at T validates at
      T+29s and fails at T+120s. Run `go test ./internal/identity/` — expect FAIL with
      "undefined: identity.ValidateTOTP".
- [ ] Implement RFC 6238 TOTP directly on `crypto/hmac` and `crypto/sha1` with a 30-second step
      and 6 digits, accepting a window of ±1 step. Encode secrets as base32 without padding.
- [ ] Implement `GenerateTOTPSecret` producing 20 random bytes and the provisioning URI
      `otpauth://totp/NovaForge:<username>?secret=<b32>&issuer=NovaForge`.
- [ ] Run `go test -run TestTOTP ./internal/identity/` — expect PASS.
- [ ] Commit as `feat: add RFC 6238 TOTP two-factor`.

## Task 8: SSH public keys

Files: `internal/identity/migrations/000003_ssh_keys.up.sql`,
`internal/identity/migrations/000003_ssh_keys.down.sql`, `internal/identity/sshkey.go`,
`internal/identity/sshkey_test.go`

Interfaces: produces
`identity.SSHKeyStore.Add(ctx, userID uuid.UUID, title, authorizedKey string) (SSHKey, error)`
and `identity.SSHKeyStore.UserByFingerprint(ctx, fingerprint string) (uuid.UUID, error)`.
The SSH server in Task 13 authenticates by calling `UserByFingerprint`.

- [ ] Write the failing test `internal/identity/sshkey_test.go`:
      `func TestAddKeyComputesFingerprint(t *testing.T)` adds a known ed25519 authorized-key
      line and asserts the stored fingerprint starts with `SHA256:`;
      `func TestRejectMalformedKey(t *testing.T)` asserts `Add(ctx, user, "t", "not-a-key")`
      returns an error containing "parse". Run `go test ./internal/identity/` — expect FAIL
      with "undefined: identity.SSHKeyStore".
- [ ] Write the up migration creating `ssh_keys` (id uuid pk, user_id uuid not null references
      users(id) on delete cascade, title text not null, fingerprint text not null unique,
      public_key text not null, created_at timestamptz not null default now()) and the matching
      down migration.
- [ ] Implement `Add` parsing with `ssh.ParseAuthorizedKey` and computing the fingerprint with
      `ssh.FingerprintSHA256`, returning `fmt.Errorf("parse public key: %w", err)` on failure.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/identity/` — expect PASS.
- [ ] Commit as `feat: add ssh public key storage with sha256 fingerprints`.

## Task 9: Capability grants

Files: `internal/capability/grant.go`, `internal/capability/grant_test.go`,
`internal/identity/migrations/000004_capabilities.up.sql`,
`internal/identity/migrations/000004_capabilities.down.sql`

Interfaces: produces `capability.Grant` with fields
`ID, OrgID, SubjectID uuid.UUID, SubjectKind string, RepoRead bool, WriteBranch string, SecretsProd bool, DeployStaging bool, DeployProd bool, ExpiresAt time.Time`,
plus `capability.Store.Issue(ctx, g Grant) (Grant, error)`,
`capability.Store.Resolve(ctx, id uuid.UUID) (Grant, error)`, and
`capability.CanWriteRef(g Grant, ref string) error`. Git transport in Task 12 and the
agent tools in a later plan both call `CanWriteRef` — it is the single enforcement point.

- [ ] Write the failing test `internal/capability/grant_test.go`:
      `func TestWriteBranchScopeEnforced(t *testing.T)` builds
      `Grant{WriteBranch: "agents/NF-182/"}` and asserts `CanWriteRef(g, "refs/heads/agents/NF-182/work")`
      is nil while `CanWriteRef(g, "refs/heads/main")` returns an error containing "not permitted";
      `func TestEmptyWriteBranchDeniesAll(t *testing.T)` asserts a zero Grant denies
      `refs/heads/main`; `func TestPrefixEscapeDenied(t *testing.T)` asserts
      `CanWriteRef(Grant{WriteBranch: "agents/NF-1/"}, "refs/heads/agents/NF-10/x")` errors.
      Run `go test ./internal/capability/` — expect FAIL with "undefined: capability.Grant".
- [ ] Implement `CanWriteRef`: strip the `refs/heads/` prefix, deny when `WriteBranch` is
      empty, deny when the branch does not have `WriteBranch` as a literal prefix, and deny any
      ref containing `..`. Return
      `fmt.Errorf("write to %s not permitted; grant allows %q", ref, g.WriteBranch)`.
- [ ] Write the up migration creating `capability_grants` (id uuid pk, org_id uuid not null
      references organizations(id) on delete cascade, subject_id uuid not null, subject_kind
      text not null check (subject_kind in ('user','agent')), repo_read boolean not null
      default false, write_branch text not null default '', secrets_prod boolean not null
      default false, deploy_staging boolean not null default false, deploy_prod boolean not
      null default false, expires_at timestamptz not null, created_at timestamptz not null
      default now()) and the matching down migration.
- [ ] Implement `Issue` and `Resolve`, with `Resolve` returning
      `errors.New("grant expired")` when `expires_at` is past.
- [ ] Run `go test ./internal/capability/` and `TEST_DATABASE_URL=... go test ./internal/capability/`
      — expect PASS.
- [ ] Commit as `feat: add capability grants with branch-scoped write enforcement`.

## Task 10: Git repository operations

Files: `internal/gitops/repo.go`, `internal/gitops/repo_test.go`

Interfaces: produces `gitops.Repo` with
`gitops.Init(root string, orgID uuid.UUID, name string) (Repo, error)`,
`gitops.Open(root string, orgID uuid.UUID, name string) (Repo, error)`, and methods
`(Repo) Path() string`, `(Repo) Branches() ([]Ref, error)`, `(Repo) Tags() ([]Ref, error)`,
`(Repo) Log(ref string, limit int) ([]Commit, error)`, `(Repo) Tree(ref, path string) ([]TreeEntry, error)`,
`(Repo) Blob(ref, path string) ([]byte, error)`, and `(Repo) Diff(from, to string) (string, error)`.
`Ref` is `{Name, SHA, Kind string}`; `Commit` is `{SHA, Message, AuthorName, AuthorEmail string, At time.Time}`;
`TreeEntry` is `{Mode, Kind, SHA, Name string, Size int64}`.

- [ ] Write the failing test `internal/gitops/repo_test.go`:
      `func TestInitAndBranches(t *testing.T)` inits a repo in `t.TempDir()`, shells
      `git --git-dir=<path> commit-tree` to create a commit, points `refs/heads/main` at it,
      and asserts `Branches()` returns exactly one ref named `main`;
      `func TestPathTraversalRejected(t *testing.T)` asserts
      `gitops.Open(root, orgID, "../escape")` returns an error containing "invalid repository name".
      Run `go test ./internal/gitops/` — expect FAIL with "undefined: gitops.Init".
- [ ] Implement a `run(dir string, args ...string) ([]byte, error)` helper wrapping
      `exec.Command("git", args...)` that captures stderr and returns
      `fmt.Errorf("git %s: %w — %s", strings.Join(args, " "), err, stderr)`.
- [ ] Implement repository path resolution as `filepath.Join(root, orgID.String(), name+".git")`
      after validating `name` against `^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$` and rejecting
      anything else with `fmt.Errorf("invalid repository name %q", name)`. The org id segment
      is what keeps one org's repositories unreachable from another's.
- [ ] Implement `Init` as `git init --bare --initial-branch=main <path>`.
- [ ] Implement `Branches` and `Tags` with
      `git for-each-ref --format=%(refname:short)%00%(objectname)%00%(objecttype) refs/heads/`
      (and `refs/tags/`), splitting on NUL.
- [ ] Implement `Log` with
      `git log --format=%H%x00%s%x00%an%x00%ae%x00%aI -n <limit> <ref>`, `Tree` with
      `git ls-tree -l <ref> -- <path>`, `Blob` with `git cat-file blob <ref>:<path>`, and
      `Diff` with `git diff <from>..<to>`.
- [ ] Run `go test ./internal/gitops/` — expect PASS.
- [ ] Commit as `feat: add git repository operations over the git binary`.

## Task 11: Git smart-HTTP transport

Files: `internal/gitops/http.go`, `internal/gitops/http_test.go`

Interfaces: produces
`gitops.NewHTTPHandler(root string, auth AuthFunc, caps CapFunc) http.Handler` where
`type AuthFunc func(ctx context.Context, user, pass string) (authz.Scope, error)` and
`type CapFunc func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error`.
It serves `GET /{org}/{repo}.git/info/refs`, `POST /{org}/{repo}.git/git-upload-pack`, and
`POST /{org}/{repo}.git/git-receive-pack`.

- [ ] Write the failing test `internal/gitops/http_test.go`:
      `func TestCloneAndPushOverHTTP(t *testing.T)` starts the handler on `httptest.NewServer`,
      runs `git clone http://user:token@127.0.0.1:PORT/<org>/<repo>.git` into `t.TempDir()`,
      commits a file, runs `git push origin main`, then asserts `Log("main", 10)` on the server
      repo contains the pushed message; `func TestPushDeniedByCapability(t *testing.T)` wires a
      `CapFunc` that returns an error for `refs/heads/main` and asserts the push fails with a
      non-zero exit and stderr containing "not permitted". Run `go test ./internal/gitops/` —
      expect FAIL with "undefined: gitops.NewHTTPHandler".
- [ ] Implement `info/refs`: require `?service=git-upload-pack` or `git-receive-pack`, set
      `Content-Type: application/x-<service>-advertisement`, write the pkt-line banner
      `# service=<service>\n` followed by a flush packet `0000`, then stream
      `git <service> --stateless-rpc --advertise-refs <path>`.
- [ ] Implement the two RPC endpoints streaming the request body into
      `git <service> --stateless-rpc <path>` and the command's stdout back to the response,
      handling `Content-Encoding: gzip` request bodies.
- [ ] Enforce authorization before any git process starts: HTTP Basic credentials go through
      `AuthFunc`, and for `git-receive-pack` parse the requested ref updates from the pkt-line
      stream and pass them to `CapFunc`, returning 403 when it errors.
- [ ] Run `go test ./internal/gitops/` — expect PASS.
- [ ] Commit as `feat: add git smart-http transport with capability-checked pushes`.

## Task 12: Push events on Redis Streams

Files: `internal/events/stream.go`, `internal/events/stream_test.go`

Interfaces: produces `events.PushEvent{OrgID, RepoID, PusherID uuid.UUID, Ref, OldSHA, NewSHA string, At time.Time}`,
`events.Publish(ctx, rdb *redis.Client, stream string, payload any) error`,
`events.EnsureGroup(ctx, rdb *redis.Client, stream, group string) error`, and the constant
`events.StreamGitPush = "stream:git:push"`. Later plans consume this stream for CI and
indexing; handlers must be idempotent because delivery is at-least-once.

- [ ] Write the failing test `internal/events/stream_test.go`:
      `func TestPublishAndConsume(t *testing.T)` publishes a `PushEvent`, reads it back with
      `XREADGROUP`, and asserts the round-tripped `NewSHA` matches;
      `func TestEnsureGroupIdempotent(t *testing.T)` calls `EnsureGroup` twice and asserts the
      second call returns nil rather than a BUSYGROUP error. Skip both when `TEST_REDIS_URL`
      is unset. Run `go test ./internal/events/` — expect FAIL with "undefined: events.Publish".
- [ ] Implement `Publish` marshalling to JSON into the field `data` via `XADD`, and
      `EnsureGroup` calling `XGroupCreateMkStream` and swallowing only errors whose text
      contains `BUSYGROUP`.
- [ ] Wire the receive-pack path from Task 11 to publish one `PushEvent` per updated ref after
      the git process exits zero.
- [ ] Run `TEST_REDIS_URL=redis://localhost:6379 go test ./internal/events/` — expect PASS.
- [ ] Commit as `feat: publish push events to redis streams`.

## Task 13: Embedded SSH server

Files: `internal/gitops/ssh.go`, `internal/gitops/ssh_test.go`

Interfaces: produces
`gitops.NewSSHServer(root string, hostKey ssh.Signer, lookup FingerprintFunc, caps CapFunc) *SSHServer`
with `type FingerprintFunc func(ctx context.Context, fingerprint string) (authz.Scope, error)`
and methods `(*SSHServer) Serve(l net.Listener) error` and `(*SSHServer) Addr() string`. It
accepts only the exec requests `git-upload-pack '<org>/<repo>.git'` and
`git-receive-pack '<org>/<repo>.git'`.

- [ ] Write the failing test `internal/gitops/ssh_test.go`:
      `func TestCloneOverSSH(t *testing.T)` generates an ed25519 host key and a client key,
      registers the client fingerprint through `FingerprintFunc`, starts the server on
      `net.Listen("tcp","127.0.0.1:0")`, and runs
      `git -c core.sshCommand="ssh -i <key> -o StrictHostKeyChecking=no -p <port>" clone ssh://git@127.0.0.1/<org>/<repo>.git`,
      asserting exit 0; `func TestUnknownKeyRejected(t *testing.T)` uses an unregistered key and
      asserts the clone fails with stderr containing "permission denied";
      `func TestNonGitCommandRejected(t *testing.T)` opens a session requesting
      `exec "/bin/sh"` and asserts the channel is closed with a non-zero status. Run
      `go test ./internal/gitops/` — expect FAIL with "undefined: gitops.NewSSHServer".
- [ ] Implement the server with `ssh.NewServerConn` and a `PublicKeyCallback` that computes
      `ssh.FingerprintSHA256(key)` and delegates to `FingerprintFunc`, rejecting with
      `fmt.Errorf("permission denied")` when it errors.
- [ ] Parse the exec payload with `ssh.Unmarshal` into `struct{ Command string }`, accept only
      the two git commands via a regexp anchored as ^git-(upload|receive)-pack followed by a
      single-quoted path, and reject everything else by sending `exit-status` 128.
- [ ] Run `CapFunc` for receive-pack before spawning git, exactly as Task 11 does, so both
      transports refuse identically.
- [ ] Run `go test ./internal/gitops/` — expect PASS.
- [ ] Commit as `feat: add embedded ssh server for git transport`.

## Task 14: identity gRPC service

Files: `proto/identity/v1/identity.proto`, `cmd/identity/main.go`,
`internal/identity/grpc.go`, `internal/identity/grpc_test.go`, `buf.gen.yaml`

Interfaces: produces the gRPC service `novaforge.identity.v1.IdentityService` with RPCs
`Register`, `Login`, `ResolveSession`, `ResolveToken`, `ResolveFingerprint`, `CreateOrg`,
`AddOrgMember`, and `IssueGrant`. The edge in Task 16 and both git transports consume these;
no other service touches the identity schema.

- [ ] Write `proto/identity/v1/identity.proto` with `syntax = "proto3";`,
      `package novaforge.identity.v1;`, `option go_package = "github.com/novaforge/novaforge/gen/identity/v1;identityv1";`
      and the eight RPCs above, where `LoginRequest` carries `username`, `password`, and
      `totp_code`, and `LoginResponse` carries `session_token` and `requires_totp`.
- [ ] Write `buf.gen.yaml` generating Go and gRPC stubs into `gen/`, and add
      `generate: buf generate` to the Makefile. Run `make generate` and commit the generated
      code so builds need no codegen step.
- [ ] Write the failing test `internal/identity/grpc_test.go`:
      `func TestLoginRequiresTOTPWhenEnabled(t *testing.T)` registers a user, enables TOTP, then
      asserts `Login` without `totp_code` returns `codes.Unauthenticated` and
      `requires_totp == true`, and that `Login` with a valid code returns a session token;
      `func TestResolveTokenRejectsRevoked(t *testing.T)` asserts a revoked token yields
      `codes.Unauthenticated`. Run `go test ./internal/identity/` — expect FAIL with
      "undefined: identity.NewGRPCServer".
- [ ] Implement `grpc.go` wiring `Store`, `SessionStore`, `TokenStore`, `SSHKeyStore`, and
      `capability.Store`, mapping domain errors to `status.Error(codes.Unauthenticated, ...)`
      and `codes.PermissionDenied` for cross-org.
- [ ] Implement `cmd/identity/main.go`: call `version.Require("git","2.40.0")` only if the
      binary is needed (it is not here), run `database.Migrate(url, "identity", migrationsFS)`,
      connect Redis, serve gRPC on `IDENTITY_GRPC_PORT` (default 9091), and shut down on
      SIGINT/SIGTERM with a 30s drain.
- [ ] Run `go test ./internal/identity/` — expect PASS.
- [ ] Commit as `feat: add identity grpc service`.

## Task 15: git-platform service

Files: `proto/git/v1/git.proto`, `cmd/git-platform/main.go`, `internal/gitops/grpc.go`,
`internal/gitops/grpc_test.go`, `internal/gitops/migrations/000001_git.up.sql`,
`internal/gitops/migrations/000001_git.down.sql`

Interfaces: produces `novaforge.git.v1.GitService` with RPCs `CreateRepo`, `GetRepo`,
`ListRepos`, `ListBranches`, `ListTags`, `ListCommits`, `GetTree`, `GetBlob`, and `GetDiff`.
It calls `IdentityService.ResolveToken` and `ResolveFingerprint` for authentication and
`IssueGrant`-derived grants for authorization; it never reads the identity schema.

- [ ] Write the up migration creating `repositories` (id uuid pk, org_id uuid not null, name
      citext not null, default_branch text not null default 'main', created_at timestamptz not
      null default now(), unique (org_id, name)) and the matching down migration. The org_id
      column is not a foreign key because organizations live in another service's schema;
      referential integrity across services is the caller's responsibility.
- [ ] Write the failing test `internal/gitops/grpc_test.go`:
      `func TestCreateRepoInitialisesBareRepo(t *testing.T)` calls `CreateRepo` and asserts the
      directory `<root>/<orgID>/<name>.git/HEAD` exists;
      `func TestListReposIsOrgScoped(t *testing.T)` creates repos in two orgs and asserts a
      scope for org A never sees org B's repository;
      `func TestGetBlobUnknownPath(t *testing.T)` asserts `codes.NotFound`. Run
      `go test ./internal/gitops/` — expect FAIL with "undefined: gitops.NewGRPCServer".
- [ ] Implement `grpc.go` deriving every query's org predicate from `authz.FromContext` and
      never from the request message, returning `codes.PermissionDenied` when
      `authz.RequireOrg` fails.
- [ ] Implement `cmd/git-platform/main.go`: assert `version.Require("git","2.40.0")` at
      startup and exit non-zero on failure, migrate schema `git`, mount the repository root
      from `GIT_DATA_DIR`, serve gRPC on 9092, the smart-HTTP handler on 8081, and the SSH
      server on 2222.
- [ ] Run `go test ./internal/gitops/` — expect PASS.
- [ ] Commit as `feat: add git-platform service with grpc, http, and ssh surfaces`.

## Task 16: OpenAPI contract and REST edge

Files: `api/openapi.yaml`, `cmd/edge/main.go`, `internal/edge/router.go`,
`internal/edge/middleware.go`, `internal/edge/router_test.go`,
`internal/edge/coverage_test.go`

Interfaces: produces `edge.NewRouter(cfg Config) http.Handler` where `Config` carries
`Identity identityv1.IdentityServiceClient`, `Git gitv1.GitServiceClient`, and
`Redis *redis.Client`. Routes are mounted under `/api/v1` and every route has an
`operationId` in `api/openapi.yaml`.

- [ ] Write `api/openapi.yaml` as OpenAPI 3.1 describing every route this task mounts:
      `POST /api/v1/auth/register`, `POST /api/v1/auth/login`, `POST /api/v1/auth/logout`,
      `GET /api/v1/user`, `GET|POST /api/v1/user/tokens`, `DELETE /api/v1/user/tokens/{id}`,
      `GET|POST /api/v1/user/ssh-keys`, `DELETE /api/v1/user/ssh-keys/{id}`,
      `POST /api/v1/user/2fa/setup`, `POST /api/v1/user/2fa/verify`,
      `GET|POST /api/v1/orgs`, `GET /api/v1/orgs/{org}`,
      `GET|POST /api/v1/orgs/{org}/members`, `GET|POST /api/v1/orgs/{org}/repos`,
      `GET|DELETE /api/v1/orgs/{org}/repos/{repo}`,
      `GET /api/v1/orgs/{org}/repos/{repo}/branches`,
      `GET /api/v1/orgs/{org}/repos/{repo}/tags`,
      `GET /api/v1/orgs/{org}/repos/{repo}/commits/{ref}`,
      `GET /api/v1/orgs/{org}/repos/{repo}/tree/{ref}/{path}`,
      `GET /api/v1/orgs/{org}/repos/{repo}/blob/{ref}/{path}`, and
      `GET /api/v1/orgs/{org}/repos/{repo}/diff`.
- [ ] Write the failing test `internal/edge/coverage_test.go`:
      `func TestEveryRouteIsInOpenAPI(t *testing.T)` walks the chi router with `chi.Walk`,
      collects `method + pattern`, parses `api/openapi.yaml`, and fails listing any route
      absent from the document — and any documented path absent from the router. Run
      `go test ./internal/edge/` — expect FAIL with "undefined: edge.NewRouter".
- [ ] Write the failing test `internal/edge/router_test.go`:
      `func TestUnauthenticatedRejected(t *testing.T)` asserts `GET /api/v1/user` without
      credentials returns 401; `func TestCrossOrgRepoDenied(t *testing.T)` asserts a session
      for org A requesting org B's repository returns 403.
- [ ] Implement `middleware.go`: authentication accepting either a session cookie or
      `Authorization: Bearer nf_...`, resolving through `IdentityService` and installing an
      `authz.Scope` in the request context; plus a Redis fixed-window rate limiter keyed on
      the resolved actor.
- [ ] Implement `router.go` mounting every documented route and translating gRPC status codes
      to HTTP (`codes.PermissionDenied` to 403, `codes.NotFound` to 404,
      `codes.Unauthenticated` to 401).
- [ ] Run `go test ./internal/edge/` — expect PASS.
- [ ] Commit as `feat: add openapi contract and rest edge with coverage test`.

## Task 17: nf CLI

Files: `cmd/nf/main.go`, `internal/cli/client.go`, `internal/cli/commands.go`,
`internal/cli/commands_test.go`

Interfaces: produces the commands `nf login`, `nf org create`, `nf repo create`,
`nf repo list`, `nf repo branches`, and `nf repo log`, all speaking the REST edge from
Task 16 and storing the session token in `$XDG_CONFIG_HOME/novaforge/config.json` with mode 0600.

- [ ] Write the failing test `internal/cli/commands_test.go`:
      `func TestRepoCreateAndList(t *testing.T)` starts a stub edge with `httptest.NewServer`,
      runs the `repo create` then `repo list` commands against it, and asserts the created
      repository name appears in stdout; `func TestConfigFilePermissions(t *testing.T)` asserts
      the written config file's mode is exactly 0600. Run `go test ./internal/cli/` — expect
      FAIL with "undefined: cli.Execute".
- [ ] Implement `client.go` as a thin REST client sending
      `Authorization: Bearer <token>` and returning
      `fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, body)` for non-2xx responses.
- [ ] Implement `commands.go` with `flag.NewFlagSet` per subcommand — no third-party CLI
      dependency — and `Execute(args []string, stdout, stderr io.Writer) int` returning the
      process exit code.
- [ ] Run `go test ./internal/cli/` — expect PASS.
- [ ] Commit as `feat: add nf cli`.

## Task 18: Helm chart and end-to-end deploy test

Files: `deploy/helm/novaforge/Chart.yaml`, `deploy/helm/novaforge/values.yaml`,
`deploy/helm/novaforge/templates/identity.yaml`,
`deploy/helm/novaforge/templates/git-platform.yaml`,
`deploy/helm/novaforge/templates/edge.yaml`, `deploy/helm/novaforge/templates/secrets.yaml`,
`deploy/helm/novaforge/templates/repos-pvc.yaml`, `tests/e2e/deploy_test.sh`,
`Dockerfile.identity`, `Dockerfile.git-platform`, `Dockerfile.edge`

Interfaces: produces the release name `novaforge` exposing Services `novaforge-identity:9091`,
`novaforge-git-platform:9092`, `novaforge-git-platform-http:8081`,
`novaforge-git-platform-ssh:2222`, and `novaforge-edge:8080`.

- [ ] Write `Dockerfile.git-platform` on `golang:1.26` builder and `alpine:3.21` runtime with
      `RUN apk add --no-cache git openssh-client`, and the other two Dockerfiles on the same
      builder with a `gcr.io/distroless/static` runtime since they never shell out to git.
- [ ] Write `Chart.yaml` (apiVersion v2, name novaforge) with dependencies on the Bitnami
      `postgresql`, `redis`, and `minio` charts pinned to exact versions, and `values.yaml`
      exposing `image.tag`, `repos.storageClass`, and `repos.size` with a default of `100Gi`.
- [ ] Write `repos-pvc.yaml` declaring a `ReadWriteMany` PersistentVolumeClaim named
      `novaforge-repos`, mounted at `/data/repos` by the git-platform Deployment so any replica
      serves any repository.
- [ ] Write the failing test `tests/e2e/deploy_test.sh`: create a kind cluster, build and load
      the three images, `helm install novaforge ./deploy/helm/novaforge --wait --timeout 10m`,
      then assert `kubectl get deploy -o jsonpath='{.items[*].status.readyReplicas}'` shows all
      three ready, and finally run `nf login`, `nf org create`, `nf repo create`, a real
      `git clone` over the port-forwarded HTTP service, a commit, and a `git push`, asserting
      the pushed SHA is returned by `nf repo log`. Run `bash tests/e2e/deploy_test.sh` — expect
      FAIL with "Error: unable to build kubernetes objects".
- [ ] Write the three Deployment/Service templates, each with a readiness probe on
      `/healthz`, resource requests of `100m`/`128Mi`, and env wired from
      `secrets.yaml` (`DATABASE_URL`, `REDIS_URL`, `JWT_SECRET`, `SSH_HOST_KEY`).
- [ ] Add a `/healthz` handler to all three services returning 200 only once their database
      and Redis pings succeed.
- [ ] Run `bash tests/e2e/deploy_test.sh` — expect PASS.
- [ ] Commit as `feat: add helm chart and end-to-end deploy test`.
