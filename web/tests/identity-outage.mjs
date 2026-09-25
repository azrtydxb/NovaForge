/* global taskSpace, localStorage */
// Operates only the existing opt-in TestGUIBrowserFixture and authorized page.
const fs = await import("node:fs/promises");
const dir = process.env.GUI_BROWSER_FIXTURE_DIR;
const origin = process.env.GUI_BROWSER_ORIGIN;
const space = Number(process.env.EGO_BROWSER_SPACE);
if (!dir || !origin || !space)
  throw new Error("Fixture, origin and existing TaskSpace required");
const fixture = JSON.parse(await fs.readFile(`${dir}/fixture.json`, "utf8"));
const task = await taskSpace(space);
const page = task.page("p1");
await page.goto(origin);
// Remove only this fixture origin's old test credential, never profile cookies.
await page.evaluate(() => localStorage.removeItem("novaforge.token"));
await page.reload();
await page.waitForSelector('button[type="submit"]');
await page.fill('input[type="text"]', fixture.username);
await page.fill('input[type="password"]', fixture.password);
await page.click('button[type="submit"]');
await page.waitForSelector('button:text-is("Sign out")');
for (const [mode, expected] of [
  ["unavailable", 503],
  ["deadline", 504],
]) {
  await fs.writeFile(`${dir}/identity-mode`, mode);
  await page.reload();
  await page.waitForFunction(
    () =>
      document.body.textContent.includes("Request failed") ||
      document.body.textContent.includes("Sign in"),
  );
  const observed = await page.evaluate(async () => {
    const token = localStorage.getItem("novaforge.token");
    const response = await fetch("/api/v1/user", {
      headers: { Authorization: `Bearer ${token}` },
    });
    return {
      retained: !!token,
      status: response.status,
      text: document.body.textContent,
    };
  });
  if (
    !observed.retained ||
    observed.status !== expected ||
    !observed.text.includes(`Request failed (${expected})`)
  ) {
    throw new Error(
      `${mode}: real edge response ${observed.status}, retained=${observed.retained}, recoverable UI=${observed.text.includes("Request failed")}`,
    );
  }
  console.log(
    `PASS ${mode}: real Identity RPC -> edge ${expected}, browser credential retained`,
  );
  await fs.writeFile(`${dir}/identity-mode`, "healthy");
  await page.reload();
  await page.waitForSelector('button:text-is("Sign out")');
}
await page.evaluate(() =>
  localStorage.setItem("novaforge.token", "invalid-fixture-credential"),
);
await page.reload();
await page.waitForSelector('button:text-is("Sign in")');
if (
  !(await page.evaluate(() => localStorage.getItem("novaforge.token") === null))
)
  throw new Error("Invalid credential was retained");
console.log(
  "PASS genuine invalid credential -> real edge 401, browser credential cleared",
);
