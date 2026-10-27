// Swarm Browser: the extension's worker. It keeps one link to Swarm Console
// (the native messaging host the console installs) and carries out the
// console's requests in tabs it opens itself, only inside a lease the person
// granted: named sites, a level, an end. Everything else is refused here,
// whatever the console or the agent asks. The person can stop it from the
// popup or the banner in a leased tab.

const HOST = "dev.swarmagent.console";
// A page load is waited for this long before the page is read as it is.
const LOAD_WAIT_MS = 30000;
// After a click or enter, the page gets this long to navigate or settle.
const SETTLE_MS = 1500;

let port = null;
let connected = false;
let hostError = "";
// By session id: {level, sites, until}.
const leases = {};
// By session id: the ids of the tabs opened for it, in order.
const sessionTabs = {};
// The person pressed stop: nothing runs until the console sends a new lease.
let stopped = false;

function connect() {
  if (port) return;
  try {
    port = chrome.runtime.connectNative(HOST);
  } catch (e) {
    port = null;
    hostError = String(e && e.message || e);
    return;
  }
  port.onMessage.addListener(onMessage);
  port.onDisconnect.addListener(() => {
    const err = chrome.runtime.lastError;
    port = null;
    connected = false;
    if (err && !hostError) hostError = err.message || "";
  });
  port.postMessage({ event: "hello", version: chrome.runtime.getManifest().version });
  connected = true;
  hostError = "";
}

chrome.runtime.onStartup.addListener(connect);
chrome.runtime.onInstalled.addListener(connect);
chrome.alarms.create("link", { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener((a) => { if (a.name === "link") { connect(); sweep(); } });

// A lease that ended takes its banners down.
function sweep() {
  const now = Date.now();
  for (const session of Object.keys(leases)) {
    if (leases[session].until <= now) {
      delete leases[session];
      for (const id of sessionTabs[session] || []) banner(id, null);
      paintGroup(session);
    }
  }
}
connect();

function post(msg) {
  if (port) {
    try { port.postMessage(msg); } catch (e) { /* the link is gone; the disconnect handler runs */ }
  }
}

async function onMessage(msg) {
  if (!msg || typeof msg !== "object") return;
  if (msg.event === "host_error") {
    hostError = msg.error || "the console is not running";
    connected = false;
    return;
  }
  if (msg.op === "ping") { post({ event: "pong" }); return; }
  if (msg.op === "lease") { lease(msg); return; }
  // The person turned Browser on again for this device in Swarm.
  if (msg.op === "resume") { stopped = false; return; }
  if (msg.op === "act" && msg.id !== undefined) {
    try {
      const result = await act(msg);
      post({ id: msg.id, result });
    } catch (e) {
      post({ id: msg.id, error: String(e && e.message || e) });
    }
  }
}

// The console repeats Swarm's record of a session's lease.
function lease(msg) {
  const session = String(msg.session || "");
  if (!session) return;
  if (!msg.level || msg.level === "none") {
    delete leases[session];
    for (const id of sessionTabs[session] || []) banner(id, null);
    paintGroup(session);
    return;
  }
  const sites = (msg.sites || []).map((s) => String(s).trim().toLowerCase()).filter(Boolean);
  leases[session] = { level: String(msg.level), sites, until: Number(msg.until) || 0 };
  // A new lease is the person's consent again.
  stopped = false;
  for (const id of sessionTabs[session] || []) banner(id, leases[session]);
  paintGroup(session);
}

// Swarm approved this one call under the device's Browser rule: it carries its
// own approval, kept for this call only. A stop still holds.
function approvedCall() {
  if (stopped) throw new Error("The person stopped Swarm in their browser; turn Browser on again on the Permissions page.");
  return { level: "full", sites: ["*"], until: Date.now() + 6 * 60 * 1000 };
}

function liveLease(session) {
  const l = leases[session];
  if (!l) throw new Error("This session has no browser lease in the extension.");
  if (Date.now() >= l.until) throw new Error("The browser lease has ended.");
  if (stopped) throw new Error("The person stopped Swarm in their browser; nothing runs until they grant a new lease.");
  return l;
}

function hostOf(url) {
  try { return new URL(url).hostname.toLowerCase(); } catch (e) { return ""; }
}

function covered(lease, url) {
  const host = hostOf(url);
  if (!host) return false;
  const scheme = url.split(":")[0].toLowerCase();
  if (scheme !== "https" && scheme !== "http") return false;
  return lease.sites.some((s) => s === "*" || host === s || host.endsWith("." + s));
}

async function tabOf(id) {
  try { return await chrome.tabs.get(id); } catch (e) { return null; }
}

// The session's tabs that still exist, in order.
async function sessionTabList(session) {
  const ids = sessionTabs[session] || [];
  const out = [];
  for (const id of ids) {
    const t = await tabOf(id);
    if (t) out.push(t);
  }
  sessionTabs[session] = out.map((t) => t.id);
  return out;
}

async function currentTab(session, index) {
  const list = await sessionTabList(session);
  if (index !== undefined && index !== null) {
    const t = list[Number(index)];
    if (!t) throw new Error("No such tab for this session (see tabs).");
    return t;
  }
  if (!list.length) throw new Error("This session has no tab yet: open a url on a leased site first.");
  return list[list.length - 1];
}

function waitForLoad(tabId, ms) {
  return new Promise((resolve) => {
    let done = false;
    const finish = () => { if (!done) { done = true; chrome.tabs.onUpdated.removeListener(listener); resolve(); } };
    const listener = (id, info) => { if (id === tabId && info.status === "complete") finish(); };
    chrome.tabs.onUpdated.addListener(listener);
    setTimeout(finish, ms);
    tabOf(tabId).then((t) => { if (t && t.status === "complete") setTimeout(finish, 300); });
  });
}

// Runs the page script in the tab once per document, then sends it a command.
async function inPage(tabId, cmd) {
  const probe = await chrome.scripting.executeScript({ target: { tabId }, func: () => Boolean(globalThis.__swarmPage) }).catch(() => null);
  if (!probe || !probe[0] || !probe[0].result) {
    await chrome.scripting.executeScript({ target: { tabId }, files: ["content.js"] });
  }
  return chrome.tabs.sendMessage(tabId, cmd);
}

async function banner(tabId, lease) {
  try {
    await inPage(tabId, { cmd: "banner", lease: lease ? { until: lease.until, level: lease.level } : null });
  } catch (e) { /* a page that cannot run scripts (chrome://, the store) shows no banner */ }
}

// The tabs Swarm drives sit in one tab group per session, titled "Swarm"
// with the level, so the person sees which tabs the agent pilots. An
// ended lease turns its group grey.
const sessionGroups = {};
async function groupTabs(session) {
  const ids = sessionTabs[session] || [];
  if (!ids.length || !chrome.tabGroups) return;
  try {
    const existing = sessionGroups[session];
    let groupId;
    try {
      groupId = await chrome.tabs.group(existing !== undefined ? { tabIds: ids, groupId: existing } : { tabIds: ids });
    } catch (e) {
      groupId = await chrome.tabs.group({ tabIds: ids });
    }
    sessionGroups[session] = groupId;
    await paintGroup(session);
  } catch (e) { /* a tab in another window or a closed one: left ungrouped */ }
}

async function paintGroup(session) {
  const groupId = sessionGroups[session];
  if (groupId === undefined || !chrome.tabGroups) return;
  const l = leases[session];
  const live = l && Date.now() < l.until && !stopped;
  try {
    await chrome.tabGroups.update(groupId, live
      ? { title: "Swarm · " + l.level, color: l.level === "full" ? "red" : "blue" }
      : { title: "Swarm · ended", color: "grey" });
  } catch (e) { delete sessionGroups[session]; }
}

// A leased tab navigating (a redirect, a click) keeps its banner; outside
// its sites the banner says so and actions are refused by covered().
chrome.tabs.onUpdated.addListener((tabId, info, tab) => {
  if (info.status !== "complete") return;
  for (const session of Object.keys(sessionTabs)) {
    if ((sessionTabs[session] || []).includes(tabId)) {
      const l = leases[session];
      banner(tabId, l && Date.now() < l.until ? l : null);
    }
  }
});

chrome.tabs.onRemoved.addListener((tabId) => {
  for (const session of Object.keys(sessionTabs)) {
    sessionTabs[session] = (sessionTabs[session] || []).filter((id) => id !== tabId);
  }
});

async function pageOf(tab, lease, extra) {
  const t = (await tabOf(tab.id)) || tab;
  if (!covered(lease, t.url || "")) {
    return Object.assign({ url: t.url, title: t.title, outside_lease: true, note: "This tab is outside the leased sites now; nothing is read or done here." }, extra || {});
  }
  const page = await inPage(t.id, { cmd: "read", query: extra && extra.query });
  return Object.assign({ url: t.url, title: t.title }, page, extra || {});
}

async function act(msg) {
  const session = String(msg.session || "");
  const lease = msg.approved === true ? approvedCall() : liveLease(session);
  // The console's view of the lease travels with each action; the stricter wins.
  if (Array.isArray(msg.sites) && msg.sites.length) {
    lease.sites = lease.sites.filter((s) => msg.sites.includes(s));
  }
  if (Number(msg.until) && Number(msg.until) < lease.until) lease.until = Number(msg.until);
  const confirmed = Boolean(msg.confirmed);
  switch (msg.action) {
    case "tabs": {
      const list = await sessionTabList(session);
      return { tabs: list.map((t, i) => ({ tab: i, url: t.url, title: t.title, in_lease: covered(lease, t.url || "") })) };
    }
    case "open": {
      const url = String(msg.url || "");
      if (!covered(lease, url)) throw new Error("That url is not on a leased site (" + lease.sites.join(", ") + ").");
      let tab;
      const list = await sessionTabList(session);
      if (msg.tab !== undefined && msg.tab !== null) {
        tab = await currentTab(session, msg.tab);
        await chrome.tabs.update(tab.id, { url });
      } else if (list.length) {
        tab = list[list.length - 1];
        await chrome.tabs.update(tab.id, { url });
      } else {
        tab = await chrome.tabs.create({ url, active: false });
        sessionTabs[session] = [...(sessionTabs[session] || []), tab.id];
        await groupTabs(session);
      }
      await waitForLoad(tab.id, LOAD_WAIT_MS);
      await banner(tab.id, lease);
      return pageOf(tab, lease);
    }
    case "read": {
      const tab = await currentTab(session, msg.tab);
      return pageOf(tab, lease);
    }
    case "find": {
      const tab = await currentTab(session, msg.tab);
      return pageOf(tab, lease, { query: String(msg.text || "") });
    }
    case "click":
    case "type": {
      const tab = await currentTab(session, msg.tab);
      const t = (await tabOf(tab.id)) || tab;
      if (!covered(lease, t.url || "")) throw new Error("This tab is outside the leased sites now.");
      const cmd = { cmd: msg.action, ref: Number(msg.ref), text: String(msg.text || ""), enter: Boolean(msg.enter), confirmed, level: lease.level };
      const outcome = await inPage(t.id, cmd);
      if (outcome && outcome.needs_confirm) return { needs_confirm: outcome.needs_confirm };
      if (outcome && outcome.error) throw new Error(outcome.error);
      await new Promise((r) => setTimeout(r, SETTLE_MS));
      await waitForLoad(t.id, LOAD_WAIT_MS);
      return pageOf(t, lease, { did: outcome && outcome.did });
    }
    case "back": {
      const tab = await currentTab(session, msg.tab);
      await chrome.tabs.goBack(tab.id).catch(() => {});
      await new Promise((r) => setTimeout(r, SETTLE_MS));
      await waitForLoad(tab.id, LOAD_WAIT_MS);
      return pageOf(tab, lease);
    }
    default:
      throw new Error("Unknown action: " + msg.action);
  }
}

// The popup and the banner talk to the worker here.
chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  if (!msg || typeof msg !== "object") return;
  if (msg.type === "state") {
    connect();
    reply({ connected, hostError, stopped, leases, tabs: sessionTabs });
    return true;
  }
  if (msg.type === "stop") {
    stopped = true;
    post({ event: "stopped" });
    for (const session of Object.keys(leases)) {
      delete leases[session];
      for (const id of sessionTabs[session] || []) banner(id, null);
      paintGroup(session);
    }
    reply({ ok: true });
    return true;
  }
});
