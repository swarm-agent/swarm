// Starts Google Chrome with a profile folder and loads the unpacked Swarm
// extension over the DevTools pipe (branded Chrome 137+ ignores
// --load-extension; Extensions.loadUnpacked is the supported way), then keeps
// running until killed. Prints the extension id.
//   node scripts/chrome-with-extension.mjs PROFILE_DIR extension/ [URL...]
// For the native messaging host to be found, PROFILE_DIR/NativeMessagingHosts
// must hold the manifest Settings → Browser → connect Chrome writes (set
// SWARM_CONSOLE_CHROME_PROFILE=PROFILE_DIR before pressing it).
import { spawn } from "node:child_process";
const [profile, ext, ...urls] = process.argv.slice(2);
const chrome = spawn("google-chrome", [
  `--user-data-dir=${profile}`, "--remote-debugging-pipe", "--enable-unsafe-extension-debugging",
  "--no-first-run", "--no-default-browser-check", "--disable-gpu", "--window-size=1000,700", "--window-position=0,0",
  ...(urls.length ? urls : ["about:blank"]),
], { stdio: ["ignore", "inherit", "inherit", "pipe", "pipe"] });
const out = chrome.stdio[3], inp = chrome.stdio[4];
let buf = "", id = 0;
const waiting = new Map();
inp.on("data", (d) => {
  buf += d.toString();
  let i;
  while ((i = buf.indexOf("\0")) >= 0) {
    const msg = JSON.parse(buf.slice(0, i)); buf = buf.slice(i + 1);
    if (msg.id && waiting.has(msg.id)) { waiting.get(msg.id)(msg); waiting.delete(msg.id); }
  }
});
function send(method, params = {}) {
  return new Promise((resolve) => { const n = ++id; waiting.set(n, resolve); out.write(JSON.stringify({ id: n, method, params }) + "\0"); });
}
const loaded = await send("Extensions.loadUnpacked", { path: ext });
console.log("loadUnpacked:", JSON.stringify(loaded));
chrome.on("exit", (code) => { console.log("chrome exited", code); process.exit(0); });
process.on("SIGTERM", () => { chrome.kill(); });
