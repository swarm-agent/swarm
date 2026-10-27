// The popup: the link to Swarm Console, the live leases, and stop.

function render(state) {
  const link = document.getElementById("link");
  if (state.connected) {
    link.textContent = "Connected to Swarm Console.";
    link.className = "ok";
  } else {
    link.textContent = "Not connected to Swarm Console" + (state.hostError ? ": " + state.hostError : ". Is the app running, and was Chrome connected in its Settings?");
    link.className = "bad";
  }
  const box = document.getElementById("leases");
  box.textContent = "";
  const now = Date.now();
  const live = Object.entries(state.leases || {}).filter(([, l]) => l.until > now);
  if (state.stopped) {
    const p = document.createElement("div");
    p.className = "lease bad";
    p.textContent = "Stopped. Nothing runs until you grant a new lease in Swarm Console.";
    box.append(p);
  } else if (!live.length) {
    const p = document.createElement("div");
    p.className = "muted";
    p.textContent = "No live lease: Swarm is not using this browser.";
    box.append(p);
  }
  for (const [session, l] of live) {
    const d = document.createElement("div");
    d.className = "lease";
    const b = document.createElement("b");
    const ends = new Date(l.until);
    b.textContent = l.level + " · until " + ends.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    const s = document.createElement("div");
    s.textContent = l.sites.includes("*") ? "any site" : l.sites.join(", ");
    const t = document.createElement("div");
    t.className = "muted";
    const tabs = (state.tabs && state.tabs[session]) || [];
    t.textContent = tabs.length + (tabs.length === 1 ? " tab" : " tabs") + " · session " + session.slice(0, 8);
    d.append(b, s, t);
    box.append(d);
  }
  document.getElementById("stop").disabled = !live.length && !state.stopped;
}

function refresh() {
  chrome.runtime.sendMessage({ type: "state" }, (state) => {
    if (chrome.runtime.lastError || !state) {
      render({ connected: false, hostError: "the extension's worker did not answer", leases: {} });
      return;
    }
    render(state);
  });
}

document.getElementById("stop").addEventListener("click", () => {
  chrome.runtime.sendMessage({ type: "stop" }, refresh);
});
refresh();
