package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/ssh"
)

// SSHKey is a row in the identity.ssh_keys table.
type SSHKey struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	Title       string
	Fingerprint string
	PublicKey   string
	CreatedAt   time.Time
}

// SSHKeyStore manages users' SSH public keys, keyed by their SHA256
// fingerprint so the SSH server can authenticate connections in constant
// lookup time.
type SSHKeyStore struct {
	pool *pgxpool.Pool
}

// NewSSHKeyStore wraps pool as an identity.SSHKeyStore.
func NewSSHKeyStore(pool *pgxpool.Pool) *SSHKeyStore {
	return &SSHKeyStore{pool: pool}
}

// Add parses authorizedKey (a single "ssh-ed25519 AAAA... comment" line),
// computes its SHA256 fingerprint, and stores it against userID.
func (s *SSHKeyStore) Add(ctx context.Context, userID uuid.UUID, title, authorizedKey string) (SSHKey, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil {
		return SSHKey{}, fmt.Errorf("parse public key: %w", err)
	}
	fingerprint := ssh.FingerprintSHA256(pub)

	k := SSHKey{
		ID:          uuid.New(),
		UserID:      userID,
		Title:       title,
		Fingerprint: fingerprint,
		PublicKey:   authorizedKey,
	}
	err = s.pool.QueryRow(ctx,
		`INSERT INTO identity.ssh_keys (id, user_id, title, fingerprint, public_key)
		 VALUES ($1, $2, $3, $4, $5) RETURNING created_at`,
		k.ID, k.UserID, k.Title, k.Fingerprint, k.PublicKey,
	).Scan(&k.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return SSHKey{}, fmt.Errorf("ssh key with fingerprint %q already registered", fingerprint)
		}
		return SSHKey{}, fmt.Errorf("add ssh key: %w", err)
	}
	return k, nil
}

// UserByFingerprint returns the user id owning the ssh key with fingerprint.
func (s *SSHKeyStore) UserByFingerprint(ctx context.Context, fingerprint string) (uuid.UUID, error) {
	var userID uuid.UUID
	err := s.pool.QueryRow(ctx,
		`SELECT user_id FROM identity.ssh_keys WHERE fingerprint = $1`,
		fingerprint,
	).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.UUID{}, fmt.Errorf("no user for fingerprint %q", fingerprint)
		}
		return uuid.UUID{}, fmt.Errorf("lookup ssh key: %w", err)
	}
	return userID, nil
}
