package gitops

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// DefaultHTTPSPort is the port the git smart-HTTP transport serves TLS on.
//
// It is a constant and not configuration on purpose: the certificate's SANs,
// the chart's Service and container ports, and the clone URL every client is
// given all have to agree about it, and a per-deployment setting would be a
// fourth place for them to disagree. internal/gitops's TestChartTLSPortMatches
// TheServedPort holds the chart to this number.
const DefaultHTTPSPort = 8443

// NewTLSServer returns an *http.Server that serves handler over TLS with the
// PEM keypair at certFile and keyFile. Serve it with srv.ServeTLS(ln, "", "")
// — the paths are already bound, so ServeTLS must not be given them again.
//
// The keypair is loaded and parsed here rather than left to ServeTLS, which
// opens the files only once it is serving, inside the goroutine the caller
// started it in. A missing or unparsable certificate there surfaces as a log
// line the readiness probe knows nothing about: the pod reports healthy, the
// plaintext port answers, and the TLS port is simply never bound — the exact
// silent-seam failure this repository keeps finding. Failing here lets the
// binary refuse to start instead.
func NewTLSServer(handler http.Handler, certFile, keyFile string) (*http.Server, error) {
	kp := &reloadingKeypair{certFile: certFile, keyFile: keyFile}
	if _, err := kp.certificate(); err != nil {
		return nil, err
	}
	return &http.Server{
		Handler: handler,
		// Matches the plaintext listener: a client that opens a connection and
		// sends no request must not hold a handler goroutine open forever.
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			// Per handshake, so a renewed keypair is served without a restart.
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				return kp.certificate()
			},
		},
	}, nil
}

// reloadingKeypair serves the keypair that is on disk now, not the one that was
// there at startup.
//
// cert-manager renews a Certificate well before it expires and the kubelet
// swaps the files in the mounted Secret in place. A process that read them once
// keeps presenting the old certificate until something restarts it — and
// nothing will, because it is healthy by every probe it has. The files are
// therefore re-read whenever their size or modification time has moved, which
// costs two stats per handshake and nothing else.
type reloadingKeypair struct {
	certFile string
	keyFile  string

	mu     sync.Mutex
	cached *tls.Certificate
	stamp  string
}

func (k *reloadingKeypair) certificate() (*tls.Certificate, error) {
	stamp, err := k.stampOnDisk()
	if err != nil {
		// A stat that fails is not a reason to drop TLS on a connection that a
		// previously loaded keypair can still serve: the mount may be mid-swap.
		k.mu.Lock()
		cached := k.cached
		k.mu.Unlock()
		if cached != nil {
			return cached, nil
		}
		return nil, err
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cached != nil && k.stamp == stamp {
		return k.cached, nil
	}
	cert, err := tls.LoadX509KeyPair(k.certFile, k.keyFile)
	if err != nil {
		if k.cached != nil {
			// Half-written files during a swap must not break live handshakes;
			// the previous pair is still valid until it expires.
			return k.cached, nil
		}
		return nil, fmt.Errorf("load tls keypair %s/%s: %w", k.certFile, k.keyFile, err)
	}
	k.cached, k.stamp = &cert, stamp
	return k.cached, nil
}

func (k *reloadingKeypair) stampOnDisk() (string, error) {
	certInfo, err := os.Stat(k.certFile)
	if err != nil {
		return "", fmt.Errorf("stat tls certificate: %w", err)
	}
	keyInfo, err := os.Stat(k.keyFile)
	if err != nil {
		return "", fmt.Errorf("stat tls key: %w", err)
	}
	return fmt.Sprintf("%d/%d/%d/%d",
		certInfo.Size(), certInfo.ModTime().UnixNano(),
		keyInfo.Size(), keyInfo.ModTime().UnixNano()), nil
}
