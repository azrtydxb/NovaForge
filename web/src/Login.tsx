import { useState } from "react";
import { ApiError, api, setStoredToken } from "./lib/api";

interface LoginResponse {
  session_token: string;
  user_id: string;
  requires_totp: boolean;
}

/** Login presents the platform's own credential flow. TOTP is asked for only
 * when the server says it is required, so a user without it never sees a
 * field they cannot fill. */
export function Login({ onSignedIn }: { onSignedIn: () => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [totp, setTotp] = useState("");
  const [needTotp, setNeedTotp] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      // The field names are the edge's own: totp_code on the way in,
      // session_token on the way back. Reading "token" instead is how this
      // screen first failed against the real service.
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

  return (
    <div
      style={{
        height: "100vh",
        display: "grid",
        placeItems: "center",
        background: "var(--bg)",
      }}
    >
      <form
        onSubmit={submit}
        style={{
          width: 320,
          background: "var(--panel)",
          border: "1px solid var(--line-2)",
          borderRadius: 12,
          padding: 24,
        }}
      >
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 10,
            marginBottom: 18,
          }}
        >
          <div
            style={{
              width: 26,
              height: 26,
              background: "var(--accent)",
              borderRadius: 6,
              display: "grid",
              placeItems: "center",
              font: "700 13px var(--sans)",
              color: "#fff",
            }}
          >
            N
          </div>
          <span style={{ font: "600 15px var(--sans)" }}>NovaForge</span>
        </div>

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
          />
        ) : null}

        {error ? (
          <div
            style={{
              font: "11px var(--mono)",
              color: "var(--bad)",
              marginBottom: 12,
              lineHeight: 1.5,
            }}
          >
            {error}
          </div>
        ) : null}

        <button
          type="submit"
          disabled={busy}
          style={{
            width: "100%",
            padding: "9px 12px",
            background: "var(--accent)",
            border: "none",
            borderRadius: 8,
            color: "#fff",
            font: "600 13px var(--sans)",
            cursor: busy ? "default" : "pointer",
            opacity: busy ? 0.6 : 1,
          }}
        >
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </div>
  );
}

function Field({
  label,
  value,
  onChange,
  type = "text",
  mono = false,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  type?: string;
  mono?: boolean;
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
    </label>
  );
}
