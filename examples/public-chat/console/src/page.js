// The staff console page. consoleApp runs in the browser (served as /console.js
// so the page needs no inline script); it only talks to this Worker's /ops routes.
// Everything from visitors or the model is rendered as text, never HTML.

export const PAGE_HTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Staff console</title>
<style>
  :root { --bg:#f6f7f9; --card:#fff; --ink:#111418; --mute:#69707a; --line:#e3e6ea; --accent:#2340ff; --ok:#1f9d55; --warn:#c2410c; }
  * { box-sizing:border-box; margin:0; }
  body { font:14px/1.45 system-ui,sans-serif; background:var(--bg); color:var(--ink); }
  header { display:flex; align-items:center; gap:14px; padding:12px 20px; background:var(--card); border-bottom:1px solid var(--line); position:sticky; top:0; }
  header h1 { font-size:16px; flex:1; }
  .pill { display:inline-flex; align-items:center; gap:8px; padding:6px 12px; border:1px solid var(--line); border-radius:999px; cursor:pointer; user-select:none; }
  .pill input { accent-color:var(--ok); }
  .dot { width:9px; height:9px; border-radius:50%; background:#b7bcc3; }
  .dot.on { background:var(--ok); }
  button { font:inherit; padding:6px 12px; border-radius:8px; border:1px solid var(--line); background:var(--card); cursor:pointer; }
  button.primary { background:var(--accent); border-color:var(--accent); color:#fff; }
  button:disabled { opacity:.45; cursor:default; }
  main { display:grid; grid-template-columns:340px 1fr; gap:16px; padding:16px 20px; }
  section { background:var(--card); border:1px solid var(--line); border-radius:12px; padding:14px; }
  h2 { font-size:12px; text-transform:uppercase; letter-spacing:.06em; color:var(--mute); margin-bottom:10px; }
  .stack { display:flex; flex-direction:column; gap:16px; }
  .item { padding:10px; border:1px solid var(--line); border-radius:10px; margin-bottom:8px; cursor:pointer; }
  .item.sel { border-color:var(--accent); }
  .item.waiting { border-color:var(--warn); background:#fff7ed; }
  .item small { color:var(--mute); display:block; }
  .row { display:flex; gap:8px; align-items:center; justify-content:space-between; }
  #log { font:12px/1.5 ui-monospace,monospace; color:var(--mute); max-height:180px; overflow:auto; white-space:pre-wrap; }
  #transcript { display:flex; flex-direction:column; gap:8px; max-height:58vh; overflow:auto; padding:4px; }
  .msg { max-width:80%; padding:8px 11px; border-radius:10px; white-space:pre-wrap; }
  .msg.visitor { align-self:flex-end; background:#111418; color:#fff; }
  .msg.agent { background:#eef0f3; }
  .msg.person { background:#e8edff; }
  .msg b { display:block; font-size:11px; opacity:.7; }
  textarea { width:100%; min-height:70px; font:inherit; padding:8px; border:1px solid var(--line); border-radius:8px; resize:vertical; }
  .empty { color:var(--mute); }
  @media (max-width:800px) { main { grid-template-columns:1fr; } }
</style></head>
<body>
<header>
  <h1>Staff console</h1>
  <span id="who" class="empty"></span>
  <label class="pill"><span id="dot" class="dot"></span><input id="avail" type="checkbox"> Available for chats</label>
  <button id="alerts">Enable alerts</button>
</header>
<main>
  <div class="stack">
    <section><h2>Waiting for a person</h2><div id="queue" class="empty">Nobody waiting.</div></section>
    <section><h2>Recent conversations</h2><div id="convs" class="empty">None yet.</div></section>
  </div>
  <div class="stack">
    <section>
      <div class="row"><h2 id="title">Select a conversation</h2>
        <span><button id="takeover" disabled>Take over</button> <button id="handback" disabled>Hand back</button> <button id="reopen" disabled>Reopen</button></span></div>
      <div id="transcript"></div>
      <div style="margin-top:10px"><textarea id="reply" placeholder="Reply as yourself (take over first)" disabled></textarea>
      <div class="row" style="margin-top:8px"><small id="state" class="empty"></small><button id="send" class="primary" disabled>Send</button></div></div>
    </section>
    <section><h2>Live events</h2><div id="log"></div></section>
  </div>
</main>
<script src="/console.js" defer></script>
</body></html>`;

function consoleApp() {
  const $ = (s) => document.querySelector(s);
  const state = { latest: 0, selected: null, available: false, beat: null, me: null, queue: [], audio: null };
  const api = async (path, body) => {
    const res = await fetch(path, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw Object.assign(new Error(data.error || String(res.status)), { status: res.status });
    return data;
  };
  const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text !== undefined) e.textContent = text; return e; };
  const ago = (t) => { const s = Math.max(0, Math.round((Date.now() - t) / 1000)); return s < 60 ? s + 's ago' : Math.round(s / 60) + 'm ago'; };
  const log = (line) => { const l = $('#log'); l.textContent = new Date().toLocaleTimeString() + '  ' + line + '\n' + l.textContent.slice(0, 8000); };

  // ---- presence: a heartbeat every 20 s while "Available" is on and this tab is open
  async function setAvailable(on) {
    state.available = on;
    $('#avail').checked = on;
    $('#dot').classList.toggle('on', on);
    clearInterval(state.beat);
    try { await api('/ops/presence', { available: on }); } catch (e) { log('presence failed: ' + e.message); }
    if (on) state.beat = setInterval(() => api('/ops/presence', { available: true }).catch(() => {}), 20_000);
  }
  $('#avail').addEventListener('change', (e) => setAvailable(e.target.checked));
  // Closing the tab signs out at once; otherwise presence lapses on its own.
  addEventListener('pagehide', () => {
    if (state.available) fetch('/ops/presence', { method: 'POST', keepalive: true, headers: { 'Content-Type': 'application/json' }, body: '{"available":false}' });
  });

  // ---- alerts: desktop notification + a short tone when someone asks for a person
  $('#alerts').addEventListener('click', async () => {
    state.audio = state.audio || new AudioContext();
    if ('Notification' in window) await Notification.requestPermission();
    $('#alerts').textContent = Notification.permission === 'granted' ? 'Alerts on' : 'Sound only';
  });
  function alertNewChat(summary) {
    if (state.audio) {
      const o = state.audio.createOscillator(), g = state.audio.createGain();
      o.frequency.value = 880; g.gain.value = 0.08; o.connect(g).connect(state.audio.destination);
      o.start(); o.stop(state.audio.currentTime + 0.25);
    }
    if ('Notification' in window && Notification.permission === 'granted') {
      const n = new Notification('A visitor wants to talk', { body: summary || 'Open the console to accept.', requireInteraction: true });
      n.onclick = () => { focus(); n.close(); };
    }
    document.title = '(!) Staff console';
  }
  addEventListener('focus', () => { document.title = 'Staff console'; });

  // ---- queue and conversations
  async function refreshQueue() {
    const { waiting } = await api('/ops/queue');
    state.queue = waiting;
    const q = $('#queue');
    q.replaceChildren();
    q.className = waiting.length ? '' : 'empty';
    if (!waiting.length) q.textContent = 'Nobody waiting.';
    for (const w of waiting) {
      const item = el('div', 'item waiting');
      const left = Math.max(0, Math.round((w.expires_at - Date.now()) / 1000));
      item.append(el('div', '', w.summary || 'Visitor asked for a person'), el('small', '', 'waiting ' + ago(w.since) + ' · ' + left + 's left'));
      const accept = el('button', 'primary', 'Accept');
      accept.addEventListener('click', async (e) => { e.stopPropagation(); await api('/ops/accept', { session_id: w.session_id }); log('accepted ' + w.session_id.slice(0, 8)); await select(w.session_id); refreshQueue(); });
      item.append(accept);
      item.addEventListener('click', () => select(w.session_id));
      q.append(item);
    }
  }
  async function refreshConversations() {
    const { conversations } = await api('/ops/conversations');
    const c = $('#convs');
    c.replaceChildren();
    const recent = conversations.sort((a, b) => b.last_at - a.last_at).slice(0, 30);
    c.className = recent.length ? '' : 'empty';
    if (!recent.length) c.textContent = 'None yet.';
    for (const v of recent) {
      const item = el('div', 'item' + (v.session_id === state.selected ? ' sel' : ''));
      const tags = [v.messages + ' msgs', ago(v.last_at)];
      if (v.person) tags.push(v.person.state);
      if (v.ended) tags.push('ended: ' + v.ended.reason);
      item.append(el('div', '', 'Visitor ' + v.visitor_id.slice(0, 8)), el('small', '', tags.join(' · ')));
      item.addEventListener('click', () => select(v.session_id));
      c.append(item);
    }
  }

  // ---- one conversation
  let transcriptTimer;
  async function select(sessionId) {
    state.selected = sessionId;
    clearInterval(transcriptTimer);
    transcriptTimer = setInterval(() => refreshTranscript().catch(() => {}), 3000);
    await refreshTranscript();
    refreshConversations().catch(() => {});
  }
  async function refreshTranscript() {
    if (!state.selected) return;
    const t = await api('/ops/transcript?session_id=' + encodeURIComponent(state.selected));
    const human = t.takeover?.state === 'human';
    $('#title').textContent = 'Conversation ' + state.selected.slice(0, 8);
    $('#state').textContent = (human ? 'You hold this conversation' : 'The AI worker is answering') + (t.ended ? ' · ended (' + t.ended.reason + ')' : '');
    $('#takeover').disabled = human;
    $('#handback').disabled = !human;
    $('#reopen').disabled = !t.ended;
    $('#reply').disabled = !human;
    $('#send').disabled = !human;
    const box = $('#transcript');
    const atBottom = box.scrollTop + box.clientHeight >= box.scrollHeight - 20;
    box.replaceChildren(...t.messages.map((m) => {
      const d = el('div', 'msg ' + m.from);
      d.append(el('b', '', m.from === 'person' ? 'Person' : m.from === 'agent' ? 'AI worker' : 'Visitor'), document.createTextNode(m.text));
      return d;
    }));
    if (atBottom) box.scrollTop = box.scrollHeight;
  }
  const act = (path) => async () => { if (!state.selected) return; await api(path, { session_id: state.selected }); await refreshTranscript(); refreshConversations(); };
  $('#takeover').addEventListener('click', act('/ops/accept'));
  $('#handback').addEventListener('click', act('/ops/handback'));
  $('#reopen').addEventListener('click', act('/ops/reopen'));
  async function send() {
    const content = $('#reply').value.trim();
    if (!content || !state.selected) return;
    $('#send').disabled = true;
    try { await api('/ops/reply', { session_id: state.selected, content }); $('#reply').value = ''; await refreshTranscript(); }
    catch (e) { log('reply failed: ' + e.message); }
    finally { $('#send').disabled = false; }
  }
  $('#send').addEventListener('click', send);
  $('#reply').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) send(); });

  // ---- live events: long poll; the gateway answers as soon as something happens
  async function listen() {
    for (;;) {
      try {
        const { events, latest } = await api('/ops/events?after=' + state.latest + '&wait=25');
        state.latest = latest;
        for (const e of events) {
          log(e.type + (e.session_id ? ' ' + e.session_id.slice(0, 8) : '') + (e.summary ? ' — ' + e.summary : '') + (e.reason ? ' — ' + e.reason : ''));
          if (e.type === 'handoff.requested' && e.staff_online !== false) alertNewChat(e.summary);
          if (e.session_id && e.session_id === state.selected) refreshTranscript().catch(() => {});
        }
        if (events.length) { refreshQueue().catch(() => {}); refreshConversations().catch(() => {}); }
      } catch (e) {
        if (e.status === 403) { log('signed out: reload the page'); setAvailable(false); return; }
        await new Promise((r) => setTimeout(r, 3000));
      }
    }
  }

  (async () => {
    state.me = await api('/me');
    $('#who').textContent = state.me.name + ' · ' + state.me.role;
    const { latest } = await api('/ops/events?after=0&wait=0');
    state.latest = latest;
    await Promise.all([refreshQueue(), refreshConversations()]);
    setInterval(() => refreshQueue().catch(() => {}), 5000);
    listen();
  })().catch((e) => log('console failed to start: ' + e.message));
}

export const PAGE_JS = '(' + consoleApp.toString() + ')();\n';
