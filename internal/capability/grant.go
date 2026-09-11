// Package capability implements scoped capability grants for users and
// agents. CanWriteRef is the single enforcement point for branch-scoped
// writes: both the git HTTP and SSH transports, and any later agent tool,
// must call it before allowing a ref update.
package capability

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Grant is a scoped capability issued to a user or an agent within an
// organization. It never grants access outside its OrgID, and a write is
// permitted only within the WriteBranch prefix.
type Grant struct {
	ID            uuid.UUID
	OrgID         uuid.UUID
	SubjectID     uuid.UUID
	SubjectKind   string // "user" or "agent"
	RepoRead      bool
	WriteBranch   string
	SecretsProd   bool
	DeployStaging bool
	DeployProd    bool
	ExpiresAt     time.Time
}

const refHeadsPrefix = "refs/heads/"

// CanWriteRef reports whether g permits a write to ref. ref is denied when it
// contains a ".." path-traversal segment, when g.WriteBranch is empty, or
// when the branch (ref with the refs/heads/ prefix stripped) does not have
// WriteBranch as a literal prefix.
func CanWriteRef(g Grant, ref string) error {
	if strings.Contains(ref, "..") {
		return fmt.Errorf("write to %s not permitted; grant allows %q", ref, g.WriteBranch)
	}
	if g.WriteBranch == "" {
		return fmt.Errorf("write to %s not permitted; grant allows %q", ref, g.WriteBranch)
	}
	branch := strings.TrimPrefix(ref, refHeadsPrefix)
	if !strings.HasPrefix(branch, g.WriteBranch) {
		return fmt.Errorf("write to %s not permitted; grant allows %q", ref, g.WriteBranch)
	}
	return nil
}

// Store persists capability grants in PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store backed by pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Issue persists g, assigning it a new ID, and returns the stored grant.
func (s *Store) Issue(ctx context.Context, g Grant) (Grant, error) {
	g.ID = uuid.New()
	const q = `
		INSERT INTO gitplatform.capability_grants
			(id, org_id, subject_id, subject_kind, repo_read, write_branch,
			 secrets_prod, deploy_staging, deploy_prod, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	_, err := s.pool.Exec(ctx, q,
		g.ID, g.OrgID, g.SubjectID, g.SubjectKind, g.RepoRead, g.WriteBranch,
		g.SecretsProd, g.DeployStaging, g.DeployProd, g.ExpiresAt)
	if err != nil {
		return Grant{}, fmt.Errorf("issue capability grant: %w", err)
	}
	return g, nil
}

// Resolve returns the grant with id, or an error when it does not exist or
// has expired.
func (s *Store) Resolve(ctx context.Context, id uuid.UUID) (Grant, error) {
	const q = `
		SELECT id, org_id, subject_id, subject_kind, repo_read, write_branch,
		       secrets_prod, deploy_staging, deploy_prod, expires_at
		FROM gitplatform.capability_grants
		WHERE id = $1`
	var g Grant
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&g.ID, &g.OrgID, &g.SubjectID, &g.SubjectKind, &g.RepoRead, &g.WriteBranch,
		&g.SecretsProd, &g.DeployStaging, &g.DeployProd, &g.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Grant{}, fmt.Errorf("resolve capability grant %s: %w", id, err)
		}
		return Grant{}, fmt.Errorf("resolve capability grant %s: %w", id, err)
	}
	if g.ExpiresAt.Before(time.Now()) {
		return Grant{}, errors.New("grant expired")
	}
	return g, nil
}

// ListActive returns every non-expired grant issued to subjectID within
// orgID. This is what a transport's CapFunc uses to find the grants that
// might authorize a given ref update, since a push arrives with a subject
// and an organization, not a grant id.
func (s *Store) ListActive(ctx context.Context, orgID, subjectID uuid.UUID) ([]Grant, error) {
	const q = `
		SELECT id, org_id, subject_id, subject_kind, repo_read, write_branch,
		       secrets_prod, deploy_staging, deploy_prod, expires_at
		FROM gitplatform.capability_grants
		WHERE org_id = $1 AND subject_id = $2 AND expires_at > now()`
	rows, err := s.pool.Query(ctx, q, orgID, subjectID)
	if err != nil {
		return nil, fmt.Errorf("list active capability grants: %w", err)
	}
	defer rows.Close()

	var grants []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(
			&g.ID, &g.OrgID, &g.SubjectID, &g.SubjectKind, &g.RepoRead, &g.WriteBranch,
			&g.SecretsProd, &g.DeployStaging, &g.DeployProd, &g.ExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan capability grant: %w", err)
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list active capability grants: %w", err)
	}
	return grants, nil
}
