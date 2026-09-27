package gitops

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLFSExpiredTicketAndPlaintextRefused(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	auth := &LFSAuth{aead: aead}
	org := uuid.New()
	raw, _ := json.Marshal(lfsTicket{OrgID: org, RepoID: uuid.New(), Repo: "repo", Operation: "download", Expires: time.Now().Add(-time.Second).Unix()})
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, raw, []byte("ssh-lfs-v1")))
	req := httptest.NewRequest("POST", "https://git.example/", nil)
	req.Header.Set("Authorization", "Bearer nflfs_"+token)
	req.TLS = &tls.ConnectionState{}
	if _, _, err := auth.authenticate(req, parsedPath{orgRef: org.String(), repo: "repo", op: lfsBatchPath}); err == nil {
		t.Fatal("expired ticket accepted")
	}
	req.TLS = nil
	if _, _, err := auth.authenticate(req, parsedPath{orgRef: org.String(), repo: "repo", op: lfsBatchPath}); err == nil {
		t.Fatal("plaintext ticket accepted")
	}
}
