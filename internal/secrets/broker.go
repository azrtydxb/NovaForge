// Package secrets owns the secrets schema and single-use, scoped redemption
// leases. Stored static values never gain expiry or revocation from a lease.
// The broker fails closed — an unreachable broker blocks any job
// that declares a secret rather than letting it run unauthenticated (see
// internal/ci/credentials.go).
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
)

// Lease is a single-use delivery token for an OpenBao dynamic credential.
// ExpiresAt is the provider lease expiry, not proof of target-enforced expiry
// during a provider outage; that guarantee is engine-specific.
type Lease struct {
	ID        uuid.UUID
	Token     string
	Name      string
	ExpiresAt time.Time
	// Environment is pinned at issuance, including when Issue resolves a name.
	Environment string
}

// Broker issues, redeems, and revokes short-lived secret leases. Values are
// encrypted at rest with AES-GCM under a key derived from SECRETS_KEK; only
// sha256(token) is ever stored for a lease, never the token itself.
type Broker struct {
	pool     *pgxpool.Pool
	provider *openBao
	key      [32]byte
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
// retain static values. They are never handed out as expiring credentials.
func (b *Broker) PutValue(ctx context.Context, orgID uuid.UUID, name, environment, value string) error {
	if err := requireOrg(ctx, orgID); err != nil {
		return err
	}
	if err := ValidateSecret(name, environment, value); err != nil {
		return err
	}
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

// Issue resolves a configured binding, preferring production when both exist.
// It never treats stored static values as dynamic credentials.
func (b *Broker) Issue(ctx context.Context, runID uuid.UUID, g capability.Grant, name string, ttl time.Duration) (Lease, error) {
	environment := EnvironmentStaging
	if b.provider != nil {
		if _, ok := b.provider.bindings[bindingKey{g.OrgID, EnvironmentProduction, name}]; ok {
			environment = EnvironmentProduction
		}
	}
	return b.IssueFor(ctx, runID, g, name, environment, ttl)
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
	expires, err := leaseExpiry(ctx, runID, g, name, ttl)
	if err != nil {
		return Lease{}, err
	}
	switch environment {
	case EnvironmentStaging:
	case EnvironmentProduction:
		if !g.SecretsProd {
			return Lease{}, fmt.Errorf("%w: secret %q in production is not permitted for this grant", ErrProductionScoped, name)
		}
	default:
		return Lease{}, fmt.Errorf("unknown environment %q", environment)
	}
	if b.provider == nil {
		return Lease{}, ErrProviderNotConfigured
	}
	binding, ok := b.provider.bindings[bindingKey{g.OrgID, environment, name}]
	if !ok {
		if environment == EnvironmentStaging {
			if _, production := b.provider.bindings[bindingKey{g.OrgID, EnvironmentProduction, name}]; production {
				return Lease{}, ErrProductionScoped
			}
		}
		return Lease{}, ErrProviderNotConfigured
	}
	if binding.RequireHardExpiry {
		return Lease{}, ErrHardExpiryUnavailable
	}
	return b.issueDynamic(ctx, runID, g.OrgID, binding, expires)
}

type leaseRow struct {
	id                           uuid.UUID
	orgID                        uuid.UUID
	runID                        uuid.UUID
	name                         string
	expiresAt                    time.Time
	revokedAt                    *time.Time
	redeemedCount                int
	environment                  string
	providerID, providerEndpoint string
	credential                   []byte
	ready                        bool
}

// Redeem consumes token once, returning the decrypted secret value. A lease
// can be redeemed only once, only before it expires, only if not revoked,
// and only with matching organization and run scopes.
func (b *Broker) Redeem(ctx context.Context, token string) (string, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return "", err
	}
	if err := requireOrg(ctx, scope.OrgID); err != nil {
		return "", err
	}
	if err := requireActor(scope); err != nil {
		return "", err
	}
	runID, ok := runIDFromContext(ctx)
	if !ok || runID == uuid.Nil {
		return "", errors.New("redemption requires a run scope")
	}
	hash := sha256.Sum256([]byte(token))

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin redeem: %w", err)
	}
	defer tx.Rollback(ctx)

	var row leaseRow
	err = tx.QueryRow(ctx, `
		SELECT id, org_id, run_id, name, expires_at, revoked_at, redeemed_count, environment, provider_lease_id, provider_endpoint, issued_ciphertext, provider_ready
		FROM secrets.secret_leases
		WHERE token_hash = $1 AND org_id = $2 AND run_id = $3 AND actor_id=$4 AND actor_kind=$5 AND service_name=$6
		FOR UPDATE`,
		hash[:], scope.OrgID, runID, scope.ActorID, scope.ActorKind, scope.ServiceName,
	).Scan(&row.id, &row.orgID, &row.runID, &row.name, &row.expiresAt, &row.revokedAt, &row.redeemedCount, &row.environment, &row.providerID, &row.providerEndpoint, &row.credential, &row.ready)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errors.New("lease not found")
		}
		return "", fmt.Errorf("lookup lease: %w", err)
	}

	if row.revokedAt != nil {
		return "", fmt.Errorf("lease %s has been revoked", row.id)
	}
	if !time.Now().Before(row.expiresAt) {
		return "", fmt.Errorf("lease %s expired at %s", row.id, row.expiresAt)
	}
	if row.redeemedCount > 0 {
		return "", fmt.Errorf("lease %s has already been redeemed", row.id)
	}

	if !row.ready || row.providerID == "" {
		return "", ErrProviderNotConfigured
	}
	if b.provider == nil || b.provider.endpoint != row.providerEndpoint {
		return "", ErrProviderUnavailable
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	// Re-check the authority, not just our local row: external revocation or
	// renewal must never turn a single-use lease into stale authorization.
	expires, err := b.provider.expiry(ctx, row.providerID)
	if err != nil {
		return "", err
	}
	if !time.Now().Before(expires) || !expires.Truncate(time.Microsecond).Equal(row.expiresAt) {
		return "", ErrProviderContract
	}
	value, err := b.decrypt(row.credential)
	if err != nil {
		return "", err
	}
	if !time.Now().Before(row.expiresAt) {
		return "", errors.New("credential expired during redemption")
	}

	tag, err := b.pool.Exec(ctx, `UPDATE secrets.secret_leases SET redeemed_count=1
 WHERE id=$1 AND org_id=$2 AND run_id=$3 AND redeemed_count=0 AND revoked_at IS NULL
 AND NOT revocation_requested AND provider_ready AND expires_at>clock_timestamp()`, row.id, scope.OrgID, runID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", ErrScopeClosed
	}
	return value, nil
}

// ErrUnderlyingCredentialNotRevocable means the broker already released a
// stored value. Updating its database row cannot invalidate that external
// credential; an operator must rotate it at its issuer.
var ErrUnderlyingCredentialNotRevocable = errors.New("underlying static credential cannot be revoked by the broker; rotate it at its issuer")

// Revoke calls the issuing authority before recording success, including after
// redemption. A failed provider revocation is retryable and never marked done.
func (b *Broker) Revoke(ctx context.Context, leaseID uuid.UUID) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	if err := requireOrg(ctx, scope.OrgID); err != nil {
		return err
	}
	var redeemed int
	var revoked *time.Time
	var providerID, endpoint string
	// Persist intent before the network call. Setting ready=false and the
	// shared lease row lock serialize redemption, but no lock spans OpenBao.
	err = b.pool.QueryRow(ctx, `UPDATE secrets.secret_leases SET revocation_requested=true,provider_ready=false,revocation_attempted_at=clock_timestamp()
 WHERE id=$1 AND org_id=$2 RETURNING redeemed_count,revoked_at,provider_lease_id,provider_endpoint`, leaseID, scope.OrgID).Scan(&redeemed, &revoked, &providerID, &endpoint)
	if err != nil {
		return err
	}
	if endpoint == "" && redeemed > 0 {
		return ErrUnderlyingCredentialNotRevocable
	}
	if revoked != nil {
		return nil
	}
	if endpoint == "" {
		if redeemed > 0 {
			return ErrUnderlyingCredentialNotRevocable
		}
		_, err = b.pool.Exec(ctx, `UPDATE secrets.secret_leases SET revoked_at=now() WHERE id=$1 AND org_id=$2`, leaseID, scope.OrgID)
		return err
	}
	return b.revokeProvider(ctx, scope.OrgID, leaseID, providerID, endpoint)
}

// Reference names a stored secret without any of its material. Listing
// secrets is a legitimate thing for a person to do; reading their values is
// not, so nothing here decrypts anything.
type Reference struct {
	Name        string
	Environment string
}

// List returns stored references and configured dynamic bindings for orgID.
// A stored reference alone is not an issuing authority.
// The ciphertext column is never selected: a query that cannot read the
// material cannot leak it, whatever a caller does with the result.
func (b *Broker) List(ctx context.Context, orgID uuid.UUID) ([]Reference, error) {
	if err := requireOrg(ctx, orgID); err != nil {
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	seen := make(map[Reference]bool, len(out))
	for _, r := range out {
		seen[r] = true
	}
	if b.provider != nil {
		for key := range b.provider.bindings {
			if key.org == orgID {
				r := Reference{Name: key.name, Environment: key.environment}
				if !seen[r] {
					out = append(out, r)
					seen[r] = true
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Environment != out[j].Environment {
			return out[i].Environment < out[j].Environment
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
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
// than stored: pending/unknown provider operations are never rendered as a
// completed revocation. Expired refers to OpenBao lease expiry, not target proof.
func (b *Broker) ListLeases(ctx context.Context, orgID uuid.UUID) ([]LeaseRecord, error) {
	if err := requireOrg(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := b.pool.Query(ctx, `
		SELECT id, name, run_id, expires_at, revoked_at, redeemed_count, provider_ready,revocation_requested,provider_endpoint,provider_lease_id
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
			l                    LeaseRecord
			revokedAt            *time.Time
			redeemed             int
			ready, requested     bool
			endpoint, providerID string
		)
		if err := rows.Scan(&l.ID, &l.SecretName, &l.RunID, &l.ExpiresAt, &revokedAt, &redeemed, &ready, &requested, &endpoint, &providerID); err != nil {
			return nil, fmt.Errorf("scan lease: %w", err)
		}
		switch {
		case endpoint == "" && redeemed > 0:
			l.State = "rotation_required"
		case revokedAt != nil:
			l.State = "revoked"
		case requested:
			l.State = "revocation_pending"
		case endpoint != "" && providerID == "":
			l.State = "issuance_unknown"
		case !ready && endpoint != "":
			l.State = "issuance_pending"
		case l.ExpiresAt.Before(now):
			l.State = "expired"
		case redeemed > 0:
			l.State = "spent"
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

// requireOrg also excludes platform workers: a nil org must never become a
// tenant merely because both the supplied id and the scope happen to be nil.
func requireOrg(ctx context.Context, orgID uuid.UUID) error {
	if orgID == uuid.Nil {
		return errors.New("an organization scope is required")
	}
	return authz.RequireOrg(ctx, orgID)
}

func leaseExpiry(ctx context.Context, runID uuid.UUID, g capability.Grant, name string, ttl time.Duration) (time.Time, error) {
	if err := requireOrg(ctx, g.OrgID); err != nil {
		return time.Time{}, err
	}
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if err := requireActor(scope); err != nil {
		return time.Time{}, err
	}
	if runID == uuid.Nil {
		return time.Time{}, errors.New("a run id is required")
	}
	if !ValidName(name) {
		return time.Time{}, errors.New("invalid secret name")
	}
	if ttl <= 0 || ttl > time.Hour {
		return time.Time{}, errors.New("lease TTL must be positive and at most one hour")
	}
	now := time.Now()
	expires := now.Add(ttl)
	if !g.ExpiresAt.IsZero() {
		if !now.Before(g.ExpiresAt) {
			return time.Time{}, errors.New("grant expired")
		}
		if g.ExpiresAt.Before(expires) {
			expires = g.ExpiresAt
		}
	}
	return expires, nil
}

func requireActor(scope authz.Scope) error {
	switch scope.ActorKind {
	case "user", "agent":
		if scope.ActorID != uuid.Nil && scope.ServiceName == "" {
			return nil
		}
	case "service":
		if scope.ServiceName != "" {
			return nil
		}
	}
	return errors.New("authenticated credential actor required")
}
