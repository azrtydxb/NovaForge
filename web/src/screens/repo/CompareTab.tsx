import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useSearchParams } from "react-router-dom";
import { api, enc } from "../../lib/api";
import {
  Async,
  Empty,
  Failed,
  Page,
  Panel,
  PanelHead,
} from "../../components/ui";
import {
  btnSmall,
  NotAvailableInline,
  selectStyle,
  useRefs,
  useRepoScope,
} from "./shared";

/** One file's hunk inside the unified diff, split at the "+++"/"---" file
 * headers git prints. Everything here is derived from the diff text the
 * platform served — no per-file endpoint exists, so none is pretended. */
interface DiffFile {
  path: string;
  lines: string[];
}

function splitDiff(unified: string): DiffFile[] {
  const files: DiffFile[] = [];
  let current: DiffFile | null = null;
  for (const line of unified.split("\n")) {
    if (line.startsWith("--- ") || line.startsWith("+++ ")) continue;
    if (line.startsWith("diff --git ") || line.startsWith("Index: ")) {
      current = { path: line, lines: [] };
      files.push(current);
      continue;
    }
    if (current === null) continue;
    current.lines.push(line);
  }
  // git names files in the "diff --git a/x b/x" line; the plain path is
  // what a reader recognises, so take the b/ side when it is there.
  for (const f of files) {
    const m = /^diff --git a\/(.*) b\/(.*)$/.exec(f.path);
    if (m) f.path = m[2] ?? m[1] ?? f.path;
  }
  return files;
}

function stats(unified: string): { files: number; add: number; del: number } {
  let add = 0;
  let del = 0;
  for (const line of unified.split("\n")) {
    if (line.startsWith("+++ ") || line.startsWith("--- ")) continue;
    if (line.startsWith("+")) add++;
    else if (line.startsWith("-")) del++;
  }
  return { files: splitDiff(unified).length, add, del };
}

export function CompareTab() {
  const { org, repo } = useRepoScope();
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}`;
  const [params, setParams] = useSearchParams();
  const { branches, tags } = useRefs(org, repo);
  const names = Array.from(
    new Set(
      [...(branches.data?.refs ?? []), ...(tags.data?.refs ?? [])].map(
        (r) => r.name,
      ),
    ),
  );

  const baseRef = params.get("base") ?? names[0] ?? "main";
  const headRef = params.get("head") ?? names[0] ?? "main";
  const mergeBase = params.get("merge_base") === "true";

  const setParam = (patch: Record<string, string | null>) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v === null) next.delete(k);
      else next.set(k, v);
    }
    setParams(next);
  };

  const diff = useQuery({
    queryKey: ["diff", base, baseRef, headRef, mergeBase],
    queryFn: () =>
      api.get<{ unified: string }>(
        `${base}/diff?from=${enc(baseRef)}&to=${enc(headRef)}${mergeBase ? "&merge_base=true" : ""}`,
      ),
  });

  const [openFiles, setOpenFiles] = useState<Set<string>>(new Set());
  const toggle = (p: string) => {
    const next = new Set(openFiles);
    if (next.has(p)) next.delete(p);
    else next.add(p);
    setOpenFiles(next);
  };

  const picker = (
    which: "base" | "head",
    value: string,
    onChange: (v: string) => void,
  ) => (
    <select
      aria-label={which === "base" ? "Compare base" : "Compare head"}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      style={{ ...selectStyle, maxWidth: 220 }}
    >
      {names.includes(value) ? null : <option>{value}</option>}
      {names.map((n) => (
        <option key={n} value={n}>
          {n}
        </option>
      ))}
    </select>
  );

  return (
    <Page
      title="Compare changes"
      subtitle="Pick two refs to see what changed between them."
    >
      <Panel>
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 8,
            flexWrap: "wrap",
            padding: "10px 14px",
            borderBottom: "1px solid var(--line)",
          }}
        >
          <span
            style={{ font: "600 11px var(--sans)", color: "var(--fg-muted)" }}
          >
            Base
          </span>
          {picker("base", baseRef, (v) => setParam({ base: v }))}
          <button
            style={btnSmall}
            title="Swap base and compare"
            onClick={() => setParam({ base: headRef, head: baseRef })}
          >
            swap
          </button>
          <span
            style={{ font: "600 11px var(--sans)", color: "var(--fg-muted)" }}
          >
            Compare
          </span>
          {picker("head", headRef, (v) => setParam({ head: v }))}
          <label
            style={{
              display: "flex",
              alignItems: "center",
              gap: 5,
              font: "11px var(--sans)",
              color: "var(--fg-muted)",
            }}
          >
            <input
              type="checkbox"
              checked={mergeBase}
              onChange={(e) =>
                setParam({ merge_base: e.target.checked ? "true" : null })
              }
            />
            from merge base
          </label>
        </div>

        {branches.error || tags.error ? (
          <Failed error={branches.error || tags.error} />
        ) : baseRef === headRef ? (
          <Empty>Pick two different refs to compare.</Empty>
        ) : (
          <Async query={diff}>
            {(d) => {
              const s = stats(d.unified);
              if (d.unified === "") {
                return (
                  <Empty>
                    No difference between {baseRef} and {headRef}.
                  </Empty>
                );
              }
              return (
                <div>
                  <div
                    style={{
                      display: "flex",
                      alignItems: "center",
                      gap: 14,
                      flexWrap: "wrap",
                      padding: "9px 14px",
                      borderBottom: "1px solid var(--line)",
                      font: "12px var(--sans)",
                    }}
                  >
                    <span>
                      <strong>{s.files}</strong> files changed
                    </span>
                    <span style={{ color: "var(--ok)" }}>+{s.add}</span>
                    <span style={{ color: "var(--bad)" }}>−{s.del}</span>
                  </div>
                  {splitDiff(d.unified).map((f) => {
                    const fa = f.lines.filter((l) => l.startsWith("+")).length;
                    const fd = f.lines.filter((l) => l.startsWith("-")).length;
                    const open = openFiles.has(f.path);
                    return (
                      <div key={f.path}>
                        <button
                          onClick={() => toggle(f.path)}
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: 10,
                            width: "100%",
                            textAlign: "left",
                            padding: "7px 14px",
                            background: "var(--panel-2)",
                            border: "none",
                            borderBottom: "1px solid var(--line)",
                            font: "600 11px var(--mono)",
                            color: "var(--fg-dim)",
                            cursor: "pointer",
                            boxSizing: "border-box",
                          }}
                        >
                          <span style={{ width: 12 }}>{open ? "▾" : "▸"}</span>
                          <span style={{ flex: 1, wordBreak: "break-all" }}>
                            {f.path}
                          </span>
                          <span style={{ color: "var(--ok)" }}>+{fa}</span>
                          <span style={{ color: "var(--bad)" }}>−{fd}</span>
                        </button>
                        {open ? (
                          <pre
                            style={{
                              margin: 0,
                              padding: "8px 14px",
                              overflow: "auto",
                              font: "11px/1.6 var(--mono)",
                              borderBottom: "1px solid var(--line)",
                            }}
                          >
                            {f.lines.map((line, i) => (
                              <div key={i} style={{ color: diffColor(line) }}>
                                {line || " "}
                              </div>
                            ))}
                          </pre>
                        ) : null}
                      </div>
                    );
                  })}
                </div>
              );
            }}
          </Async>
        )}
      </Panel>

      <Panel style={{ marginTop: 14 }}>
        <PanelHead title="WHAT THIS VIEW CANNOT SHOW" />
        <div style={{ padding: "12px 14px", display: "grid", gap: 10 }}>
          <NotAvailableInline what="Mergeability and conflicts between the two refs" />
          <NotAvailableInline what="Predicted impact from the engineering graph" />
        </div>
      </Panel>
    </Page>
  );
}

function diffColor(line: string): string {
  if (line.startsWith("+++") || line.startsWith("---"))
    return "var(--fg-muted)";
  if (line.startsWith("@@")) return "var(--info)";
  if (line.startsWith("+")) return "var(--ok)";
  if (line.startsWith("-")) return "var(--bad)";
  return "var(--fg-dim)";
}
