import { useState } from "react";
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Async, Empty, Failed, Page, Panel, PanelHead } from "../components/ui";
import { Dialog, NewButton } from "../components/Dialog";
import type {
  OrgMember,
  Repo,
  RepoCollaborator,
  Team,
  User,
} from "../lib/types";

/** People & teams is one screen over the organization's members AND its teams.
 * They belong on one screen because a team is defined entirely by them: it is
 * a named group of members carrying a role, and its role bounds its members —
 * a team granting "member" grants that much even to an owner, because a
 * narrower grant that widened to its strongest member would not be narrower.
 * Repository grants are shown per team as read-only context; they are managed
 * per repository, and the line says so. Membership itself can only be added
 * here: the edge has no endpoint that removes an organization member, so none
 * is offered — an absent affordance is the honest rendering. */
export function OrgPeople() {
  const w = useWorkspace();
  const qc = useQueryClient();
  const [creating, setCreating] = useState<"member" | "team" | null>(null);

  const members = useQuery({
    queryKey: ["members", w.org],
    queryFn: () =>
      api.get<{ members: OrgMember[] }>(`/api/v1/orgs/${enc(w.org!)}/members`),
    enabled: w.org !== null,
  });

  const teams = useQuery({
    queryKey: ["teams", w.org],
    queryFn: () =>
      api.get<{ teams: Team[] }>(`/api/v1/orgs/${enc(w.org!)}/teams`),
    enabled: w.org !== null,
  });

  // Only organization members can be added to a team: a team is a narrower
  // grant inside the organization, not a way into it, and the server refuses
  // anyone else. The viewer's own membership decides what this screen offers
  // to change — demanding a grant of a human member is a bug this codebase
  // already had once.
  const me = useQuery({
    queryKey: ["user"],
    queryFn: () => api.get<User>("/api/v1/user"),
  });
  const myRole = members.data?.members.find(
    (m) => m.user_id === me.data?.id,
  )?.role;
  const canManage = myRole === "owner" || myRole === "admin";

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

  // Repository grants are fetched per repository and reassembled per team
  // here: the platform holds a grant on (repo, user-or-team), so "what does
  // this team reach" is a join this screen performs rather than an endpoint
  // it could call. Read-only on purpose — grants are managed from the
  // repository's collaborators panel, where the grant's own repository is on
  // screen.
  const repos = w.repos;
  const grants = useQueries({
    queries: repos.map((r: Repo) => ({
      queryKey: ["collaborators", w.org, r.name],
      queryFn: () =>
        api.get<{ collaborators: RepoCollaborator[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(r.name)}/collaborators`,
        ),
      enabled: w.org !== null,
    })),
  });
  const teamGrants = new Map<string, string[]>();
  grants.forEach((g, i) => {
    const repo = repos[i];
    if (!repo) return;
    for (const c of g.data?.collaborators ?? []) {
      if (c.team_id === "") continue;
      teamGrants.set(c.team_id, [
        ...(teamGrants.get(c.team_id) ?? []),
        repo.name,
      ]);
    }
  });

  return (
    <Page
      title="People & teams"
      subtitle={w.org ?? ""}
      actions={
        canManage ? (
          <div style={{ display: "flex", gap: 8 }}>
            <NewButton
              label="Add member"
              onClick={() => {
                addMember.reset();
                setCreating("member");
              }}
            />
            <NewButton
              label="New team"
              onClick={() => {
                createTeam.reset();
                setCreating("team");
              }}
            />
          </div>
        ) : null
      }
    >
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
              // The team's role, which bounds its members: shown next to the
              // name below for the same reason it is asked for here.
              options: ["member", "admin", "owner"],
            },
          ]}
          busy={createTeam.isPending}
          error={createTeam.error}
          onSubmit={(v) => createTeam.mutate(v)}
          onClose={() => setCreating(null)}
        />
      ) : null}

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "minmax(0,2fr) minmax(0,3fr)",
          gap: 14,
          alignItems: "start",
        }}
      >
        <Panel>
          <PanelHead
            title="Members"
            count={members.data?.members.length ?? 0}
          />
          <Async query={members}>
            {(d) =>
              d.members.length === 0 ? (
                <Empty>No members.</Empty>
              ) : (
                d.members.map((m) => (
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
                    <Avatar name={m.username} />
                    <span style={{ flex: 1, minWidth: 0 }}>
                      <div style={{ font: "13px var(--sans)" }}>
                        {m.username}
                        {m.user_id === me.data?.id ? (
                          <span
                            style={{
                              font: "11px var(--sans)",
                              color: "var(--fg-faint)",
                            }}
                          >
                            {" "}
                            (you)
                          </span>
                        ) : null}
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
                ))
              )
            }
          </Async>
          {me.error ? <Failed error={me.error} /> : null}
        </Panel>

        <Panel>
          <PanelHead title="Teams" count={teams.data?.teams.length ?? 0} />
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
                      grants={teamGrants.get(t.id) ?? []}
                      editable={canManage}
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
                  {removeTeam.error ? (
                    <Failed error={removeTeam.error} />
                  ) : null}
                </>
              )
            }
          </Async>
        </Panel>
      </div>
    </Page>
  );
}

/** One team: its role, who is in it, the repositories it reaches, and the
 * controls to change the membership. The role is shown next to the name
 * because it is what the team grants, not a label — a reader deciding whether
 * to add someone needs to see how much it gives them. */
function TeamRow({
  team,
  members,
  grants,
  editable,
  onAdd,
  onRemove,
  onDelete,
}: {
  team: Team;
  members: OrgMember[];
  grants: string[];
  editable: boolean;
  onAdd: (user: string) => void;
  onRemove: (user: string) => void;
  onDelete: () => void;
}) {
  const [adding, setAdding] = useState("");
  const byID = new Map(members.map((m) => [m.user_id, m.username]));
  // Only organization members can be added: a team is a narrower grant inside
  // the organization, not a way into it, and the server refuses anyone else.
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
              aria-label={`Add a member to ${team.name}`}
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
      <div
        style={{
          font: "11px var(--sans)",
          color: "var(--fg-faint)",
          marginTop: 8,
        }}
      >
        {grants.length > 0 ? (
          <>
            Reaches {grants.length === 1 ? "repository" : "repositories"}:{" "}
            {grants.map((n, i) => (
              <span key={n}>
                {i > 0 ? ", " : ""}
                <span
                  style={{ font: "11px var(--mono)", color: "var(--fg-muted)" }}
                >
                  {n}
                </span>
              </span>
            ))}
            <span> · grants are managed on each repository</span>
          </>
        ) : (
          "No repository grants; members reach nothing through this team yet."
        )}
      </div>
    </div>
  );
}

/** Avatar is the initial tile every person here is drawn with. Tokens, not a
 * hard-coded dark, so it holds in the light theme the shell can switch to. */
function Avatar({ name }: { name: string }) {
  return (
    <span
      aria-hidden="true"
      style={{
        width: 26,
        height: 26,
        borderRadius: 99,
        background: "var(--accent-soft)",
        color: "var(--accent)",
        display: "grid",
        placeItems: "center",
        font: "600 10px var(--sans)",
        flex: "none",
      }}
    >
      {name.slice(0, 1).toUpperCase()}
    </span>
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
