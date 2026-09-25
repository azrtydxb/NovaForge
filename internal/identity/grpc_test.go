package identity_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238 mandates HMAC-SHA1 for TOTP.
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/identity"
)

func newRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	opts, err := redis.ParseURL(redisURL(t))
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { client.Close() })
	return client
}

func newGRPCServer(t *testing.T) *identity.Server {
	t.Helper()
	pool := storePool(t)
	rdb := newRedisClient(t)

	url := dbURL(t)
	if err := database.Migrate(url, "gitplatform", capability.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform (capability) schema: %v", err)
	}

	store := identity.NewStore(pool)
	sessions := identity.NewSessionStore(rdb)
	tokens := identity.NewTokenStore(pool)
	sshKeys := identity.NewSSHKeyStore(pool)
	grants := capability.NewStore(pool)
	return identity.NewGRPCServer(store, sessions, tokens, sshKeys, grants)
}

func randomUsername(prefix string) string {
	return prefix + "-" + uuid.NewString()[:8]
}

// totpCodeAt independently reimplements RFC 6238/4226 (the same algorithm
// internal/identity/totp.go uses) so this test can produce a code the
// package's own ValidateTOTP accepts, without depending on any unexported
// helper from that package.
func totpCodeAt(secret string, at time.Time) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		return ""
	}
	counter := uint64(at.Unix()) / 30

	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(counterBytes[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	truncated := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	return fmt.Sprintf("%06d", truncated%1_000_000)
}

func TestLoginRequiresTOTPWhenEnabled(t *testing.T) {
	srv := newGRPCServer(t)
	ctx := context.Background()

	username := randomUsername("totp-user")
	reg, err := srv.Register(ctx, &identityv1.RegisterRequest{
		Email:    username + "@example.com",
		Username: username,
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	userID, err := uuid.Parse(reg.GetUserId())
	if err != nil {
		t.Fatalf("parse user id: %v", err)
	}

	secret, _, err := identity.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("generate totp secret: %v", err)
	}
	store := identity.NewStore(storePool(t))
	if err := store.SetTOTPSecret(ctx, userID, secret); err != nil {
		t.Fatalf("set totp secret: %v", err)
	}

	// Without a code: Unauthenticated, requires_totp true.
	resp, err := srv.Login(ctx, &identityv1.LoginRequest{Username: username, Password: "correct horse battery staple"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want codes.Unauthenticated, got %v", err)
	}
	if resp == nil || !resp.GetRequiresTotp() {
		t.Fatalf("want requires_totp true, got %+v", resp)
	}

	// With a valid code: succeeds and returns a session token.
	code := totpCodeAt(secret, time.Now())
	resp, err = srv.Login(ctx, &identityv1.LoginRequest{
		Username: username,
		Password: "correct horse battery staple",
		TotpCode: code,
	})
	if err != nil {
		t.Fatalf("login with valid totp code: %v", err)
	}
	if resp.GetSessionToken() == "" {
		t.Fatal("want a non-empty session token")
	}
}

func TestResolveTokenRejectsRevoked(t *testing.T) {
	srv := newGRPCServer(t)
	ctx := context.Background()

	username := randomUsername("token-user")
	reg, err := srv.Register(ctx, &identityv1.RegisterRequest{
		Email:    username + "@example.com",
		Username: username,
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	userID, err := uuid.Parse(reg.GetUserId())
	if err != nil {
		t.Fatalf("parse user id: %v", err)
	}

	tokens := identity.NewTokenStore(storePool(t))
	plaintext, tok, err := tokens.Create(ctx, userID, "ci", []string{"repo:read"}, nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if err := tokens.Revoke(ctx, tok.ID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	_, err = srv.ResolveToken(ctx, &identityv1.ResolveTokenRequest{Token: plaintext})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want codes.Unauthenticated, got %v", err)
	}
}

func TestOrgMembershipAndGrantLifecycle(t *testing.T) {
	srv := newGRPCServer(t)
	ctx := context.Background()

	ownerUsername := randomUsername("owner")
	ownerReg, err := srv.Register(ctx, &identityv1.RegisterRequest{
		Email:    ownerUsername + "@example.com",
		Username: ownerUsername,
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("register owner: %v", err)
	}
	ownerID, err := uuid.Parse(ownerReg.GetUserId())
	if err != nil {
		t.Fatalf("parse owner id: %v", err)
	}

	otherUsername := randomUsername("other")
	otherReg, err := srv.Register(ctx, &identityv1.RegisterRequest{
		Email:    otherUsername + "@example.com",
		Username: otherUsername,
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("register other: %v", err)
	}
	otherID, err := uuid.Parse(otherReg.GetUserId())
	if err != nil {
		t.Fatalf("parse other id: %v", err)
	}

	ownerCtx := authz.WithScope(ctx, authz.Scope{ActorID: ownerID, ActorKind: "user"})
	otherCtx := authz.WithScope(ctx, authz.Scope{ActorID: otherID, ActorKind: "user"})

	// CreateOrg requires an authenticated caller.
	if _, err := srv.CreateOrg(ctx, &identityv1.CreateOrgRequest{Name: "acme-" + uuid.NewString()[:8]}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want codes.Unauthenticated with no scope, got %v", err)
	}

	orgResp, err := srv.CreateOrg(ownerCtx, &identityv1.CreateOrgRequest{Name: "acme-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatalf("create org: %v", err)
	}

	// A non-member is denied issuing a grant in this org.
	_, err = srv.IssueGrant(otherCtx, &identityv1.IssueGrantRequest{
		OrgId:       orgResp.GetOrg().GetId(),
		SubjectId:   otherID.String(),
		SubjectKind: "user",
		RepoRead:    true,
		WriteBranch: "agents/NF-1/",
		TtlSeconds:  3600,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("want codes.PermissionDenied for non-member, got %v", err)
	}

	// The owner adds the other user as a member.
	if _, err := srv.AddOrgMember(ownerCtx, &identityv1.AddOrgMemberRequest{
		OrgId:  orgResp.GetOrg().GetId(),
		UserId: otherID.String(),
		Role:   "member",
	}); err != nil {
		t.Fatalf("add org member: %v", err)
	}

	// Now the (former) other user can issue and fetch a grant.
	issueResp, err := srv.IssueGrant(otherCtx, &identityv1.IssueGrantRequest{
		OrgId:       orgResp.GetOrg().GetId(),
		SubjectId:   otherID.String(),
		SubjectKind: "user",
		RepoRead:    true,
		WriteBranch: "agents/NF-1/",
		TtlSeconds:  3600,
	})
	if err != nil {
		t.Fatalf("issue grant: %v", err)
	}
	if issueResp.GetGrant().GetId() == "" {
		t.Fatal("want a non-empty grant id")
	}

	// GetGrant never derives organization authority from a caller-supplied id.
	if _, err := srv.GetGrant(otherCtx, &identityv1.GetGrantRequest{Id: issueResp.GetGrant().GetId()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unscoped grant lookup: %v", err)
	}
	grantCtx := authz.WithScope(ctx, authz.Scope{OrgID: uuid.MustParse(orgResp.GetOrg().GetId()), ActorID: otherID, ActorKind: "user"})
	getResp, err := srv.GetGrant(grantCtx, &identityv1.GetGrantRequest{Id: issueResp.GetGrant().GetId()})
	if err != nil {
		t.Fatalf("get grant: %v", err)
	}
	if getResp.GetGrant().GetWriteBranch() != "agents/NF-1/" {
		t.Fatalf("want write branch %q, got %q", "agents/NF-1/", getResp.GetGrant().GetWriteBranch())
	}
}

// TestAddOrgMemberAsTheScreensAsk pins adding a member the way the GUI asks:
// by the organization's name and the new member's username. Both used to be
// refused ("invalid org_id") because only UUIDs were accepted, so no member
// could be added from the interface at all. It also pins who may add one:
// an owner or admin, not any member.
func TestAddOrgMemberAsTheScreensAsk(t *testing.T) {
	srv := newGRPCServer(t)
	ctx := context.Background()
	register := func(prefix string) (uuid.UUID, string) {
		name := randomUsername(prefix)
		reg, err := srv.Register(ctx, &identityv1.RegisterRequest{Email: name + "@example.com", Username: name, Password: "correct horse battery staple"})
		if err != nil {
			t.Fatalf("register %s: %v", prefix, err)
		}
		return uuid.MustParse(reg.GetUserId()), name
	}
	ownerID, _ := register("owner")
	memberID, memberName := register("member")
	_, thirdName := register("third")
	ownerCtx := authz.WithScope(ctx, authz.Scope{ActorID: ownerID, ActorKind: "user"})
	memberCtx := authz.WithScope(ctx, authz.Scope{ActorID: memberID, ActorKind: "user"})

	orgName := "team-" + uuid.NewString()[:8]
	if _, err := srv.CreateOrg(ownerCtx, &identityv1.CreateOrgRequest{Name: orgName}); err != nil {
		t.Fatalf("create org: %v", err)
	}

	if _, err := srv.AddOrgMember(ownerCtx, &identityv1.AddOrgMemberRequest{OrgId: orgName, Username: memberName, Role: "member"}); err != nil {
		t.Fatalf("owner adds a member by org name and username: %v", err)
	}
	if _, err := srv.AddOrgMember(memberCtx, &identityv1.AddOrgMemberRequest{OrgId: orgName, Username: thirdName, Role: "member"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("a plain member adding a member: code = %v, want PermissionDenied", status.Code(err))
	}
	if _, err := srv.AddOrgMember(ownerCtx, &identityv1.AddOrgMemberRequest{OrgId: orgName, Username: thirdName, Role: "overlord"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown role: code = %v, want InvalidArgument", status.Code(err))
	}
	if _, err := srv.AddOrgMember(ownerCtx, &identityv1.AddOrgMemberRequest{OrgId: orgName, Username: "nobody-" + uuid.NewString()[:8], Role: "member"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown username: code = %v, want NotFound", status.Code(err))
	}
}
