import { useState } from "react";
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
} from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { api, ApiError, enc } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import {
  Async,
  Empty,
  Failed,
  Loading,
  Page,
  Panel,
  PanelHead,
} from "../../components/ui";
import { Confirm, Dialog } from "../../components/Dialog";
import type {
  GateConfig,
  Hook,
  HookDelivery,
  Mirror,
  Repo,
  RepoCollaborator,
  Team,
} from "../../lib/types";
import {
  adminLabel,
  adminRow,
  btn,
  btnDanger,
  btnSmall,
  input,
  mono,
  timeAgo,
  useRepoScope,
} from "./shared";

/** The design's settings sections, in its order. Each is an anchor on one
 * page, because a setting changed in one is usually judged in the next. */
const SECTIONS = [
  ["general", "General"],
  ["collaborators", "Collaborators"],
  ["gates", "Rules & gates"],
  ["webhooks", "Webhooks"],
  ["mirroring", "Mirroring"],
  ["danger", "Danger zone"],
] as const;

export function SettingsTab() {
  const { org, repo, canAdminister } = useRepoScope();
  const navigate = useNavigate();
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}`;
  const w = useWorkspace();
  const qc = useQueryClient();
  const record = w.repos.find((r) => r.name === repo);

  // Administration is a PATCH with only the field being changed, so renaming
  // does not also reset the default branch and archiving does not also rename.
  const administer = useMutation({
    mutationFn: (patch: {
      name?: string;
      default_branch?: string;
      archived?: boolean;
    }) => api.patch<Repo>(base, patch),
    onSuccess: (updated) => {
      if (w.repo === repo && updated.name !== repo) w.setRepo(updated.name);
      void qc.invalidateQueries({ queryKey: ["repos"] });
      void qc.invalidateQueries({ queryKey: ["repo", org, repo] });
      // The URL names the old repository, which no longer exists; following
      // the rename keeps the screen on the settings it is showing.
      if (updated.name !== repo)
        navigate(`/repos/${enc(updated.name)}/settings`);
    },
  });

  const transfer = useMutation({
    mutationFn: (toOrg: string) =>
      api.post<Repo>(`${base}/transfer`, { to_org: toOrg }),
    onSuccess: () => {
      if (w.repo === repo) w.setRepo(null);
      void qc.invalidateQueries({ queryKey: ["repos"] });
    },
  });

  const remove = useMutation({
    mutationFn: () => api.del(base),
    onSuccess: () => {
      setDeleting(false);
      if (w.repo === repo) w.setRepo(null);
      void qc.invalidateQueries({ queryKey: ["repos"] });
    },
  });

  const [deleting, setDeleting] = useState(false);
  const [transferring, setTransferring] = useState(false);

  // The default branch can only be set to a branch that exists, so the choice
  // is the repository's own branches rather than free text.
  const branches = useQuery({
    queryKey: ["branches", org, repo],
    queryFn: () =>
      api.get<{ refs: { name: string; sha: string; kind: string }[] }>(
        `${base}/branches`,
      ),
  });
  const branchNames = (branches.data?.refs ?? []).map((r) => r.name);

  const otherOrgs = w.orgs
    .filter((o) => o.name !== org)
    .map((o) => ({ id: o.id, name: o.name }));

  return (
    <Page title="Settings" subtitle={`${org} / ${repo}`}>
      {!canAdminister ? (
        <div
          style={{
            marginBottom: 12,
            font: "12px var(--sans)",
            color: "var(--fg-muted)",
          }}
        >
          You can read these settings. Changing them needs an owner or admin of{" "}
          {org}.
        </div>
      ) : null}

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "minmax(0,1fr) 220px",
          gap: 14,
          alignItems: "start",
        }}
      >
        <div style={{ display: "grid", gap: 14, minWidth: 0 }}>
          <Panel>
            <PanelHead title="GENERAL" />
            <div style={{ padding: 12, display: "grid", gap: 10 }}>
              <GeneralSection
                record={record}
                branchNames={branchNames}
                administer={administer}
              />
              {administer.error ? <Failed error={administer.error} /> : null}
              {branches.error ? <Failed error={branches.error} /> : null}
            </div>
          </Panel>

          <Panel>
            <PanelHead title="COLLABORATORS" />
            {canAdminister ? (
              <Collaborators org={org} repo={repo} />
            ) : (
              <Empty>
                Only an owner or admin of this organization can change who holds
                this repository.
              </Empty>
            )}
          </Panel>

          <Panel>
            <PanelHead title="RULES & GATES" />
            <GateList org={org} repo={repo} />
          </Panel>

          <Panel>
            <PanelHead title="WEBHOOKS" />
            <Hooks org={org} repo={repo} />
          </Panel>

          <Panel>
            <PanelHead title="MIRRORING" />
            <MirrorPanel org={org} repo={repo} canAdminister={canAdminister} />
          </Panel>

          <Panel style={{ borderColor: "#e5534b44" }}>
            <PanelHead title="DANGER ZONE" />
            <div style={{ padding: 12, display: "grid", gap: 12 }}>
              <div style={adminRow}>
                <span style={adminLabel}>Transfer</span>
                <span
                  style={{
                    flex: 1,
                    font: "12px var(--sans)",
                    color: "var(--fg-muted)",
                  }}
                >
                  Move this repository, with its history, to another
                  organization you belong to.
                </span>
                <button
                  style={btn}
                  onClick={() => setTransferring(true)}
                  disabled={otherOrgs.length === 0}
                  title={
                    otherOrgs.length === 0
                      ? "You belong to no other organization."
                      : undefined
                  }
                >
                  Transfer
                </button>
              </div>
              <div style={adminRow}>
                <span style={adminLabel}>Delete</span>
                <span
                  style={{
                    flex: 1,
                    font: "12px var(--sans)",
                    color: "var(--fg-muted)",
                  }}
                >
                  Permanently delete this repository and its entire history.
                  Nothing on the platform can be recovered.
                </span>
                {canAdminister ? (
                  <button
                    style={btnDanger}
                    onClick={() => {
                      remove.reset();
                      setDeleting(true);
                    }}
                  >
                    Delete repository
                  </button>
                ) : null}
              </div>
              {transfer.error ? <Failed error={transfer.error} /> : null}
              {remove.error ? <Failed error={remove.error} /> : null}
            </div>
          </Panel>
        </div>

        <nav
          aria-label="Settings sections"
          style={{
            position: "sticky",
            top: 0,
            display: "grid",
            gap: 2,
            font: "12px var(--sans)",
          }}
        >
          {SECTIONS.map(([id, label]) => (
            <a
              key={id}
              href={`#${id}`}
              style={{
                color: "var(--fg-muted)",
                textDecoration: "none",
                padding: "5px 10px",
                borderRadius: 6,
              }}
            >
              {label}
            </a>
          ))}
        </nav>
      </div>

      {transferring && record ? (
        <TransferDialog
          orgs={otherOrgs}
          transfer={transfer}
          onClose={() => setTransferring(false)}
        />
      ) : null}

      {deleting ? (
        <Confirm
          title={`Delete ${repo}`}
          body={
            <>
              This permanently deletes the repository <strong>{repo}</strong>{" "}
              and its entire history from this organization. Clones elsewhere
              are unaffected; nothing on the platform can be recovered.
            </>
          }
          confirmLabel="Delete repository"
          typeToConfirm={repo}
          danger
          busy={remove.isPending}
          error={remove.error}
          onConfirm={() => remove.mutate()}
          onClose={() => setDeleting(false)}
        />
      ) : null}
    </Page>
  );
}

/** GeneralSection holds the fields the platform actually stores on a
 * repository: its name, its default branch, and the archive state. The
 * design's description field has no endpoint behind it, so it is not drawn
 * as an editable field that would silently save nothing. */
function GeneralSection({
  record,
  branchNames,
  administer,
}: {
  record: Repo | undefined;
  branchNames: string[];
  administer: UseMutationResult<
    Repo,
    unknown,
    { name?: string; default_branch?: string; archived?: boolean }
  >;
}) {
  const [name, setName] = useState("");
  if (!record) return <Loading />;
  return (
    <>
      <label style={adminRow}>
        <span style={adminLabel}>Repository name</span>
        <input
          value={name}
          placeholder={record.name}
          onChange={(e) => setName(e.target.value)}
          style={{ ...input, minWidth: 200 }}
        />
        <button
          style={btn}
          disabled={name === "" || name === record.name || administer.isPending}
          onClick={() => administer.mutate({ name })}
        >
          Rename
        </button>
      </label>

      <label style={adminRow}>
        <span style={adminLabel}>Default branch</span>
        <select
          value={record.default_branch}
          onChange={(e) =>
            administer.mutate({ default_branch: e.target.value })
          }
          disabled={administer.isPending || branchNames.length === 0}
          style={{ ...input, minWidth: 200 }}
        >
          {branchNames.length === 0 ? (
            <option value={record.default_branch}>
              {record.default_branch}
            </option>
          ) : (
            branchNames.map((b) => (
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
          {record.archived
            ? "Writes are refused. History stays readable."
            : "Keep the history readable and stop it changing."}
        </span>
        <button
          style={btn}
          disabled={administer.isPending}
          onClick={() => administer.mutate({ archived: !record.archived })}
        >
          {record.archived ? "Un-archive" : "Archive"}
        </button>
      </div>

      <div style={adminRow}>
        <span style={adminLabel}>Description</span>
        <span
          style={{
            flex: 1,
            font: "12px var(--sans)",
            color: "var(--fg-muted)",
          }}
        >
          Not available in this deployment — the platform stores no description
          for a repository.
        </span>
      </div>
    </>
  );
}

/** GateList is the repository's gate policy as the platform reads it from
 * its default branch. Gates are changed through a proposed Engineering Run,
 * not edited in place, so this is a view and the Rules screen owns the
 * change. */
function GateList({ org, repo }: { org: string; repo: string }) {
  const gates = useQuery({
    queryKey: ["gates", org, repo],
    queryFn: () =>
      api.get<{ ref: string; commit_sha: string; gates: GateConfig[] }>(
        `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}/gates`,
      ),
  });
  return (
    <div style={{ padding: "0 0 12px" }}>
      <div
        style={{
          padding: "8px 14px",
          font: "12px var(--sans)",
          color: "var(--fg-muted)",
        }}
      >
        Gates a merge must pass, read from the policy at{" "}
        <span style={mono}>{gates.data?.ref ?? "…"}</span>. Changes are proposed
        as a run — see <Link to="/rules">Rules &amp; gates</Link>.
      </div>
      <Async query={gates}>
        {(d) =>
          d.gates.length === 0 ? (
            <Empty>No gates are declared on this repository.</Empty>
          ) : (
            d.gates.map((g) => (
              <div
                key={g.name}
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 10,
                  padding: "7px 14px",
                  borderTop: "1px solid var(--line)",
                }}
              >
                <span style={{ font: "600 12px var(--mono)", flex: 1 }}>
                  {g.name}
                </span>
                <span
                  style={{
                    font: "11px var(--mono)",
                    color: "var(--fg-faint)",
                    flex: 1,
                  }}
                >
                  {g.path}
                </span>
                <span
                  style={{
                    font: "11px var(--mono)",
                    color: g.enabled ? "var(--ok)" : "var(--fg-faint)",
                  }}
                >
                  {g.enabled
                    ? "blocking"
                    : g.declared
                      ? "declared, off"
                      : "not declared"}
                </span>
              </div>
            ))
          )
        }
      </Async>
    </div>
  );
}

/** TransferDialog names the one thing transfer needs: which organization.
 * Only organizations this person belongs to are offered, because a transfer
 * into one they do not would be refused. */
function TransferDialog({
  orgs,
  transfer,
  onClose,
}: {
  orgs: { id: string; name: string }[];
  transfer: UseMutationResult<Repo, unknown, string>;
  onClose: () => void;
}) {
  return (
    <Dialog
      title="Transfer repository"
      description="Moves this repository and its history to another organization. This workspace stops showing it; the people of the new organization see it instead."
      submitLabel="Transfer"
      busy={transfer.isPending}
      error={transfer.error}
      fields={[
        {
          name: "to",
          label: "Organization",
          required: true,
          type: "select",
          options: orgs.map((o) => o.name),
        },
      ]}
      onSubmit={(v) => {
        const target = orgs.find((o) => o.name === v.to);
        if (target) transfer.mutate(target.id);
      }}
      onClose={onClose}
    />
  );
}

/** Collaborators: who holds this repository without belonging to the
 * organization that owns it. A grant is shown with the role it gives, since
 * that is what the grant is; a list of names without roles would hide the
 * difference between someone who can read and someone who can push. */
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
    <div style={{ padding: 12, display: "grid", gap: 10 }}>
      <div style={{ font: "12px var(--sans)", color: "var(--fg-muted)" }}>
        Access to this repository alone, for someone who is not a member of this
        organization. A grant to a team reaches whoever is in it.
      </div>

      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 8,
          flexWrap: "wrap",
        }}
      >
        <input
          value={user}
          placeholder="username"
          onChange={(e) => setUser(e.target.value)}
          style={{ ...input, minWidth: 140 }}
        />
        <select
          value={role}
          onChange={(e) => setRole(e.target.value)}
          style={{ ...input, minWidth: 100 }}
        >
          <option value="read">read</option>
          <option value="write">write</option>
        </select>
        <button
          style={btn}
          disabled={user === "" || grant.isPending}
          onClick={() => grant.mutate({ user, role })}
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
          style={{ ...input, minWidth: 170 }}
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
            data.collaborators.map((g) => {
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
                    style={btnSmall}
                    onClick={() =>
                      revoke.mutate({ subject, kind: isTeam ? "team" : "user" })
                    }
                  >
                    Revoke
                  </button>
                </div>
              );
            })
          )
        }
      </Async>
    </div>
  );
}

/** Hooks is the webhook panel: the endpoints this repository notifies, and
 * what each one answered. The delivery history is the point of it — a
 * webhook that is quietly failing is the most common integration complaint,
 * and the platform knows exactly why. */
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

  // Switching a hook off sends only `active`, and rotating sends only
  // `secret`: the edge applies exactly the fields present, so a rotation
  // cannot silently switch the hook off and switching it off cannot strip
  // its signature.
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
    <div style={{ padding: 12, display: "grid", gap: 10 }}>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 8,
          flexWrap: "wrap",
        }}
      >
        <input
          value={url}
          placeholder="https://example.com/novaforge"
          onChange={(e) => setUrl(e.target.value)}
          style={{ ...input, flex: 1, minWidth: 200 }}
        />
        <input
          value={events}
          placeholder="push, engineering_run, ci_result"
          onChange={(e) => setEvents(e.target.value)}
          style={{ ...input, width: 200 }}
        />
        <input
          value={secret}
          type="password"
          placeholder="signing secret (optional)"
          onChange={(e) => setSecret(e.target.value)}
          style={{ ...input, width: 170 }}
        />
        <button
          style={btn}
          disabled={url === "" || create.isPending}
          onClick={() => create.mutate()}
        >
          Add
        </button>
      </div>
      <div style={{ color: "var(--fg-faint)", fontSize: 12 }}>
        Events: push, engineering_run, ci_result. Leave blank for all events,
        signed HMAC-SHA256 in X-NovaForge-Signature. Delivery IDs stay the same
        across retries.
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
                    <span style={{ ...mono, flex: 1, wordBreak: "break-all" }}>
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
                      style={btnSmall}
                      disabled={update.isPending}
                      onClick={() =>
                        update.mutate({ id: h.id, active: !h.active })
                      }
                    >
                      {h.active ? "Disable" : "Enable"}
                    </button>
                    <button
                      style={btnSmall}
                      disabled={remove.isPending}
                      onClick={() => remove.mutate(h.id)}
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
                      flexWrap: "wrap",
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
                      style={btnSmall}
                      onClick={() => setOpen(open === h.id ? null : h.id)}
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
  );
}

/** Deliveries is one hook's attempt history, newest first. Each row says what
 * the endpoint answered; a failure that never reached it at all has no
 * status, so the error is shown in its place rather than a fabricated 0. */
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
                  flexWrap: "wrap",
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

/** MirrorPanel is what this repository follows, if anything.
 *
 * A repository that is not a mirror is an absence, not a failure: the
 * endpoint answers 404 and the panel says the history is this platform's
 * own. Rendering `Failed` for it would make every ordinary repository look
 * broken. */
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
      // write-only on the platform, and leaving it in an input is the one
      // place it would still be readable.
      setCredential("");
      invalidate();
    },
  });
  const refresh = useMutation({
    mutationFn: () => api.post<Mirror>(`${base}/refresh`, {}),
    onSettled: invalidate,
  });
  const stop = useMutation({
    mutationFn: () => api.del(base),
    onSuccess: invalidate,
  });

  const m = mirror.data;
  return (
    <div style={{ padding: 12, display: "grid", gap: 10 }}>
      <span style={{ font: "11px var(--sans)", color: "var(--fg-faint)" }}>
        a mirror refuses pushes; upstream owns its history
      </span>
      {mirror.isPending ? (
        <Loading />
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
                : `${new Date(m.last_synced_at).toLocaleString()} (${timeAgo(m.last_synced_at)})`}
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
          {canAdminister ? (
            <div>
              <button
                style={btn}
                disabled={refresh.isPending}
                onClick={() => refresh.mutate()}
              >
                {refresh.isPending ? "Refreshing…" : "Refresh now"}
              </button>
            </div>
          ) : null}
          {refresh.error ? <Failed error={refresh.error} /> : null}
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
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: 8,
              flexWrap: "wrap",
            }}
          >
            <span style={adminLabel}>
              {notMirrored ? "Follow" : "Repoint to"}
            </span>
            <input
              value={remote}
              placeholder="https://github.com/owner/project.git"
              onChange={(e) => setRemote(e.target.value)}
              style={{ ...input, flex: 1, minWidth: 220 }}
            />
            <input
              value={credential}
              type="password"
              placeholder="access token (optional)"
              onChange={(e) => setCredential(e.target.value)}
              style={{ ...input, width: 180 }}
            />
            <input
              value={every}
              placeholder="seconds (blank = hourly)"
              onChange={(e) => setEvery(e.target.value)}
              style={{ ...input, width: 170 }}
            />
            <button
              style={btn}
              onClick={() => save.mutate()}
              disabled={remote === "" || save.isPending}
            >
              {notMirrored ? "Start mirroring" : "Save"}
            </button>
          </div>
          {notMirrored ? null : (
            <div style={adminRow}>
              <span style={adminLabel}>Stop</span>
              <button
                style={btn}
                onClick={() => stop.mutate()}
                disabled={stop.isPending}
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
  );
}
