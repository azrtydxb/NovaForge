import { useEffect, useRef, useState } from "react";
import { ApiError, api, setStoredToken } from "./lib/api";

interface LoginResponse {
  session_token: string;
  user_id: string;
  requires_totp: boolean;
}

/** What the edge says about how this deployment signs people in
 * (GET /api/v1/auth/oidc/config). */
interface SignInOptions {
  enabled: boolean;
  issuer?: string;
  button_label?: string;
  password_login_enabled: boolean;
}

/** The short codes the edge's single sign-on callback sends back as
 * ?oidc_error=. The callback never puts anything else in the URL. */
const OIDC_ERRORS: Record<string, string> = {
  not_configured: "Single sign-on is not configured in this deployment.",
  provider_unavailable:
    "The identity provider could not be reached. Try again shortly.",
  denied: "Sign-in was cancelled or refused at the identity provider.",
  bad_state:
    "That sign-in attempt expired or was started in another browser. Start again.",
  exchange_failed:
    "The identity provider did not accept the sign-in. Start again.",
  invalid_token: "The identity provider's response could not be verified.",
  access_denied: "This account is not allowed to sign in.",
  signin_failed: "Sign-in could not be completed. Try again.",
};

/** oidcReturn reads, and removes from the address bar, what the single
 * sign-on callback left there: either that a session is waiting to be
 * collected or a failure code. Removing it keeps a reload or a bookmark from
 * replaying it. */
function oidcReturn(): { complete: boolean; error: string | null } {
  const params = new URLSearchParams(window.location.search);
  const complete = params.get("oidc") === "complete";
  const code = params.get("oidc_error");
  if (complete || code !== null) {
    params.delete("oidc");
    params.delete("oidc_error");
    const rest = params.toString();
    window.history.replaceState(
      null,
      "",
      window.location.pathname +
        (rest ? `?${rest}` : "") +
        window.location.hash,
    );
  }
  return {
    complete,
    error:
      code === null
        ? null
        : (OIDC_ERRORS[code] ?? `Single sign-on failed (${code}).`),
  };
}

/** Login is the design's sign-in artboard: the platform's own credential flow,
 * presented as one card with a mode tab. TOTP is asked for only when the
 * server says it is required, so a user without it never sees a field they
 * cannot fill.
 *
 * When the deployment has single sign-on, its button leads the card and the
 * password form follows only if the deployment still allows it. The
 * provider's callback lands back here: it leaves the session in a one-shot
 * HttpOnly cookie, never in the URL, and this screen collects it. */
export function Login({ onSignedIn }: { onSignedIn: () => void }) {
  const [mode, setMode] = useState<"sign-in" | "create">("sign-in");
  const [email, setEmail] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [totp, setTotp] = useState("");
  const [needTotp, setNeedTotp] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // null until the edge answers. Until then the card draws neither the SSO
  // button nor the password form, rather than guessing which one exists.
  const [options, setOptions] = useState<SignInOptions | null>(null);
  const [optionsNote, setOptionsNote] = useState<string | null>(null);
  // The hand-off cookie is read once and cleared by the edge, so collecting it
  // twice (React runs mount effects twice in development) would turn a
  // successful sign-in into a "nothing waiting" error.
  const collected = useRef(false);
  // The parent passes a fresh callback on every render; the effect below
  // must run once per mount, not once per render, so it reads the latest
  // callback through a ref instead of depending on it.
  const signedIn = useRef(onSignedIn);
  signedIn.current = onSignedIn;

  useEffect(() => {
    const ret = oidcReturn();
    if (ret.error) setError(ret.error);
    if (ret.complete && !collected.current) {
      collected.current = true;
      setBusy(true);
      api
        .post<{ session_token: string }>("/api/v1/auth/oidc/session")
        .then((res) => {
          if (!res.session_token) {
            setError("The server returned no session token.");
            return;
          }
          setStoredToken(res.session_token);
          signedIn.current();
        })
        .catch((err: unknown) =>
          setError(err instanceof Error ? err.message : String(err)),
        )
        .finally(() => setBusy(false));
    }
    api
      .get<SignInOptions>("/api/v1/auth/oidc/config")
      .then(setOptions)
      .catch((err: unknown) => {
        // An edge that predates single sign-on has no such route; it signs
        // in with a password as it always did. Any other failure is said so,
        // and the password form is offered as the one sign-in that may work.
        setOptions({ enabled: false, password_login_enabled: true });
        if (!(err instanceof ApiError && err.status === 404))
          setOptionsNote(
            "The sign-in options could not be loaded; single sign-on may be unavailable.",
          );
      });
  }, []);

  const sso = options?.enabled === true;
  const passwordAllowed = options?.password_login_enabled === true;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      // The field names are the edge's own: totp_code on the way in,
      // session_token on the way back. Reading "token" instead is how this
      // screen first failed against the real service.
      if (mode === "create") {
        // Registering does not sign you in: the platform issues a session
        // through the login path only, so the account is created and then
        // authenticated with the same credentials.
        await api.post("/api/v1/auth/register", { email, username, password });
      }
      const body: Record<string, string> = { username, password };
      if (totp) body.totp_code = totp;
      const res = await api.post<LoginResponse>("/api/v1/auth/login", body);
      if (res.requires_totp && !res.session_token) {
        setNeedTotp(true);
        setError("This account requires an authentication code.");
        return;
      }
      if (!res.session_token) {
        setError("The server returned no session token.");
        return;
      }
      setStoredToken(res.session_token);
      onSignedIn();
    } catch (err) {
      // A TOTP challenge arrives as a 401 carrying requires_totp, not as bad
      // credentials, so the field is offered rather than the attempt refused.
      if (err instanceof ApiError && err.requiresTOTP) {
        setNeedTotp(true);
        setError("Enter your authentication code.");
        return;
      }
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  function switchMode(next: "sign-in" | "create") {
    if (next === mode) return;
    setMode(next);
    setError(null);
  }

  return (
    <div
      style={{
        minHeight: "100vh",
        display: "grid",
        placeItems: "center",
        background: "var(--bg)",
        padding: 24,
      }}
    >
      <div style={{ width: 360, maxWidth: "100%" }}>
        {/* The wordmark sits outside the card, as the artboard draws it: the
            product signs you in, the card is only the form. */}
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 10,
            justifyContent: "center",
            marginBottom: 22,
          }}
        >
          <div
            aria-hidden="true"
            style={{
              width: 28,
              height: 28,
              background: "var(--accent)",
              borderRadius: 7,
              display: "grid",
              placeItems: "center",
              font: "700 14px var(--sans)",
              color: "#fff",
            }}
          >
            N
          </div>
          <span
            style={{
              font: "700 15px var(--sans)",
              letterSpacing: ".12em",
              color: "var(--fg)",
            }}
          >
            NOVAFORGE
          </span>
        </div>

        <form
          onSubmit={submit}
          style={{
            background: "var(--panel)",
            border: "1px solid var(--line-2)",
            borderRadius: 12,
            padding: 24,
          }}
        >
          <h1
            style={{
              font: "600 17px var(--sans)",
              margin: "0 0 2px",
              color: "var(--fg)",
            }}
          >
            {mode === "create" ? "Create your account" : "Sign in"}
          </h1>
          <p
            style={{
              margin: "0 0 16px",
              font: "12px var(--sans)",
              color: "var(--fg-muted)",
            }}
          >
            {options === null
              ? "Checking how this deployment signs people in…"
              : mode === "create"
                ? "The account is created, then signed in with the same credentials."
                : sso && passwordAllowed
                  ? "With your organization's identity provider, or the username and password your deployment issued."
                  : sso
                    ? "With your organization's identity provider."
                    : "With the username and password your deployment issued."}
          </p>

          {sso && mode === "sign-in" ? (
            <>
              {/* A plain link, not a fetch: the provider's sign-in page is a
                  full navigation, and the edge answers it with a redirect. */}
              <a
                href="/api/v1/auth/oidc/start"
                aria-disabled={busy}
                onClick={(e) => {
                  if (busy) e.preventDefault();
                }}
                style={{
                  display: "block",
                  width: "100%",
                  boxSizing: "border-box",
                  padding: "9px 12px",
                  background: "var(--accent)",
                  border: "none",
                  borderRadius: 8,
                  color: "#fff",
                  font: "600 13px var(--sans)",
                  textAlign: "center",
                  textDecoration: "none",
                  cursor: busy ? "default" : "pointer",
                  opacity: busy ? 0.6 : 1,
                  marginBottom:
                    passwordAllowed || error || optionsNote ? 14 : 0,
                }}
              >
                {busy
                  ? "Signing in…"
                  : options?.button_label || "Sign in with azrty"}
              </a>
              {passwordAllowed ? (
                <div
                  aria-hidden="true"
                  style={{
                    display: "grid",
                    gridTemplateColumns: "1fr auto 1fr",
                    alignItems: "center",
                    gap: 10,
                    marginBottom: 14,
                    font: "11px var(--sans)",
                    color: "var(--fg-faint)",
                  }}
                >
                  <span style={{ borderTop: "1px solid var(--line)" }} />
                  or with a password
                  <span style={{ borderTop: "1px solid var(--line)" }} />
                </div>
              ) : null}
            </>
          ) : null}

          {/* The mode tab, not a link at the bottom: which fields the form
              asks for is the first thing a reader needs to know. */}
          {passwordAllowed ? (
            <div
              role="tablist"
              aria-label="Sign in or create an account"
              style={{
                display: "grid",
                gridTemplateColumns: "1fr 1fr",
                gap: 4,
                background: "var(--panel-2)",
                border: "1px solid var(--line)",
                borderRadius: 8,
                padding: 3,
                marginBottom: 16,
              }}
            >
              <ModeTab
                active={mode === "sign-in"}
                label="Sign in"
                onClick={() => switchMode("sign-in")}
              />
              <ModeTab
                active={mode === "create"}
                label="Create account"
                onClick={() => switchMode("create")}
              />
            </div>
          ) : null}

          {passwordAllowed ? (
            <>
              {mode === "create" ? (
                <Field label="Email" value={email} onChange={setEmail} />
              ) : null}
              <Field label="Username" value={username} onChange={setUsername} />
              <Field
                label="Password"
                value={password}
                onChange={setPassword}
                type="password"
              />
              {needTotp ? (
                <Field
                  label="Authentication code"
                  value={totp}
                  onChange={setTotp}
                  mono
                  help="The six-digit code from your authenticator app."
                />
              ) : null}
            </>
          ) : null}

          {error ? (
            <div
              role="alert"
              style={{
                font: "11px var(--mono)",
                color: "var(--bad)",
                background: "var(--bad-bg)",
                border: "1px solid var(--bad-bg)",
                borderRadius: 8,
                padding: "8px 10px",
                marginBottom: 12,
                lineHeight: 1.5,
              }}
            >
              {error}
            </div>
          ) : null}

          {optionsNote ? (
            <p
              style={{
                margin: "0 0 12px",
                font: "11px var(--sans)",
                color: "var(--fg-muted)",
              }}
            >
              {optionsNote}
            </p>
          ) : null}

          {passwordAllowed ? (
            <button
              type="submit"
              disabled={busy}
              style={{
                width: "100%",
                padding: "9px 12px",
                // With single sign-on leading the card, the password button
                // steps back so there is one primary action.
                background: sso ? "var(--panel-2)" : "var(--accent)",
                border: sso ? "1px solid var(--line-2)" : "none",
                borderRadius: 8,
                color: sso ? "var(--fg)" : "#fff",
                font: "600 13px var(--sans)",
                cursor: busy ? "default" : "pointer",
                opacity: busy ? 0.6 : 1,
              }}
            >
              {busy
                ? mode === "create"
                  ? "Creating…"
                  : "Signing in…"
                : mode === "create"
                  ? "Create account"
                  : "Sign in"}
            </button>
          ) : null}
        </form>

        <p
          style={{
            margin: "14px 0 0",
            textAlign: "center",
            font: "11px var(--sans)",
            color: "var(--fg-faint)",
            lineHeight: 1.6,
          }}
        >
          Sessions are bearer tokens held by this browser.{" "}
          {sso
            ? "Single sign-on uses your identity provider's own second factor; password accounts with two-factor are challenged for a code at sign-in."
            : "Two-factor accounts are challenged for a code at sign-in, not at every request."}
        </p>
      </div>
    </div>
  );
}

function ModeTab({
  active,
  label,
  onClick,
}: {
  active: boolean;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      style={{
        padding: "6px 10px",
        borderRadius: 6,
        border: "none",
        background: active ? "var(--panel)" : "transparent",
        color: active ? "var(--fg)" : "var(--fg-muted)",
        font: `500 12px var(--sans)`,
        cursor: "pointer",
        boxShadow: active ? "0 1px 2px rgba(0,0,0,.12)" : "none",
      }}
    >
      {label}
    </button>
  );
}

function Field({
  label,
  value,
  onChange,
  type = "text",
  mono = false,
  help,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  type?: string;
  mono?: boolean;
  help?: string;
}) {
  return (
    <label style={{ display: "block", marginBottom: 12 }}>
      <span
        style={{
          display: "block",
          font: "600 10px var(--sans)",
          letterSpacing: ".08em",
          color: "var(--fg-muted)",
          marginBottom: 5,
        }}
      >
        {label.toUpperCase()}
      </span>
      <input
        type={type}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        autoComplete={
          type === "password"
            ? "current-password"
            : label === "Username"
              ? "username"
              : "off"
        }
        style={{
          width: "100%",
          padding: "8px 10px",
          background: "var(--bg)",
          border: "1px solid var(--line-2)",
          borderRadius: 7,
          color: "var(--fg)",
          font: mono ? "13px var(--mono)" : "13px var(--sans)",
          outline: "none",
        }}
      />
      {help ? (
        <span
          style={{
            display: "block",
            font: "11px var(--sans)",
            color: "var(--fg-faint)",
            marginTop: 4,
          }}
        >
          {help}
        </span>
      ) : null}
    </label>
  );
}
