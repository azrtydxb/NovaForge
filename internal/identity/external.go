package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// ExternalProfile is what an OpenID Connect provider asserted about an account,
// after the edge verified the ID token carrying it. Only Issuer and Subject
// decide who the person is; the rest is profile.
type ExternalProfile struct {
	Issuer            string
	Subject           string
	Email             string
	EmailVerified     bool
	PreferredUsername string
	Name              string
}

// Username rules for a provisioned account. Register has never constrained
// usernames, but a name derived from a provider claim is taken from another
// system, so it is held to what is safe everywhere NovaForge prints or routes
// one — URLs, git output, a shell: lowercase letters, digits, '.', '_' and '-',
// starting and ending alphanumeric, at most 39 characters.
var (
	usernameDisallowed = regexp.MustCompile(`[^a-z0-9._-]+`)
	repeatedDash       = regexp.MustCompile(`-{2,}`)
)

const maxUsernameLen = 39

func trimToAlnum(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// DeriveUsername turns a provider's preferred_username (falling back to the
// email) into a username that satisfies the rules above. Zitadel's
// preferred_username is a login name, often an email address, so only its
// local part is kept. A claim that leaves nothing usable becomes "user".
func DeriveUsername(preferred, email string) string {
	for _, raw := range []string{preferred, email} {
		base := strings.TrimSpace(raw)
		if i := strings.IndexByte(base, '@'); i >= 0 {
			base = base[:i]
		}
		base = usernameDisallowed.ReplaceAllString(strings.ToLower(base), "-")
		base = trimToAlnum(repeatedDash.ReplaceAllString(base, "-"))
		if len(base) > maxUsernameLen {
			base = trimToAlnum(base[:maxUsernameLen])
		}
		if base != "" {
			return base
		}
	}
	return "user"
}

// usernameCandidates is the derived name followed by suffixed forms of it.
// The suffix comes from a hash of (issuer, subject), so it is deterministic:
// the same external account is always offered the same names in the same
// order, and two accounts that collide on a base name get different ones.
func usernameCandidates(base, issuer, subject string) []string {
	sum := sha256.Sum256([]byte(issuer + "\x00" + subject))
	h := hex.EncodeToString(sum[:])
	out := []string{base}
	for _, n := range []int{6, 10, 16, 32} {
		suffix := "-" + h[:n]
		b := base
		if len(b)+len(suffix) > maxUsernameLen {
			b = trimToAlnum(b[:maxUsernameLen-len(suffix)])
		}
		if b == "" {
			b = "user"
		}
		out = append(out, b+suffix)
	}
	return out
}

// SignInExternal returns the user linked to the profile's (issuer, subject),
// provisioning one on the first sign-in, and reports whether it did.
//
// The link is (issuer, subject) only. An existing account whose username or
// email matches the profile is never linked to it — not by username, not by
// email, verified or not — because the provider's claims are controlled by
// whoever administers that provider account, and linking on them would hand
// over the NovaForge account that shares them. Instead:
//
//   - the username is derived from preferred_username and, if taken, given a
//     deterministic suffix (usernameCandidates);
//   - the email is recorded only when the provider verified it and no other
//     account holds it; otherwise the new account holds none.
//
// A provisioned account belongs to no organization. Organizations stay the
// security boundary: someone has to invite it before it can see anything.
func (s *Store) SignInExternal(ctx context.Context, p ExternalProfile) (User, bool, error) {
	if p.Issuer == "" || p.Subject == "" {
		return User{}, false, errors.New("issuer and subject are required")
	}
	// A transaction that lost a race on the email (another account took it
	// between the check and the insert) is retried from the top.
	for attempt := 0; attempt < 3; attempt++ {
		u, created, err := s.signInExternalOnce(ctx, p)
		if errors.Is(err, errRetrySignIn) {
			continue
		}
		return u, created, err
	}
	return User{}, false, errors.New("external sign-in kept colliding; giving up")
}

var errRetrySignIn = errors.New("retry external sign-in")

func (s *Store) signInExternalOnce(ctx context.Context, p ExternalProfile) (User, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// Two first sign-ins of the same account at once — two tabs, a retried
	// redirect — must provision one user, not two with one orphaned. The
	// lock is per identity, so unrelated sign-ins do not wait on each other.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		fmt.Sprintf("identity.external:%d:%s:%s", len(p.Issuer), p.Issuer, p.Subject)); err != nil {
		return User{}, false, fmt.Errorf("lock external identity: %w", err)
	}

	var userID uuid.UUID
	err = tx.QueryRow(ctx,
		`UPDATE identity.external_identities
		    SET email = $3, email_verified = $4, preferred_username = $5, display_name = $6, last_sign_in_at = now()
		  WHERE issuer = $1 AND subject = $2
		  RETURNING user_id`,
		p.Issuer, p.Subject, nullIfEmpty(p.Email), p.EmailVerified, p.PreferredUsername, p.Name,
	).Scan(&userID)
	switch {
	case err == nil:
		if err := tx.Commit(ctx); err != nil {
			return User{}, false, fmt.Errorf("commit: %w", err)
		}
		u, err := s.UserByID(ctx, userID)
		return u, false, err
	case !errors.Is(err, pgx.ErrNoRows):
		return User{}, false, fmt.Errorf("look up external identity: %w", err)
	}

	// First sign-in. The email is the account's only when the provider
	// vouches for it and nobody here already holds it.
	var email *string
	if p.EmailVerified && p.Email != "" {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.users WHERE email = $1)`, p.Email).Scan(&taken); err != nil {
			return User{}, false, fmt.Errorf("check email: %w", err)
		}
		if !taken {
			email = &p.Email
		}
	}

	u := User{ID: uuid.New()}
	if email != nil {
		u.Email = *email
	}
	inserted := false
	for _, name := range usernameCandidates(DeriveUsername(p.PreferredUsername, p.Email), p.Issuer, p.Subject) {
		// A savepoint per candidate: a unique violation aborts the
		// transaction, and only the savepoint lets the next name be tried.
		sp, err := tx.Begin(ctx)
		if err != nil {
			return User{}, false, fmt.Errorf("savepoint: %w", err)
		}
		// password_hash is empty: VerifyPassword accepts only an argon2id
		// encoding, so an account provisioned here has no password that could
		// ever verify. It signs in through its provider or not at all.
		err = sp.QueryRow(ctx,
			`INSERT INTO identity.users (id, email, username, password_hash) VALUES ($1, $2, $3, '')
			 RETURNING created_at`, u.ID, email, name).Scan(&u.CreatedAt)
		if err == nil {
			if err := sp.Commit(ctx); err != nil {
				return User{}, false, fmt.Errorf("release savepoint: %w", err)
			}
			u.Username = name
			inserted = true
			break
		}
		_ = sp.Rollback(ctx)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolation {
			return User{}, false, fmt.Errorf("provision user: %w", err)
		}
		if pgErr.ConstraintName != "users_username_key" {
			// The email was taken by a registration since the check above.
			return User{}, false, errRetrySignIn
		}
	}
	if !inserted {
		return User{}, false, fmt.Errorf("no free username for %q", p.PreferredUsername)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO identity.external_identities
		   (issuer, subject, user_id, email, email_verified, preferred_username, display_name)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		p.Issuer, p.Subject, u.ID, nullIfEmpty(p.Email), p.EmailVerified, p.PreferredUsername, p.Name,
	); err != nil {
		return User{}, false, fmt.Errorf("link external identity: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, false, fmt.Errorf("commit: %w", err)
	}
	return u, true, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Bounds on what the edge may hand over. OIDC caps a subject at 255 ASCII
// characters; the rest are generous limits so one provider claim cannot fill a
// row.
const (
	maxIssuerLen  = 2048
	maxSubjectLen = 255
	maxClaimLen   = 512
)

// SignInExternal is the RPC behind single sign-on. It trusts its request
// completely — the edge has verified the ID token — which is exactly why only
// the edge may call it: the caller must present a platform token naming
// svcauth.EdgeOIDCService. A signed-in user, an org-scoped service and any
// other platform worker are refused, because any of them could otherwise mint
// a session for whichever subject it named.
func (s *Server) SignInExternal(ctx context.Context, req *identityv1.SignInExternalRequest) (*identityv1.SignInExternalResponse, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || !scope.IsPlatformWorker() || scope.PlatformWorker != svcauth.EdgeOIDCService {
		return nil, status.Error(codes.PermissionDenied, "only the edge may sign in an external identity")
	}
	if req.GetIssuer() == "" || req.GetSubject() == "" {
		return nil, status.Error(codes.InvalidArgument, "issuer and subject are required")
	}
	if len(req.GetIssuer()) > maxIssuerLen || len(req.GetSubject()) > maxSubjectLen ||
		len(req.GetEmail()) > maxClaimLen || len(req.GetPreferredUsername()) > maxClaimLen || len(req.GetName()) > maxClaimLen {
		return nil, status.Error(codes.InvalidArgument, "an external identity claim is too long")
	}
	u, created, err := s.store.SignInExternal(ctx, ExternalProfile{
		Issuer: req.GetIssuer(), Subject: req.GetSubject(),
		Email: req.GetEmail(), EmailVerified: req.GetEmailVerified(),
		PreferredUsername: req.GetPreferredUsername(), Name: req.GetName(),
	})
	if err != nil {
		log.Printf("identity: external sign-in for issuer %q: %v", req.GetIssuer(), err)
		return nil, status.Error(codes.Internal, "identity datastore unavailable")
	}
	token, err := s.sessions.Create(ctx, u.ID, s.sessionTTL)
	if err != nil {
		log.Printf("identity: external sign-in session: %v", err)
		return nil, status.Error(codes.Internal, "create session")
	}
	return &identityv1.SignInExternalResponse{
		SessionToken: token, UserId: u.ID.String(), Username: u.Username, Created: created,
	}, nil
}
