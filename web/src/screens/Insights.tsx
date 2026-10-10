import { Page, Panel, PanelHead } from "../components/ui";

/** Insights is the redesign's analytics surface, drawn from its artboard.
 *
 * The platform has no analytics endpoint — nothing behind this screen has
 * ever answered a request, so there is no number to show and none is
 * invented. A chart of plausible-looking delivery metrics would be read as
 * data and acted on; CLAUDE.md's rule is that a GUI showing a plausible
 * number for something the platform does not know is worse than one that
 * admits the gap. The screen renders its intended structure and each section
 * says what is missing, so the surface exists and its emptiness is
 * load-bearing: it names what a deployment would have to add. */
export function Insights() {
  return (
    <Page
      title="Insights"
      subtitle="How this organization builds — delivery, work, agents and quality, over time"
    >
      <div
        role="status"
        style={{
          padding: "12px 16px",
          border: "1px solid var(--line-2)",
          borderRadius: 10,
          font: "13px/1.6 var(--sans)",
          color: "var(--fg-muted)",
          marginBottom: 14,
        }}
        data-avail="none"
        data-avail-reason="the platform exposes no analytics endpoint, so this surface has no data source"
      >
        Not available in this deployment — the platform exposes no analytics
        endpoint, so there is nothing here to report. Every other number in this
        application is the platform's own; these sections name what an analytics
        backend would have to serve.
      </div>
      <div style={{ display: "grid", gap: 14 }}>
        <Panel>
          <PanelHead>DELIVERY</PanelHead>
          <SectionEmpty what="run outcomes, merge rate and lead time per repository" />
        </Panel>
        <Panel>
          <PanelHead>WORK</PanelHead>
          <SectionEmpty what="work-item throughput and cycle time across the board" />
        </Panel>
        <Panel>
          <PanelHead>AGENTS</PanelHead>
          <SectionEmpty what="agent runs, token spend against budget and success rate" />
        </Panel>
        <Panel>
          <PanelHead>QUALITY</PanelHead>
          <SectionEmpty what="gate failure rate, flaky tests and coverage trend" />
        </Panel>
      </div>
    </Page>
  );
}

/** SectionEmpty is a section's honest body: the section keeps its place in
 * the structure without borrowing a number from nowhere. */
function SectionEmpty({ what }: { what: string }) {
  return (
    <div
      style={{
        padding: "18px 16px",
        font: "13px var(--sans)",
        color: "var(--fg-faint)",
        lineHeight: 1.6,
      }}
    >
      No analytics backend serves {what} in this deployment.
    </div>
  );
}
