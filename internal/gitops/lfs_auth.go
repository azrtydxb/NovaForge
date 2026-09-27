package gitops

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"golang.org/x/crypto/ssh"
)

const lfsAuthTTL = 5 * time.Minute

type lfsOperationKey struct{}

// LFSAuth mints encrypted, purpose-bound credentials rather than a broad PAT.
// The underlying SSH identity is re-resolved on each request, so removing a key,
// membership or agent credential is not delayed by this credential's TTL.
type LFSAuth struct {
	base      string
	aead      cipher.AEAD
	store     *LFSStore
	lookup    FingerprintFunc
	passwords PasswordFunc
}
type lfsTicket struct {
	OrgID       uuid.UUID `json:"org"`
	RepoID      uuid.UUID `json:"repo_id"`
	Repo        string    `json:"repo"`
	Operation   string    `json:"operation"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Password    string    `json:"password,omitempty"`
	Expires     int64     `json:"expires"`
}

func NewLFSAuth(secret, base string, store *LFSStore, lookup FingerprintFunc, passwords PasswordFunc) (*LFSAuth, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || secret == "" || store == nil || lookup == nil {
		return nil, fmt.Errorf("SSH LFS requires a credential key, storage and an HTTPS origin")
	}
	key := sha256.Sum256([]byte("novaforge:ssh-lfs:v1:" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &LFSAuth{base: strings.TrimSuffix(base, "/"), aead: aead, store: store, lookup: lookup, passwords: passwords}, nil
}

var lfsCommand = regexp.MustCompile(`^git-lfs-authenticate (?:'([^']+)'|([^ \t]+)) (upload|download)$`)

func (s *SSHServer) WithLFS(auth *LFSAuth) *SSHServer { s.lfsAuth = auth; return s }

func (s *SSHServer) authenticateLFS(conn *ssh.ServerConn, ch ssh.Channel, command string) {
	m := lfsCommand.FindStringSubmatch(command)
	if m == nil || s.lfsAuth == nil {
		fmt.Fprintln(ch.Stderr(), "SSH LFS authentication is unavailable or malformed")
		writeExitStatus(ch, 1)
		return
	}
	path := m[1]
	if path == "" {
		path = m[2]
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 2 || !strings.HasSuffix(parts[1], ".git") || !repoNameRe.MatchString(strings.TrimSuffix(parts[1], ".git")) {
		fmt.Fprintln(ch.Stderr(), "invalid LFS repository path")
		writeExitStatus(ch, 1)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t := lfsTicket{Repo: strings.TrimSuffix(parts[1], ".git"), Operation: m[3], Expires: time.Now().Add(lfsAuthTTL).Unix(), Fingerprint: conn.Permissions.Extensions["fingerprint"], Password: conn.Permissions.Extensions[passwordExtension]}
	scope, err := s.lfsAuth.subject(ctx, t, parts[0])
	if err == nil && scope.OrgID != uuid.Nil {
		t.OrgID = scope.OrgID
		t.RepoID, _, err = s.lfsAuth.store.RepoForLFS(ctx, t.OrgID, t.Repo)
	} else if err == nil {
		err = fmt.Errorf("missing organization")
	}
	if err == nil {
		err = s.caps(ctx, scope, t.OrgID, t.Repo, nil)
	}
	if err != nil {
		fmt.Fprintln(ch.Stderr(), "no access to LFS repository")
		writeExitStatus(ch, 1)
		return
	}
	raw, err := json.Marshal(t)
	if err != nil {
		writeExitStatus(ch, 1)
		return
	}
	nonce := make([]byte, s.lfsAuth.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		writeExitStatus(ch, 1)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(s.lfsAuth.aead.Seal(nonce, nonce, raw, []byte("ssh-lfs-v1")))
	response := struct {
		Href      string            `json:"href"`
		Header    map[string]string `json:"header"`
		ExpiresIn int               `json:"expires_in"`
	}{
		Href: s.lfsAuth.base + "/" + t.OrgID.String() + "/" + t.Repo + ".git/info/lfs", Header: map[string]string{"Authorization": "Bearer nflfs_" + token}, ExpiresIn: int(lfsAuthTTL / time.Second),
	}
	if err := json.NewEncoder(ch).Encode(response); err != nil {
		writeExitStatus(ch, 1)
		return
	}
	writeExitStatus(ch, 0)
}

func (a *LFSAuth) subject(ctx context.Context, t lfsTicket, org string) (authz.Scope, error) {
	if t.Password != "" && a.passwords != nil {
		return a.passwords(ctx, t.Password)
	}
	if t.Fingerprint != "" {
		return a.lookup(ctx, t.Fingerprint, org)
	}
	return authz.Scope{}, fmt.Errorf("missing SSH identity")
}

func (a *LFSAuth) authenticate(r *http.Request, pp parsedPath) (authz.Scope, string, error) {
	fail := func() (authz.Scope, string, error) { return authz.Scope{}, "", fmt.Errorf("invalid LFS credential") }
	if r.TLS == nil || len(r.Header.Get("Authorization")) > 16384 {
		return fail()
	}
	if pp.op != lfsBatchPath && !strings.HasPrefix(pp.op, lfsObjectsPath) {
		return fail()
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer nflfs_"))
	if err != nil || len(raw) < a.aead.NonceSize() {
		return fail()
	}
	n := a.aead.NonceSize()
	plain, err := a.aead.Open(nil, raw[:n], raw[n:], []byte("ssh-lfs-v1"))
	if err != nil {
		return fail()
	}
	var ticket lfsTicket
	if json.Unmarshal(plain, &ticket) != nil || time.Now().Unix() >= ticket.Expires || ticket.OrgID.String() != pp.orgRef || ticket.Repo != pp.repo {
		return fail()
	}
	if ticket.Operation != "upload" && ticket.Operation != "download" {
		return fail()
	}
	if pp.op != lfsBatchPath && ((r.Method == http.MethodPut && ticket.Operation != "upload") || (r.Method == http.MethodGet && ticket.Operation != "download")) {
		return fail()
	}
	scope, err := a.subject(r.Context(), ticket, pp.orgRef)
	if err != nil || scope.OrgID != ticket.OrgID {
		return fail()
	}
	id, _, err := a.store.RepoForLFS(r.Context(), scope.OrgID, pp.repo)
	if err != nil || id != ticket.RepoID {
		return fail()
	}
	return scope, ticket.Operation, nil
}
