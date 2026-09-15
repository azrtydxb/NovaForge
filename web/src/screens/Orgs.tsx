import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Async, Empty, Failed, Page, Panel, PanelHead } from "../components/ui";
import { Dialog, NewButton } from "../components/Dialog";
import type { OrgMember, User } from "../lib/types";

/** Orgs is the design's admin view: the organization's repositories and its
 * members. Agents appear here alongside people because they are members —
 * with an authority that is a capability grant rather than a token. */
export function Orgs() {
  const w = useWorkspace();
  const qc = useQueryClient();
  const [creating, setCreating] = useState<"org" | "repo" | "member" | null>(
    null,
  );

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
  const isOwner =
    members.data?.members.find((m) => m.user_id === me.data?.id)?.role ===
    "owner";
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
          <PanelHead>MEMBERS</PanelHead>
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
