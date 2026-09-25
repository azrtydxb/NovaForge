// Node transport-parser regression, not browser/UI acceptance.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import ts from "typescript";

const source = await readFile(
  new URL("../src/lib/api.ts", import.meta.url),
  "utf8",
);
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    target: ts.ScriptTarget.ES2022,
    module: ts.ModuleKind.ESNext,
  },
});
const { api, setStoredToken } = await import(
  `data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`
);
setStoredToken("transport-test-token");
const oldFetch = globalThis.fetch;
try {
  let request;
  globalThis.fetch = async (path, options) => {
    request = { path, options };
    return new globalThis.Response(
      "event: connected\ndata: {}\n\n" +
        'id: run:2\nevent: message\ndata: {"tool_call":{},"tool_call_id":"call"}\n\n' +
        'event: message\ndata: {"state_change":{}}\n\n' +
        "event: heartbeat\ndata: {}\n\n",
      { headers: { "Content-Type": "text/event-stream" } },
    );
  };
  let cursor = "run:1";
  const seen = [];
  await api.events(
    "/events",
    new globalThis.AbortController().signal,
    (event, data, id) => {
      if (id) cursor = id;
      seen.push([event, data, id]);
    },
    cursor,
  );
  assert.equal(
    request.options.headers.Authorization,
    "Bearer transport-test-token",
  );
  assert.equal(request.options.headers["Last-Event-ID"], "run:1");
  assert.equal(seen[1][2], "run:2");
  assert.equal(seen[2][2], undefined);
  assert.equal(
    cursor,
    "run:2",
    "state/heartbeat must not reset durable cursor",
  );
  console.log("api.events cursor forwarding and non-resetting parser passed");
} finally {
  globalThis.fetch = oldFetch;
}
