import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Async, Empty, Page, Panel, PanelHead } from "../components/ui";
import type { Dashboard } from "../lib/types";

/** STATE_TONE colours an exception by the state it is in. */
const STATE_TONE: Record<string, string> = {
  blocked: "var(--bad)",
  review: "var(--warn)",
  planning: "var(--fg-muted)",
  open: "var(--link)",
  in_progress: "var(--ok)",
};

/** Exceptions is the design's "what needs a person" list.
 *
 * The list is the platform's own — the dashboard returns it alongside the
 * counts — rather than one this client derives. Deriving a second list would
 * be a second opinion about what is wrong, and the two would eventually
 * disagree with the number on the rail. */
export function Exceptions() {
  const w = useWorkspace();

  const dash = useQuery({
    queryKey: ["dashboard", w.org],
    queryFn: () => api.get<Dashboard>(`/api/v1/orgs/${enc(w.org!)}/dashboard`),
    enabled: w.org !== null,
  });

  return (
    <Page
      title="Exceptions"
      subtitle="The platform runs itself until something needs a decision only a person can make"
    >
      <Async query={dash}>
        {(d) => (
          <>
            <div
              style={{
                display: "grid",
                gridTemplateColumns: "repeat(auto-fit,minmax(150px,1fr))",
                gap: 10,
                marginBottom: 16,
              }}
            >
              <Count
                label="Agents blocked"
                value={d.agents_blocked}
                tone="var(--bad)"
              />
              <Count
                label="Gate failures"
                value={d.gate_failures}
                tone="var(--bad)"
              />
              <Count
                label="Need human review"
                value={d.need_human_review}
                tone="var(--warn)"
              />
              <Count
                label="Architecture decisions"
                value={d.architecture_decisions}
                tone="var(--warn)"
              />
              <Count
                label="Ready to auto-merge"
                value={d.ready_to_auto_merge}
                tone="var(--link)"
              />
            </div>

            <Panel>
              <PanelHead>
                NEEDS A PERSON
                <span style={{ color: "var(--fg-faint)" }}>
                  {d.exceptions?.length ?? 0}
                </span>
              </PanelHead>
              {!d.exceptions || d.exceptions.length === 0 ? (
                <Empty>
                  Nothing needs you right now.
                  <br />
                  Blocked work, failing gates and decisions only a person can
                  make appear here.
                </Empty>
              ) : (
                d.exceptions.map((e) => (
                  <div
                    key={e.key}
                    style={{
                      display: "flex",
                      alignItems: "center",
                      gap: 12,
                      padding: "12px 14px",
                      borderBottom: "1px solid var(--line)",
                    }}
                  >
                    <span
                      style={{
                        width: 8,
                        height: 8,
                        borderRadius: 99,
                        flex: "none",
                        background: STATE_TONE[e.state] ?? "var(--fg-muted)",
                      }}
                    />
                    <span
                      style={{
                        width: 70,
                        font: "600 12px var(--mono)",
                        color: "var(--link)",
                      }}
                    >
                      {e.key}
                    </span>
                    <span style={{ flex: 1 }}>
                      <div style={{ font: "13px var(--sans)" }}>{e.title}</div>
                      <div
                        style={{
                          font: "11px var(--mono)",
                          color: "var(--fg-muted)",
                          marginTop: 3,
                        }}
                      >
                        {e.reason}
                      </div>
                    </span>
                    <Link
                      to="/work"
                      style={{
                        padding: "5px 11px",
                        border: "1px solid var(--line-2)",
                        borderRadius: 7,
                        font: "12px var(--sans)",
                      }}
                    >
                      Open work
                    </Link>
                  </div>
                ))
              )}
            </Panel>
          </>
        )}
      </Async>
    </Page>
  );
}

function Count({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone: string;
}) {
  return (
    <Panel style={{ padding: "13px 14px" }}>
      <div
        style={{
          font: "600 22px var(--sans)",
          color: value > 0 ? tone : "var(--fg-muted)",
        }}
      >
        {value}
      </div>
      <div
        style={{
          font: "11px var(--sans)",
          color: "var(--fg-muted)",
          marginTop: 3,
        }}
      >
        {label}
      </div>
    </Panel>
  );
}
