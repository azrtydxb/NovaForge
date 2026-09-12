import { Component, type ErrorInfo, type ReactNode } from "react";

/** ErrorBoundary contains a screen's crash to that screen.
 *
 * Without it a single undefined field takes the whole application to a blank
 * page with nothing but a console message — which is what happened the first
 * time this was driven against the real API, where the members list returned
 * "username" and this client read "name". A blank page tells a user nothing
 * and tells an operator nothing either. */
export class ErrorBoundary extends Component<
  { children: ReactNode; where: string },
  { error: Error | null }
> {
  state: { error: Error | null } = { error: null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Kept on the console as well as on screen: the stack is what a developer
    // needs and the message is what the person looking at it needs.
    console.error(`NovaForge: ${this.props.where} failed`, error, info);
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div style={{ padding: "22px 26px" }}>
        <div
          style={{
            border: "1px solid #e5534b44",
            background: "rgba(229,83,75,.06)",
            borderRadius: 10,
            padding: 18,
            maxWidth: 620,
          }}
        >
          <div
            style={{
              font: "600 13px var(--sans)",
              color: "var(--bad)",
              marginBottom: 6,
            }}
          >
            This screen failed to render
          </div>
          <div
            style={{
              font: "12px/1.6 var(--mono)",
              color: "var(--fg-dim)",
              wordBreak: "break-word",
            }}
          >
            {this.state.error.message}
          </div>
          <button
            onClick={() => this.setState({ error: null })}
            style={{
              marginTop: 14,
              padding: "6px 12px",
              border: "1px solid var(--line-2)",
              borderRadius: 7,
              background: "transparent",
              color: "var(--fg-muted)",
              font: "12px var(--sans)",
              cursor: "pointer",
            }}
          >
            Try again
          </button>
        </div>
      </div>
    );
  }
}
