import { Link } from "react-router-dom";
import { Page, Panel, PanelHead } from "../components/ui";

/** Runners is the design's "Runner pools" screen. This deployment does not
 * expose runner pools or capacity on any endpoint — api/openapi.yaml carries
 * no such route — so the screen renders its structure and says so, per the
 * app's convention that a GUI showing a plausible number for something the
 * platform does not know is worse than one that admits the gap.
 *
 * Even derivation from CI data is closed off: a CI job's JSON carries id,
 * name, status, detail, agent_role, agent_run_id and work_item_key — no
 * runner or pool field — so "which runner executed this job" cannot be
 * derived either. The storage layer tracks a runner_id per job, but the REST
 * edge does not publish it, and a client deriving capacity from what it
 * cannot see would be inventing it. */
export function Runners() {
  return (
    <Page title="Runner pools" subtitle="The cluster's runner capacity">
      <div style={{ display: "grid", gap: 14, maxWidth: 720 }}>
        <Panel>
          <PanelHead title="RUNNER POOLS" />
          <div
            role="status"
            style={{
              padding: "20px 16px",
              border: "1px solid var(--line-2)",
              borderRadius: 10,
              margin: 14,
              font: "13px var(--sans)",
              color: "var(--fg-muted)",
              lineHeight: 1.6,
            }}
          >
            <div style={{ font: "600 12px var(--sans)", marginBottom: 4 }}>
              Not available in this deployment
            </div>
            <div style={{ font: "12px var(--mono)", opacity: 0.9 }}>
              No endpoint publishes runner pools or capacity, so this screen has
              nothing to show.
            </div>
          </div>
        </Panel>

        <Panel>
          <PanelHead title="WHERE JOBS RUN" />
          <div
            style={{
              padding: 14,
              font: "13px/1.7 var(--sans)",
              color: "var(--fg-dim)",
            }}
          >
            <p style={{ margin: "0 0 10px" }}>
              Every CI job runs in its own Kubernetes pod in the platform's
              cluster. Which runner executed a given job is not published on the
              API, so per-pool usage cannot be shown here.
            </p>
            <p style={{ margin: 0 }}>
              Jobs declaring an{" "}
              <code style={{ font: "12px var(--mono)" }}>agent</code> role do
              not run on a runner at all — they execute as Agent Runs, and their
              execution is visible on the <Link to="/agents">Agents</Link>{" "}
              screen.
            </p>
          </div>
        </Panel>
        <div style={{ font: "11px var(--sans)", color: "var(--fg-faint)" }}>
          CI execution itself is on the <Link to="/ci">CI runs</Link> screen.
        </div>
      </div>
    </Page>
  );
}
