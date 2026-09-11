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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/capability"
)

// Lease is a short-lived, single-use credential issued to one run.
type Lease struct {
	ID        uuid.UUID
	Token     string
	Name      string
	ExpiresAt time.Time
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

// BrokerClient is the interface consumers such as internal/ci use to
// request and redeem credentials without depending on Broker's storage
// internals. In production this is backed by a gRPC client to the secrets
// service (or, in-process, directly by *Broker, which satisfies it);
// tests stub it directly.
type BrokerClient interface {
	Issue(ctx context.Context, runID uuid.UUID, g capability.Grant, name string, ttl time.Duration) (Lease, error)
	Redeem(ctx context.Context, token string) (string, error)
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
	_, err = b.pool.Exec(ctx, `
		INSERT INTO secrets.secret_leases (id, org_id, run_id, name, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		lease.ID, g.OrgID, runID, name, hash[:], lease.ExpiresAt,
	)
	if err != nil {
		return Lease{}, fmt.Errorf("issue lease for %q: %w", name, err)
	}
	return lease, nil
}

type leaseRow struct {
	id            uuid.UUID
	orgID         uuid.UUID
	runID         uuid.UUID
	name          string
	expiresAt     time.Time
	revokedAt     *time.Time
	redeemedCount int
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
		SELECT id, org_id, run_id, name, expires_at, revoked_at, redeemed_count
		FROM secrets.secret_leases
		WHERE token_hash = $1
		FOR UPDATE`,
		hash[:],
	).Scan(&row.id, &row.orgID, &row.runID, &row.name, &row.expiresAt, &row.revokedAt, &row.redeemedCount)
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

	stored, err := lookupValue(ctx, tx, row.orgID, row.name)
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
	tag, err := b.pool.Exec(ctx, `
		UPDATE secrets.secret_leases SET revoked_at = now()
		WHERE id = $1 AND revoked_at IS NULL`,
		leaseID,
	)
	if err != nil {
		return fmt.Errorf("revoke lease %s: %w", leaseID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("lease %s not found or already revoked", leaseID)
	}
	return nil
}
