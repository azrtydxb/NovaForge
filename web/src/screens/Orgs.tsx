import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Async, Empty, Failed, Page, Panel, PanelHead } from "../components/ui";
import { Dialog, NewButton } from "../components/Dialog";
import type { OrgMember, Team, User } from "../lib/types";

/** Orgs is the design's admin view: the organization's repositories and its
 * members. Agents appear here alongside people because they are members —
 * with an authority that is a capability grant rather than a token. */
export function Orgs() {
  const w = useWorkspace();
  const qc = useQueryClient();
  const [creating, setCreating] = useState<
    "org" | "repo" | "member" | "team" | null
  >(null);

  const addMember = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post(`/api/v1/orgs/${enc(w.org!)}/members`, {
        username: v.username,
        role: v.role,
      }),
    onSuccess: () => {
      setCreating(null);
      qc.invalidateQueries({ queryKey: ["members"] });
    },
  });

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

  const teams = useQuery({
    queryKey: ["teams", w.org],
    queryFn: () =>
      api.get<{ teams: Team[] }>(`/api/v1/orgs/${enc(w.org!)}/teams`),
    enabled: w.org !== null,
  });
  const createTeam = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post(`/api/v1/orgs/${enc(w.org!)}/teams`, {
        name: v.name,
        role: v.role,
      }),
    onSuccess: () => {
      setCreating(null);
      void qc.invalidateQueries({ queryKey: ["teams"] });
    },
  });
  const addToTeam = useMutation({
    mutationFn: (v: { team: string; user: string }) =>
      api.post(`/api/v1/orgs/${enc(w.org!)}/teams/${enc(v.team)}/members`, {
        user: v.user,
      }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["teams"] }),
  });
  const removeFromTeam = useMutation({
    mutationFn: (v: { team: string; user: string }) =>
      api.del(
        `/api/v1/orgs/${enc(w.org!)}/teams/${enc(v.team)}/members/${enc(v.user)}`,
      ),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["teams"] }),
  });
  const removeTeam = useMutation({
    mutationFn: (id: string) =>
      api.del(`/api/v1/orgs/${enc(w.org!)}/teams/${enc(id)}`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["teams"] }),
  });

  const members = useQuery({
    queryKey: ["members", w.org],
    queryFn: () =>
      api.get<{ members: OrgMember[] }>(`/api/v1/orgs/${enc(w.org!)}/members`),
    enabled: w.org !== null,
  });

  // Deleting the organization is offered only to its owners: identity refuses
  // everyone else, and a button that can only lead to a refusal is noise.
  const me = useQuery({
    queryKey: ["user"],
    queryFn: () => api.get<User>("/api/v1/user"),
  });
  const role = members.data?.members.find(
    (m) => m.user_id === me.data?.id,
  )?.role;
  const isOwner = role === "owner";
  const canManageMembers = isOwner || role === "admin";
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
      title="Org & repositories"
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
      {creating === "member" ? (
        <Dialog
          title="Add a member"
          submitLabel="Add"
          fields={[
            { name: "username", label: "Username", required: true },
            {
              name: "role",
              label: "Role",
              type: "select",
              options: ["member", "admin"],
              required: true,
            },
          ]}
          busy={addMember.isPending}
          error={addMember.error}
          onSubmit={(v) => addMember.mutate(v)}
          onClose={() => setCreating(null)}
        />
      ) : null}
      {creating === "team" ? (
        <Dialog
          title="New team"
          submitLabel="Create"
          fields={[
            {
              name: "name",
              label: "Name",
              required: true,
              placeholder: "reviewers",
            },
            {
              name: "role",
              label: "Role",
              required: true,
              // The team's role, which bounds its members: a team granting
              // "member" grants that much even to an owner.
              options: ["member", "admin", "owner"],
            },
          ]}
          busy={createTeam.isPending}
          error={createTeam.error}
          onSubmit={(v) => createTeam.mutate(v)}
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
          gridTemplateColumns: "minmax(0,1fr) minmax(0,1fr)",
          gap: 14,
        }}
      >
        <Panel>
          <PanelHead>
            REPOSITORIES
            <span style={{ color: "var(--fg-faint)" }}>{w.repos.length}</span>
          </PanelHead>
          {w.repos.length === 0 ? (
            <Empty>No repositories yet.</Empty>
          ) : (
            w.repos.map((r) => (
              <div
                key={r.id}
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 11,
                  padding: "10px 14px",
                  borderBottom: "1px solid var(--line)",
                }}
              >
                <span style={{ flex: 1, font: "13px var(--sans)" }}>
                  {r.name}
                </span>
                <span
                  style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}
                >
                  {r.default_branch}
                </span>
              </div>
            ))
          )}
        </Panel>

        <Panel>
          <PanelHead>
            MEMBERS
            <div style={{ flex: 1 }} />
            {canManageMembers ? (
              <NewButton
                label="Add member"
                onClick={() => {
                  addMember.reset();
                  setCreating("member");
                }}
              />
            ) : null}
          </PanelHead>
          <Async query={members}>
            {(d) =>
              d.members.length === 0 ? (
                <Empty>No members.</Empty>
              ) : (
                <>
                  {d.members.map((m) => (
                    <div
                      key={m.user_id}
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: 11,
                        padding: "10px 14px",
                        borderBottom: "1px solid var(--line)",
                      }}
                    >
                      <span
                        style={{
                          width: 26,
                          height: 26,
                          borderRadius: 99,
                          background: "#2f3542",
                          display: "grid",
                          placeItems: "center",
                          font: "600 10px var(--sans)",
                          flex: "none",
                        }}
                      >
                        {m.username.slice(0, 1).toUpperCase()}
                      </span>
                      <span style={{ flex: 1 }}>
                        <div style={{ font: "13px var(--sans)" }}>
                          {m.username}
                        </div>
                        <div
                          style={{
                            font: "11px var(--mono)",
                            color: "var(--fg-faint)",
                            marginTop: 2,
                          }}
                        >
                          {m.user_id}
                        </div>
                      </span>
                      <span
                        style={{
                          font: "11px var(--mono)",
                          color: "var(--fg-muted)",
                        }}
                      >
                        {m.role}
                      </span>
                    </div>
                  ))}
                </>
              )
            }
          </Async>
        </Panel>
      </div>

      <Panel style={{ marginTop: 14 }}>
        <PanelHead>
          TEAMS
          <span style={{ color: "var(--fg-faint)" }}>
            {teams.data?.teams.length ?? 0}
          </span>
          <div style={{ flex: 1 }} />
          {canManageMembers ? (
            <button
              onClick={() => {
                createTeam.reset();
                setCreating("team");
              }}
              style={{
                padding: "4px 10px",
                borderRadius: 6,
                border: "1px solid var(--line)",
                background: "transparent",
                color: "var(--fg)",
                font: "500 11px var(--sans)",
                cursor: "pointer",
              }}
            >
              New team
            </button>
          ) : null}
        </PanelHead>
        <Async query={teams}>
          {(data) =>
            data.teams.length === 0 ? (
              <Empty>
                No teams yet. A team grants access to a group rather than to
                each person in turn, and its role bounds its members.
              </Empty>
            ) : (
              <>
                {data.teams.map((t) => (
                  <TeamRow
                    key={t.id}
                    team={t}
                    members={members.data?.members ?? []}
                    editable={canManageMembers}
                    onAdd={(user) => addToTeam.mutate({ team: t.id, user })}
                    onRemove={(user) =>
                      removeFromTeam.mutate({ team: t.id, user })
                    }
                    onDelete={() => removeTeam.mutate(t.id)}
                  />
                ))}
                {addToTeam.error ? <Failed error={addToTeam.error} /> : null}
                {removeFromTeam.error ? (
                  <Failed error={removeFromTeam.error} />
                ) : null}
                {removeTeam.error ? <Failed error={removeTeam.error} /> : null}
              </>
            )
          }
        </Async>
      </Panel>

      {w.org && isOwner ? (
        <Panel style={{ marginTop: 14, borderColor: "var(--bad)" }}>
          <PanelHead>
            <span style={{ color: "var(--bad)" }}>DANGER ZONE</span>
          </PanelHead>
          <div style={{ padding: "12px 14px", display: "grid", gap: 10 }}>
            <div style={{ font: "13px var(--sans)" }}>
              Delete the organization <b>{w.org}</b>. Every repository, Work
              Item, Engineering Run, CI run and artifact, agent, Agent Run,
              index entry and secret it holds is removed by the services that
              hold them. This cannot be undone.
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
    </Page>
  );
}

/** One team: its role, who is in it, and the controls to change that. The role is
 * shown next to the name because it is what the team grants, not a label — a
 * reader deciding whether to add someone needs to see how much it gives them. */
function TeamRow({
  team,
  members,
  editable,
  onAdd,
  onRemove,
  onDelete,
}: {
  team: Team;
  members: OrgMember[];
  editable: boolean;
  onAdd: (user: string) => void;
  onRemove: (user: string) => void;
  onDelete: () => void;
}) {
  const [adding, setAdding] = useState("");
  const byID = new Map(members.map((m) => [m.user_id, m.username]));
  // Only organization members can be added: a team is a narrower grant inside the
  // organization, not a way into it, and the server refuses anyone else.
  const candidates = members.filter(
    (m) => !team.member_ids.includes(m.user_id),
  );
  return (
    <div
      style={{ padding: "11px 14px", borderBottom: "1px solid var(--line)" }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
        <span style={{ font: "600 13px var(--sans)" }}>{team.name}</span>
        <span style={{ font: "11px var(--mono)", color: "var(--fg-muted)" }}>
          grants {team.role}
        </span>
        <div style={{ flex: 1 }} />
        {editable ? (
          <>
            <select
              value={adding}
              onChange={(e) => setAdding(e.target.value)}
              style={{
                padding: "4px 8px",
                borderRadius: 6,
                border: "1px solid var(--line)",
                background: "transparent",
                color: "var(--fg)",
                font: "11px var(--sans)",
              }}
            >
              <option value="">
                {candidates.length === 0
                  ? "every member is in this team"
                  : "add a member"}
              </option>
              {candidates.map((m) => (
                <option key={m.user_id} value={m.user_id}>
                  {m.username}
                </option>
              ))}
            </select>
            <button
              disabled={adding === ""}
              onClick={() => {
                onAdd(adding);
                setAdding("");
              }}
              style={teamButton}
            >
              Add
            </button>
            <button onClick={onDelete} style={teamButton}>
              Delete team
            </button>
          </>
        ) : null}
      </div>
      <div
        style={{
          display: "flex",
          gap: 6,
          flexWrap: "wrap",
          marginTop: 8,
        }}
      >
        {team.member_ids.length === 0 ? (
          <span style={{ font: "12px var(--sans)", color: "var(--fg-muted)" }}>
            No members, so this team grants nothing to anyone.
          </span>
        ) : (
          team.member_ids.map((id) => (
            <span
              key={id}
              style={{
                display: "inline-flex",
                alignItems: "center",
                gap: 6,
                padding: "3px 8px",
                borderRadius: 99,
                border: "1px solid var(--line)",
                font: "11px var(--sans)",
              }}
            >
              {byID.get(id) ?? id}
              {editable ? (
                <button
                  onClick={() => onRemove(id)}
                  title="Remove from this team"
                  style={{
                    border: "none",
                    background: "transparent",
                    color: "var(--fg-faint)",
                    cursor: "pointer",
                    font: "12px var(--sans)",
                    padding: 0,
                  }}
                >
                  ×
                </button>
              ) : null}
            </span>
          ))
        )}
      </div>
    </div>
  );
}

const teamButton: React.CSSProperties = {
  padding: "4px 9px",
  borderRadius: 6,
  border: "1px solid var(--line)",
  background: "transparent",
  color: "var(--fg)",
  font: "500 11px var(--sans)",
  cursor: "pointer",
};
