import type { CSSProperties, ReactNode } from "react";
import { ApiError } from "../lib/api";

export function Page({
  title,
  subtitle,
  actions,
  children,
}: {
  title: string;
  subtitle?: string;
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div style={{ flex: 1, overflowY: "auto", padding: "22px 26px" }}>
      <header
        style={{
          display: "flex",
          alignItems: "flex-end",
          gap: 14,
          marginBottom: 18,
        }}
      >
        <div style={{ flex: 1 }}>
          <h1 style={{ font: "600 19px var(--sans)", margin: 0 }}>{title}</h1>
          {subtitle ? (
            <div
              style={{
                font: "12px var(--sans)",
                color: "var(--fg-muted)",
                marginTop: 4,
              }}
            >
              {subtitle}
            </div>
          ) : null}
        </div>
        {actions}
      </header>
      {children}
    </div>
  );
}

export function Panel({
  children,
  style,
}: {
  children: ReactNode;
  style?: CSSProperties;
}) {
  return (
    <div
      style={{
        background: "var(--panel)",
        border: "1px solid var(--line)",
        borderRadius: 10,
        ...style,
      }}
    >
      {children}
    </div>
  );
}

export function PanelHead({ children }: { children: ReactNode }) {
  return (
    <div
      style={{
        padding: "11px 14px",
        borderBottom: "1px solid var(--line)",
        font: "600 11px var(--sans)",
        letterSpacing: ".08em",
        color: "var(--fg-muted)",
        display: "flex",
        alignItems: "center",
        gap: 10,
      }}
    >
      {children}
    </div>
  );
}

export function Pill({
  children,
  bg,
  fg,
}: {
  children: ReactNode;
  bg: string;
  fg: string;
}) {
  return (
    <span
      style={{
        background: bg,
        color: fg,
        borderRadius: 99,
        padding: "2px 8px",
        font: "600 10px var(--mono)",
        whiteSpace: "nowrap",
      }}
    >
      {children}
    </span>
  );
}

/** STATE_COLORS maps a backend state onto the design's palette. States come
 * from the platform (work item states, run states), so this is the one place
 * that decides what each looks like. */
export const STATE_COLORS: Record<string, [string, string]> = {
  open: ["var(--info-bg)", "var(--link)"],
  planning: ["rgba(139,145,160,.13)", "var(--fg-muted)"],
  in_progress: ["var(--ok-bg)", "var(--ok)"],
  review: ["var(--warn-bg)", "var(--warn)"],
  blocked: ["var(--bad-bg)", "var(--bad)"],
  done: ["rgba(255,255,255,.07)", "var(--fg-muted)"],
  queued: ["rgba(139,145,160,.13)", "var(--fg-muted)"],
  running: ["var(--ok-bg)", "var(--ok)"],
  succeeded: ["var(--ok-bg)", "var(--ok)"],
  success: ["var(--ok-bg)", "var(--ok)"],
  failed: ["var(--bad-bg)", "var(--bad)"],
  failure: ["var(--bad-bg)", "var(--bad)"],
  over_budget: ["var(--warn-bg)", "var(--warn)"],
  cancelled: ["rgba(255,255,255,.07)", "var(--fg-muted)"],
  merged: ["rgba(255,255,255,.07)", "var(--fg-muted)"],
  closed: ["rgba(255,255,255,.07)", "var(--fg-muted)"],
  pass: ["var(--ok-bg)", "var(--ok)"],
  fail: ["var(--bad-bg)", "var(--bad)"],
  error: ["var(--bad-bg)", "var(--bad)"],
  // Tool-call outcomes: denied by the run's capability grant, or refused
  // before dispatch (unknown tool, budget already spent).
  denied: ["var(--bad-bg)", "var(--bad)"],
  refused: ["var(--warn-bg)", "var(--warn)"],
  pending: ["var(--warn-bg)", "var(--warn)"],
  approved: ["var(--ok-bg)", "var(--ok)"],
  rejected: ["var(--bad-bg)", "var(--bad)"],
  revoked: ["rgba(255,255,255,.07)", "var(--fg-muted)"],
};

export function StatePill({ state }: { state: string }) {
  const [bg, fg] = STATE_COLORS[state] ?? [
    "rgba(255,255,255,.07)",
    "var(--fg-muted)",
  ];
  return (
    <Pill bg={bg} fg={fg}>
      {state.replace(/_/g, " ")}
    </Pill>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return (
    <div
      style={{
        padding: "28px 16px",
        textAlign: "center",
        font: "13px var(--sans)",
        color: "var(--fg-muted)",
        lineHeight: 1.6,
      }}
    >
      {children}
    </div>
  );
}

export function Loading() {
  return (
    <div
      role="status"
      style={{
        padding: "28px 16px",
        textAlign: "center",
        font: "12px var(--mono)",
        color: "var(--fg-faint)",
        animation: "nfpulse 1.6s infinite",
      }}
    >
      loading…
    </div>
  );
}

/** Failed renders an error honestly, and distinguishes a feature this
 * deployment has not configured from a request that actually failed. A screen
 * that silently shows an empty list for both is a screen that lies about the
 * state of the system. */
export function Failed({ error }: { error: unknown }) {
  const apiErr = error instanceof ApiError ? error : null;
  const unavailable = apiErr?.unavailable ?? false;
  return (
    <div
      role={unavailable ? "status" : "alert"}
      style={{
        padding: "20px 16px",
        border: `1px solid ${unavailable ? "var(--line-2)" : "#e5534b44"}`,
        background: unavailable ? "transparent" : "rgba(229,83,75,.06)",
        borderRadius: 10,
        font: "13px var(--sans)",
        color: unavailable ? "var(--fg-muted)" : "var(--bad)",
        lineHeight: 1.6,
      }}
    >
      <div style={{ font: "600 12px var(--sans)", marginBottom: 4 }}>
        {unavailable
          ? "Not available in this deployment"
          : `Request failed${apiErr ? ` (${apiErr.status})` : ""}`}
      </div>
      <div style={{ font: "12px var(--mono)", opacity: 0.9 }}>
        {error instanceof Error ? error.message : String(error)}
      </div>
    </div>
  );
}

/** Async renders the three states every data-backed panel has, so no screen
 * has to remember to handle all of them. */
export function Async<T>({
  query,
  children,
  empty,
}: {
  query: {
    isLoading: boolean;
    error: unknown;
    data: T | undefined;
  };
  children: (data: T) => ReactNode;
  empty?: ReactNode;
}) {
  if (query.isLoading) return <Loading />;
  if (query.error) return <Failed error={query.error} />;
  if (query.data === undefined)
    return <Empty>{empty ?? "Nothing here."}</Empty>;
  return <>{children(query.data)}</>;
}

export const mono: CSSProperties = { font: "12px var(--mono)" };
