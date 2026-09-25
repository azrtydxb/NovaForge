// api.ts is the single place this app talks to NovaForge. Every screen goes
// through it, so there is exactly one definition of how a credential is
// presented and how a failure is reported — the alternative is each screen
// inventing its own, which is how two screens end up disagreeing about
// whether the user is signed in.

/** ApiError carries the HTTP status so a caller can distinguish "not signed
 * in" (401) and "this deployment does not have that" (501) from a genuine
 * failure. The message is the server's own `error` field where it sent one. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    /** requiresTOTP marks the 401 the edge sends when an account has
     * two-factor enabled. It is a challenge, not bad credentials, and the
     * sign-in screen offers the field rather than refusing the attempt. */
    readonly requiresTOTP = false,
  ) {
    super(message);
    this.name = "ApiError";
  }

  /** unavailable reports a feature the deployment has not configured, which
   * a screen shows as an explanation rather than as an error. */
  get unavailable(): boolean {
    return this.status === 501;
  }
}

const TOKEN_KEY = "novaforge.token";
// Persistence is optional; blocked site data must not break this page’s session.
let memoryToken: string | null | undefined;

export function storedToken(): string | null {
  if (memoryToken !== undefined) return memoryToken;
  try {
    return window.localStorage.getItem(TOKEN_KEY);
  } catch {
    // Private windows and blocked site data throw rather than returning null.
    return null;
  }
}

export function setStoredToken(token: string | null): void {
  memoryToken = token;
  try {
    if (token === null) window.localStorage.removeItem(TOKEN_KEY);
    else window.localStorage.setItem(TOKEN_KEY, token);
  } catch {
    /* a session that cannot be remembered still works for this page */
  }
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  const token = storedToken();
  if (token) headers["Authorization"] = `Bearer ${token}`;
  if (body !== undefined) headers["Content-Type"] = "application/json";

  const init: RequestInit = { method, headers };
  if (body !== undefined) init.body = JSON.stringify(body);
  const res = await fetch(path, init);

  if (res.status === 204) return undefined as T;

  const text = await res.text();
  let parsed: unknown = null;
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      if (res.ok)
        throw new ApiError(res.status, "The server returned invalid JSON.");
      parsed = null;
    }
  }

  if (!res.ok) {
    throw new ApiError(
      res.status,
      errorMessage(parsed) || text || res.statusText,
      flagged(parsed, "requires_totp"),
    );
  }
  return parsed as T;
}

/** errorMessage pulls the edge's own `error` field out of a failure body.
 * Every handler reports failure that way (see internal/edge's WriteError), so
 * the user sees the service's explanation rather than a bare status code. */
function errorMessage(parsed: unknown): string {
  if (parsed === null || typeof parsed !== "object") return "";
  const err = (parsed as Record<string, unknown>)["error"];
  return typeof err === "string" ? err : "";
}

/** flagged reads a boolean field out of a failure body. */
function flagged(parsed: unknown, field: string): boolean {
  if (parsed === null || typeof parsed !== "object") return false;
  return (parsed as Record<string, unknown>)[field] === true;
}

/** text fetches a response that is not JSON. The blob endpoint serves a
 * file's raw bytes with Content-Type application/octet-stream — a file is
 * bytes, and wrapping it in JSON would mean base64 and a size limit. Asking
 * for it as JSON is how the file viewer first failed: the parse produced
 * null and the screen read a field off it. */
async function text(path: string): Promise<string> {
  const headers: Record<string, string> = {};
  const token = storedToken();
  if (token) headers["Authorization"] = `Bearer ${token}`;

  const res = await fetch(path, { method: "GET", headers });
  const body = await res.text();
  if (!res.ok) {
    let parsed: unknown = null;
    try {
      parsed = JSON.parse(body);
    } catch {
      parsed = null;
    }
    throw new ApiError(
      res.status,
      errorMessage(parsed) || body || res.statusText,
    );
  }
  return body;
}

/** download saves a file the edge serves as an attachment. A plain link
 * cannot do it: the credential lives in this page, not in a cookie every
 * session has, so the request is made here with it and the bytes are handed
 * to the browser as a local object. */
async function download(path: string, filename: string): Promise<void> {
  const headers: Record<string, string> = {};
  const token = storedToken();
  if (token) headers["Authorization"] = `Bearer ${token}`;

  const res = await fetch(path, { method: "GET", headers });
  if (!res.ok) {
    const body = await res.text();
    let parsed: unknown = null;
    try {
      parsed = JSON.parse(body);
    } catch {
      parsed = null;
    }
    throw new ApiError(
      res.status,
      errorMessage(parsed) || body || res.statusText,
    );
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  try {
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
  } finally {
    // Revoked on the next tick: revoking synchronously can cancel the save
    // before the browser has started it.
    setTimeout(() => URL.revokeObjectURL(url), 0);
  }
}

/** Events use fetch, not EventSource: bearer credentials never enter a URL.
 * Cancellation tears down the reader when the route or principal changes. */
/** A stream is resumed from the last event the caller actually handled, so a
 * reconnect neither replays what was already shown nor skips what arrived while
 * the connection was down. The server reads that position from Last-Event-ID
 * and it was never sent: every reconnect started from wherever the server chose.
 * Only frames carrying an id move the position — a heartbeat or a state change
 * without one must not reset it. */
async function events(
  path: string,
  signal: AbortSignal,
  receive: (event: string, data: unknown, id?: string) => void,
  cursor?: string,
): Promise<void> {
  const token = storedToken();
  const res = await fetch(path, {
    headers: {
      Accept: "text/event-stream",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(cursor ? { "Last-Event-ID": cursor } : {}),
    },
    signal,
  });
  if (!res.ok) {
    const text = await res.text();
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = null;
    }
    throw new ApiError(
      res.status,
      errorMessage(parsed) || text || res.statusText,
    );
  }
  if (
    !res.headers.get("Content-Type")?.includes("text/event-stream") ||
    !res.body
  )
    throw new Error("The server did not return an event stream.");
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let pending = "";
  try {
    while (!signal.aborted) {
      const { done, value } = await reader.read();
      pending += decoder.decode(value, { stream: !done });
      let delimiter: RegExpExecArray | null;
      while ((delimiter = /\r?\n\r?\n/.exec(pending))) {
        const frame = pending.slice(0, delimiter.index);
        pending = pending.slice(delimiter.index + delimiter[0].length);
        if (frame.length > 1_048_576)
          throw new Error("Event exceeds the 1 MiB frame limit.");
        let event = "message";
        let id: string | undefined;
        const data: string[] = [];
        for (const line of frame.split(/\r?\n/)) {
          if (line.startsWith("event:")) event = line.slice(6).trim();
          if (line.startsWith("id:")) id = line.slice(3).trim();
          if (line.startsWith("data:"))
            data.push(line.slice(5).replace(/^ /, ""));
        }
        if (data.length && !signal.aborted)
          receive(event, JSON.parse(data.join("\n")), id);
      }
      if (pending.length > 1_048_576)
        throw new Error("Event exceeds the 1 MiB frame limit.");
      if (done) return;
    }
  } finally {
    await reader.cancel();
    reader.releaseLock();
  }
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  text,
  download,
  events,
  patch: <T>(path: string, body: unknown) => request<T>("PATCH", path, body),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, body),
  del: <T>(path: string, body?: unknown) => request<T>("DELETE", path, body),
};

/** enc escapes one path segment. Organization and repository names travel in
 * the URL, so a name with a slash or a space must not silently become a
 * different route. */
export const enc = encodeURIComponent;
