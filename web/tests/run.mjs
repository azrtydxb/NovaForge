import { spawn } from "node:child_process";
import { setTimeout } from "node:timers/promises";

// The supported browser owns its existing TaskSpace. Never silently install
// or launch another browser just to turn a failed browser check green.
const space = Number(process.env.EGO_BROWSER_SPACE);
if (!Number.isSafeInteger(space) || space <= 0) {
  throw new Error(
    "Set EGO_BROWSER_SPACE to the existing ego-browser TaskSpace id.",
  );
}
const port = 5190;
const server = spawn(
  process.execPath,
  [
    "node_modules/vite/bin/vite.js",
    "--host",
    "127.0.0.1",
    "--port",
    String(port),
    "--strictPort",
  ],
  { stdio: "ignore" },
);
try {
  let ready = false;
  for (let n = 0; n < 100; n++) {
    if (server.exitCode !== null)
      throw new Error(
        `Vite failed (${server.exitCode}); port ${port} must be free.`,
      );
    try {
      ready = (await fetch(`http://127.0.0.1:${port}/tests/`)).ok;
    } catch {
      /* wait for the owned server */
    }
    if (ready) break;
    await setTimeout(100);
  }
  if (!ready) throw new Error("Vite did not become ready.");
  const script = `
    const task = await taskSpace(${space});
    const page = task.page("p1");
    await page.goto("http://127.0.0.1:${port}/tests/");
    await page.waitForFunction(() => typeof window.runGUIRegressions === "function");
    const results = await page.evaluate(() => window.runGUIRegressions());
    console.log(JSON.stringify(results, null, 2));
    if (results.some(r => r.status !== "passed")) throw new Error("GUI regressions failed");
  `;
  const browser = spawn("ego-browser", ["nodejs"], {
    stdio: ["pipe", "inherit", "inherit"],
  });
  const completed = new Promise((resolve, reject) => {
    browser.on("error", reject);
    browser.on("exit", (code) => resolve(code ?? 1));
  });
  browser.stdin.end(script);
  process.exitCode = Number(await completed);
} finally {
  server.kill("SIGTERM");
}
