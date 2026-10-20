package identity_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/identity"
	"github.com/novaforge/novaforge/internal/svcauth"
)

const externalSecret = "identity-external-signin-secret"

// externalRPC serves identity behind its production interceptor on a real
// listener, so the authorization under test is the one a deployed edge meets
// — not a scope a test installed by hand.
func externalRPC(t *testing.T) (identityv1.IdentityServiceClient, *identity.Server) {
	t.Helper()
	srv := newGRPCServer(t)
	srv.HMACSecret = externalSecret
	conn := rpcListener(t, func(g *grpc.Server) { identityv1.RegisterIdentityServiceServer(g, srv) }, identity.UnaryAuthInterceptor(srv))
	return identityv1.NewIdentityServiceClient(conn), srv
}

func asBearer(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

func edgeCtx(t *testing.T) context.Context {
	t.Helper()
	tok, err := svcauth.MintPlatform(externalSecret, svcauth.EdgeOIDCService, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return asBearer(context.Background(), tok)
}

func externalIssuer() string { return "https://login.example.test/" + uuid.NewString()[:8] }

func TestSignInExternalCreatesUserAndLinkOnFirstSignInOnly(t *testing.T) {
	client, _ := externalRPC(t)
	iss, sub := externalIssuer(), uuid.NewString()
	name := "Ada." + uuid.NewString()[:6]
	req := &identityv1.SignInExternalRequest{
		Issuer: iss, Subject: sub, Email: strings.ToLower(name) + "@example.test", EmailVerified: true,
		PreferredUsername: name + "@example.test", Name: "Ada Lovelace",
	}

	first, err := client.SignInExternal(edgeCtx(t), req)
	if err != nil {
		t.Fatalf("first sign-in: %v", err)
	}
	if !first.GetCreated() || first.GetSessionToken() == "" || first.GetUserId() == "" {
		t.Fatalf("first sign-in must provision a user and return a session, got %+v", first)
	}
	// preferred_username is a Zitadel login name: the local part, lowercased,
	// is what the platform's username rules keep.
	if want := strings.ToLower(name); first.GetUsername() != want {
		t.Fatalf("username = %q, want %q", first.GetUsername(), want)
	}
	// The session it returned is a real one: identity resolves it to the user.
	who, err := client.GetCurrentUser(asBearer(context.Background(), first.GetSessionToken()), &identityv1.GetCurrentUserRequest{})
	if err != nil {
		t.Fatalf("the returned session does not resolve: %v", err)
	}
	if who.GetUser().GetId() != first.GetUserId() || who.GetUser().GetEmail() != req.Email {
		t.Fatalf("session resolves to %+v, want user %s with email %s", who.GetUser(), first.GetUserId(), req.Email)
	}
	// A just-in-time user belongs to no organization until someone invites it.
	orgs, err := client.ListOrgs(asBearer(context.Background(), first.GetSessionToken()), &identityv1.ListOrgsRequest{})
	if err != nil || len(orgs.GetOrgs()) != 0 {
		t.Fatalf("a provisioned user must belong to no organization, got %v (%v)", orgs.GetOrgs(), err)
	}

	// The second sign-in, even with every profile claim changed, is the same
	// person: the link is (issuer, subject).
	again, err := client.SignInExternal(edgeCtx(t), &identityv1.SignInExternalRequest{
		Issuer: iss, Subject: sub, Email: "renamed-" + uuid.NewString()[:6] + "@example.test", EmailVerified: true,
		PreferredUsername: "someone-else",
	})
	if err != nil {
		t.Fatalf("second sign-in: %v", err)
	}
	if again.GetCreated() || again.GetUserId() != first.GetUserId() || again.GetUsername() != first.GetUsername() {
		t.Fatalf("second sign-in = %+v, want the existing user %s unchanged", again, first.GetUserId())
	}
	if again.GetSessionToken() == first.GetSessionToken() {
		t.Fatal("every sign-in must mint its own session")
	}

	// The same subject from another issuer is another identity.
	other, err := client.SignInExternal(edgeCtx(t), &identityv1.SignInExternalRequest{
		Issuer: externalIssuer(), Subject: sub, PreferredUsername: name,
	})
	if err != nil {
		t.Fatalf("other issuer: %v", err)
	}
	if other.GetUserId() == first.GetUserId() {
		t.Fatal("a subject is only unique within its issuer; another issuer's subject must not reach this account")
	}
}

// TestSignInExternalNeverClaimsAnExistingAccount is the account-takeover
// case: an identity provider asserts the username and the email of an account
// that already exists here. Matching on either would hand that account to
// whoever controls the claim at the provider.
func TestSignInExternalNeverClaimsAnExistingAccount(t *testing.T) {
	client, srv := externalRPC(t)
	_ = srv
	local := "victim-" + uuid.NewString()[:8]
	email := local + "@example.test"
	reg, err := client.Register(context.Background(), &identityv1.RegisterRequest{Email: email, Username: local, Password: "correct horse battery"})
	if err != nil {
		t.Fatal(err)
	}

	iss, sub := externalIssuer(), uuid.NewString()
	for _, verified := range []bool{false, true} {
		got, err := client.SignInExternal(edgeCtx(t), &identityv1.SignInExternalRequest{
			Issuer: iss, Subject: sub, Email: email, EmailVerified: verified, PreferredUsername: local,
		})
		if err != nil {
			t.Fatalf("sign-in (email_verified=%v): %v", verified, err)
		}
		if got.GetUserId() == reg.GetUserId() {
			t.Fatalf("an external identity claiming username %q and email %q (verified=%v) was given the existing account", local, email, verified)
		}
		// Collisions get a deterministic suffix, so the same subject always
		// lands on the same name and a different one does not.
		if !strings.HasPrefix(got.GetUsername(), local+"-") || got.GetUsername() == local {
			t.Fatalf("colliding username = %q, want %q with a suffix", got.GetUsername(), local)
		}
		who, err := client.GetCurrentUser(asBearer(context.Background(), got.GetSessionToken()), &identityv1.GetCurrentUserRequest{})
		if err != nil {
			t.Fatal(err)
		}
		// The address belongs to the existing account, so the new one holds
		// none rather than an invented one.
		if who.GetUser().GetEmail() != "" {
			t.Fatalf("new account was given email %q, which already belongs to another account", who.GetUser().GetEmail())
		}
	}

	// The local account is untouched: its password still signs it in.
	login, err := client.Login(context.Background(), &identityv1.LoginRequest{Username: local, Password: "correct horse battery"})
	if err != nil || login.GetUserId() != reg.GetUserId() {
		t.Fatalf("the existing account no longer signs in: %v", err)
	}

	// A second external subject asserting the same claims is a third person.
	third, err := client.SignInExternal(edgeCtx(t), &identityv1.SignInExternalRequest{
		Issuer: iss, Subject: uuid.NewString(), Email: email, EmailVerified: true, PreferredUsername: local,
	})
	if err != nil {
		t.Fatal(err)
	}
	if third.GetUserId() == reg.GetUserId() || strings.EqualFold(third.GetUsername(), local) {
		t.Fatalf("second colliding subject = %+v", third)
	}
}

// TestSignInExternalRefusesEveryCallerButTheEdge: the RPC mints a session for
// whatever subject it is handed, so a caller that is not the edge — a signed-in
// user, an org-scoped service, another platform worker, a forged token — must
// be refused before anything is created.
func TestSignInExternalRefusesEveryCallerButTheEdge(t *testing.T) {
	client, _ := externalRPC(t)
	iss := externalIssuer()

	user := "caller-" + uuid.NewString()[:8]
	if _, err := client.Register(context.Background(), &identityv1.RegisterRequest{Email: user + "@example.test", Username: user, Password: "pw-123456"}); err != nil {
		t.Fatal(err)
	}
	login, err := client.Login(context.Background(), &identityv1.LoginRequest{Username: user, Password: "pw-123456"})
	if err != nil {
		t.Fatal(err)
	}
	orgScoped, err := svcauth.Mint(externalSecret, svcauth.EdgeOIDCService, uuid.New(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	otherWorker, err := svcauth.MintPlatform(externalSecret, "indexer", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := svcauth.MintPlatform("not-the-deployment-secret", svcauth.EdgeOIDCService, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	callers := map[string]context.Context{
		"anonymous":            context.Background(),
		"signed-in user":       asBearer(context.Background(), login.GetSessionToken()),
		"org-scoped service":   asBearer(context.Background(), orgScoped),
		"other platform agent": asBearer(context.Background(), otherWorker),
		"forged edge token":    asBearer(context.Background(), forged),
	}
	for name, ctx := range callers {
		t.Run(name, func(t *testing.T) {
			sub := uuid.NewString()
			_, err := client.SignInExternal(ctx, &identityv1.SignInExternalRequest{Issuer: iss, Subject: sub, PreferredUsername: "intruder"})
			if c := status.Code(err); c != codes.PermissionDenied {
				t.Fatalf("SignInExternal as %s: code %v (%v), want PermissionDenied", name, c, err)
			}
			// Nothing was provisioned for the refused call: the edge, signing
			// the same subject in afterwards, creates the user itself.
			ok, err := client.SignInExternal(edgeCtx(t), &identityv1.SignInExternalRequest{Issuer: iss, Subject: sub, PreferredUsername: "intruder"})
			if err != nil || !ok.GetCreated() {
				t.Fatalf("refused call left state behind: created=%v err=%v", ok.GetCreated(), err)
			}
		})
	}

	if _, err := client.SignInExternal(edgeCtx(t), &identityv1.SignInExternalRequest{Issuer: iss}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a sign-in without a subject must be refused as invalid, got %v", err)
	}
}

func TestPasswordLoginCanBeSwitchedOff(t *testing.T) {
	client, srv := externalRPC(t)
	user := "pw-" + uuid.NewString()[:8]
	if _, err := client.Register(context.Background(), &identityv1.RegisterRequest{Email: user + "@example.test", Username: user, Password: "pw-123456"}); err != nil {
		t.Fatal(err)
	}
	srv.PasswordLoginEnabled = false
	if _, err := client.Login(context.Background(), &identityv1.LoginRequest{Username: user, Password: "pw-123456"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("Login with password login disabled: %v, want FailedPrecondition", err)
	}
	other := "pw-" + uuid.NewString()[:8]
	if _, err := client.Register(context.Background(), &identityv1.RegisterRequest{Email: other + "@example.test", Username: other, Password: "pw-123456"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("Register with password login disabled: %v, want FailedPrecondition", err)
	}
	// Single sign-on is unaffected.
	if _, err := client.SignInExternal(edgeCtx(t), &identityv1.SignInExternalRequest{Issuer: externalIssuer(), Subject: uuid.NewString()}); err != nil {
		t.Fatalf("SSO with password login disabled: %v", err)
	}
}

func TestDeriveUsername(t *testing.T) {
	cases := []struct{ preferred, email, want string }{
		{"pascal", "", "pascal"},
		{"Pascal.Watteel@azrty.com", "", "pascal.watteel"},
		{"", "Grace@example.test", "grace"},
		{"  Ünïcødé  Name ", "", "n-c-d-name"},
		{"--..__", "", "user"},
		{"", "", "user"},
		{strings.Repeat("a", 80), "", strings.Repeat("a", 39)},
		{"a/b\\c;rm -rf", "", "a-b-c-rm-rf"},
	}
	for _, c := range cases {
		if got := identity.DeriveUsername(c.preferred, c.email); got != c.want {
			t.Errorf("DeriveUsername(%q, %q) = %q, want %q", c.preferred, c.email, got, c.want)
		}
	}
}
