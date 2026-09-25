import { useState } from "react";
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
} from "@tanstack/react-query";
import { api, ApiError, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import {
  Async,
  Empty,
  Failed,
  mono,
  Page,
  Panel,
  PanelHead,
} from "../components/ui";
import { Confirm, Dialog } from "../components/Dialog";
import type {
  Commit,
  Hook,
  HookDelivery,
  Mirror,
  OrgMember,
  Ref,
  Release,
  Repo,
  RepoCollaborator,
  Team,
  TreeEntry,
  User,
} from "../lib/types";

/** Repos is the design's browser: a repository, its tree, and a file. The
 * tree is read one directory at a time, which is how git-platform serves it. */
export function Repos() {
  const w = useWorkspace();
  const [repo, setRepo] = useState<string | null>(w.repo);
  const active = repo ?? w.repo ?? w.repos[0]?.name ?? null;
  const [deleting, setDeleting] = useState(false);
  const [importing, setImporting] = useState(false);
  const qc = useQueryClient();

  // Whether to offer deletion is decided from the platform's own record of
  // this person's role, the same role git-platform enforces. Offering the
  // button to a member would only lead them to a refusal.
  const me = useQuery({
    queryKey: ["user"],
    queryFn: () => api.get<User>("/api/v1/user"),
  });
  const members = useQuery({
    queryKey: ["members", w.org],
    queryFn: () =>
      api.get<{ members: OrgMember[] }>(`/api/v1/orgs/${enc(w.org!)}/members`),
    enabled: w.org !== null,
  });
  const myRole = members.data?.members.find(
    (m) => m.user_id === me.data?.id,
  )?.role;
  const canDelete = myRole === "owner" || myRole === "admin";

  // Administration is a PATCH with only the field being changed, so renaming does
  // not also reset the default branch and archiving does not also rename.
  const administer = useMutation({
    mutationFn: (patch: {
      name?: string;
      default_branch?: string;
      archived?: boolean;
    }) =>
      api.patch<Repo>(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(active!)}`,
        patch,
      ),
    onSuccess: (updated) => {
      // A rename changes the name every other screen addresses it by.
      if (w.repo === active && updated.name !== active) w.setRepo(updated.name);
      setRepo(updated.name);
      void qc.invalidateQueries({ queryKey: ["repos"] });
    },
  });

  const transfer = useMutation({
    mutationFn: (toOrg: string) =>
      api.post<Repo>(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(active!)}/transfer`,
        { to_org: toOrg },
      ),
    onSuccess: () => {
      // It belongs to another organization now, so this workspace cannot show it.
      if (w.repo === active) w.setRepo(null);
      setRepo(null);
      void qc.invalidateQueries({ queryKey: ["repos"] });
    },
  });

  // The default branch can only be set to a branch that exists, so the choice is
  // the repository's own branches rather than free text.
  const branches = useQuery({
    queryKey: ["branches", w.org, active],
    queryFn: () =>
      api.get<{ refs: Ref[] }>(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(active!)}/branches`,
      ),
    enabled: w.org !== null && active !== null,
  });
  const branchNames = (branches.data?.refs ?? []).map((r) => r.name);
  // Only organizations this person belongs to: a transfer into one they do not
  // would be refused, and offering it would invite the refusal.
  const otherOrgs = w.orgs
    .filter((o) => o.name !== w.org)
    .map((o) => ({ id: o.id, name: o.name }));

  const remove = useMutation({
    mutationFn: (name: string) =>
      api.del(`/api/v1/orgs/${enc(w.org!)}/repos/${enc(name)}`),
    onSuccess: (_d, name) => {
      setDeleting(false);
      setRepo(null);
      // A workspace still scoped to the deleted repository would make every
      // screen ask for something that no longer exists.
      if (w.repo === name) w.setRepo(null);
      qc.invalidateQueries({ queryKey: ["repos"] });
    },
  });

  return (
    <Page
      title="Repositories"
      subtitle="Standard Git, browsed through the platform"
      actions={
        <div style={{ display: "flex", gap: 6 }}>
          <button onClick={() => setImporting(true)} style={adminButton}>
            Import repository
          </button>
          {active !== null && canDelete ? (
            <button
              onClick={() => {
                remove.reset();
                setDeleting(true);
              }}
              style={dangerButton}
            >
              Delete repository
            </button>
          ) : null}
        </div>
      }
    >
      {importing && w.org !== null ? (
        <ImportDialog
          org={w.org}
          onClose={() => setImporting(false)}
          onImported={(name) => {
            setImporting(false);
            setRepo(name);
            void qc.invalidateQueries({ queryKey: ["repos"] });
          }}
        />
      ) : null}

      {deleting && active !== null ? (
        <Confirm
          title={`Delete ${active}`}
          body={
            <>
              This permanently deletes the repository <strong>{active}</strong>{" "}
              and its entire history from this organization. Clones elsewhere
              are unaffected; nothing on the platform can be recovered.
            </>
          }
          confirmLabel="Delete repository"
          typeToConfirm={active}
          danger
          busy={remove.isPending}
          error={remove.error}
          onConfirm={() => remove.mutate(active)}
          onClose={() => setDeleting(false)}
        />
      ) : null}

      <div
        style={{ display: "flex", gap: 6, marginBottom: 12, flexWrap: "wrap" }}
      >
        {w.repos.map((r) => (
          <button
            key={r.id}
            onClick={() => setRepo(r.name)}
            style={{
              padding: "5px 11px",
              borderRadius: 7,
              border: "1px solid var(--line)",
              background:
                active === r.name ? "rgba(77,127,255,.18)" : "transparent",
              color: active === r.name ? "#fff" : "var(--fg-muted)",
              font: "500 12px var(--sans)",
              cursor: "pointer",
            }}
          >
            {r.name}
          </button>
        ))}
      </div>

      {active === null ? (
        <Panel>
          <Empty>This organization has no repositories yet.</Empty>
        </Panel>
      ) : (
        <>
          {canDelete ? (
            <Administration
              repo={w.repos.find((r) => r.name === active)}
              branches={branchNames}
              orgs={otherOrgs}
              administer={administer}
              transfer={transfer}
            />
          ) : null}
          {canDelete ? (
            <Collaborators
              key={`collab:${w.org}/${active}`}
              org={w.org!}
              repo={active}
            />
          ) : null}
          <MirrorPanel
            key={`mirror:${w.org}/${active}`}
            org={w.org!}
            repo={active}
            canAdminister={canDelete}
          />
          <Hooks key={`hooks:${w.org}/${active}`} org={w.org!} repo={active} />
          <Browser
            key={`${w.org}/${active}`}
            org={w.org!}
            repo={active}
            defaultBranch={
              w.repos.find((r) => r.name === active)?.default_branch ?? "main"
            }
          />
        </>
      )}
    </Page>
  );
}

/** Administration of the selected repository. Archiving is shown as a state to
 * move in and out of, next to but distinct from deleting: one is reversible and
 * the other is not, and a control that made them look alike would invite the
 * wrong one. */
function Administration({
  repo,
  branches,
  orgs,
  administer,
  transfer,
}: {
  repo: Repo | undefined;
  branches: string[];
  orgs: { id: string; name: string }[];
  administer: UseMutationResult<
    Repo,
    unknown,
    { name?: string; default_branch?: string; archived?: boolean }
  >;
  transfer: UseMutationResult<Repo, unknown, string>;
}) {
  const [name, setName] = useState("");
  const [toOrg, setToOrg] = useState("");
  if (!repo) return null;
  return (
    <Panel style={{ marginBottom: 12 }}>
      <PanelHead>
        ADMINISTRATION
        <div style={{ flex: 1 }} />
        {repo.archived ? (
          <span style={{ font: "11px var(--mono)", color: "var(--warn)" }}>
            archived — reads only
          </span>
        ) : null}
      </PanelHead>
      <div style={{ padding: 12, display: "grid", gap: 10 }}>
        <label style={adminRow}>
          <span style={adminLabel}>Rename</span>
          <input
            value={name}
            placeholder={repo.name}
            onChange={(e) => setName(e.target.value)}
            style={adminInput}
          />
          <button
            disabled={name === "" || name === repo.name || administer.isPending}
            onClick={() => administer.mutate({ name })}
            style={adminButton}
          >
            Rename
          </button>
        </label>

        <label style={adminRow}>
          <span style={adminLabel}>Default branch</span>
          <select
            value={repo.default_branch}
            onChange={(e) =>
              administer.mutate({ default_branch: e.target.value })
            }
            disabled={administer.isPending || branches.length === 0}
            style={adminInput}
          >
            {branches.length === 0 ? (
              <option value={repo.default_branch}>{repo.default_branch}</option>
            ) : (
              branches.map((b) => (
                <option key={b} value={b}>
                  {b}
                </option>
              ))
            )}
          </select>
          <span style={{ font: "11px var(--sans)", color: "var(--fg-faint)" }}>
            a fresh clone checks this out
          </span>
        </label>

        <div style={adminRow}>
          <span style={adminLabel}>Archive</span>
          <span
            style={{
              flex: 1,
              font: "12px var(--sans)",
              color: "var(--fg-muted)",
            }}
          >
            {repo.archived
              ? "Writes are refused. History stays readable."
              : "Keep the history readable and stop it changing."}
          </span>
          <button
            disabled={administer.isPending}
            onClick={() => administer.mutate({ archived: !repo.archived })}
            style={adminButton}
          >
            {repo.archived ? "Un-archive" : "Archive"}
          </button>
        </div>

        <label style={adminRow}>
          <span style={adminLabel}>Transfer</span>
          <select
            value={toOrg}
            onChange={(e) => setToOrg(e.target.value)}
            disabled={orgs.length === 0 || transfer.isPending}
            style={adminInput}
          >
            <option value="">
              {orgs.length === 0
                ? "no other organization you belong to"
                : "choose an organization"}
            </option>
            {orgs.map((o) => (
              <option key={o.id} value={o.id}>
                {o.name}
              </option>
            ))}
          </select>
          <button
            disabled={toOrg === "" || transfer.isPending}
            onClick={() => transfer.mutate(toOrg)}
            style={adminButton}
          >
            Transfer
          </button>
        </label>

        {administer.error ? <Failed error={administer.error} /> : null}
        {transfer.error ? <Failed error={transfer.error} /> : null}
      </div>
    </Panel>
  );
}

const adminRow: React.CSSProperties = {
  display: "flex",
  alignItems: "center",
  gap: 10,
};
const adminLabel: React.CSSProperties = {
  font: "600 11px var(--sans)",
  color: "var(--fg-muted)",
  minWidth: 110,
};
const adminInput: React.CSSProperties = {
  padding: "5px 9px",
  borderRadius: 6,
  border: "1px solid var(--line)",
  background: "transparent",
  color: "var(--fg)",
  font: "12px var(--sans)",
  minWidth: 200,
};
const adminButton: React.CSSProperties = {
  padding: "5px 11px",
  borderRadius: 6,
  border: "1px solid var(--line)",
  background: "transparent",
  color: "var(--fg)",
  font: "500 12px var(--sans)",
  cursor: "pointer",
};

/** ImportDialog brings a repository in from another Git host.
 *
 * The token field is a password field and the help text says where the value
 * goes, because the honest alternative — pasting a token into the URL, which is
 * what every Git host's own instructions do — is refused by the platform on
 * purpose: a URL with a token in it ends up in tables and log lines. */
function ImportDialog({
  org,
  onClose,
  onImported,
}: {
  org: string;
  onClose: () => void;
  onImported: (name: string) => void;
}) {
  const imported = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post<{ repo: Repo; mirror: Mirror | null }>(
        `/api/v1/orgs/${enc(org)}/repos/import`,
        {
          name: v.name,
          remote: v.remote,
          credential: v.credential,
          mirror: v.mode === MIRRORED,
          interval_seconds: Number(v.interval) || 0,
        },
      ),
    onSuccess: (r) => onImported(r.repo.name),
  });

  return (
    <Dialog
      title="Import a repository"
      description="The whole history is cloned from the address below. Nothing is created here unless the clone finishes, so a failed import can simply be retried."
      submitLabel="Import"
      busy={imported.isPending}
      error={imported.error}
      fields={[
        {
          name: "remote",
          label: "Upstream URL",
          placeholder: "https://github.com/owner/project.git",
          required: true,
          help: "http or https only. A URL containing a username or token is refused — use the token field instead.",
        },
        {
          name: "name",
          label: "Name here",
          placeholder: "project",
          required: true,
        },
        {
          name: "credential",
          label: "Access token",
          type: "password",
          help: "Only needed for a private upstream. It is stored encrypted and never shown again.",
        },
        {
          name: "mode",
          label: "Afterwards",
          type: "select",
          options: [ONE_OFF, MIRRORED],
          help: `${ONE_OFF} leaves the repository writable here. ${MIRRORED} keeps following upstream, and refuses pushes.`,
        },
        {
          name: "interval",
          label: "Refresh every",
          placeholder: "3600 seconds",
          help: "Only used when mirrored; blank means hourly.",
        },
      ]}
      onSubmit={(values) => imported.mutate(values)}
      onClose={onClose}
    />
  );
}

const ONE_OFF = "one-off copy";
const MIRRORED = "keep mirrored";

/** MirrorPanel is what this repository follows, if anything.
 *
 * A repository that is not a mirror is an absence, not a failure: the endpoint
 * answers 404 and the panel says the history is this platform's own. Rendering
 * `Failed` for it would make every ordinary repository look broken. */
function MirrorPanel({
  org,
  repo,
  canAdminister,
}: {
  org: string;
  repo: string;
  canAdminister: boolean;
}) {
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}/mirror`;
  const qc = useQueryClient();
  const [remote, setRemote] = useState("");
  const [credential, setCredential] = useState("");
  // Not named `interval`: a state setter called setInterval would shadow the
  // global of the same name inside this component.
  const [every, setEvery] = useState("");

  const mirror = useQuery({
    queryKey: ["mirror", org, repo],
    // A 404 is the answer, not a transient failure, so it is not retried.
    retry: false,
    queryFn: () => api.get<Mirror>(base),
  });
  const notMirrored =
    mirror.error instanceof ApiError && mirror.error.status === 404;
  const invalidate = () =>
    void qc.invalidateQueries({ queryKey: ["mirror", org, repo] });

  const save = useMutation({
    mutationFn: () =>
      api.post<Mirror>(base, {
        remote,
        credential,
        interval_seconds: Number(every) || 0,
      }),
    onSuccess: () => {
      // The token is cleared from the form as soon as it has been sent: it is
      // write-only on the platform, and leaving it in an input is the one place
      // it would still be readable.
      setCredential("");
      invalidate();
    },
  });
  const stop = useMutation({
    mutationFn: () => api.del(base),
    onSuccess: invalidate,
  });

  const m = mirror.data;
  return (
    <Panel style={{ marginBottom: 12 }}>
      <PanelHead>
        UPSTREAM
        <div style={{ flex: 1 }} />
        <span style={{ font: "11px var(--sans)", color: "var(--fg-faint)" }}>
          a mirror refuses pushes; upstream owns its history
        </span>
      </PanelHead>
      <div style={{ padding: 12, display: "grid", gap: 10 }}>
        {mirror.isPending ? (
          <Empty>Checking…</Empty>
        ) : notMirrored ? (
          <Empty>
            This repository&apos;s history is its own. It follows nothing.
          </Empty>
        ) : mirror.error ? (
          <Failed error={mirror.error} />
        ) : m ? (
          <>
            <div style={adminRow}>
              <span style={adminLabel}>Follows</span>
              <span style={mono}>{m.remote}</span>
            </div>
            <div style={adminRow}>
              <span style={adminLabel}>Last refreshed</span>
              <span style={{ font: "12px var(--sans)" }}>
                {m.last_synced_at === ""
                  ? "never since the remote changed — due now"
                  : new Date(m.last_synced_at).toLocaleString()}
              </span>
            </div>
            <div style={adminRow}>
              <span style={adminLabel}>Every</span>
              <span style={{ font: "12px var(--sans)" }}>
                {m.interval_seconds === 0
                  ? "every pass of the mirrorer"
                  : `${m.interval_seconds} seconds`}
              </span>
            </div>
            <div style={adminRow}>
              <span style={adminLabel}>Credential</span>
              <span style={{ font: "12px var(--sans)" }}>
                {m.has_credential
                  ? "stored, encrypted — it is never shown again"
                  : "none; upstream is read anonymously"}
              </span>
            </div>
            {/* The recorded reason, not a red dot: a mirror that is quietly
                failing is the complaint, and the platform knows exactly why. */}
            {m.last_error === "" ? null : (
              <div style={adminRow}>
                <span style={adminLabel}>Last failure</span>
                <span style={{ font: "12px var(--sans)", color: "#ff6b6b" }}>
                  {m.last_error}
                </span>
              </div>
            )}
          </>
        ) : null}

        {canAdminister ? (
          <>
            <div style={adminRow}>
              <span style={adminLabel}>
                {notMirrored ? "Follow" : "Repoint to"}
              </span>
              <input
                value={remote}
                placeholder="https://github.com/owner/project.git"
                onChange={(e) => setRemote(e.target.value)}
                style={{ ...adminInput, flex: 1 }}
              />
              <input
                value={credential}
                type="password"
                placeholder="access token (optional)"
                onChange={(e) => setCredential(e.target.value)}
                style={adminInput}
              />
              <input
                value={every}
                placeholder="seconds (blank = hourly)"
                onChange={(e) => setEvery(e.target.value)}
                style={adminInput}
              />
              <button
                onClick={() => save.mutate()}
                disabled={remote === "" || save.isPending}
                style={adminButton}
              >
                {notMirrored ? "Start mirroring" : "Save"}
              </button>
            </div>
            {notMirrored ? null : (
              <div style={adminRow}>
                <span style={adminLabel}>Stop</span>
                <button
                  onClick={() => stop.mutate()}
                  disabled={stop.isPending}
                  style={adminButton}
                >
                  Stop following upstream
                </button>
                <span
                  style={{ font: "11px var(--sans)", color: "var(--fg-faint)" }}
                >
                  keeps the history already fetched, and makes the repository
                  writable again
                </span>
              </div>
            )}
            {save.error ? <Failed error={save.error} /> : null}
            {stop.error ? <Failed error={stop.error} /> : null}
          </>
        ) : null}
      </div>
    </Panel>
  );
}

/** Hooks is the webhook panel: the endpoints this repository notifies, and what
 * each one answered.
 *
 * The delivery history is the point of it. A webhook that is quietly failing is
 * the most common integration complaint there is, and the platform knows exactly
 * why — a status, or the connection error when there was no response at all — so
 * the screen shows that rather than a green dot per hook. */
function Hooks({ org, repo }: { org: string; repo: string }) {
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}/hooks`;
  const qc = useQueryClient();
  const [url, setUrl] = useState("");
  const [events, setEvents] = useState("push");
  const [secret, setSecret] = useState("");
  const [open, setOpen] = useState<string | null>(null);

  const hooks = useQuery({
    queryKey: ["hooks", org, repo],
    queryFn: () => api.get<{ hooks: Hook[] }>(base),
  });
  const invalidate = () =>
    void qc.invalidateQueries({ queryKey: ["hooks", org, repo] });

  const create = useMutation({
    mutationFn: () =>
      api.post<Hook>(base, {
        url,
        // An empty field means every event, which is what the platform means by
        // an empty list — not a hook subscribed to the event named "".
        events: events
          .split(",")
          .map((e) => e.trim())
          .filter((e) => e !== ""),
        secret,
      }),
    onSuccess: () => {
      setUrl("");
      setSecret("");
      invalidate();
    },
  });

  // Switching a hook off sends only `active`, and rotating sends only `secret`:
  // the edge applies exactly the fields present, so a rotation cannot silently
  // switch the hook off and switching it off cannot strip its signature.
  const update = useMutation({
    mutationFn: (v: { id: string; active?: boolean; secret?: string }) =>
      api.patch<Hook>(`${base}/${enc(v.id)}`, {
        ...(v.active === undefined ? {} : { active: v.active }),
        ...(v.secret === undefined ? {} : { secret: v.secret }),
      }),
    onSuccess: invalidate,
  });

  const remove = useMutation({
    mutationFn: (id: string) => api.del(`${base}/${enc(id)}`),
    onSuccess: (_d, id) => {
      if (open === id) setOpen(null);
      invalidate();
    },
  });

  return (
    <Panel style={{ marginBottom: 12 }}>
      <PanelHead>
        WEBHOOKS
        <div style={{ flex: 1 }} />
        <span style={{ font: "11px var(--sans)", color: "var(--fg-faint)" }}>
          signed with HMAC-SHA256 in X-NovaForge-Signature
        </span>
      </PanelHead>
      <div style={{ padding: 12, display: "grid", gap: 10 }}>
        <div style={adminRow}>
          <span style={adminLabel}>Endpoint</span>
          <input
            value={url}
            placeholder="https://example.com/novaforge"
            onChange={(e) => setUrl(e.target.value)}
            style={{ ...adminInput, flex: 1 }}
          />
          <input
            value={events}
            placeholder="push (blank = every event)"
            onChange={(e) => setEvents(e.target.value)}
            style={adminInput}
          />
          <input
            value={secret}
            type="password"
            placeholder="signing secret (optional)"
            onChange={(e) => setSecret(e.target.value)}
            style={adminInput}
          />
          <button
            disabled={url === "" || create.isPending}
            onClick={() => create.mutate()}
            style={adminButton}
          >
            Add
          </button>
        </div>
        {create.error ? <Failed error={create.error} /> : null}
        {update.error ? <Failed error={update.error} /> : null}
        {remove.error ? <Failed error={remove.error} /> : null}

        <Async
          query={hooks}
          empty="No webhooks are registered for this repository."
        >
          {(data) =>
            data.hooks.length === 0 ? (
              <Empty>No webhooks are registered for this repository.</Empty>
            ) : (
              <div style={{ display: "grid", gap: 8 }}>
                {data.hooks.map((h) => (
                  <div
                    key={h.id}
                    style={{
                      border: "1px solid var(--line)",
                      borderRadius: 8,
                      padding: 10,
                      display: "grid",
                      gap: 6,
                    }}
                  >
                    <div
                      style={{ display: "flex", alignItems: "center", gap: 10 }}
                    >
                      <span
                        style={{ ...mono, flex: 1, wordBreak: "break-all" }}
                      >
                        {h.url}
                      </span>
                      <span
                        style={{
                          font: "11px var(--sans)",
                          color: h.active ? "var(--fg-muted)" : "var(--warn)",
                        }}
                      >
                        {h.active ? "active" : "off"}
                      </span>
                      <button
                        disabled={update.isPending}
                        onClick={() =>
                          update.mutate({ id: h.id, active: !h.active })
                        }
                        style={adminButton}
                      >
                        {h.active ? "Disable" : "Enable"}
                      </button>
                      <button
                        disabled={remove.isPending}
                        onClick={() => remove.mutate(h.id)}
                        style={adminButton}
                      >
                        Remove
                      </button>
                    </div>
                    <div
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: 10,
                        font: "11px var(--sans)",
                        color: "var(--fg-faint)",
                      }}
                    >
                      <span>
                        {h.events.length === 0
                          ? "every event"
                          : h.events.join(", ")}
                      </span>
                      {/* Whether a secret is set is all the platform will say
                          about it: it is never sent back, so a rotation is the
                          only way to know one again. */}
                      <span>{h.has_secret ? "signed" : "unsigned"}</span>
                      <div style={{ flex: 1 }} />
                      <button
                        onClick={() => setOpen(open === h.id ? null : h.id)}
                        style={adminButton}
                      >
                        {open === h.id ? "Hide deliveries" : "Deliveries"}
                      </button>
                    </div>
                    {open === h.id ? (
                      <Deliveries org={org} repo={repo} hookID={h.id} />
                    ) : null}
                  </div>
                ))}
              </div>
            )
          }
        </Async>
      </div>
    </Panel>
  );
}

/** Deliveries is one hook's attempt history, newest first. Each row says what the
 * endpoint answered; a failure that never reached it at all has no status, so the
 * error is shown in its place rather than a fabricated 0. */
function Deliveries({
  org,
  repo,
  hookID,
}: {
  org: string;
  repo: string;
  hookID: string;
}) {
  const deliveries = useQuery({
    queryKey: ["hook-deliveries", org, repo, hookID],
    queryFn: () =>
      api.get<{ deliveries: HookDelivery[] }>(
        `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}/hooks/${enc(hookID)}/deliveries`,
      ),
  });
  return (
    <Async query={deliveries} empty="This endpoint has not been called yet.">
      {(data) =>
        data.deliveries.length === 0 ? (
          <Empty>This endpoint has not been called yet.</Empty>
        ) : (
          <div style={{ display: "grid", gap: 4 }}>
            {data.deliveries.map((d) => (
              <div
                key={d.id}
                style={{
                  display: "flex",
                  gap: 10,
                  alignItems: "baseline",
                  font: "11px var(--mono)",
                  color: d.delivered ? "var(--fg-muted)" : "var(--bad)",
                }}
              >
                <span style={{ minWidth: 150 }}>{d.at}</span>
                <span style={{ minWidth: 60 }}>{d.event}</span>
                <span style={{ minWidth: 70 }}>
                  {d.status_code === 0 ? "no response" : d.status_code}
                </span>
                <span style={{ minWidth: 70 }}>attempt {d.attempt}</span>
                <span style={{ flex: 1, wordBreak: "break-word" }}>
                  {d.error}
                </span>
              </div>
            ))}
          </div>
        )
      }
    </Async>
  );
}

function Browser({
  org,
  repo,
  defaultBranch,
}: {
  org: string;
  repo: string;
  defaultBranch: string;
}) {
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}`;
  const [path, setPath] = useState("");
  const [file, setFile] = useState<string | null>(null);

  const [branch, setBranch] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const qc = useQueryClient();

  const createBranch = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post(`${base}/branches`, { name: v.name, from: v.from }),
    onSuccess: (_d, v) => {
      setCreating(false);
      setBranch(v.name!);
      qc.invalidateQueries({ queryKey: ["branches"] });
    },
  });

  const branches = useQuery({
    queryKey: ["branches", org, repo],
    queryFn: () => api.get<{ refs: Ref[] }>(`${base}/branches`),
  });
  const refs = branches.data?.refs ?? [];
  const tagList = useQuery({
    queryKey: ["tags", org, repo],
    queryFn: () => api.get<{ refs: Ref[] }>(`${base}/tags`),
  });
  const tags = tagList.data?.refs ?? [];
  // A repository with no branch has no commit to read a tree or a log from.
  // Asking anyway answered 404, which the panels showed as "not available in
  // this deployment" — wrong on both counts for an empty repository.
  const empty = branches.data !== undefined && refs.length === 0;

  // The branch shown is the one chosen, else the repository's default, else
  // whatever exists. A repository whose default branch has no commit yet —
  // which is every repository an agent has written to and nobody has pushed
  // to — would otherwise show nothing at all.
  const head =
    (branch && [...refs, ...tags].some((r) => r.name === branch)
      ? branch
      : null) ??
    refs.find((r) => r.name === defaultBranch)?.name ??
    refs[0]?.name ??
    defaultBranch;

  const tree = useQuery({
    queryKey: ["tree", org, repo, head, path],
    queryFn: () =>
      api.get<{ entries: TreeEntry[] }>(
        `${base}/tree/${enc(head)}/${path.split("/").map(enc).join("/")}`,
      ),
    enabled: branches.data !== undefined && !empty,
  });

  // A blob is served as raw bytes, not JSON: a file is bytes, and wrapping it
  // in JSON would mean base64 and a size limit.
  const blob = useQuery({
    queryKey: ["blob", org, repo, head, file],
    queryFn: () =>
      api.text(
        `${base}/blob/${enc(head)}/${file!.split("/").map(enc).join("/")}`,
      ),
    enabled: file !== null,
  });

  const commits = useQuery({
    queryKey: ["commits", org, repo, head],
    queryFn: () =>
      api.get<{ commits: Commit[] }>(`${base}/commits/${enc(head)}?limit=8`),
    enabled: branches.data !== undefined && !empty,
  });

  const segments = path ? path.split("/").filter(Boolean) : [];

  return (
    <div
      style={{
        display: "grid",
        gridTemplateColumns: "minmax(0,300px) minmax(0,1fr)",
        gap: 14,
      }}
    >
      {creating ? (
        <Dialog
          title="New branch"
          submitLabel="Create"
          fields={[
            {
              name: "name",
              label: "Name",
              required: true,
              placeholder: "feature/x",
            },
            {
              name: "from",
              label: "From",
              placeholder: head,
              help: "Empty starts from the repository's default branch.",
            },
          ]}
          busy={createBranch.isPending}
          error={createBranch.error}
          onSubmit={(v) => createBranch.mutate(v)}
          onClose={() => setCreating(false)}
        />
      ) : null}
      <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
        <Panel>
          <PanelHead>
            {repo}
            <div style={{ flex: 1 }} />
            <button
              onClick={() => setCreating(true)}
              style={{
                background: "transparent",
                border: "1px solid var(--line-2)",
                borderRadius: 6,
                color: "var(--fg-muted)",
                font: "10px var(--sans)",
                padding: "3px 7px",
                cursor: "pointer",
              }}
            >
              new branch
            </button>
            <select
              aria-label="Branch or tag"
              value={head}
              onChange={(e) => {
                setBranch(e.target.value);
                setPath("");
                setFile(null);
              }}
              style={{
                background: "var(--bg)",
                border: "1px solid var(--line-2)",
                borderRadius: 6,
                color: "var(--fg-dim)",
                font: "11px var(--mono)",
                padding: "3px 6px",
                outline: "none",
                maxWidth: 200,
              }}
            >
              {refs.length === 0 ? <option>{head}</option> : null}
              <optgroup label="Branches">
                {refs.map((r) => (
                  <option key={r.name} value={r.name}>
                    {r.name}
                  </option>
                ))}
              </optgroup>
              {tags.length > 0 ? (
                <optgroup label="Tags">
                  {tags.map((r) => (
                    <option key={`tag:${r.name}`} value={r.name}>
                      {r.name}
                    </option>
                  ))}
                </optgroup>
              ) : null}
            </select>
          </PanelHead>
          <div
            style={{
              padding: "8px 14px",
              borderBottom: "1px solid var(--line)",
              font: "11px var(--mono)",
              color: "var(--fg-muted)",
            }}
          >
            <button
              onClick={() => {
                setPath("");
                setFile(null);
              }}
              style={crumb}
            >
              /
            </button>
            {segments.map((s, i) => (
              <button
                key={i}
                onClick={() => {
                  setPath(segments.slice(0, i + 1).join("/"));
                  setFile(null);
                }}
                style={crumb}
              >
                {s}/
              </button>
            ))}
          </div>
          {branches.error || tagList.error ? (
            <Failed error={branches.error || tagList.error} />
          ) : empty ? (
            <Empty>
              This repository is empty. Push a first commit to {defaultBranch}.
            </Empty>
          ) : (
            <Async query={tree}>
              {(d) =>
                d.entries.length === 0 ? (
                  <Empty>This repository has no commits yet.</Empty>
                ) : (
                  <>
                    {path ? (
                      <button
                        onClick={() => {
                          setPath(segments.slice(0, -1).join("/"));
                          setFile(null);
                        }}
                        style={entryStyle("var(--fg-muted)")}
                      >
                        ../
                      </button>
                    ) : null}
                    {d.entries.map((e) => (
                      <button
                        key={e.name}
                        onClick={() => {
                          const next = path ? `${path}/${e.name}` : e.name;
                          if (e.kind === "tree") {
                            setPath(next);
                            setFile(null);
                          } else {
                            setFile(next);
                          }
                        }}
                        style={entryStyle(
                          e.kind === "tree" ? "var(--fg-dim)" : "var(--link)",
                        )}
                      >
                        {e.kind === "tree" ? "▸ " : "  "}
                        {e.name}
                      </button>
                    ))}
                  </>
                )
              }
            </Async>
          )}
        </Panel>

        <Panel>
          <PanelHead>RECENT COMMITS</PanelHead>
          {empty ? (
            <Empty>No commits yet.</Empty>
          ) : (
            <Async query={commits}>
              {(d) =>
                d.commits.length === 0 ? (
                  <Empty>No commits.</Empty>
                ) : (
                  <>
                    {d.commits.map((c) => (
                      <div
                        key={c.sha}
                        style={{
                          padding: "9px 14px",
                          borderBottom: "1px solid var(--line)",
                        }}
                      >
                        <div style={{ font: "12px var(--sans)" }}>
                          {c.message.split("\n")[0]}
                        </div>
                        <div
                          style={{
                            font: "11px var(--mono)",
                            color: "var(--fg-faint)",
                            marginTop: 3,
                          }}
                        >
                          {c.sha.slice(0, 8)} · {c.author_name}
                        </div>
                      </div>
                    ))}
                  </>
                )
              }
            </Async>
          )}
        </Panel>

        <Releases base={base} org={org} repo={repo} tags={tags} />

        {empty ? null : (
          <Compare
            base={base}
            refs={[...refs, ...tags]}
            defaultFrom={defaultBranch}
            defaultTo={head}
          />
        )}
      </div>

      <Panel style={{ minWidth: 0 }}>
        <PanelHead>{file ?? "SELECT A FILE"}</PanelHead>
        {file === null ? (
          <Empty>Pick a file from the tree.</Empty>
        ) : (
          <Async query={blob}>
            {(d) => (
              <pre
                style={{
                  margin: 0,
                  padding: 16,
                  maxHeight: "calc(100vh - 260px)",
                  overflow: "auto",
                  font: "12px/1.65 var(--mono)",
                  color: "var(--fg-dim)",
                  whiteSpace: "pre-wrap",
                  wordBreak: "break-word",
                }}
              >
                {d}
              </pre>
            )}
          </Async>
        )}
      </Panel>
    </div>
  );
}

/** Releases lists a repository's releases and the files published with each one.
 *
 * A release can only be published on a tag the repository already has, so the tag
 * is chosen from the repository's own tags rather than typed: the platform refuses
 * anything else, and a free-text field would only lead people to that refusal.
 * When there are no tags the panel says so instead of offering an empty picker.
 *
 * Nothing here invents a number: sizes and types are what the platform recorded
 * when the file arrived, and a release with no assets is shown as having none. */
function Releases({
  base,
  org,
  repo,
  tags,
}: {
  base: string;
  org: string;
  repo: string;
  tags: Ref[];
}) {
  const qc = useQueryClient();
  const [publishing, setPublishing] = useState(false);
  const [uploadError, setUploadError] = useState<unknown>(null);

  const releases = useQuery({
    queryKey: ["releases", org, repo],
    queryFn: () => api.get<{ releases: Release[] }>(`${base}/releases`),
  });
  const invalidate = () =>
    void qc.invalidateQueries({ queryKey: ["releases", org, repo] });

  const publish = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post<Release>(`${base}/releases`, {
        tag: v.tag,
        name: v.name,
        body: v.body,
      }),
    onSuccess: () => {
      setPublishing(false);
      invalidate();
    },
  });

  const remove = useMutation({
    mutationFn: (tag: string) => api.del(`${base}/releases/${enc(tag)}`),
    onSuccess: invalidate,
  });

  const addAsset = useMutation({
    mutationFn: ({ tag, file }: { tag: string; file: File }) =>
      api.upload(
        `${base}/releases/${enc(tag)}/assets?name=${enc(file.name)}`,
        file,
      ),
    onSuccess: () => {
      setUploadError(null);
      invalidate();
    },
    onError: setUploadError,
  });

  const tagNames = tags.map((t) => t.name);

  return (
    <Panel>
      <PanelHead>
        RELEASES
        <div style={{ flex: 1 }} />
        <button
          onClick={() => {
            publish.reset();
            setPublishing(true);
          }}
          disabled={tagNames.length === 0}
          title={
            tagNames.length === 0
              ? "Push a tag first: a release is published on a tag."
              : undefined
          }
          style={{
            background: "transparent",
            border: "1px solid var(--line-2)",
            borderRadius: 6,
            color: "var(--fg-muted)",
            font: "10px var(--sans)",
            padding: "3px 7px",
            cursor: tagNames.length === 0 ? "default" : "pointer",
          }}
        >
          publish a release
        </button>
      </PanelHead>

      {publishing ? (
        <Dialog
          title="Publish a release"
          submitLabel="Publish"
          fields={[
            {
              name: "tag",
              label: "Tag",
              required: true,
              type: "select",
              options: tagNames,
              help: "Only a tag that exists in this repository can carry a release.",
            },
            { name: "name", label: "Title", placeholder: "v1.0.0" },
            { name: "body", label: "Notes", type: "textarea" },
          ]}
          busy={publish.isPending}
          error={publish.error}
          onSubmit={(v) => publish.mutate(v)}
          onClose={() => setPublishing(false)}
        />
      ) : null}

      <Async query={releases}>
        {(d) =>
          d.releases.length === 0 ? (
            <Empty>
              No releases yet. Publish one on a tag to hand out a build.
            </Empty>
          ) : (
            <>
              {d.releases.map((rel) => (
                <div
                  key={rel.id}
                  style={{
                    padding: "9px 14px",
                    borderBottom: "1px solid var(--line)",
                  }}
                >
                  <div
                    style={{ display: "flex", alignItems: "center", gap: 8 }}
                  >
                    <span style={{ font: "600 12px var(--mono)" }}>
                      {rel.tag}
                    </span>
                    <span
                      style={{
                        font: "12px var(--sans)",
                        color: "var(--fg-dim)",
                      }}
                    >
                      {rel.name}
                    </span>
                    <div style={{ flex: 1 }} />
                    <label style={assetAction}>
                      add asset
                      <input
                        type="file"
                        style={{ display: "none" }}
                        disabled={addAsset.isPending}
                        onChange={(e) => {
                          const file = e.target.files?.[0];
                          // The input is cleared so choosing the same file twice
                          // still fires a change, which is how a retry after a
                          // failed upload silently did nothing.
                          e.target.value = "";
                          if (file) addAsset.mutate({ tag: rel.tag, file });
                        }}
                      />
                    </label>
                    <button
                      onClick={() => remove.mutate(rel.tag)}
                      disabled={remove.isPending}
                      style={{ ...assetAction, color: "var(--bad)" }}
                    >
                      delete
                    </button>
                  </div>
                  {rel.body ? (
                    <div
                      style={{
                        font: "11px/1.6 var(--sans)",
                        color: "var(--fg-muted)",
                        marginTop: 3,
                        whiteSpace: "pre-wrap",
                      }}
                    >
                      {rel.body}
                    </div>
                  ) : null}
                  {rel.assets.length === 0 ? (
                    <div
                      style={{
                        font: "11px var(--sans)",
                        color: "var(--fg-faint)",
                        marginTop: 4,
                      }}
                    >
                      No assets on this release.
                    </div>
                  ) : (
                    <div style={{ marginTop: 5, display: "grid", gap: 3 }}>
                      {rel.assets.map((a) => (
                        <div
                          key={a.id}
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: 8,
                            font: "11px var(--mono)",
                            color: "var(--fg-muted)",
                          }}
                        >
                          {/* The credential lives in this page, not in a cookie,
                              so a plain link cannot fetch the file. */}
                          <button
                            onClick={() =>
                              void api.download(
                                `${base}/releases/${enc(rel.tag)}/assets/${enc(a.name)}`,
                                a.name,
                              )
                            }
                            style={{ ...assetAction, color: "var(--link)" }}
                          >
                            {a.name}
                          </button>
                          <span>{formatBytes(a.size_bytes)}</span>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              ))}
            </>
          )
        }
      </Async>
      {uploadError ? <Failed error={uploadError} /> : null}
      {remove.error ? <Failed error={remove.error} /> : null}
    </Panel>
  );
}

/** formatBytes reports the size the platform recorded, never a rounded guess at
 * one it does not have. */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

const assetAction: React.CSSProperties = {
  background: "transparent",
  border: "none",
  padding: 0,
  color: "var(--fg-muted)",
  font: "10px var(--sans)",
  cursor: "pointer",
};

/** Compare shows the unified diff between two refs, branches or tags, as
 * git-platform computes it. Nothing is fetched until someone asks, so the
 * panel never shows an empty diff for a question nobody put. */
function Compare({
  base,
  refs,
  defaultFrom,
  defaultTo,
}: {
  base: string;
  refs: Ref[];
  defaultFrom: string;
  defaultTo: string;
}) {
  const [from, setFrom] = useState(defaultFrom);
  const [to, setTo] = useState(defaultTo);
  const [asked, setAsked] = useState<{ from: string; to: string } | null>(null);
  const diff = useQuery({
    queryKey: ["diff", base, asked?.from, asked?.to],
    queryFn: () =>
      api.get<{ unified: string }>(
        `${base}/diff?from=${enc(asked!.from)}&to=${enc(asked!.to)}`,
      ),
    enabled: asked !== null,
  });
  const names = Array.from(new Set(refs.map((r) => r.name)));
  const picker = (value: string, onChange: (v: string) => void) => (
    <select
      aria-label={onChange === setFrom ? "Compare from ref" : "Compare to ref"}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      style={selectStyle}
    >
      {names.includes(value) ? null : <option>{value}</option>}
      {names.map((n) => (
        <option key={n} value={n}>
          {n}
        </option>
      ))}
    </select>
  );
  return (
    <Panel>
      <PanelHead>COMPARE</PanelHead>
      <div
        style={{
          display: "flex",
          gap: 6,
          alignItems: "center",
          flexWrap: "wrap",
          padding: "8px 14px",
        }}
      >
        {picker(from, setFrom)}
        <span style={{ color: "var(--fg-faint)", font: "11px var(--mono)" }}>
          ..
        </span>
        {picker(to, setTo)}
        <button
          onClick={() => setAsked({ from, to })}
          disabled={from === to}
          style={{
            background: "transparent",
            border: "1px solid var(--line-2)",
            borderRadius: 6,
            color: "var(--fg-muted)",
            font: "10px var(--sans)",
            padding: "3px 7px",
            cursor: from === to ? "default" : "pointer",
          }}
        >
          show diff
        </button>
      </div>
      {asked === null ? (
        <Empty>Choose two refs to see what changed between them.</Empty>
      ) : (
        <Async query={diff}>
          {(d) =>
            d.unified === "" ? (
              <Empty>
                No difference between {asked.from} and {asked.to}.
              </Empty>
            ) : (
              <pre
                style={{
                  margin: 0,
                  padding: 14,
                  maxHeight: 360,
                  overflow: "auto",
                  font: "11px/1.6 var(--mono)",
                  whiteSpace: "pre",
                }}
              >
                {d.unified.split("\n").map((line, i) => (
                  <div key={i} style={{ color: diffColor(line) }}>
                    {line || " "}
                  </div>
                ))}
              </pre>
            )
          }
        </Async>
      )}
    </Panel>
  );
}

function diffColor(line: string): string {
  if (line.startsWith("+++") || line.startsWith("---"))
    return "var(--fg-muted)";
  if (line.startsWith("+")) return "var(--ok)";
  if (line.startsWith("-")) return "var(--bad)";
  return "var(--fg-dim)";
}

const selectStyle: React.CSSProperties = {
  background: "var(--bg)",
  border: "1px solid var(--line-2)",
  borderRadius: 6,
  color: "var(--fg-dim)",
  font: "11px var(--mono)",
  padding: "3px 6px",
  outline: "none",
  maxWidth: 160,
};

const dangerButton: React.CSSProperties = {
  padding: "7px 14px",
  background: "transparent",
  border: "1px solid #e5534b66",
  borderRadius: 8,
  color: "var(--bad)",
  font: "600 12px var(--sans)",
  cursor: "pointer",
};

const crumb: React.CSSProperties = {
  background: "transparent",
  border: "none",
  color: "var(--link)",
  font: "11px var(--mono)",
  cursor: "pointer",
  padding: 0,
};

function entryStyle(color: string): React.CSSProperties {
  return {
    display: "block",
    width: "100%",
    textAlign: "left",
    padding: "6px 14px",
    background: "transparent",
    border: "none",
    color,
    font: "12px var(--mono)",
    cursor: "pointer",
  };
}

/** Who holds this repository without belonging to the organization that owns it.
 *
 * Only an owner or admin sees this panel, because only they can change it and the
 * server refuses anyone else. A grant is shown with the role it gives, since that is
 * what the grant is; a list of names without roles would hide the difference between
 * someone who can read and someone who can push. */
function Collaborators({ org, repo }: { org: string; repo: string }) {
  const qc = useQueryClient();
  const [user, setUser] = useState("");
  const [role, setRole] = useState("read");
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}/collaborators`;

  const grants = useQuery({
    queryKey: ["collaborators", org, repo],
    queryFn: () => api.get<{ collaborators: RepoCollaborator[] }>(base),
  });
  const teams = useQuery({
    queryKey: ["teams", org],
    queryFn: () => api.get<{ teams: Team[] }>(`/api/v1/orgs/${enc(org)}/teams`),
  });
  const grant = useMutation({
    mutationFn: (v: { user?: string; team_id?: string; role: string }) =>
      api.post(base, v),
    onSuccess: () => {
      setUser("");
      void qc.invalidateQueries({ queryKey: ["collaborators", org, repo] });
    },
  });
  const revoke = useMutation({
    mutationFn: (v: { subject: string; kind: string }) =>
      api.del(`${base}/${enc(v.subject)}?kind=${v.kind}`),
    onSuccess: () =>
      void qc.invalidateQueries({ queryKey: ["collaborators", org, repo] }),
  });

  const teamName = new Map(
    (teams.data?.teams ?? []).map((t) => [t.id, t.name]),
  );

  return (
    <Panel style={{ marginBottom: 12 }}>
      <PanelHead>
        COLLABORATORS
        <span style={{ color: "var(--fg-faint)" }}>
          {grants.data?.collaborators.length ?? 0}
        </span>
      </PanelHead>
      <div style={{ padding: 12, display: "grid", gap: 10 }}>
        <div style={{ font: "12px var(--sans)", color: "var(--fg-muted)" }}>
          Access to this repository alone, for someone who is not a member of
          this organization. A grant to a team reaches whoever is in it.
        </div>

        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <input
            value={user}
            placeholder="username"
            onChange={(e) => setUser(e.target.value)}
            style={adminInput}
          />
          <select
            value={role}
            onChange={(e) => setRole(e.target.value)}
            style={{ ...adminInput, minWidth: 100 }}
          >
            <option value="read">read</option>
            <option value="write">write</option>
          </select>
          <button
            disabled={user === "" || grant.isPending}
            onClick={() => grant.mutate({ user, role })}
            style={adminButton}
          >
            Grant
          </button>
          <select
            defaultValue=""
            onChange={(e) => {
              if (e.target.value !== "") {
                grant.mutate({ team_id: e.target.value, role });
                e.target.value = "";
              }
            }}
            disabled={(teams.data?.teams ?? []).length === 0 || grant.isPending}
            style={{ ...adminInput, minWidth: 160 }}
          >
            <option value="">
              {(teams.data?.teams ?? []).length === 0
                ? "no teams to grant to"
                : "…or grant to a team"}
            </option>
            {(teams.data?.teams ?? []).map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
          </select>
        </div>
        {grant.error ? <Failed error={grant.error} /> : null}
        {revoke.error ? <Failed error={revoke.error} /> : null}

        <Async query={grants}>
          {(data) =>
            data.collaborators.length === 0 ? (
              <Empty>
                Nobody outside this organization holds this repository.
              </Empty>
            ) : (
              <>
                {data.collaborators.map((g) => {
                  const isTeam = g.team_id !== "";
                  const subject = isTeam ? g.team_id : g.user_id;
                  return (
                    <div
                      key={subject}
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: 10,
                        padding: "7px 0",
                        borderTop: "1px solid var(--line)",
                      }}
                    >
                      <span style={{ font: "12px var(--mono)", flex: 1 }}>
                        {isTeam
                          ? `team ${teamName.get(g.team_id) ?? g.team_id}`
                          : g.user_id}
                      </span>
                      <span
                        style={{
                          font: "11px var(--mono)",
                          color: "var(--fg-muted)",
                        }}
                      >
                        {g.role}
                      </span>
                      <button
                        onClick={() =>
                          revoke.mutate({
                            subject,
                            kind: isTeam ? "team" : "user",
                          })
                        }
                        style={adminButton}
                      >
                        Revoke
                      </button>
                    </div>
                  );
                })}
              </>
            )
          }
        </Async>
      </div>
    </Panel>
  );
}
