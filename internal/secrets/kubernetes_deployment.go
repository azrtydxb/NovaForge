package secrets

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/novaforge/novaforge/internal/authz"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// KubernetesDeploymentBinding pins provider output to one repository/target.
// The provider role must generate an independent service account per lease.
// Certificate trust and API address are operator configuration, never output
// accepted from the provider or from an Engineering Run.
type KubernetesDeploymentBinding struct {
	RepoID    uuid.UUID `json:"repo_id"`
	Target    string    `json:"target"`
	Namespace string    `json:"namespace"`
	Server    string    `json:"server"`
	CAFile    string    `json:"ca_file"`
}

func validateKubernetesDeployment(binding OpenBaoBinding) error {
	k := binding.KubernetesDeployment
	u, err := url.Parse(k.Server)
	if k.RepoID == uuid.Nil || k.Target == "" || k.Namespace == "" || strings.ContainsAny(k.Namespace, "/\\ ") || k.CAFile == "" || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || binding.Method != "POST" || !binding.JSON || binding.Field != "" || !binding.RequireHardExpiry {
		return errors.New("invalid operator Kubernetes deployment binding")
	}
	return nil
}

func (b *Broker) prepareKubernetesDeployment(ctx context.Context, req DeploymentCredentialRequest, binding OpenBaoBinding) (DeploymentCredential, error) {
	k := binding.KubernetesDeployment
	if req.RepoID != k.RepoID || req.Target != k.Target {
		return DeploymentCredential{}, ErrProviderNotConfigured
	}
	scope, _ := authz.FromContext(ctx)
	// Serialize issuer replay without holding a table transaction over network I/O.
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return DeploymentCredential{}, err
	}
	defer conn.Release()
	lock := scope.OrgID.String() + "/deployment/" + req.OperationID.String() + "/" + req.AttemptID.String()
	var held bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, lock).Scan(&held); err != nil || !held {
		return DeploymentCredential{}, ErrProviderUnavailable
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lock); err != nil {
			_ = conn.Conn().Close(cleanup)
		}
	}()
	ctx = WithAttemptID(ctx, req.AttemptID)
	var id uuid.UUID
	var ready, revoked bool
	var encrypted []byte
	var providerID, providerEndpoint string
	var expiry time.Time
	err = conn.QueryRow(ctx, `SELECT id,provider_ready,revocation_requested OR revoked_at IS NOT NULL,issued_ciphertext,expires_at,provider_lease_id,provider_endpoint FROM secrets.secret_leases WHERE org_id=$1 AND run_id=$2 AND attempt_id=$3 AND name=$4 ORDER BY id LIMIT 1`, scope.OrgID, req.OperationID, req.AttemptID, req.Name).Scan(&id, &ready, &revoked, &encrypted, &expiry, &providerID, &providerEndpoint)
	var value string
	if errors.Is(err, pgx.ErrNoRows) {
		// TokenRequest's minimum lifetime is ten minutes. Leave room for provider
		// processing inside the fixed twelve-minute ceiling; shorter authority is
		// refused, never extended to accommodate the provider.
		if time.Until(req.ExpiresAt) < 11*time.Minute {
			return DeploymentCredential{}, ErrHardExpiryUnavailable
		}
		params := map[string]string{}
		for name, value := range binding.Parameters {
			params[name] = value
		}
		params["kubernetes_namespace"] = k.Namespace
		params["ttl"] = "600s"
		binding.Parameters = params
		lease, e := b.issueDynamic(ctx, req.OperationID, scope.OrgID, binding, req.ExpiresAt)
		if e != nil {
			return DeploymentCredential{}, e
		}
		id, expiry = lease.ID, lease.ExpiresAt
		value, err = b.Redeem(WithRunID(ctx, req.OperationID), lease.Token)
	} else if err == nil {
		if !ready || revoked {
			return DeploymentCredential{}, ErrScopeClosed
		}
		if providerEndpoint != b.provider.endpoint {
			return DeploymentCredential{}, ErrProviderContract
		}
		current, e := b.provider.expiry(ctx, providerID)
		if e != nil || !current.Truncate(time.Microsecond).Equal(expiry) || !current.After(time.Now()) {
			return DeploymentCredential{}, ErrProviderContract
		}
		value, err = b.decrypt(encrypted)
	}
	if err != nil {
		return DeploymentCredential{}, err
	}
	fail := func(cause error) (DeploymentCredential, error) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		return DeploymentCredential{}, errors.Join(cause, b.Revoke(cleanup, id))
	}
	var data struct {
		Token     string `json:"service_account_token"`
		Name      string `json:"service_account_name"`
		Namespace string `json:"service_account_namespace"`
	}
	if json.Unmarshal([]byte(value), &data) != nil || data.Token == "" || data.Name == "" || data.Namespace != k.Namespace {
		return fail(ErrProviderContract)
	}
	parts := strings.Split(data.Token, ".")
	if len(parts) != 3 {
		return fail(ErrHardExpiryUnavailable)
	}
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fail(ErrHardExpiryUnavailable)
	}
	var claims struct {
		Expires int64  `json:"exp"`
		Subject string `json:"sub"`
	}
	if json.Unmarshal(claimsRaw, &claims) != nil || claims.Subject != "system:serviceaccount:"+k.Namespace+":"+data.Name {
		return fail(ErrHardExpiryUnavailable)
	}
	cutoff := time.Unix(claims.Expires, 0)
	if !cutoff.After(time.Now().Add(5*time.Minute)) || cutoff.After(req.ExpiresAt) || !expiry.After(time.Now()) {
		return fail(ErrHardExpiryUnavailable)
	}
	ca, err := os.ReadFile(k.CAFile)
	if err != nil {
		return fail(ErrProviderUnavailable)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return fail(ErrProviderContract)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrProviderContract }}
	// The target authenticates the signed JWT (including expiry) and confirms the
	// fixed namespace permission needed by the approved Helm chart. Decoding exp
	// alone would trust an unverified provider assertion and is insufficient.
	probe, err := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(k.Server, "/")+"/api/v1/namespaces/"+url.PathEscape(k.Namespace)+"/configmaps?limit=1", nil)
	if err != nil {
		return fail(ErrProviderContract)
	}
	probe.Header.Set("Authorization", "Bearer "+data.Token)
	response, err := client.Do(probe)
	if err != nil {
		return fail(ErrProviderUnavailable)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("target credential verification refused: HTTP %s", strconv.Itoa(response.StatusCode)))
	}
	// Recheck a cancellation committed while the provider/target call was in flight.
	if err = b.deploymentIntent(ctx, req, false); err != nil {
		return fail(err)
	}
	config := clientcmdapi.Config{APIVersion: "v1", Kind: "Config", CurrentContext: "deployment", Clusters: map[string]*clientcmdapi.Cluster{"target": {Server: k.Server, CertificateAuthorityData: ca}}, AuthInfos: map[string]*clientcmdapi.AuthInfo{"lease": {Token: data.Token}}, Contexts: map[string]*clientcmdapi.Context{"deployment": {Cluster: "target", AuthInfo: "lease", Namespace: k.Namespace}}}
	encoded, err := clientcmd.Write(config)
	if err != nil {
		return fail(ErrProviderContract)
	}
	if expiry.Before(cutoff) {
		cutoff = expiry
	}
	return DeploymentCredential{Kubeconfig: string(encoded), ExpiresAt: cutoff}, nil
}
