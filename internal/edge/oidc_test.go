package edge_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/edge"
	"github.com/novaforge/novaforge/internal/identity"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// The identity provider is an EXTERNAL system, so it is the one thing here
// that is a test double: an in-process issuer serving a real discovery
// document, a real JWKS and a token endpoint that checks PKCE and signs real
// RS256 ID tokens. Everything NovaForge owns — the edge router, identity, its
// PostgreSQL schema and its Redis sessions — is the real thing.

const oidcSecret = "edge-oidc-test-hmac-secret"
const oidcClientID = "novaforge-test-client"

type issuedCode struct {
	challenge, nonce, clientID, redirect, subject string
}

// tokenShape lets a case bend the ID token the issuer mints.
type tokenShape struct {
	claims map[string]any
	// key and kid sign the token; nil key means the issuer's published one.
	key *rsa.PrivateKey
	kid string
	// tamper flips a byte of the signature after signing.
	tamper bool
}

type fakeIssuer struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	keys    map[string]*rsa.PrivateKey // published, by kid
	codes   map[string]issuedCode
	shape   func(*tokenShape)
	subject string
	// jwksFetches counts JWKS requests, to prove a rotated key is refetched.
	jwksFetches int
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	f := &fakeIssuer{t: t, keys: map[string]*rsa.PrivateKey{}, codes: map[string]issuedCode{}}
	f.keys["key-1"] = rsaKey(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.srv.URL,
			"authorization_endpoint":                f.srv.URL + "/authorize",
			"token_endpoint":                        f.srv.URL + "/token",
			"jwks_uri":                              f.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"code_challenge_methods_supported":      []string{"S256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.jwksFetches++
		set := jose.JSONWebKeySet{}
		for kid, k := range f.keys {
			set.Keys = append(set.Keys, jose.JSONWebKey{Key: &k.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"})
		}
		_ = json.NewEncoder(w).Encode(set)
	})
	// /authorize stands in for the person signing in at the provider: it
	// records what the request bound the code to and sends the browser back.
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			t.Errorf("authorization request is not code+PKCE S256: %s", r.URL.RawQuery)
		}
		if !strings.Contains(" "+q.Get("scope")+" ", " openid ") {
			t.Errorf("authorization request does not ask for openid: %q", q.Get("scope"))
		}
		code := uuid.NewString()
		f.mu.Lock()
		f.subject = uuid.NewString()
		f.codes[code] = issuedCode{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"),
			clientID: q.Get("client_id"), redirect: q.Get("redirect_uri"), subject: f.subject}
		f.mu.Unlock()
		back, _ := url.Parse(q.Get("redirect_uri"))
		bq := back.Query()
		bq.Set("code", code)
		bq.Set("state", q.Get("state"))
		back.RawQuery = bq.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", f.token)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func rsaKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (f *fakeIssuer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	refuse := func(reason string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": reason})
	}
	f.mu.Lock()
	grant, ok := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code")) // a code is single-use
	shape := f.shape
	f.mu.Unlock()
	switch {
	case !ok:
		refuse("unknown or spent code")
		return
	case r.PostForm.Get("grant_type") != "authorization_code":
		refuse("grant_type")
		return
	case r.PostForm.Get("client_secret") != "" || r.Header.Get("Authorization") != "":
		// A public client holds no secret; presenting one means the edge is
		// configured as something it is not.
		refuse("public client presented a secret")
		return
	case r.PostForm.Get("client_id") != grant.clientID || r.PostForm.Get("redirect_uri") != grant.redirect:
		refuse("client_id or redirect_uri differ from the authorization request")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != grant.challenge {
		refuse("PKCE verifier does not match the challenge")
		return
	}
	now := time.Now()
	ts := tokenShape{claims: map[string]any{
		"iss": f.srv.URL, "sub": grant.subject, "aud": grant.clientID,
		"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": grant.nonce,
		"email": "ada@example.test", "email_verified": true,
		"preferred_username": "ada.lovelace@example.test", "name": "Ada Lovelace",
		// A Zitadel project-roles claim: present, and deliberately ignored.
		"urn:zitadel:iam:org:project:roles": map[string]any{"admin": map[string]any{}},
	}, kid: "key-1"}
	if shape != nil {
		shape(&ts)
	}
	key := ts.key
	if key == nil {
		f.mu.Lock()
		key = f.keys[ts.kid]
		f.mu.Unlock()
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", ts.kid))
	if err != nil {
		f.t.Errorf("signer: %v", err)
		return
	}
	payload, _ := json.Marshal(ts.claims)
	jws, err := signer.Sign(payload)
	if err != nil {
		f.t.Errorf("sign: %v", err)
		return
	}
	raw, _ := jws.CompactSerialize()
	if ts.tamper {
		parts := strings.Split(raw, ".")
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sig[len(sig)/2] ^= 0x01
		parts[2] = base64.RawURLEncoding.EncodeToString(sig)
		raw = strings.Join(parts, ".")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "opaque-access-token", "token_type": "Bearer", "expires_in": 300, "id_token": raw,
	})
}

func (f *fakeIssuer) setShape(fn func(*tokenShape)) {
	f.mu.Lock()
	f.shape = fn
	f.mu.Unlock()
}

func (f *fakeIssuer) lastSubject() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subject
}

type oidcHarness struct {
	issuer   *fakeIssuer
	edge     *httptest.Server
	identity identityv1.IdentityServiceClient
	client   *http.Client
}

// liveIdentity serves the real identity service, behind its production
// interceptor, on a real listener, and dials it the way cmd/edge does.
func liveIdentity(t *testing.T) identityv1.IdentityServiceClient {
	t.Helper()
	pool, url := liveDB(t)
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	migrateLive(t, url, "identity", "", identity.MigrationsFS)
	migrateLive(t, url, "gitplatform", "", capability.MigrationsFS)
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { rdb.Close() })

	srv := identity.NewGRPCServer(identity.NewStore(pool), identity.NewSessionStore(rdb),
		identity.NewTokenStore(pool), identity.NewSSHKeyStore(pool), capability.NewStore(pool))
	srv.HMACSecret = oidcSecret
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer(grpc.UnaryInterceptor(identity.UnaryAuthInterceptor(srv)))
	identityv1.RegisterIdentityServiceServer(g, srv)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(edge.ForwardCredential))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return identityv1.NewIdentityServiceClient(conn)
}

func newOIDCHarness(t *testing.T) *oidcHarness {
	t.Helper()
	h := &oidcHarness{issuer: newFakeIssuer(t), identity: liveIdentity(t)}
	var router http.Handler
	h.edge = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r) }))
	t.Cleanup(h.edge.Close)
	cfg := edge.Config{Identity: h.identity, OIDC: edge.OIDCConfig{
		Issuer: h.issuer.srv.URL, ClientID: oidcClientID,
		RedirectURI: h.edge.URL + "/api/v1/auth/oidc/callback",
		HMACSecret:  oidcSecret,
	}}
	cfg.Handlers = edge.Handlers(cfg)
	router = edge.NewRouter(cfg)
	// Redirects are followed by hand so every hop can be inspected.
	h.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return h
}

// get issues a GET carrying cookies and returns the response with its body read.
func (h *oidcHarness) get(t *testing.T, u string, cookies []*http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp
}

// flow runs start and the provider's authorization, returning the callback URL
// the provider sent the browser to and the cookies the edge set.
func (h *oidcHarness) flow(t *testing.T) (*url.URL, []*http.Cookie) {
	t.Helper()
	start := h.get(t, h.edge.URL+"/api/v1/auth/oidc/start", nil)
	if start.StatusCode != http.StatusFound {
		t.Fatalf("start answered %d, want 302", start.StatusCode)
	}
	authURL := start.Header.Get("Location")
	if !strings.HasPrefix(authURL, h.issuer.srv.URL+"/authorize?") {
		t.Fatalf("start redirected to %q, not the issuer's discovered authorization endpoint", authURL)
	}
	authz := h.get(t, authURL, nil)
	cb, err := url.Parse(authz.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return cb, start.Cookies()
}

func cookieNamed(cs []*http.Cookie, name string) *http.Cookie {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// signedInBefore reports whether identity ever linked this subject, by asking
// it as the edge would: a subject the callback refused must be new to it.
func (h *oidcHarness) signedInBefore(t *testing.T, subject string) bool {
	t.Helper()
	tok, err := svcauth.MintPlatform(oidcSecret, svcauth.EdgeOIDCService, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+tok)
	resp, err := h.identity.SignInExternal(ctx, &identityv1.SignInExternalRequest{Issuer: h.issuer.srv.URL, Subject: subject})
	if err != nil {
		t.Fatalf("probe identity: %v", err)
	}
	return !resp.GetCreated()
}

func TestOIDCSignInEndToEnd(t *testing.T) {
	h := newOIDCHarness(t)

	// The login page asks what to draw.
	cfgResp, err := http.Get(h.edge.URL + "/api/v1/auth/oidc/config")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	_ = json.NewDecoder(cfgResp.Body).Decode(&cfg)
	cfgResp.Body.Close()
	if cfg["enabled"] != true || cfg["issuer"] != h.issuer.srv.URL || cfg["password_login_enabled"] != true || cfg["button_label"] != "Sign in with azrty" {
		t.Fatalf("oidc config = %v", cfg)
	}

	cb, cookies := h.flow(t)
	if cb.Path != "/api/v1/auth/oidc/callback" || cb.Query().Get("code") == "" {
		t.Fatalf("provider sent the browser to %s", cb)
	}
	state := cookieNamed(cookies, "nf_oidc_state")
	if state == nil || !state.HttpOnly || state.SameSite != http.SameSiteLaxMode || state.MaxAge <= 0 {
		t.Fatalf("state cookie = %+v, want HttpOnly, SameSite=Lax, short-lived", state)
	}

	done := h.get(t, cb.String(), cookies)
	loc := done.Header.Get("Location")
	if done.StatusCode != http.StatusFound || loc != "/?oidc=complete" {
		t.Fatalf("callback answered %d → %q, want 302 → /?oidc=complete", done.StatusCode, loc)
	}
	handoff := cookieNamed(done.Cookies(), "nf_oidc_handoff")
	if handoff == nil || !handoff.HttpOnly || handoff.Value == "" {
		t.Fatalf("callback set no HttpOnly hand-off cookie: %v", done.Cookies())
	}
	if c := cookieNamed(done.Cookies(), "nf_oidc_state"); c == nil || c.MaxAge >= 0 {
		t.Fatalf("callback did not spend the state cookie: %+v", c)
	}

	// The page exchanges the hand-off for its session.
	claim := func(c *http.Cookie) (*http.Response, map[string]string) {
		req, _ := http.NewRequest(http.MethodPost, h.edge.URL+"/api/v1/auth/oidc/session", nil)
		if c != nil {
			req.AddCookie(c)
		}
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp, body
	}
	resp, body := claim(handoff)
	if resp.StatusCode != http.StatusOK || body["session_token"] == "" {
		t.Fatalf("hand-off answered %d %v", resp.StatusCode, body)
	}
	session := body["session_token"]
	if strings.Contains(loc, session) || strings.Contains(cb.String(), session) {
		t.Fatal("the session token appeared in a URL")
	}
	if c := cookieNamed(resp.Cookies(), "nf_oidc_handoff"); c == nil || c.MaxAge >= 0 {
		t.Fatalf("the hand-off cookie was not cleared once read: %+v", c)
	}

	// The session is a real NovaForge one, for the provisioned user.
	req, _ := http.NewRequest(http.MethodGet, h.edge.URL+"/api/v1/user", nil)
	req.Header.Set("Authorization", "Bearer "+session)
	me, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var user map[string]any
	_ = json.NewDecoder(me.Body).Decode(&user)
	me.Body.Close()
	if me.StatusCode != http.StatusOK || user["id"] != body["user_id"] {
		t.Fatalf("GET /api/v1/user with the SSO session = %d %v", me.StatusCode, user)
	}
	if !h.signedInBefore(t, h.issuer.lastSubject()) {
		t.Fatal("identity holds no link for the subject that just signed in")
	}

	// The hand-off is stateless — the edge stores nothing — so it is one-shot
	// because the browser drops it (asserted above), not because the edge
	// remembers it. A copied value is therefore the session itself, exactly as
	// the nf_session cookie is. What the edge does refuse is a hand-off it did
	// not seal, and the absence of one.
	if resp, _ := claim(nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a hand-off without the cookie answered %d, want 401", resp.StatusCode)
	}
	forged := *handoff
	payload, _, _ := strings.Cut(forged.Value, ".")
	forged.Value = payload + ".forged-signature"
	if resp, _ := claim(&forged); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a hand-off with a forged seal answered %d, want 401", resp.StatusCode)
	}
	// The same callback replayed — state cookie and code — signs no one in.
	if again := h.get(t, cb.String(), cookies); !strings.HasPrefix(again.Header.Get("Location"), "/?oidc_error=") {
		t.Fatalf("replayed callback → %q, want an error", again.Header.Get("Location"))
	}
}

// TestOIDCCallbackRefusesEveryBadToken drives the whole flow once per defect.
// Each must end at the login page with a code, set no hand-off, and never
// reach identity: the subject is checked afterwards and must still be new.
func TestOIDCCallbackRefusesEveryBadToken(t *testing.T) {
	h := newOIDCHarness(t)
	stranger := rsaKey(t)

	cases := []struct {
		name  string
		shape func(*tokenShape)
		// bend changes the callback the browser makes, for the cases that
		// are not about the token.
		bend func(cb *url.URL, cookies []*http.Cookie) []*http.Cookie
		code string
	}{
		{name: "wrong state", code: "bad_state", bend: func(cb *url.URL, cs []*http.Cookie) []*http.Cookie {
			q := cb.Query()
			q.Set("state", "attacker-chosen-state")
			cb.RawQuery = q.Encode()
			return cs
		}},
		{name: "no state cookie", code: "bad_state", bend: func(*url.URL, []*http.Cookie) []*http.Cookie { return nil }},
		{name: "forged state cookie", code: "bad_state", bend: func(cb *url.URL, cs []*http.Cookie) []*http.Cookie {
			// The attacker knows the state (it is in the URL) and writes a
			// cookie claiming it, with its own verifier and nonce.
			payload, _ := json.Marshal(map[string]any{"s": cb.Query().Get("state"), "n": "x", "v": "y", "e": time.Now().Add(time.Hour).Unix()})
			return []*http.Cookie{{Name: "nf_oidc_state", Value: base64.RawURLEncoding.EncodeToString(payload) + ".AAAA"}}
		}},
		{name: "provider error", code: "denied", bend: func(cb *url.URL, cs []*http.Cookie) []*http.Cookie {
			q := cb.Query()
			q.Del("code")
			q.Set("error", "access_denied")
			cb.RawQuery = q.Encode()
			return cs
		}},
		{name: "wrong nonce", code: "invalid_token", shape: func(s *tokenShape) { s.claims["nonce"] = "a-nonce-from-another-flow" }},
		{name: "no nonce", code: "invalid_token", shape: func(s *tokenShape) { delete(s.claims, "nonce") }},
		{name: "wrong audience", code: "invalid_token", shape: func(s *tokenShape) { s.claims["aud"] = "some-other-client" }},
		{name: "wrong issuer", code: "invalid_token", shape: func(s *tokenShape) { s.claims["iss"] = "https://login.attacker.test" }},
		{name: "expired", code: "invalid_token", shape: func(s *tokenShape) {
			s.claims["exp"] = time.Now().Add(-time.Minute).Unix()
			s.claims["iat"] = time.Now().Add(-6 * time.Minute).Unix()
		}},
		{name: "issued in the future", code: "invalid_token", shape: func(s *tokenShape) { s.claims["iat"] = time.Now().Add(10 * time.Minute).Unix() }},
		{name: "not yet valid", code: "invalid_token", shape: func(s *tokenShape) { s.claims["nbf"] = time.Now().Add(3 * time.Minute).Unix() }},
		{name: "stale iat", code: "invalid_token", shape: func(s *tokenShape) { s.claims["iat"] = time.Now().Add(-time.Hour).Unix() }},
		{name: "unknown kid", code: "invalid_token", shape: func(s *tokenShape) { s.key, s.kid = stranger, "never-published" }},
		{name: "published kid, wrong key", code: "invalid_token", shape: func(s *tokenShape) { s.key = stranger }},
		{name: "tampered signature", code: "invalid_token", shape: func(s *tokenShape) { s.tamper = true }},
		{name: "tampered claims", code: "invalid_token", shape: func(s *tokenShape) { s.tamper = true; s.claims["sub"] = "someone-else" }},
		{name: "PKCE verifier mismatch", code: "exchange_failed", bend: func(cb *url.URL, cs []*http.Cookie) []*http.Cookie {
			// A code intercepted and redeemed with another browser's state:
			// the edge sends that state's verifier, which is not the one the
			// code was bound to, and the provider refuses the exchange.
			other, otherCookies := h.flow(t)
			q := cb.Query()
			q.Set("state", other.Query().Get("state"))
			cb.RawQuery = q.Encode()
			return otherCookies
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h.issuer.setShape(c.shape)
			defer h.issuer.setShape(nil)
			cb, cookies := h.flow(t)
			subject := h.issuer.lastSubject()
			if c.bend != nil {
				cookies = c.bend(cb, cookies)
			}
			resp := h.get(t, cb.String(), cookies)
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("callback answered %d, want a 302 to the login page", resp.StatusCode)
			}
			if got, want := resp.Header.Get("Location"), "/?oidc_error="+c.code; got != want {
				t.Fatalf("callback → %q, want %q", got, want)
			}
			if hc := cookieNamed(resp.Cookies(), "nf_oidc_handoff"); hc != nil && hc.MaxAge >= 0 {
				t.Fatal("a refused callback set a session hand-off")
			}
			if cookieNamed(resp.Cookies(), "nf_session") != nil {
				t.Fatal("a refused callback set a session cookie")
			}
			if h.signedInBefore(t, subject) {
				t.Fatal("a refused callback reached identity and linked the subject")
			}
		})
	}
}

// TestOIDCPicksUpARotatedKey: a provider that rotates its signing key is
// followed — the unknown kid makes the edge refetch the JWKS — rather than
// every sign-in failing until the edge restarts.
func TestOIDCPicksUpARotatedKey(t *testing.T) {
	h := newOIDCHarness(t)
	cb, cookies := h.flow(t)
	if resp := h.get(t, cb.String(), cookies); resp.Header.Get("Location") != "/?oidc=complete" {
		t.Fatalf("first sign-in → %q", resp.Header.Get("Location"))
	}
	h.issuer.mu.Lock()
	h.issuer.keys["key-2"] = rsaKey(t)
	before := h.issuer.jwksFetches
	h.issuer.mu.Unlock()
	h.issuer.setShape(func(s *tokenShape) { s.kid = "key-2" })

	cb, cookies = h.flow(t)
	if resp := h.get(t, cb.String(), cookies); resp.Header.Get("Location") != "/?oidc=complete" {
		t.Fatalf("sign-in with the rotated key → %q", resp.Header.Get("Location"))
	}
	h.issuer.mu.Lock()
	defer h.issuer.mu.Unlock()
	if h.issuer.jwksFetches <= before {
		t.Fatal("the rotated key verified without the JWKS being refetched")
	}
}

// TestOIDCCookiesAreHostBoundOverHTTPS: on an https deployment the flow's
// cookies are Secure and __Host- prefixed, so a sibling subdomain cannot plant
// one. This needs no datastore — start never reaches identity.
func TestOIDCCookiesAreHostBoundOverHTTPS(t *testing.T) {
	iss := newFakeIssuer(t)
	cfg := edge.Config{OIDC: edge.OIDCConfig{
		Issuer: iss.srv.URL, ClientID: oidcClientID,
		RedirectURI: "https://novaforge.example.test/api/v1/auth/oidc/callback", HMACSecret: oidcSecret,
	}}
	cfg.Handlers = edge.Handlers(cfg)
	rec := httptest.NewRecorder()
	edge.NewRouter(cfg).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/start", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("start answered %d", rec.Code)
	}
	c := cookieNamed(rec.Result().Cookies(), "__Host-nf_oidc_state")
	if c == nil || !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" {
		t.Fatalf("https state cookie = %+v, want __Host-, Secure, HttpOnly, Path=/", c)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("redirect_uri") != cfg.OIDC.RedirectURI || loc.Query().Get("client_id") != oidcClientID || loc.Query().Get("nonce") == "" {
		t.Fatalf("authorization request = %s", loc.RawQuery)
	}
}

func TestOIDCDisabledByDefault(t *testing.T) {
	cfg := edge.Config{OIDC: edge.OIDCConfig{}}
	cfg.Handlers = edge.Handlers(cfg)
	router := edge.NewRouter(cfg)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/config", nil))
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got["enabled"] != false || got["password_login_enabled"] != true {
		t.Fatalf("config with OIDC off = %d %v", rec.Code, got)
	}
	for _, path := range []string{"/api/v1/auth/oidc/start", "/api/v1/auth/oidc/callback?code=x&state=y"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/?oidc_error=not_configured" {
			t.Fatalf("%s with OIDC off → %d %q", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestOIDCConfigValidate(t *testing.T) {
	ok := edge.OIDCConfig{Issuer: "https://login.example.test", ClientID: "c", RedirectURI: "https://nf.example.test/api/v1/auth/oidc/callback", HMACSecret: "s"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("complete config refused: %v", err)
	}
	if err := (edge.OIDCConfig{}).Validate(); err != nil {
		t.Fatalf("OIDC off refused: %v", err)
	}
	bad := map[string]edge.OIDCConfig{
		"partial":          {Issuer: ok.Issuer, ClientID: ok.ClientID, HMACSecret: "s"},
		"no secret":        {Issuer: ok.Issuer, ClientID: ok.ClientID, RedirectURI: ok.RedirectURI},
		"relative":         {Issuer: ok.Issuer, ClientID: ok.ClientID, RedirectURI: "/callback", HMACSecret: "s"},
		"nobody can login": {PasswordLoginDisabled: true},
	}
	for name, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, c)
		}
	}
}

func TestPasswordLoginSwitchedOffAtTheEdge(t *testing.T) {
	cfg := edge.Config{Identity: identityv1.NewIdentityServiceClient(nil), OIDC: edge.OIDCConfig{PasswordLoginDisabled: true}}
	cfg.Handlers = edge.Handlers(cfg)
	router := edge.NewRouter(cfg)
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/register"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"username":"a","password":"b","email":"c"}`)))
		if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "requires_totp") {
			t.Fatalf("%s with password login off = %d %s, want 403 without a TOTP challenge", path, rec.Code, rec.Body.String())
		}
	}
}
