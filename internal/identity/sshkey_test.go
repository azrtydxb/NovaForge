package identity_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/identity"
	"golang.org/x/crypto/ssh"
)

// generateAuthorizedKey produces a fresh ed25519 authorized-key line for
// tests, avoiding any hardcoded key material in source.
func generateAuthorizedKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("NewPublicKey: %v", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
}

func TestAddKeyComputesFingerprint(t *testing.T) {
	store := newStore(t)
	keys := identity.NewSSHKeyStore(storePool(t))
	ctx := context.Background()

	username := uniqueName("dave")
	user, err := store.CreateUser(ctx, username+"@example.com", username, "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	authorizedKey := generateAuthorizedKey(t)
	key, err := keys.Add(ctx, user.ID, "laptop", authorizedKey)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !strings.HasPrefix(key.Fingerprint, "SHA256:") {
		t.Fatalf("want fingerprint starting with %q, got %q", "SHA256:", key.Fingerprint)
	}
}

func TestRejectMalformedKey(t *testing.T) {
	store := newStore(t)
	keys := identity.NewSSHKeyStore(storePool(t))
	ctx := context.Background()

	username := uniqueName("erin")
	user, err := store.CreateUser(ctx, username+"@example.com", username, "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	_, err = keys.Add(ctx, user.ID, "t", "not-a-key")
	if err == nil || !contains(err.Error(), "parse") {
		t.Fatalf("want error containing %q, got %v", "parse", err)
	}
}
