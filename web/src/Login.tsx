import { useState } from "react";
import { api, setStoredToken } from "./lib/api";

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
      const body: Record<string, string> = { username, password };
      if (totp) body.totp = totp;
      const res = await api.post<{ token: string }>("/api/v1/auth/login", body);
      setStoredToken(res.token);
      onSignedIn();
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      if (/totp|two.factor|2fa/i.test(message)) setNeedTotp(true);
      setError(message);
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
