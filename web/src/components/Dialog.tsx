import { useState, type ReactNode } from "react";
import { Failed } from "./ui";

/** Field is one input in a create dialog. */
export interface Field {
  name: string;
  label: string;
  placeholder?: string;
  type?: "text" | "password" | "textarea" | "select";
  options?: string[];
  required?: boolean;
  help?: string;
}

/** Dialog is the one create form in this application. Every screen that
 * creates something uses it, so a new object is asked for the same way
 * everywhere and there is one place that decides how a failure is shown. */
export function Dialog({
  title,
  description,
  submitLabel,
  fields,
  busy,
  error,
  onSubmit,
  onClose,
}: {
  title: string;
  /** description explains what submitting will do. A confirmation with no
   * fields is a Dialog that is all description. */
  description?: ReactNode;
  submitLabel: string;
  fields: Field[];
  busy: boolean;
  error: unknown;
  onSubmit: (values: Record<string, string>) => void;
  onClose: () => void;
}) {
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(
      fields.map((f) => [
        f.name,
        f.type === "select" ? (f.options?.[0] ?? "") : "",
      ]),
    ),
  );

  return (
    <div
      onClick={onClose}
      style={{
        position: "fixed",
        inset: 0,
        background: "rgba(0,0,0,.55)",
        display: "grid",
        placeItems: "center",
        zIndex: 100,
      }}
    >
      <form
        onClick={(e) => e.stopPropagation()}
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit(values);
        }}
        style={{
          width: 420,
          maxHeight: "80vh",
          overflowY: "auto",
          background: "var(--panel)",
          border: "1px solid var(--line-3)",
          borderRadius: 12,
          padding: 22,
        }}
      >
        <h2 style={{ font: "600 15px var(--sans)", margin: "0 0 16px" }}>
          {title}
        </h2>

        {description ? (
          <div
            style={{
              font: "13px var(--sans)",
              color: "var(--fg-dim)",
              lineHeight: 1.6,
              marginBottom: 16,
            }}
          >
            {description}
          </div>
        ) : null}

        {fields.map((f) => (
          <label key={f.name} style={{ display: "block", marginBottom: 13 }}>
            <span
              style={{
                display: "block",
                font: "600 10px var(--sans)",
                letterSpacing: ".08em",
                color: "var(--fg-muted)",
                marginBottom: 5,
              }}
            >
              {f.label.toUpperCase()}
              {f.required ? "" : " (optional)"}
            </span>
            {f.type === "textarea" ? (
              <textarea
                value={values[f.name] ?? ""}
                onChange={(e) =>
                  setValues((v) => ({ ...v, [f.name]: e.target.value }))
                }
                placeholder={f.placeholder}
                rows={3}
                style={{ ...inputStyle, resize: "vertical" }}
              />
            ) : f.type === "select" ? (
              <select
                value={values[f.name] ?? ""}
                onChange={(e) =>
                  setValues((v) => ({ ...v, [f.name]: e.target.value }))
                }
                style={inputStyle}
              >
                {f.options?.map((o) => (
                  <option key={o} value={o}>
                    {o}
                  </option>
                ))}
              </select>
            ) : (
              <input
                type={f.type ?? "text"}
                value={values[f.name] ?? ""}
                onChange={(e) =>
                  setValues((v) => ({ ...v, [f.name]: e.target.value }))
                }
                placeholder={f.placeholder}
                style={inputStyle}
              />
            )}
            {f.help ? (
              <span
                style={{
                  display: "block",
                  font: "11px var(--sans)",
                  color: "var(--fg-faint)",
                  marginTop: 4,
                }}
              >
                {f.help}
              </span>
            ) : null}
          </label>
        ))}

        {error ? (
          <div style={{ marginBottom: 13 }}>
            <Failed error={error} />
          </div>
        ) : null}

        <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
          <button type="button" onClick={onClose} style={secondaryButton}>
            Cancel
          </button>
          <button type="submit" disabled={busy} style={primaryButton(busy)}>
            {busy ? "Working…" : submitLabel}
          </button>
        </div>
      </form>
    </div>
  );
}

/** Confirm asks before an action that cannot be taken back. With
 * `typeToConfirm` the user must type that exact text first — reserved for the
 * actions whose loss is total, like deleting a repository, where a reflexive
 * click on a button is exactly the mistake being guarded against. */
export function Confirm({
  title,
  body,
  confirmLabel,
  typeToConfirm,
  danger,
  busy,
  error,
  onConfirm,
  onClose,
}: {
  title: string;
  body: ReactNode;
  confirmLabel: string;
  typeToConfirm?: string;
  danger?: boolean;
  busy: boolean;
  error: unknown;
  onConfirm: () => void;
  onClose: () => void;
}) {
  const [typed, setTyped] = useState("");
  const ready = typeToConfirm === undefined || typed === typeToConfirm;

  return (
    <div
      onClick={onClose}
      style={{
        position: "fixed",
        inset: 0,
        background: "rgba(0,0,0,.55)",
        display: "grid",
        placeItems: "center",
        zIndex: 100,
      }}
    >
      <form
        onClick={(e) => e.stopPropagation()}
        onSubmit={(e) => {
          e.preventDefault();
          if (ready && !busy) onConfirm();
        }}
        style={{
          width: 420,
          maxWidth: "calc(100vw - 32px)",
          background: "var(--panel)",
          border: `1px solid ${danger ? "#e5534b66" : "var(--line-3)"}`,
          borderRadius: 12,
          padding: 22,
        }}
      >
        <h2 style={{ font: "600 15px var(--sans)", margin: "0 0 12px" }}>
          {title}
        </h2>
        <div
          style={{
            font: "13px/1.6 var(--sans)",
            color: "var(--fg-dim)",
            marginBottom: 14,
          }}
        >
          {body}
        </div>

        {typeToConfirm !== undefined ? (
          <label style={{ display: "block", marginBottom: 13 }}>
            <span
              style={{
                display: "block",
                font: "12px var(--sans)",
                color: "var(--fg-muted)",
                marginBottom: 5,
              }}
            >
              Type{" "}
              <code style={{ font: "600 12px var(--mono)" }}>
                {typeToConfirm}
              </code>{" "}
              to confirm
            </span>
            <input
              autoFocus
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              style={inputStyle}
            />
          </label>
        ) : null}

        {error ? (
          <div style={{ marginBottom: 13 }}>
            <Failed error={error} />
          </div>
        ) : null}

        <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
          <button type="button" onClick={onClose} style={secondaryButton}>
            Keep it
          </button>
          <button
            type="submit"
            disabled={busy || !ready}
            style={{
              ...primaryButton(busy || !ready),
              background: danger ? "var(--bad)" : "var(--accent)",
            }}
          >
            {busy ? "Working…" : confirmLabel}
          </button>
        </div>
      </form>
    </div>
  );
}

/** NewButton is the consistent affordance for "create one of these". */
export function NewButton({
  label,
  onClick,
}: {
  label: string;
  onClick: () => void;
}) {
  return (
    <button onClick={onClick} style={primaryButton(false)}>
      {label}
    </button>
  );
}

const inputStyle: React.CSSProperties = {
  width: "100%",
  padding: "8px 10px",
  background: "var(--bg)",
  border: "1px solid var(--line-2)",
  borderRadius: 7,
  color: "var(--fg)",
  font: "13px var(--sans)",
  outline: "none",
};

function primaryButton(busy: boolean): React.CSSProperties {
  return {
    padding: "7px 14px",
    background: "var(--accent)",
    border: "none",
    borderRadius: 8,
    color: "#fff",
    font: "600 12px var(--sans)",
    cursor: busy ? "default" : "pointer",
    opacity: busy ? 0.6 : 1,
  };
}

const secondaryButton: React.CSSProperties = {
  padding: "7px 14px",
  background: "transparent",
  border: "1px solid var(--line-2)",
  borderRadius: 8,
  color: "var(--fg-muted)",
  font: "12px var(--sans)",
  cursor: "pointer",
};

export type { ReactNode };
