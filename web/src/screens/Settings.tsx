import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Async, Empty, Page, Panel, PanelHead, Pill } from "../components/ui";
import { Dialog } from "../components/Dialog";
import type { GateConfig, GateProposal } from "../lib/types";

interface ApprovalRule {
  action: string;
  policy: string;
}

const POLICY_TONE: Record<string, [string, string]> = {
  automatic: ["var(--ok-bg)", "var(--ok)"],
  policy: ["var(--warn-bg)", "var(--warn)"],
  architecture: ["var(--warn-bg)", "var(--warn)"],
  explicit: ["var(--warn-bg)", "var(--warn)"],
  human: ["var(--bad-bg)", "var(--bad)"],
  forbidden: ["var(--bad-bg)", "var(--bad)"],
};

/** Settings shows what governs agents here: the approval policy the platform
 * enforces, and the gates a repository declares.
 *
 * The gate configuration is read from the repository's default branch —
 * .novaforge/gates is under source control precisely so it is reviewable. A
 * toggle here therefore never writes that branch: it opens an Engineering Run
 * carrying the one-file change, and the gate stays as it is until that run is
 * reviewed and merged. Flipping the switch back on screen immediately would
 * show a policy that is not in force, so the switch keeps showing what the
 * default branch says. */
export function Settings() {
  const w = useWorkspace();
  const navigate = useNavigate();
  const repo = w.repo;
  const [pending, setPending] = useState<GateConfig | null>(null);

  const approvals = useQuery({
    queryKey: ["approvals"],
    queryFn: () =>
      api.get<{ rules: ApprovalRule[] }>("/api/v1/approvals/policy"),
  });

  const gates = useQuery({
    queryKey: ["gate-config", w.org, repo],
    queryFn: () =>
      api.get<{ ref: string; commit_sha: string; gates: GateConfig[] }>(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo!)}/gates`,
      ),
    enabled: w.org !== null && repo !== null,
    retry: false,
  });

  const propose = useMutation({
    mutationFn: (g: GateConfig) =>
      api.post<GateProposal>(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo!)}/gates/${enc(g.name)}/proposals`,
        { enabled: !g.enabled },
      ),
  });

  return (
    <Page
      title="Settings"
      subtitle="What agents may do, and what a change must pass"
    >
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "minmax(0,1fr) minmax(0,1fr)",
          gap: 14,
        }}
      >
        <Panel>
          <PanelHead>APPROVAL POLICY</PanelHead>
          <Async query={approvals}>
            {(d) => (
              <>
                {d.rules.map((r) => {
                  const [bg, fg] = POLICY_TONE[
                    r.policy.split(" ")[0] ?? ""
                  ] ?? ["rgba(255,255,255,.07)", "var(--fg-muted)"];
                  return (
                    <div
                      key={r.action}
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: 11,
                        padding: "10px 14px",
                        borderBottom: "1px solid var(--line)",
                      }}
                    >
                      <span style={{ flex: 1, font: "13px var(--sans)" }}>
                        {r.action}
                      </span>
                      <Pill bg={bg} fg={fg}>
                        {r.policy}
                      </Pill>
                    </div>
                  );
                })}
              </>
            )}
          </Async>
        </Panel>

        <Panel>
          <PanelHead>
            GATES
            {repo ? (
              <span style={{ color: "var(--fg-faint)" }}>
                {repo}
                {gates.data ? ` @ ${gates.data.ref}` : ""}
              </span>
            ) : null}
          </PanelHead>
          {repo === null ? (
            <Empty>No repository selected.</Empty>
          ) : (
            <Async query={gates}>
              {(d) => (
                <>
                  {d.gates.map((g) => (
                    <div
                      key={g.name}
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: 11,
                        padding: "10px 14px",
                        borderBottom: "1px solid var(--line)",
                      }}
                    >
                      <span style={{ flex: 1, minWidth: 0 }}>
                        <div style={{ font: "12px var(--mono)" }}>{g.name}</div>
                        <div
                          style={{
                            font: "10px var(--mono)",
                            color: "var(--fg-faint)",
                            marginTop: 3,
                          }}
                        >
                          {g.declared
                            ? `${g.path}${
                                Object.keys(g.params).length > 0
                                  ? ` · ${JSON.stringify(g.params)}`
                                  : ""
                              }`
                            : "not declared"}
                        </div>
                      </span>
                      <span
                        style={{
                          font: "11px var(--sans)",
                          color: g.enabled ? "var(--ok)" : "var(--fg-muted)",
                        }}
                      >
                        {g.enabled
                          ? "blocks merge"
                          : g.declared
                            ? "advisory"
                            : "off"}
                      </span>
                      <Toggle
                        on={g.enabled}
                        label={`${g.enabled ? "Disable" : "Enable"} the ${g.name} gate`}
                        onClick={() => {
                          propose.reset();
                          setPending(g);
                        }}
                      />
                    </div>
                  ))}
                </>
              )}
            </Async>
          )}
        </Panel>
      </div>

      {pending ? (
        <Dialog
          title={`${pending.enabled ? "Disable" : "Enable"} the ${pending.name} gate?`}
          description={
            <>
              This does not change the gate now. NovaForge will commit the
              change to{" "}
              <code style={{ font: "11px var(--mono)" }}>
                {pending.declared
                  ? pending.path
                  : `.novaforge/gates/${pending.name}.yaml`}
              </code>{" "}
              on a new branch and open an Engineering Run against{" "}
              {gates.data?.ref ?? "the default branch"}. The gate stays{" "}
              {pending.enabled ? "on" : "off"} until that run is reviewed and
              merged — and the run itself is judged by the gates as they are
              today.
            </>
          }
          submitLabel="Open a run for review"
          fields={[]}
          busy={propose.isPending}
          error={propose.error}
          onSubmit={() =>
            propose.mutate(pending, {
              onSuccess: (p) => {
                setPending(null);
                navigate(`/runs/${enc(repo!)}/${p.run_number}`);
              },
            })
          }
          onClose={() => setPending(null)}
        />
      ) : null}
    </Page>
  );
}

/** Toggle is a switch that asks rather than acts: pressing it opens the
 * confirmation, and its position only ever reflects the default branch. */
function Toggle({
  on,
  label,
  onClick,
}: {
  on: boolean;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      role="switch"
      aria-checked={on}
      aria-label={label}
      title={label}
      onClick={onClick}
      style={{
        width: 34,
        height: 19,
        flex: "none",
        padding: 2,
        border: "none",
        borderRadius: 10,
        background: on ? "var(--accent)" : "var(--line-3)",
        cursor: "pointer",
        display: "flex",
        justifyContent: on ? "flex-end" : "flex-start",
      }}
    >
      <span
        style={{
          width: 15,
          height: 15,
          borderRadius: "50%",
          background: "#fff",
        }}
      />
    </button>
  );
}
