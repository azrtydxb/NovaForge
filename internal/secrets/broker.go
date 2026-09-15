// Package secrets owns the secrets schema and the secret broker: short-lived,
// single-use, scoped credential leases instead of durable secrets handed to
// agents. The broker fails closed — an unreachable broker blocks any job
// that declares a secret rather than letting it run unauthenticated (see
// internal/ci/credentials.go).
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
)

// Lease is a short-lived, single-use credential issued to one run.
type Lease struct {
	ID        uuid.UUID
	Token     string
	Name      string
	ExpiresAt time.Time
	// Environment is the environment the lease was issued for, or empty for
	// a lease issued by Issue, which does not scope by environment.
	Environment string
}

// Broker issues, redeems, and revokes short-lived secret leases. Values are
// encrypted at rest with AES-GCM under a key derived from SECRETS_KEK; only
// sha256(token) is ever stored for a lease, never the token itself.
type Broker struct {
	pool *pgxpool.Pool
	key  [32]byte
}

// NewBroker wraps pool as a secrets.Broker. kek is the raw SECRETS_KEK value
// of any length; it is hashed into a 32-byte AES-256 key so operators are
// never forced into picking an exact 16/24/32-byte secret.
func NewBroker(pool *pgxpool.Pool, kek []byte) *Broker {
	return &Broker{pool: pool, key: sha256.Sum256(kek)}
}

// runCtxKey scopes a context to the run attempting a redemption, so Redeem
// can refuse a lease issued to a different run than the one asking for it.
type runCtxKey struct{}

// WithRunID returns a copy of ctx scoped to runID. internal/ci wraps every
// job's context with its own run before calling Redeem.
func WithRunID(ctx context.Context, runID uuid.UUID) context.Context {
	return context.WithValue(ctx, runCtxKey{}, runID)
}

func runIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	return RunIDFromContext(ctx)
}

// RunIDFromContext returns the run ID ctx was scoped to via WithRunID, if
// any. Consumers such as internal/ci use it to thread the same run scope
// through to Broker.Issue when resolving job credentials.
func RunIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	v, ok := ctx.Value(runCtxKey{}).(uuid.UUID)
	return v, ok
}

func (b *Broker) encrypt(plaintext string) ([]byte, error) {
	block, err := aes.NewCipher(b.key[:])
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	// The nonce is prefixed to the ciphertext so it travels with it; GCM
	// authenticates both, so a tampered blob fails to decrypt rather than
	// silently returning garbage.
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func (b *Broker) decrypt(blob []byte) (string, error) {
	block, err := aes.NewCipher(b.key[:])
	if err != nil {
		return "", fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("new gcm: %w", err)
	}
	if len(blob) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ciphertext := blob[:gcm.NonceSize()], blob[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt secret value: %w", err)
	}
	return string(plaintext), nil
}

// PutValue stores value for name in environment ("staging" or
// "production"), encrypted at rest. Operators and org admins call this to
// configure secrets; tests use it to seed fixtures.
func (b *Broker) PutValue(ctx context.Context, orgID uuid.UUID, name, environment, value string) error {
	ciphertext, err := b.encrypt(value)
	if err != nil {
		return err
	}
	_, err = b.pool.Exec(ctx, `
		INSERT INTO secrets.secret_values (org_id, name, environment, ciphertext)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id, name, environment) DO UPDATE SET ciphertext = EXCLUDED.ciphertext`,
		orgID, name, environment, ciphertext,
	)
	if err != nil {
		return fmt.Errorf("put secret value %q: %w", name, err)
	}
	return nil
}

// queryRower is satisfied by both *pgxpool.Pool and pgx.Tx, so lookups can
// run either standalone or inside Redeem's transaction.
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type storedSecret struct {
	environment string
	ciphertext  []byte
}

// lookupValue finds the secret_values row for (orgID, name). A name is
// expected to resolve to exactly one environment in practice; when more
// than one exists, "production" is preferred so a stray staging duplicate
// can never mask that a secret is in fact production-scoped.
func lookupValue(ctx context.Context, q queryRower, orgID uuid.UUID, name string) (storedSecret, error) {
	var s storedSecret
	err := q.QueryRow(ctx, `
		SELECT environment, ciphertext FROM secrets.secret_values
		WHERE org_id = $1 AND name = $2
		ORDER BY (environment = 'production') DESC
		LIMIT 1`,
		orgID, name,
	).Scan(&s.environment, &s.ciphertext)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedSecret{}, fmt.Errorf("secret %q not found", name)
		}
		return storedSecret{}, fmt.Errorf("lookup secret %q: %w", name, err)
	}
	return s, nil
}

// Issue issues a short-lived, single-use lease for the secret named name,
// scoped to runID and g's org. A production-scoped secret is refused to a
// grant without SecretsProd.
func (b *Broker) Issue(ctx context.Context, runID uuid.UUID, g capability.Grant, name string, ttl time.Duration) (Lease, error) {
	stored, err := lookupValue(ctx, b.pool, g.OrgID, name)
	if err != nil {
		return Lease{}, err
	}
	if stored.environment == "production" && !g.SecretsProd {
		return Lease{}, fmt.Errorf("secret %q is production-scoped; not permitted for this grant", name)
	}
	return b.insertLease(ctx, runID, g.OrgID, name, "", ttl)
}

// Environments are the two places a secret can be scoped to.
const (
	EnvironmentStaging    = "staging"
	EnvironmentProduction = "production"
)

// ErrProductionScoped reports a request for production material that the
// grant does not allow.
var ErrProductionScoped = errors.New("production-scoped secret not permitted")

// IssueFor issues a lease for name as it exists in environment. A staging
// request never falls back to a production value of the same name — that
// fallback is precisely how a staging-scoped job would read a production
// credential — and a production request needs a grant with SecretsProd.
func (b *Broker) IssueFor(ctx context.Context, runID uuid.UUID, g capability.Grant, name, environment string, ttl time.Duration) (Lease, error) {
	switch environment {
	case EnvironmentStaging:
	case EnvironmentProduction:
		if !g.SecretsProd {
			return Lease{}, fmt.Errorf("%w: secret %q in production is not permitted for this grant", ErrProductionScoped, name)
		}
	default:
		return Lease{}, fmt.Errorf("unknown environment %q", environment)
	}
	if _, err := lookupEnvValue(ctx, b.pool, g.OrgID, name, environment); err != nil {
		if environment == EnvironmentStaging {
			if _, perr := lookupEnvValue(ctx, b.pool, g.OrgID, name, EnvironmentProduction); perr == nil {
				return Lease{}, fmt.Errorf("%w: secret %q exists only in production; a staging job may not have it", ErrProductionScoped, name)
			}
		}
		return Lease{}, err
	}
	lease, err := b.insertLease(ctx, runID, g.OrgID, name, environment, ttl)
	if err != nil {
		return Lease{}, err
	}
	lease.Environment = environment
	return lease, nil
}

func (b *Broker) insertLease(ctx context.Context, runID, orgID uuid.UUID, name, environment string, ttl time.Duration) (Lease, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return Lease{}, fmt.Errorf("generate lease token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	hash := sha256.Sum256([]byte(token))

	lease := Lease{
		ID:        uuid.New(),
		Token:     token,
		Name:      name,
		ExpiresAt: time.Now().Add(ttl),
	}
	_, err := b.pool.Exec(ctx, `
		INSERT INTO secrets.secret_leases (id, org_id, run_id, name, token_hash, expires_at, environment)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		lease.ID, orgID, runID, name, hash[:], lease.ExpiresAt, environment,
	)
	if err != nil {
		return Lease{}, fmt.Errorf("issue lease for %q: %w", name, err)
	}
	return lease, nil
}

// lookupEnvValue finds the value of name in exactly one environment.
func lookupEnvValue(ctx context.Context, q queryRower, orgID uuid.UUID, name, environment string) (storedSecret, error) {
	s := storedSecret{environment: environment}
	err := q.QueryRow(ctx, `
		SELECT ciphertext FROM secrets.secret_values
		WHERE org_id = $1 AND name = $2 AND environment = $3`,
		orgID, name, environment,
	).Scan(&s.ciphertext)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedSecret{}, fmt.Errorf("secret %q not found in %s", name, environment)
		}
		return storedSecret{}, fmt.Errorf("lookup secret %q: %w", name, err)
	}
	return s, nil
}

type leaseRow struct {
	id            uuid.UUID
	orgID         uuid.UUID
	runID         uuid.UUID
	name          string
	expiresAt     time.Time
	revokedAt     *time.Time
	redeemedCount int
	environment   string
}

// Redeem consumes token once, returning the decrypted secret value. A lease
// can be redeemed only once, only before it expires, only if not revoked,
// and — when ctx carries a run scope via WithRunID — only by the run it was
// issued to.
func (b *Broker) Redeem(ctx context.Context, token string) (string, error) {
	hash := sha256.Sum256([]byte(token))

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin redeem: %w", err)
	}
	defer tx.Rollback(ctx)

	var row leaseRow
	err = tx.QueryRow(ctx, `
		SELECT id, org_id, run_id, name, expires_at, revoked_at, redeemed_count, environment
		FROM secrets.secret_leases
		WHERE token_hash = $1
		FOR UPDATE`,
		hash[:],
	).Scan(&row.id, &row.orgID, &row.runID, &row.name, &row.expiresAt, &row.revokedAt, &row.redeemedCount, &row.environment)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errors.New("lease not found")
		}
		return "", fmt.Errorf("lookup lease: %w", err)
	}

	if scopedRun, ok := runIDFromContext(ctx); ok && scopedRun != row.runID {
		return "", fmt.Errorf("lease %s was issued to a different run; not permitted for run %s", row.id, scopedRun)
	}
	if row.revokedAt != nil {
		return "", fmt.Errorf("lease %s has been revoked", row.id)
	}
	if time.Now().After(row.expiresAt) {
		return "", fmt.Errorf("lease %s expired at %s", row.id, row.expiresAt)
	}
	if row.redeemedCount > 0 {
		return "", fmt.Errorf("lease %s has already been redeemed", row.id)
	}

	var stored storedSecret
	if row.environment != "" {
		stored, err = lookupEnvValue(ctx, tx, row.orgID, row.name, row.environment)
	} else {
		stored, err = lookupValue(ctx, tx, row.orgID, row.name)
	}
	if err != nil {
		return "", err
	}
	value, err := b.decrypt(stored.ciphertext)
	if err != nil {
		return "", err
	}

	if _, err := tx.Exec(ctx, `UPDATE secrets.secret_leases SET redeemed_count = redeemed_count + 1 WHERE id = $1`, row.id); err != nil {
		return "", fmt.Errorf("mark lease %s redeemed: %w", row.id, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit redeem of lease %s: %w", row.id, err)
	}
	return value, nil
}

// Revoke invalidates leaseID immediately, before it would otherwise expire.
func (b *Broker) Revoke(ctx context.Context, leaseID uuid.UUID) error {
	// Organizations are a hard boundary, and a lease id is guessable in the
	// way any uuid is: without this predicate one organization could revoke
	// another's live credential and stall its runs.
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	tag, err := b.pool.Exec(ctx, `
		UPDATE secrets.secret_leases SET revoked_at = now()
		WHERE id = $1 AND org_id = $2 AND revoked_at IS NULL`,
		leaseID, scope.OrgID,
	)
	if err != nil {
		return fmt.Errorf("revoke lease %s: %w", leaseID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("lease %s not found or already revoked", leaseID)
	}
	return nil
}

// Reference names a stored secret without any of its material. Listing
// secrets is a legitimate thing for a person to do; reading their values is
// not, so nothing here decrypts anything.
type Reference struct {
	Name        string
	Environment string
}

// List returns the secrets registered for orgID, names and environments only.
// The ciphertext column is never selected: a query that cannot read the
// material cannot leak it, whatever a caller does with the result.
func (b *Broker) List(ctx context.Context, orgID uuid.UUID) ([]Reference, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := b.pool.Query(ctx, `
		SELECT name, environment FROM secrets.secret_values
		WHERE org_id = $1 ORDER BY environment, name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	defer rows.Close()

	var out []Reference
	for rows.Next() {
		var r Reference
		if err := rows.Scan(&r.Name, &r.Environment); err != nil {
			return nil, fmt.Errorf("scan secret: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LeaseRecord is one brokered credential as an operator sees it: which secret,
// which run, and whether it is still live. The token itself is not here — only
// its hash is stored, and even that is not returned.
type LeaseRecord struct {
	ID         uuid.UUID
	SecretName string
	RunID      uuid.UUID
	State      string
	ExpiresAt  time.Time
}

// ListLeases returns orgID's leases, newest first. State is derived rather
// than stored: a lease is revoked, spent, expired, or issued, and deriving it
// here means the four cannot disagree with the columns they come from.
func (b *Broker) ListLeases(ctx context.Context, orgID uuid.UUID) ([]LeaseRecord, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := b.pool.Query(ctx, `
		SELECT id, name, run_id, expires_at, revoked_at, redeemed_count
		FROM secrets.secret_leases
		WHERE org_id = $1 ORDER BY expires_at DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list leases: %w", err)
	}
	defer rows.Close()

	now := time.Now()
	var out []LeaseRecord
	for rows.Next() {
		var (
			l         LeaseRecord
			revokedAt *time.Time
			redeemed  int
		)
		if err := rows.Scan(&l.ID, &l.SecretName, &l.RunID, &l.ExpiresAt, &revokedAt, &redeemed); err != nil {
			return nil, fmt.Errorf("scan lease: %w", err)
		}
		switch {
		case revokedAt != nil:
			l.State = "revoked"
		case redeemed > 0:
			l.State = "spent"
		case l.ExpiresAt.Before(now):
			l.State = "expired"
		default:
			l.State = "issued"
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// nameRe is the shape of a secret's name. A job reads a secret as an
// environment variable of the same name, so a name must be a valid one.
var nameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

// maxValueBytes bounds a stored value: credentials, not files.
const maxValueBytes = 64 << 10

// ValidateSecret reports why name, environment and value cannot be stored.
func ValidateSecret(name, environment, value string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("secret name %q must be upper-case letters, digits and underscores, starting with a letter or underscore", name)
	}
	if environment != EnvironmentStaging && environment != EnvironmentProduction {
		return fmt.Errorf("environment %q must be staging or production", environment)
	}
	if value == "" {
		return errors.New("a secret needs a value")
	}
	if len(value) > maxValueBytes {
		return fmt.Errorf("secret value is %d bytes, over the %d byte limit", len(value), maxValueBytes)
	}
	return nil
}

// ValidName reports whether name can be a secret's name.
func ValidName(name string) bool { return nameRe.MatchString(name) }
