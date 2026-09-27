package gitops_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/platformtest"
	"golang.org/x/crypto/ssh"
)

// All identity, database, Git, SSH, TLS and object-store paths are real. The
// standard git-lfs client must discover the HTTPS endpoint over SSH by itself.
func TestLFSOverSSHWithVerifiedHTTPS(t *testing.T) {
	p := platformtest.Start(t)
	user := p.NewUser(t, "lfsssh")
	org := p.NewOrg(t, user, "lfsssh")
	repo := p.NewRepo(t, user, org, "assets", nil)
	keyPath := p.AddSSHKey(t, user)
	blobs, err := blobstore.New(context.Background(), blobstore.Options{Endpoint: os.Getenv("TEST_S3_ENDPOINT"), AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("TEST_S3_SECRET_KEY"), Bucket: "novaforge-test-lfs-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatal(err)
	}
	lfs := gitops.NewLFSStore(p.Pool, blobs, 16<<20)
	lookup := gitops.NewFingerprintFunc(p.Identity)
	passwords := gitops.NewAgentPasswordFunc(platformtest.HMACSecret)
	caps := gitops.NewGrantCapFunc(p.Grants)
	tlsServer := httptest.NewUnstartedServer(nil)
	broker, err := gitops.NewLFSAuth(platformtest.HMACSecret, "https://"+tlsServer.Listener.Addr().String(), lfs, lookup, passwords)
	if err != nil {
		t.Fatal(err)
	}
	tlsServer.Config.Handler = gitops.NewHTTPHandlerWithLFS(p.GitRoot, gitops.NewCredentialAuthFunc(p.Identity, platformtest.HMACSecret, nil), caps, lfs, broker)
	tlsServer.StartTLS()
	defer tlsServer.Close()
	hostKey, _, _ := newEd25519Signer(t)
	server := gitops.NewSSHServer(p.GitRoot, hostKey, lookup, caps).WithPasswords(passwords).WithLFS(broker)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	defer listener.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsServer.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	if err := os.WriteFile(knownHosts, []byte(fmt.Sprintf("[%s]:%s %s", host, port, ssh.MarshalAuthorizedKey(hostKey.PublicKey()))), 0600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_SSL_CAINFO="+caFile,
		"GIT_SSH_COMMAND=ssh -i "+keyPath+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile="+knownHosts)
	remote := "ssh://git@" + listener.Addr().String() + "/" + org.ID + "/" + repo.Name + ".git"
	work := filepath.Join(t.TempDir(), "work")
	runCmdEnv(t, "", env, "git", "clone", remote, work)
	runCmdEnv(t, work, env, "git", "config", "user.email", "lfs@example.test")
	runCmdEnv(t, work, env, "git", "config", "user.name", "LFS test")
	runCmdEnv(t, work, env, "git", "lfs", "install", "--local")
	runCmdEnv(t, work, env, "git", "lfs", "track", "*.bin")
	payload := make([]byte, 1<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "large.bin"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	runCmdEnv(t, work, env, "git", "add", ".")
	runCmdEnv(t, work, env, "git", "commit", "-m", "large object over SSH")
	runCmdEnv(t, work, env, "git", "push", "origin", "HEAD:main")
	fresh := filepath.Join(t.TempDir(), "fresh")
	runCmdEnv(t, "", env, "git", "clone", remote, fresh)
	runCmdEnv(t, fresh, env, "git", "lfs", "install", "--local")
	runCmdEnv(t, fresh, env, "git", "lfs", "pull")
	got, err := os.ReadFile(filepath.Join(fresh, "large.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("SSH LFS content mismatch: %v", err)
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "git", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(hostKey.PublicKey())})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	session, err := conn.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	authJSON, err := session.Output("git-lfs-authenticate " + org.ID + "/" + repo.Name + ".git download")
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Href   string            `json:"href"`
		Header map[string]string `json:"header"`
	}
	if err := json.Unmarshal(authJSON, &action); err != nil {
		t.Fatal(err)
	}
	request := func(endpoint, method, body string) int {
		t.Helper()
		req, err := http.NewRequest(method, endpoint, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", action.Header["Authorization"])
		resp, err := tlsServer.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if code := request(action.Href+"/objects/batch", "POST", `{"operation":"upload","objects":[]}`); code != 403 {
		t.Fatalf("download token uploaded: %d", code)
	}
	if code := request(strings.Replace(action.Href, repo.Name+".git", "other.git", 1)+"/objects/batch", "POST", `{"operation":"download","objects":[]}`); code != 401 {
		t.Fatalf("token reached another repository: %d", code)
	}
	if code := request(strings.TrimSuffix(action.Href, "/info/lfs")+"/info/refs?service=git-upload-pack", "GET", ""); code != 401 {
		t.Fatalf("LFS token reached Git: %d", code)
	}
	keys, err := p.Identity.ListSSHKeys(platformtest.WithCredential(context.Background(), user.Session, ""), &identityv1.ListSSHKeysRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys.GetKeys() {
		if _, err := p.Identity.DeleteSSHKey(platformtest.WithCredential(context.Background(), user.Session, ""), &identityv1.DeleteSSHKeyRequest{Id: key.GetId()}); err != nil {
			t.Fatal(err)
		}
	}
	if code := request(action.Href+"/objects/batch", "POST", `{"operation":"download","objects":[]}`); code != 401 {
		t.Fatalf("revoked key still authorized LFS: %d", code)
	}
}
