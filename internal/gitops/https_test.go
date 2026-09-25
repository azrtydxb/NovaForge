package gitops_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/gitops"
)

// TestCloneAndPushOverHTTPS is the point of the whole feature: an unmodified
// git client, verifying the served certificate against its issuer's CA, clones
// and pushes. Nothing here trusts the server blindly — GIT_SSL_CAINFO names the
// CA and sslVerify stays on, so a handshake that did not actually present a
// chain for the address being dialled fails the test.
func TestCloneAndPushOverHTTPS(t *testing.T) {
	root := t.TempDir()
	orgID := uuid.New()
	if _, err := gitops.Init(root, orgID, "widgets"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	certDir := t.TempDir()
	ca := writeKeypair(t, certDir, "tls.crt", "tls.key")

	srv, err := gitops.NewTLSServer(testGitHandler(t, root, orgID), filepath.Join(certDir, "tls.crt"), filepath.Join(certDir, "tls.key"))
	if err != nil {
		t.Fatalf("NewTLSServer: %v", err)
	}
	addr := serveTLS(t, srv)

	work := t.TempDir()
	env := append(os.Environ(), "GIT_SSL_CAINFO="+ca)
	repoURL := fmt.Sprintf("https://user:token@%s/%s/widgets.git", addr, orgID)
	runCmdEnv(t, "", env, "git", "clone", repoURL, work)
	writeFileHTTP(t, work+"/hello.txt", "hi\n")
	runCmdEnv(t, work, env, "git", "add", "hello.txt")
	runCmdEnv(t, work, env, "git", "-c", "user.email=a@b.c", "-c", "user.name=Test", "commit", "-m", "over tls")
	runCmdEnv(t, work, env, "git", "push", "origin", "HEAD:refs/heads/main")

	repo, err := gitops.Open(root, orgID, "widgets")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	log, err := repo.Log("main", 10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(log) == 0 || log[0].Message != "over tls" {
		t.Fatalf("want the commit pushed over TLS at the tip, got %+v", log)
	}
}

// TestCloneOverHTTPSByTheCertificateDNSName covers what the cluster actually
// looks like, which the loopback test above does not: cert-manager issues a
// certificate for git-platform's Service names, and an external client reaches
// the Service at a LoadBalancer address no DNS SAN covers. The clone therefore
// names the certificate's host and sends the connection elsewhere with git's
// http.curloptResolve — the mechanism tests/e2e/deploy_test.sh depends on, and
// worth pinning here because that suite cannot run on a workstation.
func TestCloneOverHTTPSByTheCertificateDNSName(t *testing.T) {
	root := t.TempDir()
	orgID := uuid.New()
	if _, err := gitops.Init(root, orgID, "widgets"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	certDir := t.TempDir()
	// The name a Service of this release would have; nothing resolves it.
	const host = "novaforge-git-platform.novaforge.svc"
	ca := writeKeypairFor(t, certDir, host)

	srv, err := gitops.NewTLSServer(testGitHandler(t, root, orgID),
		filepath.Join(certDir, "tls.crt"), filepath.Join(certDir, "tls.key"))
	if err != nil {
		t.Fatalf("NewTLSServer: %v", err)
	}
	addr := serveTLS(t, srv)
	port := addr[strings.LastIndex(addr, ":")+1:]

	work := t.TempDir() + "/repo"
	runCmdEnv(t, "", os.Environ(), "git",
		"-c", fmt.Sprintf("http.curloptResolve=%s:%s:127.0.0.1", host, port),
		"-c", "http.sslCAInfo="+ca,
		"clone", fmt.Sprintf("https://user:token@%s:%s/%s/widgets.git", host, port, orgID), work)
}

// TestPlaintextRequestOnTheTLSPortIsRefused pins the half of this that a
// working HTTPS clone does not prove. A listener that answered both would let
// a client that never asked for TLS — or one whose HTTPS attempt was stripped
// on the way — push a token in the clear and see it succeed, which is exactly
// the failure HTTPS was added to remove. The request must be refused at the
// transport, not served.
func TestPlaintextRequestOnTheTLSPortIsRefused(t *testing.T) {
	root := t.TempDir()
	orgID := uuid.New()
	if _, err := gitops.Init(root, orgID, "widgets"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	certDir := t.TempDir()
	writeKeypair(t, certDir, "tls.crt", "tls.key")

	srv, err := gitops.NewTLSServer(testGitHandler(t, root, orgID), filepath.Join(certDir, "tls.crt"), filepath.Join(certDir, "tls.key"))
	if err != nil {
		t.Fatalf("NewTLSServer: %v", err)
	}
	addr := serveTLS(t, srv)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://user:token@%s/%s/widgets.git/info/refs?service=git-upload-pack", addr, orgID))
	if err == nil {
		// Go's TLS server answers a plaintext request with a bare 400 saying so
		// and closes the connection: the request never reaches the git handler.
		// That is a refusal. What must not happen is the transport serving it.
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode < 400 {
			t.Fatalf("a plaintext request on the TLS port was answered with %s: the port serves both and a client can be downgraded", resp.Status)
		}
		if strings.Contains(string(body), "git-upload-pack") {
			t.Fatalf("the git transport answered a plaintext request on the TLS port: %s", body)
		}
	}

	// And a real git client over http:// fails too, rather than falling back.
	out, gitErr := exec.Command("git", "clone",
		fmt.Sprintf("http://user:token@%s/%s/widgets.git", addr, orgID), t.TempDir()+"/clone").CombinedOutput()
	if gitErr == nil {
		t.Fatalf("git cloned over plaintext http:// from the TLS port: %s", out)
	}
}

// TestNewTLSServerRefusesAnUnreadableKeypair keeps the failure at startup. The
// obvious shape — hand the paths to ServeTLS and let it open them — reports a
// missing certificate from inside a goroutine long after the process has
// declared itself up, and this repository's readiness probe would call that
// pod healthy while its TLS port was never bound. So the pair is loaded and
// parsed before anything is served.
func TestNewTLSServerRefusesAnUnreadableKeypair(t *testing.T) {
	certDir := t.TempDir()
	writeKeypair(t, certDir, "tls.crt", "tls.key")
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	if _, err := gitops.NewTLSServer(handler, filepath.Join(certDir, "absent.crt"), filepath.Join(certDir, "tls.key")); err == nil {
		t.Error("NewTLSServer accepted a certificate path that does not exist")
	}
	if err := os.WriteFile(filepath.Join(certDir, "garbage.crt"), []byte("not a pem file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitops.NewTLSServer(handler, filepath.Join(certDir, "garbage.crt"), filepath.Join(certDir, "tls.key")); err == nil {
		t.Error("NewTLSServer accepted a certificate that is not a PEM keypair")
	}
}

// TestTLSServerPicksUpARenewedCertificate covers the failure that only appears
// months in: cert-manager renews the Certificate in place, the kubelet swaps
// the files in the mounted Secret, and a process that read the keypair once at
// startup goes on presenting the expired one until somebody restarts it — with
// every probe green. The server must therefore serve what is on disk now.
func TestTLSServerPicksUpARenewedCertificate(t *testing.T) {
	certDir := t.TempDir()
	ca := writeKeypair(t, certDir, "tls.crt", "tls.key")
	srv, err := gitops.NewTLSServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") }),
		filepath.Join(certDir, "tls.crt"), filepath.Join(certDir, "tls.key"))
	if err != nil {
		t.Fatalf("NewTLSServer: %v", err)
	}
	addr := serveTLS(t, srv)

	before := servedSerial(t, addr, ca)
	// The renewal writes a new keypair over the same paths, as the kubelet does.
	// The modification time must move or the reload cannot be observed at all;
	// a same-second rewrite is indistinguishable from no change.
	time.Sleep(1100 * time.Millisecond)
	caNew := writeKeypair(t, certDir, "tls.crt", "tls.key")
	after := servedSerial(t, addr, caNew)
	if before.Cmp(after) == 0 {
		t.Fatalf("the server still presents certificate serial %s after the keypair on disk was renewed", before)
	}
}

// TestChartTLSPortMatchesTheServedPort ties the chart to the binary. The port
// is a constant rather than configuration (the certificate's SANs, the Service
// and the clone URL all have to agree, and a fourth place to set it is a fourth
// place to get it wrong), which only holds while the chart agrees with it.
func TestChartTLSPortMatchesTheServedPort(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRootFromTest(t), "deploy", "helm", "novaforge", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Services map[string]struct {
			HTTPSPort int `yaml:"httpsPort"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	git, ok := values.Services["git-platform"]
	if !ok {
		t.Fatal("values.yaml lists no git-platform service")
	}
	if git.HTTPSPort != gitops.DefaultHTTPSPort {
		t.Errorf("the chart publishes git-platform's TLS port as %d, but the binary serves %d",
			git.HTTPSPort, gitops.DefaultHTTPSPort)
	}
}

// repoRootFromTest walks up to the module root, which is where the chart lives.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// testGitHandler is the smart-HTTP handler with authentication and capability
// checks that accept the fixed test credential: this suite is about the
// transport, and the handler's own rules are covered by http_test.go.
func testGitHandler(t *testing.T, root string, orgID uuid.UUID) http.Handler {
	t.Helper()
	auth := func(ctx context.Context, user, pass, orgRef string) (authz.Scope, error) {
		if user != "user" || pass != "token" {
			return authz.Scope{}, errors.New("invalid credentials")
		}
		return authz.Scope{OrgID: orgID, ActorID: uuid.New(), ActorKind: "user"}, nil
	}
	caps := func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		return nil
	}
	return gitops.NewHTTPHandler(root, auth, caps)
}

// serveTLS starts srv on a loopback port and returns its address.
func serveTLS(t *testing.T, srv *http.Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Logf("ServeTLS: %v", err)
		}
	}()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// servedSerial completes a handshake against addr, verifying the chain against
// caFile, and returns the serial of the certificate the server presented.
func servedSerial(t *testing.T, addr, caFile string) *big.Int {
	t.Helper()
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("the test CA is not a PEM certificate")
	}
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("tls dial %s: %v", addr, err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber
}

// writeKeypair issues a fresh keypair for 127.0.0.1 into dir/certName and
// dir/keyName. See issueKeypair.
func writeKeypair(t *testing.T, dir, certName, keyName string) string {
	t.Helper()
	return issueKeypair(t, dir, certName, keyName, "127.0.0.1", []net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"})
}

// writeKeypairFor issues a fresh keypair whose only name is host, as
// cert-manager's would be: a DNS name and no address.
func writeKeypairFor(t *testing.T, dir, host string) string {
	t.Helper()
	return issueKeypair(t, dir, "tls.crt", "tls.key", host, nil, []string{host})
}

// issueKeypair writes a self-signed certificate and key and returns the path of
// the CA file a client verifies it with. Self-signed, so the CA file is the
// certificate itself; on the cluster cert-manager's ClusterIssuer plays that
// part and the client is given its ca.crt.
func issueKeypair(t *testing.T, dir, certName, keyName, commonName string, ips []net.IP, dnsNames []string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           ips,
		DNSNames:              dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, certName)
	keyPath := filepath.Join(dir, keyName)
	writeFileHTTP(t, certPath, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	writeFileHTTP(t, keyPath, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})))
	return certPath
}

func runCmdEnv(t *testing.T, dir string, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = env
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v — %s", name, strings.Join(args, " "), err, out)
	}
}
