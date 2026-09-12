import { useQuery } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Async, Empty, Page, Panel, PanelHead, Pill } from "../components/ui";
import type { TreeEntry } from "../lib/types";

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
 * The gate configuration is read from the repository itself — .novaforge/gates
 * is under source control precisely so it is reviewable, and reading it here
 * through the same blob API anyone else would use keeps this screen honest
 * about where the truth lives. */
export function Settings() {
  const w = useWorkspace();
  const repo = w.repo ?? w.repos[0]?.name ?? null;

  const approvals = useQuery({
    queryKey: ["approvals"],
    queryFn: () =>
      api.get<{ rules: ApprovalRule[] }>("/api/v1/approvals/policy"),
  });

  const gates = useQuery({
    queryKey: ["gate-config", w.org, repo],
    queryFn: () =>
      api.get<{ entries: TreeEntry[] }>(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo!)}/tree/main/.novaforge/gates`,
      ),
    enabled: w.org !== null && repo !== null,
    retry: false,
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
              <span style={{ color: "var(--fg-faint)" }}>{repo}</span>
            ) : null}
          </PanelHead>
          {repo === null ? (
            <Empty>No repository selected.</Empty>
          ) : gates.isError ? (
            <Empty>
              This repository declares no gates.
              <br />
              Add them under{" "}
              <code style={{ font: "11px var(--mono)" }}>
                .novaforge/gates/
              </code>{" "}
              — they are under source control so a change to them is itself
              reviewable.
            </Empty>
          ) : (
            <Async query={gates}>
              {(d) =>
                d.entries.length === 0 ? (
                  <Empty>No gate files.</Empty>
                ) : (
                  <>
                    {d.entries.map((e) => (
                      <div
                        key={e.name}
                        style={{
                          padding: "10px 14px",
                          borderBottom: "1px solid var(--line)",
                          font: "12px var(--mono)",
                          color: "var(--fg-dim)",
                        }}
                      >
                        {e.name}
                      </div>
                    ))}
                  </>
                )
              }
            </Async>
          )}
        </Panel>
      </div>
    </Page>
  );
}
