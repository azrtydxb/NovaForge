package edge

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// OIDCConfig is single sign-on through an external OpenID Connect provider.
//
// The client is a PUBLIC one — authorization code with PKCE (S256) and no
// client secret — because that is how the identity provider is provisioned:
// there is no secret to hold, so there is none to leak, and PKCE is what binds
// the code to the browser that started the flow instead.
type OIDCConfig struct {
	Issuer      string
	ClientID    string
	RedirectURI string
	// PasswordLoginDisabled turns off the platform's own username/password
	// sign-in, leaving SSO only. Identity enforces the same switch; the edge
	// reads it to tell the login page what to draw and to refuse before
	// calling identity. It is a "disabled" flag so the zero value keeps the
	// sign-in every existing caller of edge.Config relies on.
	PasswordLoginDisabled bool
	// ButtonLabel is what the login page's SSO button says.
	ButtonLabel string
	// HMACSecret is the deployment's shared service secret. It mints the
	// platform token identity requires of the edge, and (through a derived
	// key, never directly) signs the flow's cookies.
	HMACSecret string
	// HTTPClient reaches the provider. Nil uses a client with a timeout: the
	// default client has none, and a provider that stops answering would hold
	// every sign-in open forever.
	HTTPClient *http.Client
	// Now is the verifier's clock; nil is time.Now. Tests move it.
	Now func() time.Time
}

// Enabled reports whether SSO is configured. All three of issuer, client id
// and redirect URI are needed; Validate refuses a partial configuration so this
// never quietly reads "off" for a deployment that set two of them.
func (c OIDCConfig) Enabled() bool {
	return c.Issuer != "" && c.ClientID != "" && c.RedirectURI != ""
}

// Validate refuses a configuration that names some of the provider settings
// but not all of them, or that enables SSO without the secret its cookies are
// signed with. The edge refuses to start on it: an SSO button that can never
// work looks configured and is not.
func (c OIDCConfig) Validate() error {
	set := 0
	for _, v := range []string{c.Issuer, c.ClientID, c.RedirectURI} {
		if v != "" {
			set++
		}
	}
	if set == 0 {
		if c.PasswordLoginDisabled {
			return errors.New("password login is disabled and OIDC is not configured: nobody could sign in")
		}
		return nil
	}
	if set != 3 {
		return errors.New("OIDC_ISSUER, OIDC_CLIENT_ID and OIDC_REDIRECT_URI must be set together")
	}
	for name, raw := range map[string]string{"OIDC_ISSUER": c.Issuer, "OIDC_REDIRECT_URI": c.RedirectURI} {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("%s %q is not an absolute http(s) URL", name, raw)
		}
	}
	if c.HMACSecret == "" {
		return errors.New("OIDC is configured but HMAC_SECRET is empty: the sign-in state cannot be signed")
	}
	return nil
}

// Flow lifetimes. The state cookie covers a person typing a password and a
// second factor at the provider; the hand-off covers one redirect and one
// request from the page that redirect loads.
const (
	oidcStateTTL   = 10 * time.Minute
	oidcHandoffTTL = 2 * time.Minute
	// oidcClockSkew is what the iat and nbf checks tolerate between this
	// process's clock and the provider's. go-oidc already enforces exp
	// strictly; its own nbf leeway is five minutes, which is wider than a
	// token that was minted for this exchange seconds ago needs.
	oidcClockSkew = time.Minute
	// oidcMaxTokenAge bounds iat from below: the token endpoint mints the ID
	// token in the exchange this callback just made, so an old one is a
	// replayed one.
	oidcMaxTokenAge = 10 * time.Minute
)

// OIDC failure codes, carried to the login page as ?oidc_error=<code>. They are
// short and fixed so nothing about the failure — and never a token — reaches
// the browser's address bar, history or a referrer.
const (
	oidcErrNotConfigured = "not_configured"
	oidcErrProvider      = "provider_unavailable"
	oidcErrDenied        = "denied"
	oidcErrState         = "bad_state"
	oidcErrExchange      = "exchange_failed"
	oidcErrToken         = "invalid_token"
	oidcErrAccess        = "access_denied"
	oidcErrSignIn        = "signin_failed"
)

type oidcFlow struct {
	cfg      OIDCConfig
	identity identityv1.IdentityServiceClient
	secure   bool

	mu       sync.Mutex
	provider *oidc.Provider
}

func newOIDCFlow(cfg OIDCConfig, identity identityv1.IdentityServiceClient) *oidcFlow {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ButtonLabel == "" {
		cfg.ButtonLabel = "Sign in with azrty"
	}
	u, _ := url.Parse(cfg.RedirectURI)
	return &oidcFlow{
		cfg:      cfg,
		identity: identity,
		// The browser comes back to the redirect URI, so its scheme decides
		// whether the cookies can be Secure. Plain http is only a development
		// deployment; a Secure cookie there would never be returned and the
		// flow would fail with bad_state every time.
		secure: u != nil && u.Scheme == "https",
	}
}

// discover returns the provider, fetching its discovery document once. A
// failure is not cached, so a provider that was down at the first sign-in is
// tried again at the next one instead of disabling SSO until a restart.
//
// go-oidc validates that the document's issuer is exactly the configured one,
// and builds the provider's JWKS key set on a background context carrying only
// this HTTP client, so a key refresh triggered later by an unknown kid does not
// inherit the cancellation of the request that happened to discover it.
func (f *oidcFlow) discover() (*oidc.Provider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.provider != nil {
		return f.provider, nil
	}
	ctx, cancel := context.WithTimeout(oidc.ClientContext(context.Background(), f.cfg.HTTPClient), 15*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, f.cfg.Issuer)
	if err != nil {
		return nil, err
	}
	f.provider = p
	return p, nil
}

func (f *oidcFlow) oauth2Config(p *oidc.Provider) *oauth2.Config {
	ep := p.Endpoint()
	// A public client authenticates by naming itself in the body. Auto-detect
	// would first try HTTP basic auth with an empty secret, which some
	// providers reject outright.
	ep.AuthStyle = oauth2.AuthStyleInParams
	return &oauth2.Config{
		ClientID:    f.cfg.ClientID,
		Endpoint:    ep,
		RedirectURL: f.cfg.RedirectURI,
		Scopes:      []string{oidc.ScopeOpenID, "email", "profile"},
	}
}

// The flow's cookies. With https they carry the __Host- prefix, which the
// browser only accepts from this exact host, over TLS, for Path=/ and without a
// Domain: a sibling subdomain cannot plant one to log a victim into the
// attacker's account.
func (f *oidcFlow) cookieName(base string) string {
	if f.secure {
		return "__Host-" + base
	}
	return base
}

func (f *oidcFlow) setCookie(w http.ResponseWriter, base, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: f.cookieName(base), Value: value, Path: "/",
		MaxAge: int(ttl.Seconds()), HttpOnly: true, Secure: f.secure,
		// Lax, not Strict: the callback is a top-level navigation arriving
		// from the provider's site, and Strict would withhold the cookie on
		// exactly that request.
		SameSite: http.SameSiteLaxMode,
	})
}

func (f *oidcFlow) clearCookie(w http.ResponseWriter, base string) {
	http.SetCookie(w, &http.Cookie{
		Name: f.cookieName(base), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: f.secure, SameSite: http.SameSiteLaxMode,
	})
}

const (
	oidcStateCookie   = "nf_oidc_state"
	oidcHandoffCookie = "nf_oidc_handoff"
)

// oidcState is what the start of a flow leaves in the browser. It is kept in a
// signed cookie rather than on the server because the edge owns no data — no
// database, no Redis — and runs as several replicas: the callback may reach a
// replica that never saw the start. The cookie is bound to the flow by State,
// which must equal the state the provider hands back.
//
// Holding the PKCE verifier and the nonce in the browser that started the flow
// is the model PKCE was designed for. An attacker who can read this HttpOnly
// cookie is already inside the victim's browser; one who cannot read it cannot
// forge it, because it is MACed with a key only the platform holds.
type oidcState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Expires  int64  `json:"e"`
}

type oidcHandoff struct {
	Session string `json:"t"`
	UserID  string `json:"u"`
	Expires int64  `json:"e"`
}

// cookieKey derives the key the flow's cookies are MACed with. Service tokens
// are signed with HMAC_SECRET itself; deriving a separate key per purpose means
// a value signed for one can never verify as the other.
func (f *oidcFlow) cookieKey(purpose string) []byte {
	m := hmac.New(sha256.New, []byte(f.cfg.HMACSecret))
	m.Write([]byte("novaforge/edge/oidc/" + purpose + "/v1"))
	return m.Sum(nil)
}

func (f *oidcFlow) seal(purpose string, v any) (string, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	m := hmac.New(sha256.New, f.cookieKey(purpose))
	m.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)), nil
}

func (f *oidcFlow) open(purpose, sealed string, v any) error {
	payload, sig, ok := strings.Cut(sealed, ".")
	if !ok {
		return errors.New("malformed cookie")
	}
	m := hmac.New(sha256.New, f.cookieKey(purpose))
	m.Write([]byte(payload))
	want := base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return errors.New("cookie signature does not verify")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// fail sends the browser back to the login page with a short code. Every
// failure ends here — never a 500 page, never a body a person has to read —
// and the reason is logged for the operator, never shown.
func (f *oidcFlow) fail(w http.ResponseWriter, r *http.Request, code string, err error) {
	if err != nil {
		log.Printf("edge: oidc %s: %s: %v", r.URL.Path, code, err)
	}
	f.clearCookie(w, oidcStateCookie)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/?oidc_error="+url.QueryEscape(code), http.StatusFound)
}

func (f *oidcFlow) config(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{
		"enabled":                f.cfg.Enabled(),
		"password_login_enabled": !f.cfg.PasswordLoginDisabled,
	}
	if f.cfg.Enabled() {
		out["issuer"] = f.cfg.Issuer
		out["button_label"] = f.cfg.ButtonLabel
	}
	WriteJSON(w, http.StatusOK, out)
}

func (f *oidcFlow) start(w http.ResponseWriter, r *http.Request) {
	if !f.cfg.Enabled() {
		f.fail(w, r, oidcErrNotConfigured, nil)
		return
	}
	p, err := f.discover()
	if err != nil {
		f.fail(w, r, oidcErrProvider, err)
		return
	}
	st := oidcState{Verifier: oauth2.GenerateVerifier(), Expires: f.cfg.Now().Add(oidcStateTTL).Unix()}
	if st.State, err = randomToken(); err == nil {
		st.Nonce, err = randomToken()
	}
	if err != nil {
		f.fail(w, r, oidcErrSignIn, err)
		return
	}
	sealed, err := f.seal(oidcStateCookie, st)
	if err != nil {
		f.fail(w, r, oidcErrSignIn, err)
		return
	}
	f.setCookie(w, oidcStateCookie, sealed, oidcStateTTL)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, f.oauth2Config(p).AuthCodeURL(st.State,
		oauth2.S256ChallengeOption(st.Verifier), oidc.Nonce(st.Nonce)), http.StatusFound)
}

// idClaims are the ID token claims NovaForge reads. Authorization is decided
// here, by organization membership, so the provider's role claims are not
// read at all.
type idClaims struct {
	Email             string          `json:"email"`
	EmailVerified     json.RawMessage `json:"email_verified"`
	PreferredUsername string          `json:"preferred_username"`
	Name              string          `json:"name"`
	IssuedAt          *float64        `json:"iat"`
	NotBefore         *float64        `json:"nbf"`
}

// verified reads email_verified, which the specification makes a boolean and
// some providers send as the string "true". Anything else is false: an email
// the provider does not vouch for is not treated as the user's.
func (c idClaims) verified() bool {
	var b bool
	if json.Unmarshal(c.EmailVerified, &b) == nil {
		return b
	}
	var s string
	return json.Unmarshal(c.EmailVerified, &s) == nil && strings.EqualFold(s, "true")
}

func (f *oidcFlow) callback(w http.ResponseWriter, r *http.Request) {
	if !f.cfg.Enabled() {
		f.fail(w, r, oidcErrNotConfigured, nil)
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		// The person declined, or the provider refused them. Its
		// error_description is free text from another system and stays out
		// of the redirect.
		f.fail(w, r, oidcErrDenied, fmt.Errorf("provider returned error %q", e))
		return
	}
	ck, err := r.Cookie(f.cookieName(oidcStateCookie))
	if err != nil {
		f.fail(w, r, oidcErrState, errors.New("no state cookie"))
		return
	}
	var st oidcState
	if err := f.open(oidcStateCookie, ck.Value, &st); err != nil {
		f.fail(w, r, oidcErrState, err)
		return
	}
	if f.cfg.Now().Unix() > st.Expires {
		f.fail(w, r, oidcErrState, errors.New("state expired"))
		return
	}
	if got := q.Get("state"); got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(st.State)) != 1 {
		f.fail(w, r, oidcErrState, errors.New("state does not match the flow this browser started"))
		return
	}
	code := q.Get("code")
	if code == "" {
		f.fail(w, r, oidcErrState, errors.New("callback carries no code"))
		return
	}
	// The state is spent: whatever happens next, this cookie cannot be used
	// for a second callback.
	f.clearCookie(w, oidcStateCookie)

	p, err := f.discover()
	if err != nil {
		f.fail(w, r, oidcErrProvider, err)
		return
	}
	ctx, cancel := context.WithTimeout(oidc.ClientContext(r.Context(), f.cfg.HTTPClient), 15*time.Second)
	defer cancel()
	tok, err := f.oauth2Config(p).Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		f.fail(w, r, oidcErrExchange, err)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		f.fail(w, r, oidcErrToken, errors.New("token response carries no id_token"))
		return
	}
	// go-oidc checks the signature against the provider's JWKS (refetching it
	// when the kid is unknown, so a rotated key is picked up), that iss is the
	// configured issuer, that aud contains our client id, and exp.
	idt, err := p.Verifier(&oidc.Config{ClientID: f.cfg.ClientID, Now: f.cfg.Now}).Verify(ctx, raw)
	if err != nil {
		f.fail(w, r, oidcErrToken, err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(st.Nonce)) != 1 || st.Nonce == "" {
		f.fail(w, r, oidcErrToken, errors.New("nonce does not match the flow this browser started"))
		return
	}
	var claims idClaims
	if err := idt.Claims(&claims); err != nil {
		f.fail(w, r, oidcErrToken, err)
		return
	}
	if err := f.checkTimes(claims); err != nil {
		f.fail(w, r, oidcErrToken, err)
		return
	}
	if idt.Subject == "" {
		f.fail(w, r, oidcErrToken, errors.New("ID token has no subject"))
		return
	}

	if f.identity == nil {
		f.fail(w, r, oidcErrSignIn, errors.New("identity service is not configured"))
		return
	}
	svc, err := svcauth.MintPlatform(f.cfg.HMACSecret, svcauth.EdgeOIDCService, time.Minute)
	if err != nil {
		f.fail(w, r, oidcErrSignIn, err)
		return
	}
	// The edge acts as itself here, not as a caller: nobody is signed in yet.
	// The token goes on the call directly; there is no request credential for
	// ForwardCredential to add alongside it.
	rpcCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+svc)
	resp, err := f.identity.SignInExternal(rpcCtx, &identityv1.SignInExternalRequest{
		Issuer: idt.Issuer, Subject: idt.Subject,
		Email: claims.Email, EmailVerified: claims.verified(),
		PreferredUsername: claims.PreferredUsername, Name: claims.Name,
	})
	if err != nil {
		switch status.Code(err) {
		case codes.PermissionDenied, codes.Unauthenticated, codes.FailedPrecondition:
			f.fail(w, r, oidcErrAccess, err)
		default:
			f.fail(w, r, oidcErrSignIn, err)
		}
		return
	}

	// The session reaches the application through a one-shot HttpOnly cookie
	// the page exchanges with POST /api/v1/auth/oidc/session, never through
	// the URL: a token in a query string or fragment lands in browser
	// history, and a query string in access logs and Referer headers too.
	handoff, err := f.seal(oidcHandoffCookie, oidcHandoff{
		Session: resp.GetSessionToken(), UserID: resp.GetUserId(),
		Expires: f.cfg.Now().Add(oidcHandoffTTL).Unix(),
	})
	if err != nil {
		f.fail(w, r, oidcErrSignIn, err)
		return
	}
	f.setCookie(w, oidcHandoffCookie, handoff, oidcHandoffTTL)
	// The same session cookie password login sets, so the two sign-ins leave
	// the browser in the same state.
	http.SetCookie(w, &http.Cookie{
		Name: "nf_session", Value: resp.GetSessionToken(), Path: "/",
		HttpOnly: true, Secure: f.secure, SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/?oidc=complete", http.StatusFound)
}

// checkTimes enforces iat and nbf with a small skew. go-oidc checks exp but
// not iat at all, and allows nbf five minutes.
func (f *oidcFlow) checkTimes(c idClaims) error {
	now := f.cfg.Now()
	if c.IssuedAt == nil {
		return errors.New("ID token has no iat")
	}
	iat := time.Unix(int64(*c.IssuedAt), 0)
	if iat.After(now.Add(oidcClockSkew)) {
		return fmt.Errorf("ID token issued in the future (iat %v)", iat)
	}
	if iat.Before(now.Add(-oidcMaxTokenAge)) {
		return fmt.Errorf("ID token is too old to have been minted for this exchange (iat %v)", iat)
	}
	if c.NotBefore != nil {
		if nbf := time.Unix(int64(*c.NotBefore), 0); nbf.After(now.Add(oidcClockSkew)) {
			return fmt.Errorf("ID token not valid yet (nbf %v)", nbf)
		}
	}
	return nil
}

// session hands the page the session the callback created, once. Reading the
// cookie clears it; a second call, or one after the hand-off expired, finds
// nothing. The response is readable only by this origin: the cookie is
// SameSite=Lax, so a cross-site POST arrives without it.
func (f *oidcFlow) session(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ck, err := r.Cookie(f.cookieName(oidcHandoffCookie))
	if err != nil || ck.Value == "" {
		WriteError(w, http.StatusUnauthorized, errors.New("no single sign-on is waiting to be completed"))
		return
	}
	f.clearCookie(w, oidcHandoffCookie)
	var h oidcHandoff
	if err := f.open(oidcHandoffCookie, ck.Value, &h); err != nil || f.cfg.Now().Unix() > h.Expires || h.Session == "" {
		WriteError(w, http.StatusUnauthorized, errors.New("the single sign-on hand-off is invalid or expired; sign in again"))
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"session_token": h.Session, "user_id": h.UserID})
}

func addOIDCHandlers(h map[string]http.HandlerFunc, identity identityv1.IdentityServiceClient, cfg OIDCConfig) {
	f := newOIDCFlow(cfg, identity)
	h["getOIDCConfig"] = f.config
	h["startOIDC"] = f.start
	h["oidcCallback"] = f.callback
	h["completeOIDC"] = f.session
}
