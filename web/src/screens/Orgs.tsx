import { useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Empty, Failed, Page, Panel, PanelHead } from "../components/ui";
import { Dialog, NewButton } from "../components/Dialog";
import type { OrgMember, User } from "../lib/types";

/** Orgs is the design's organizations screen. The organization's people and
 * teams have their own screen — People & teams — so this screen is what
 * choosing an organization means: the organizations this account belongs to,
 * the repositories the current one holds, and the deletion only an owner can
 * do. */
export function Orgs() {
  const w = useWorkspace();
  const qc = useQueryClient();
  const [creating, setCreating] = useState<"org" | "repo" | null>(null);

  const createOrg = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post("/api/v1/orgs", { name: v.name }),
    onSuccess: (_data, v) => {
      setCreating(null);
      // The new organization becomes the current scope: creating one and then
      // having to go and find it is not what anyone meant by creating it.
      w.setOrg(v.name!);
      qc.invalidateQueries();
    },
  });

  const createRepo = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post(`/api/v1/orgs/${enc(w.org!)}/repos`, { name: v.name }),
    onSuccess: () => {
      setCreating(null);
      qc.invalidateQueries();
    },
  });

  // Deleting the organization is offered only to its owners: identity refuses
  // everyone else, and a button that can only lead to a refusal is noise. The
  // members list is read for the viewer's role alone.
  const members = useQuery({
    queryKey: ["members", w.org],
    queryFn: () =>
      api.get<{ members: OrgMember[] }>(`/api/v1/orgs/${enc(w.org!)}/members`),
    enabled: w.org !== null,
  });
  const me = useQuery({
    queryKey: ["user"],
    queryFn: () => api.get<User>("/api/v1/user"),
  });
  const role = members.data?.members.find(
    (m) => m.user_id === me.data?.id,
  )?.role;
  const isOwner = role === "owner";

  const [confirmName, setConfirmName] = useState("");
  const deleteOrg = useMutation({
    mutationFn: (name: string) =>
      api.del(`/api/v1/orgs/${enc(name)}`, { confirm_name: confirmName }),
    onSuccess: (_d, name) => {
      setConfirmName("");
      const next = w.orgs.find((o) => o.name !== name);
      if (next) w.setOrg(next.name);
      qc.invalidateQueries();
    },
  });

  return (
    <Page
      title="Organizations"
      subtitle={w.org ?? ""}
      actions={
        <div style={{ display: "flex", gap: 8 }}>
          <NewButton
            label="New organization"
            onClick={() => setCreating("org")}
          />
          {w.org ? (
            <NewButton
              label="New repository"
              onClick={() => setCreating("repo")}
            />
          ) : null}
        </div>
      }
    >
      {creating === "org" ? (
        <Dialog
          title="New organization"
          submitLabel="Create"
          fields={[
            {
              name: "name",
              label: "Name",
              required: true,
              placeholder: "acme",
              help: "Organizations are the platform's hard security boundary: nothing reads across one.",
            },
          ]}
          busy={createOrg.isPending}
          error={createOrg.error}
          onSubmit={(v) => createOrg.mutate(v)}
          onClose={() => setCreating(null)}
        />
      ) : null}
      {creating === "repo" ? (
        <Dialog
          title="New repository"
          submitLabel="Create"
          fields={[
            {
              name: "name",
              label: "Name",
              required: true,
              placeholder: "platform",
            },
          ]}
          busy={createRepo.isPending}
          error={createRepo.error}
          onSubmit={(v) => createRepo.mutate(v)}
          onClose={() => setCreating(null)}
        />
      ) : null}

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "minmax(0,1fr) minmax(0,2fr)",
          gap: 14,
          alignItems: "start",
        }}
      >
        {/* The organizations this account belongs to. Choosing one is the
            context switch every other screen reads, so the list is clickable
            and the current one is marked. */}
        <Panel>
          <PanelHead title="Organizations" count={w.orgs.length} />
          {w.orgs.length === 0 ? (
            <Empty>
              You belong to no organization yet. Create one to hold
              repositories, work and agents.
            </Empty>
          ) : (
            w.orgs.map((o) => {
              const current = o.name === w.org;
              return (
                <button
                  key={o.id}
                  onClick={() => w.setOrg(o.name)}
                  aria-pressed={current}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 10,
                    width: "100%",
                    padding: "10px 14px",
                    border: "none",
                    borderBottom: "1px solid var(--line)",
                    background: current
                      ? "var(--accent-softer)"
                      : "transparent",
                    color: "var(--fg)",
                    font: "13px var(--sans)",
                    cursor: "pointer",
                    textAlign: "left",
                  }}
                >
                  <span
                    aria-hidden="true"
                    style={{
                      width: 26,
                      height: 26,
                      borderRadius: 7,
                      background: "var(--accent-soft)",
                      color: "var(--accent)",
                      display: "grid",
                      placeItems: "center",
                      font: "600 11px var(--sans)",
                      flex: "none",
                    }}
                  >
                    {o.name.slice(0, 1).toUpperCase()}
                  </span>
                  <span style={{ flex: 1, font: "600 13px var(--sans)" }}>
                    {o.name}
                  </span>
                  {current ? (
                    <span
                      style={{
                        font: "600 10px var(--mono)",
                        letterSpacing: ".08em",
                        color: "var(--accent)",
                      }}
                    >
                      CURRENT
                    </span>
                  ) : null}
                </button>
              );
            })
          )}
        </Panel>

        <div style={{ display: "grid", gap: 14 }}>
          <Panel>
            <PanelHead title="Repositories" count={w.repos.length}>
              <Link to="/repos">All repositories</Link>
            </PanelHead>
            {w.repos.length === 0 ? (
              <Empty>No repositories yet.</Empty>
            ) : (
              w.repos.map((r) => (
                <Link
                  key={r.id}
                  to={`/repos/${r.name}`}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 11,
                    padding: "10px 14px",
                    borderBottom: "1px solid var(--line)",
                    color: "var(--fg)",
                  }}
                >
                  <span style={{ flex: 1, font: "13px var(--sans)" }}>
                    {r.name}
                  </span>
                  {r.archived ? (
                    <span
                      style={{
                        font: "600 10px var(--mono)",
                        letterSpacing: ".08em",
                        color: "var(--warn)",
                      }}
                    >
                      ARCHIVED
                    </span>
                  ) : null}
                  <span
                    style={{
                      font: "11px var(--mono)",
                      color: "var(--fg-faint)",
                    }}
                  >
                    {r.default_branch}
                  </span>
                </Link>
              ))
            )}
          </Panel>

          {w.org && isOwner ? (
            <Panel style={{ borderColor: "var(--bad)" }}>
              <PanelHead>
                <span style={{ color: "var(--bad)" }}>DANGER ZONE</span>
              </PanelHead>
              <div style={{ padding: "12px 14px", display: "grid", gap: 10 }}>
                <div style={{ font: "13px var(--sans)" }}>
                  Delete the organization <b>{w.org}</b>. Every repository, Work
                  Item, Engineering Run, CI run and artifact, agent, Agent Run,
                  index entry and secret it holds is removed by the services
                  that hold them. This cannot be undone.
                </div>
                <label
                  style={{
                    font: "12px var(--sans)",
                    color: "var(--fg-dim)",
                    display: "grid",
                    gap: 6,
                  }}
                >
                  Type the organization&apos;s name to confirm
                  <input
                    value={confirmName}
                    onChange={(e) => setConfirmName(e.target.value)}
                    placeholder={w.org}
                    aria-label="Organization name to confirm deletion"
                    style={{
                      font: "13px var(--mono)",
                      padding: "7px 9px",
                      borderRadius: 6,
                      border: "1px solid var(--line)",
                      background: "transparent",
                      color: "inherit",
                    }}
                  />
                </label>
                {deleteOrg.error ? <Failed error={deleteOrg.error} /> : null}
                <div>
                  <button
                    type="button"
                    disabled={confirmName !== w.org || deleteOrg.isPending}
                    onClick={() => deleteOrg.mutate(w.org!)}
                    style={{
                      font: "600 12px var(--sans)",
                      padding: "7px 12px",
                      borderRadius: 6,
                      border: "1px solid var(--bad)",
                      background:
                        confirmName === w.org ? "var(--bad)" : "transparent",
                      color: confirmName === w.org ? "#fff" : "var(--bad)",
                      cursor:
                        confirmName === w.org && !deleteOrg.isPending
                          ? "pointer"
                          : "not-allowed",
                    }}
                  >
                    {deleteOrg.isPending ? "Deleting…" : "Delete organization"}
                  </button>
                </div>
              </div>
            </Panel>
          ) : null}
        </div>
      </div>
    </Page>
  );
}
