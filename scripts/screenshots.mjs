// Headless Chrome screenshots via CDP for visual checks. Usage:
//   node scripts/screenshots.mjs <origin> <sessionCookie> <outDir> <urlSpec>...
// urlSpec = name|path|width|height|appearance(light|dark|-)
import { spawn } from "node:child_process";
import { writeFileSync, rmSync } from "node:fs";
rmSync("/tmp/tmpio-shots-profile", { recursive: true, force: true }); // never reuse cached static assets

const [origin, cookie, outDir, ...specs] = process.argv.slice(2);
const chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const port = 9333;
const proc = spawn(chrome, [`--headless=new`, `--remote-debugging-port=${port}`, `--user-data-dir=/tmp/tmpio-shots-profile`, `--no-first-run`, `--hide-scrollbars`, `about:blank`], { stdio: "ignore" });
await new Promise(r => setTimeout(r, 1500));
const version = await (await fetch(`http://127.0.0.1:${port}/json/version`)).json();
const ws = new WebSocket(version.webSocketDebuggerUrl);
await new Promise(r => ws.onopen = r);
let id = 0; const pending = new Map();
ws.onmessage = e => { const m = JSON.parse(e.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); } };
const send = (method, params = {}, sessionId) => new Promise(res => { const i = ++id; pending.set(i, res); ws.send(JSON.stringify({ id: i, method, params, sessionId })); });
const { result: { targetId } } = await send("Target.createTarget", { url: "about:blank" });
const { result: { sessionId } } = await send("Target.attachToTarget", { targetId, flatten: true });
const s = (m, p) => send(m, p, sessionId);
await s("Page.enable"); await s("Network.enable"); await s("Emulation.setFocusEmulationEnabled", { enabled: true });
const host = new URL(origin).hostname;
if (cookie) await s("Network.setCookie", { name: "tmp_session", value: cookie, domain: host, path: "/", httpOnly: true });
for (const spec of specs) {
  const [name, path, w, h, appearance] = spec.split("|");
  await s("Emulation.setDeviceMetricsOverride", { width: +w, height: +h, deviceScaleFactor: 1, mobile: +w < 700 });
  if (appearance && appearance !== "-") await s("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: appearance }] });
  const loaded = new Promise(r => { const h2 = e => { const m = JSON.parse(e.data); if (m.method === "Page.loadEventFired" && m.sessionId === sessionId) { ws.removeEventListener("message", h2); r(); } }; ws.addEventListener("message", h2); });
  await s("Page.navigate", { url: origin + path });
  await Promise.race([loaded, new Promise(r => setTimeout(r, 4000))]);
  await new Promise(r => setTimeout(r, Number(process.env.SHOT_DELAY || 400)));
  if (appearance === "dark") await s("Runtime.evaluate", { expression: `document.documentElement.setAttribute('data-appearance','dark')` });
  if (process.env.SHOT_JS) { await s("Runtime.evaluate", { expression: process.env.SHOT_JS, awaitPromise: true }); await new Promise(r => setTimeout(r, 300)); }
  const { result } = await s("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
  writeFileSync(`${outDir}/${name}.png`, Buffer.from(result.data, "base64"));
  const { result: { result: { value: title } } } = await s("Runtime.evaluate", { expression: "document.title + ' | h1=' + (document.querySelector('h1')||{}).textContent + ' | scrollW=' + document.documentElement.scrollWidth + ' innerW=' + innerWidth", returnByValue: true });
  console.log(name, "->", title);
}
ws.close(); proc.kill();
