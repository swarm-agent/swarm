// The Bots HQ page. hqApp runs in the browser (served as /hq.js, so the page
// needs no inline script); it talks only to this Worker: /me, /bots,
// /bots/summary, /hours and the /ops routes (with the bot chosen on screen).
// Everything from visitors or the model is rendered as text, never HTML.

export const PAGE_HTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Bots HQ</title>
<style>
  :root { --bg:#0b0e14; --panel:#11161f; --panel2:#151b26; --line:#1f2733; --ink:#e6edf3; --mute:#8b96a5; --dim:#5d6876;
          --accent:#87ceeb; --live:#3fb950; --stage:#d29922; --warn:#f0883e; --bad:#f85149; --radius:14px; }
  * { box-sizing:border-box; margin:0; }
  [hidden] { display:none !important; }
  html, body { height:100%; }
  body { font:14px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif; background:var(--bg); color:var(--ink); }
  button { font:inherit; color:inherit; cursor:pointer; border:1px solid var(--line); background:var(--panel2); border-radius:10px; padding:7px 12px; }
  button:hover:not(:disabled) { border-color:#2d3746; }
  button:disabled { opacity:.4; cursor:default; }
  button.primary { background:var(--accent); border-color:var(--accent); color:#06101f; font-weight:600; }
  button.danger { color:var(--bad); }
  button.ghost { background:transparent; }
  a.button { color:var(--accent); border:1px solid var(--line); border-radius:10px; padding:7px 12px; text-decoration:none; }
  select, input[type=text], textarea { font:inherit; color:var(--ink); background:var(--bg); border:1px solid var(--line); border-radius:10px; padding:8px 10px; }
  textarea { width:100%; min-height:76px; resize:vertical; }
  .app { display:grid; grid-template-columns:260px 1fr; grid-template-rows:60px 1fr; height:100vh; }
  .top { grid-column:1 / -1; display:flex; align-items:center; gap:14px; padding:0 18px; border-bottom:1px solid var(--line); background:var(--panel); }
  .brand { display:flex; align-items:center; gap:10px; font-weight:650; font-size:16px; width:224px; }
  .brand svg { width:26px; height:26px; }
  .seg { display:inline-flex; background:var(--bg); border:1px solid var(--line); border-radius:12px; padding:3px; }
  .seg button { border:0; background:transparent; padding:6px 14px; border-radius:9px; color:var(--mute); display:inline-flex; align-items:center; gap:7px; }
  .seg button.on { background:var(--panel2); color:var(--ink); }
  .spacer { flex:1; }
  .chip { display:inline-flex; align-items:center; gap:8px; padding:7px 12px; border-radius:999px; border:1px solid var(--line); background:var(--panel2); }
  .dot { width:8px; height:8px; border-radius:50%; background:var(--dim); flex-shrink:0; }
  .dot.live { background:var(--live); } .dot.stage { background:var(--stage); } .dot.bad { background:var(--bad); } .dot.on { background:var(--live); box-shadow:0 0 0 3px #3fb95033; }
  .who { color:var(--mute); font-size:13px; }
  aside { border-right:1px solid var(--line); background:var(--panel); padding:14px 10px; overflow:auto; }
  .navlabel { font-size:11px; letter-spacing:.08em; text-transform:uppercase; color:var(--dim); padding:12px 10px 6px; }
  .nav { display:flex; align-items:center; gap:10px; width:100%; text-align:left; border:0; background:transparent; padding:9px 10px; border-radius:10px; color:var(--ink); }
  .nav:hover { background:var(--panel2); }
  .nav.sel { background:var(--panel2); box-shadow:inset 3px 0 0 var(--accent); }
  .nav .name { flex:1; }
  .nav.ghost { color:var(--dim); cursor:default; }
  .badge { min-width:20px; padding:1px 7px; border-radius:999px; background:var(--warn); color:#120a02; font-size:12px; font-weight:700; text-align:center; }
  .badge:empty { display:none; }
  .envpill { font-size:11px; padding:2px 8px; border-radius:999px; border:1px solid var(--line); color:var(--mute); }
  .envpill.live { color:var(--live); border-color:#3fb95055; } .envpill.stage { color:var(--stage); border-color:#d2992255; }
  main { overflow:auto; padding:22px 24px 40px; }
  h1 { font-size:22px; font-weight:650; display:flex; align-items:center; gap:10px; }
  .sub { color:var(--mute); margin-top:4px; }
  .kpis { display:grid; grid-template-columns:repeat(auto-fit,minmax(170px,1fr)); gap:12px; margin:20px 0; }
  .kpi { background:var(--panel); border:1px solid var(--line); border-radius:var(--radius); padding:14px 16px; }
  .kpi .v { font-size:26px; font-weight:650; margin-top:2px; }
  .kpi .l { color:var(--mute); font-size:12px; text-transform:uppercase; letter-spacing:.06em; }
  .kpi.alert .v { color:var(--warn); }
  .cards { display:grid; grid-template-columns:repeat(auto-fill,minmax(300px,1fr)); gap:14px; }
  .card { background:var(--panel); border:1px solid var(--line); border-radius:var(--radius); padding:16px 18px; display:flex; flex-direction:column; gap:12px; }
  .card .head { display:flex; align-items:center; gap:10px; }
  .card .head .name { font-weight:650; font-size:16px; flex:1; }
  .stat { display:flex; justify-content:space-between; color:var(--mute); }
  .stat b { color:var(--ink); font-weight:600; }
  .bar { height:6px; border-radius:6px; background:var(--bg); overflow:hidden; }
  .bar i { display:block; height:100%; background:var(--accent); border-radius:6px; }
  .bar.hot i { background:var(--warn); }
  .note { color:var(--mute); font-size:13px; }
  .empty { color:var(--dim); }
  .botgrid { display:grid; grid-template-columns:300px minmax(0,1fr) 280px; gap:14px; margin-top:18px; }
  .panel { background:var(--panel); border:1px solid var(--line); border-radius:var(--radius); padding:14px; min-width:0; }
  .tabs { display:flex; gap:4px; margin-bottom:10px; }
  .tabs button { border:0; background:transparent; color:var(--mute); padding:5px 10px; border-radius:8px; }
  .tabs button.on { background:var(--panel2); color:var(--ink); }
  .item { padding:10px 12px; border:1px solid var(--line); border-radius:12px; margin-bottom:8px; cursor:pointer; background:var(--panel2); }
  .item:hover { border-color:#2d3746; }
  .item.sel { border-color:var(--accent); }
  .item.waiting { border-color:#f0883e88; background:#1d150d; }
  .item small { color:var(--mute); display:block; }
  .item .row { margin-top:6px; }
  .row { display:flex; gap:8px; align-items:center; justify-content:space-between; flex-wrap:wrap; }
  .convhead { display:flex; align-items:center; justify-content:space-between; gap:10px; flex-wrap:wrap; margin-bottom:10px; }
  .convhead h2 { font-size:15px; }
  .actions { display:flex; gap:6px; flex-wrap:wrap; }
  #transcript { display:flex; flex-direction:column; gap:10px; height:46vh; overflow:auto; padding:6px 2px; }
  .msg { max-width:82%; padding:9px 12px; border-radius:14px; white-space:pre-wrap; overflow-wrap:anywhere; }
  .msg.visitor { align-self:flex-end; background:#10202a; color:#eaf6fb; border-bottom-right-radius:4px; }
  .msg.agent { background:var(--panel2); border-bottom-left-radius:4px; }
  .msg.person { background:#1b2a3b; border-bottom-left-radius:4px; }
  .msg b { display:block; font-size:11px; color:var(--mute); font-weight:600; margin-bottom:2px; }
  .info { margin:4px 0 10px; padding:9px 12px; border-radius:10px; background:var(--panel2); color:var(--mute); }
  .info.warm { background:#1d150d; color:#f3c69c; }
  #vtyping { font-size:12px; color:var(--mute); padding:4px 2px 0; display:flex; align-items:center; gap:6px; }
  #vtyping i { width:5px; height:5px; border-radius:50%; background:var(--mute); animation:tdot 1.1s infinite; }
  #vtyping i:nth-child(2) { animation-delay:.15s; } #vtyping i:nth-child(3) { animation-delay:.3s; }
  @keyframes tdot { 0%,60%,100% { opacity:.3; } 30% { opacity:1; } }
  .composer { margin-top:10px; }
  #log { font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,monospace; color:var(--mute); max-height:260px; overflow:auto; white-space:pre-wrap; }
  .side h3 { font-size:12px; text-transform:uppercase; letter-spacing:.06em; color:var(--dim); margin:2px 0 10px; }
  .side .stat { padding:5px 0; border-bottom:1px solid #161c26; }
  .switch { display:inline-flex; align-items:center; gap:8px; cursor:pointer; user-select:none; }
  .switch input { appearance:none; width:34px; height:20px; border-radius:20px; background:var(--line); position:relative; cursor:pointer; transition:background .15s; }
  .switch input::after { content:""; position:absolute; top:2px; left:2px; width:16px; height:16px; border-radius:50%; background:#fff; transition:left .15s; }
  .switch input:checked { background:var(--live); } .switch input:checked::after { left:16px; }
  dialog { margin:auto; border:1px solid var(--line); border-radius:18px; background:var(--panel); color:var(--ink); padding:0; width:min(520px,calc(100vw - 32px)); }
  dialog::backdrop { background:#000a; }
  .dlg { padding:22px; display:flex; flex-direction:column; gap:16px; }
  .dlg h2 { font-size:18px; }
  .choices { display:grid; grid-template-columns:repeat(3,1fr); gap:8px; }
  .choice { padding:10px; border-radius:12px; border:1px solid var(--line); background:var(--panel2); text-align:center; }
  .choice.on { border-color:var(--accent); box-shadow:inset 0 0 0 1px var(--accent); }
  .times { display:flex; align-items:center; gap:10px; flex-wrap:wrap; }
  details summary { cursor:pointer; color:var(--mute); }
  .botrow { display:flex; align-items:center; justify-content:space-between; gap:10px; padding:8px 0; border-bottom:1px solid #161c26; flex-wrap:wrap; }
  @media (max-width:1100px) { .botgrid { grid-template-columns:280px minmax(0,1fr); } .botgrid .side { grid-column:1 / -1; } }
  @media (max-width:760px) { .app { grid-template-columns:1fr; grid-template-rows:auto auto 1fr; } .top { flex-wrap:wrap; padding:10px; height:auto; } .brand { width:auto; }
    aside { border-right:0; border-bottom:1px solid var(--line); } .botgrid { grid-template-columns:1fr; } .choices { grid-template-columns:repeat(2,1fr); } }
</style></head>
<body>
<div class="app">
  <header class="top">
    <div class="brand"><svg viewBox="0 0 400 400" aria-hidden="true"><rect x="20" y="20" width="360" height="360" rx="90" fill="#87ceeb" opacity=".15"/><rect x="60" y="60" width="280" height="280" rx="65" fill="#87ceeb" opacity=".35"/><rect x="100" y="100" width="200" height="200" rx="45" fill="#87ceeb" opacity=".6"/><rect x="140" y="140" width="120" height="120" rx="25" fill="#87ceeb" opacity=".85"/></svg>Bots HQ</div>
    <div class="seg" role="group" aria-label="Environment"><button id="envlive" class="on"><span class="dot live"></span>Live<span id="badgelive" class="badge"></span></button><button id="envstage"><span class="dot stage"></span>Staging<span id="badgestage" class="badge"></span></button></div>
    <div class="spacer"></div>
    <button id="hoursbtn" class="chip"><span id="hoursdot" class="dot"></span><span id="hourstext">Online hours</span></button>
    <button id="alerts" class="chip">Enable alerts</button>
    <span id="who" class="who"></span>
  </header>
  <aside><nav id="tabs" aria-label="Bots"></nav></aside>
  <main>
    <section id="overview"></section>
    <section id="botview" hidden>
      <div class="row">
        <div><h1 id="botname"></h1><div id="botsub" class="sub"></div></div>
        <label class="switch" id="availpill"><input id="avail" type="checkbox"><span>I'm available on this bot</span></label>
      </div>
      <div class="botgrid">
        <div class="panel">
          <div class="tabs"><button id="t-waiting" class="on">Waiting <span id="qcount" class="badge"></span></button><button id="t-recent">Recent</button><button id="t-blocked">Blocked</button></div>
          <div id="queue" class="empty">Nobody waiting.</div>
          <div id="convs" class="empty" hidden>None yet.</div>
          <div id="blocks" class="empty" hidden>Nobody blocked.</div>
        </div>
        <div class="panel">
          <div class="convhead"><h2 id="title">Select a conversation</h2>
            <div class="actions"><button id="takeover" disabled>Take over</button><button id="handback" disabled>Hand back</button><button id="resolve" disabled>Resolve</button><button id="reopen" disabled>Reopen</button><button id="block" class="danger" disabled>Block</button><a id="mail" class="button" hidden>Email them</a></div></div>
          <div id="contact" class="info warm" hidden></div>
          <div id="card" class="info" hidden></div>
          <div id="transcript"></div>
          <div id="vtyping" hidden><i></i><i></i><i></i> Visitor is typing…</div>
          <div class="composer"><textarea id="reply" placeholder="Take over to reply as yourself" disabled></textarea>
            <div class="row" style="margin-top:8px"><small id="state" class="note"></small><button id="send" class="primary" disabled>Send</button></div></div>
        </div>
        <div class="panel side"><h3>This bot</h3><div id="botstats"></div><h3 style="margin-top:16px">Live events</h3><div id="log"></div></div>
      </div>
    </section>
  </main>
</div>
<dialog id="hoursdlg"><form method="dialog" class="dlg" id="hoursform"></form></dialog>
<script src="/hq.js" defer></script>
</body></html>`;

function hqApp() {
  const $ = (s) => document.querySelector(s);
  const ENDED = { resolved: 'Resolved', blocked: 'Blocked', lead_saved: 'Left their details', abuse: 'Ended by the AI (abuse)', cannot_help: 'Ended by the AI' };
  const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
  const PRESETS = { every: DAYS, sunthu: ['sun', 'mon', 'tue', 'wed', 'thu'], monfri: ['mon', 'tue', 'wed', 'thu', 'fri'] };
  const ZONES = [['Asia/Jerusalem', 'Israel'], ['UTC', 'UTC'], ['Europe/London', 'London'], ['Europe/Berlin', 'Berlin'], ['America/New_York', 'New York'], ['America/Los_Angeles', 'Los Angeles']];
  const state = { env: 'production', bot: null, bots: [], summaries: [], hours: null, onCall: {}, waiting: {}, gen: 0, latest: 0, selected: null, available: false, beat: null, me: null, audio: null, list: 'waiting' };

  const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text !== undefined) e.textContent = text; return e; };
  const ago = (t) => { const s = Math.max(0, Math.round((Date.now() - t) / 1000)); return s < 60 ? s + 's ago' : s < 3600 ? Math.round(s / 60) + 'm ago' : Math.round(s / 3600) + 'h ago'; };
  const money = (n) => '$' + Number(n || 0).toFixed(Number(n) < 1 ? 3 : 2);
  const log = (line) => { const l = $('#log'); l.textContent = new Date().toLocaleTimeString() + '  ' + line + '\n' + l.textContent.slice(0, 8000); };
  const raw = async (path, body) => {
    const res = await fetch(path, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw Object.assign(new Error(data.error || String(res.status)), { status: res.status });
    return data;
  };
  // Every /ops call goes to the bot on screen: ?bot= on reads, "bot" in the body on writes.
  const api = (path, body) => body === undefined
    ? raw(path + (path.includes('?') ? '&' : '?') + 'bot=' + encodeURIComponent(state.bot))
    : raw(path, { ...body, bot: state.bot });
  const botById = (id) => state.bots.find((b) => b.id === id);
  const envBots = () => state.bots.filter((b) => b.env === state.env);
  const summaryOf = (id) => state.summaries.find((s) => s.id === id);
  const isLive = (b) => b.env === 'production';

  // ---- presence: a heartbeat every 20 s while "available" is on and this tab is open
  async function setAvailable(on) {
    state.available = on;
    $('#avail').checked = on;
    clearInterval(state.beat);
    if (!state.bot) return;
    try { await api('/ops/presence', { available: on }); } catch (e) { log('presence failed: ' + e.message); }
    if (on) state.beat = setInterval(() => api('/ops/presence', { available: true }).catch(() => {}), 20_000);
  }
  $('#avail').addEventListener('change', (e) => setAvailable(e.target.checked));
  addEventListener('pagehide', () => {
    if (state.available && state.bot) fetch('/ops/presence', { method: 'POST', keepalive: true, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ available: false, bot: state.bot }) });
  });

  // ---- alerts: a desktop notification and a short tone when someone wants a person
  $('#alerts').addEventListener('click', async () => {
    state.audio = state.audio || new AudioContext();
    if ('Notification' in window) await Notification.requestPermission();
    $('#alerts').textContent = 'Notification' in window && Notification.permission === 'granted' ? 'Alerts on' : 'Sound only';
  });
  function alertNewChat(summary, botName) {
    if (state.audio) {
      const o = state.audio.createOscillator(), g = state.audio.createGain();
      o.frequency.value = 880; g.gain.value = 0.08; o.connect(g).connect(state.audio.destination);
      o.start(); o.stop(state.audio.currentTime + 0.25);
    }
    if ('Notification' in window && Notification.permission === 'granted') {
      const n = new Notification((botName ? botName + ': ' : '') + 'a visitor wants to talk', { body: summary || 'Open Bots HQ to accept.', requireInteraction: true });
      n.onclick = () => { focus(); n.close(); };
    }
    document.title = '(!) Bots HQ';
  }
  addEventListener('focus', () => { document.title = 'Bots HQ'; });

  // ---- environment switch and the sidebar
  function setEnv(env) {
    state.env = env;
    $('#envlive').classList.toggle('on', env === 'production');
    $('#envstage').classList.toggle('on', env === 'staging');
    showBot(null);
  }
  $('#envlive').addEventListener('click', () => setEnv('production'));
  $('#envstage').addEventListener('click', () => setEnv('staging'));
  function renderSidebar() {
    const nav = $('#tabs');
    const item = (label, id, { dot = '', badge = 0, ghost = false, note = '' } = {}) => {
      const b = el('button', 'nav' + (state.bot === id ? ' sel' : '') + (ghost ? ' ghost' : ''));
      if (dot) b.append(el('span', 'dot ' + dot));
      b.append(el('span', 'name', label));
      if (note) b.append(el('span', 'envpill', note));
      b.append(el('span', 'badge', badge ? String(badge) : ''));
      if (!ghost) b.addEventListener('click', () => showBot(id));
      return b;
    };
    const total = envBots().reduce((n, b) => n + (state.waiting[b.id] ?? 0), 0);
    const parts = [el('div', 'navlabel', state.env === 'production' ? 'Live' : 'Staging'), item('Overview', null, { badge: total })];
    parts.push(el('div', 'navlabel', 'Bots'));
    for (const b of envBots()) {
      const s = summaryOf(b.id);
      parts.push(item(b.name, b.id, { dot: !s ? '' : s.up ? (isLive(b) ? 'live' : 'stage') : 'bad', badge: state.waiting[b.id] ?? 0, ghost: b.kind !== 'gateway' && false }));
    }
    // Bots that exist only on the other side (for example nothing live yet).
    const here = new Set(envBots().map((b) => b.name));
    for (const name of [...new Set(state.bots.map((b) => b.name))].filter((n) => !here.has(n))) {
      parts.push(item(name, '__none', { ghost: true, note: state.env === 'production' ? 'not live yet' : 'no staging' }));
    }
    nav.replaceChildren(...parts);
    const count = (env) => state.bots.filter((b) => b.env === env).reduce((n, b) => n + (state.waiting[b.id] ?? 0), 0);
    $('#badgelive').textContent = count('production') || '';
    $('#badgestage').textContent = count('staging') || '';
  }

  // ---- overview: headline numbers, then one card per bot
  function renderOverview() {
    const box = $('#overview');
    const list = state.summaries.filter((s) => s.env === state.env);
    const sum = (f) => list.reduce((n, s) => n + (Number(f(s.summary || {})) || 0), 0);
    const live = state.env === 'production';
    const head = el('div');
    const h1 = el('h1', '', live ? 'Live bots' : 'Staging bots');
    h1.append(el('span', 'envpill ' + (live ? 'live' : 'stage'), live ? 'what visitors use' : 'for testing only'));
    head.append(h1, el('div', 'sub', live ? 'Real visitors on the public sites.' : 'Test copies, separate from the live bots. Chats here never reach real visitors.'));
    const kpis = el('div', 'kpis');
    const kpi = (label, value, alert) => { const k = el('div', 'kpi' + (alert ? ' alert' : '')); k.append(el('div', 'l', label), el('div', 'v', value)); kpis.append(k); };
    const waiting = sum((s) => s.conversations?.waiting_for_person);
    kpi('Waiting for a person', String(waiting), waiting > 0);
    kpi('Chats today', String(sum((s) => s.conversations?.today)));
    kpi('Spend today', money(sum((s) => s.spend?.today_usd)) + ' of ' + money(sum((s) => s.spend?.cap_usd)));
    const errors = sum((s) => s.errors_today);
    kpi('Errors today', String(errors), errors > 0);
    kpi('Team online', String(Math.max(0, ...list.map((s) => s.summary?.staff_online ?? 0))));
    const cards = el('div', 'cards');
    for (const s of list) {
      const b = botById(s.id) || s;
      const card = el('div', 'card');
      const h = el('div', 'head');
      h.append(el('span', 'dot ' + (s.up ? (live ? 'live' : 'stage') : 'bad')), el('span', 'name', s.name), el('span', 'envpill ' + (live ? 'live' : 'stage'), live ? 'live' : 'staging'));
      card.append(h);
      const stat = (label, value) => { const r = el('div', 'stat'); r.append(el('span', '', label), el('b', '', value)); card.append(r); };
      const m = s.summary;
      if (!s.up) card.append(el('div', 'note', 'Unreachable: ' + (s.error || 'no answer')));
      else if (s.kind === 'status') { for (const [k, v] of Object.entries(m || {}).slice(0, 8)) if (typeof v !== 'object') stat(k.replace(/_/g, ' '), String(v)); }
      else if (!m) card.append(el('div', 'note', s.note || 'No summary yet.'));
      else {
        stat('Waiting for a person', String(m.conversations?.waiting_for_person ?? 0));
        stat('Chats today', String(m.conversations?.today ?? 0));
        stat('Errors today', String(m.errors_today ?? 0));
        const cap = m.spend?.cap_usd || 0, spent = m.spend?.today_usd || 0;
        stat('Spend today', money(spent) + (m.spend?.cap_on ? ' of ' + money(cap) : ' · no cap!'));
        const bar = el('div', 'bar' + (cap && spent / cap > 0.8 ? ' hot' : ''));
        const fill = el('i'); fill.style.width = Math.min(100, cap ? (spent / cap) * 100 : 0) + '%'; bar.append(fill); card.append(bar);
        stat('Online hours', hoursLabel(scheduleOf(s.id)) + (state.onCall[s.id] ? ' · on call now' : ''));
        stat('Version', String(m.version || '?'));
      }
      if (b.kind === 'gateway') { const open = el('button', '', 'Open conversations'); open.addEventListener('click', () => showBot(s.id)); card.append(open); }
      cards.append(card);
    }
    if (!list.length) cards.append(el('div', 'empty', 'No bots here yet.'));
    box.replaceChildren(head, kpis, cards);
  }

  async function pollSummaries() {
    try {
      const { bots } = await raw('/bots/summary');
      for (const b of bots) {
        const w = b.summary?.conversations?.waiting_for_person ?? 0;
        if (w > (state.waiting[b.id] ?? 0)) alertNewChat('Waiting on ' + b.name + (b.env === 'staging' ? ' (staging)' : ''), b.name);
        state.waiting[b.id] = w;
      }
      state.summaries = bots;
      renderSidebar();
      if (!state.bot) renderOverview(); else renderBotStats();
    } catch (e) { log('summary failed: ' + e.message); }
  }

  // ---- one bot
  function renderBotStats() {
    const s = summaryOf(state.bot), m = s?.summary || {};
    const box = $('#botstats');
    const rows = [['Status', s ? (s.up ? 'up' : 'unreachable') : '…'], ['Chats today', m.conversations?.today ?? '–'], ['Chats kept', m.conversations?.total ?? '–'],
      ['Team online', m.staff_online ?? '–'], ['Errors today', m.errors_today ?? '–'], ['Spend today', m.spend ? money(m.spend.today_usd) + ' of ' + money(m.spend.cap_usd) : '–'],
      ['Online hours', hoursLabel(scheduleOf(state.bot)) + (state.onCall[state.bot] ? ' · on call now' : '')], ['Version', m.version || '–']];
    box.replaceChildren(...rows.map(([k, v]) => { const r = el('div', 'stat'); r.append(el('span', '', k), el('b', '', String(v))); return r; }));
  }
  function showList(which) {
    state.list = which;
    for (const w of ['waiting', 'recent', 'blocked']) $('#t-' + w).classList.toggle('on', w === which);
    $('#queue').hidden = which !== 'waiting'; $('#convs').hidden = which !== 'recent'; $('#blocks').hidden = which !== 'blocked';
  }
  $('#t-waiting').addEventListener('click', () => showList('waiting'));
  $('#t-recent').addEventListener('click', () => showList('recent'));
  $('#t-blocked').addEventListener('click', () => showList('blocked'));

  async function showBot(id) {
    state.gen++;
    if (state.available) await setAvailable(false);
    state.bot = id; state.selected = null; state.latest = 0;
    history.replaceState(null, '', id ? '/?bot=' + encodeURIComponent(id) : '/');
    renderSidebar();
    $('#overview').hidden = Boolean(id); $('#botview').hidden = !id;
    if (!id) { renderOverview(); return; }
    const b = botById(id);
    $('#botname').replaceChildren(document.createTextNode(b.name + ' '), el('span', 'envpill ' + (isLive(b) ? 'live' : 'stage'), isLive(b) ? 'live' : 'staging'));
    $('#botsub').textContent = isLive(b) ? 'Real visitors. Take over to reply as yourself.' : 'Test copy. Safe to try things.';
    $('#transcript').replaceChildren(); $('#title').textContent = 'Select a conversation'; $('#log').textContent = '';
    for (const id2 of ['takeover', 'handback', 'resolve', 'reopen', 'block', 'reply', 'send']) $('#' + id2).disabled = true;
    $('#contact').hidden = true; $('#card').hidden = true; $('#mail').hidden = true; $('#state').textContent = '';
    renderBotStats();
    showList(state.list);
    try {
      const { latest } = await api('/ops/events?after=0&wait=0');
      state.latest = latest;
      await Promise.all([refreshQueue(), refreshConversations(), refreshBlocks()]);
      if (!state.waiting[id]) showList('recent');
    } catch (e) { log(b.name + ' unreachable: ' + e.message); }
    listen();
  }

  async function refreshQueue() {
    const { waiting } = await api('/ops/queue');
    $('#qcount').textContent = waiting.length || '';
    const q = $('#queue');
    q.replaceChildren();
    q.className = waiting.length ? '' : 'empty';
    if (!waiting.length) q.textContent = 'Nobody waiting.';
    for (const w of waiting) {
      const item = el('div', 'item waiting');
      const left = Math.max(0, Math.round((w.expires_at - Date.now()) / 1000));
      item.append(el('div', '', w.summary || 'Visitor asked for a person'), el('small', '', 'waiting ' + ago(w.since) + ' · ' + left + 's left to accept'));
      const accept = el('button', 'primary', 'Accept');
      const row = el('div', 'row'); row.append(accept); item.append(row);
      accept.addEventListener('click', async (e) => { e.stopPropagation(); await api('/ops/accept', { session_id: w.session_id }); log('accepted ' + w.session_id.slice(0, 8)); await select(w.session_id); refreshQueue(); });
      item.addEventListener('click', () => select(w.session_id));
      q.append(item);
    }
  }
  async function refreshConversations() {
    const { conversations } = await api('/ops/conversations');
    const c = $('#convs');
    c.replaceChildren();
    const recent = conversations.sort((a, b) => b.last_at - a.last_at).slice(0, 40);
    c.className = recent.length ? '' : 'empty';
    if (!recent.length) c.textContent = 'No conversations yet.';
    for (const v of recent) {
      const item = el('div', 'item' + (v.session_id === state.selected ? ' sel' : ''));
      const tags = [v.messages + ' messages', ago(v.last_at)];
      if (v.person) tags.push(v.person.state);
      if (v.ended) tags.push(ENDED[v.ended.reason] || 'ended: ' + v.ended.reason);
      if (v.contact) tags.push('left their email');
      item.append(el('div', '', 'Visitor ' + v.visitor_id.slice(0, 8)), el('small', '', tags.join(' · ')));
      item.addEventListener('click', () => select(v.session_id));
      c.append(item);
    }
  }
  async function refreshBlocks() {
    const { blocks } = await api('/ops/blocks');
    const box = $('#blocks');
    box.replaceChildren();
    box.className = blocks.length ? '' : 'empty';
    if (!blocks.length) box.textContent = 'Nobody blocked.';
    for (const b of blocks.sort((x, y) => y.at - x.at)) {
      const item = el('div', 'item');
      item.append(el('div', '', 'Visitor ' + String(b.visitor_id || '?').slice(0, 8)), el('small', '', [b.by ? 'by ' + b.by : '', 'until ' + new Date(b.until).toLocaleDateString()].filter(Boolean).join(' · ')));
      const undo = el('button', '', 'Unblock');
      undo.addEventListener('click', async (e) => { e.stopPropagation(); await api('/ops/unblock', { net: b.net }); log('unblocked ' + String(b.visitor_id).slice(0, 8)); refreshBlocks(); });
      const row = el('div', 'row'); row.append(undo); item.append(row);
      if (b.session_id) item.addEventListener('click', () => select(b.session_id));
      box.append(item);
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
    if (!state.selected || !state.bot) return;
    const t = await api('/ops/transcript?session_id=' + encodeURIComponent(state.selected));
    const human = t.takeover?.state === 'human';
    $('#title').textContent = 'Visitor ' + (t.visitor_id || state.selected).slice(0, 8);
    const agent = t.agent_label || 'AI';
    $('#state').textContent = t.ended ? (ENDED[t.ended.reason] || 'Ended (' + t.ended.reason + ')') + (t.ended.by ? ' by ' + t.ended.by : '') + (t.blocked ? ' · network blocked' : '')
      : human ? 'You are replying in this chat' : agent + ' is answering';
    $('#takeover').disabled = human || Boolean(t.ended);
    $('#handback').disabled = !human;
    $('#resolve').disabled = Boolean(t.ended);
    $('#block').disabled = t.ended?.reason === 'blocked';
    $('#reopen').disabled = !t.ended;
    $('#card').hidden = !t.proposal;
    if (t.proposal) $('#card').textContent = 'Waiting for the visitor to send their card: ' + [t.proposal.name, t.proposal.email, t.proposal.need].filter(Boolean).join(' · ');
    const contact = t.contact;
    $('#contact').hidden = !contact;
    $('#mail').hidden = !contact;
    if (contact) {
      $('#contact').textContent = 'Left their details: ' + [contact.name, contact.email, contact.need].filter(Boolean).join(' · ');
      const last = t.messages.filter((m) => m.from === 'visitor').slice(-1)[0];
      $('#mail').href = 'mailto:' + encodeURIComponent(contact.email) + '?subject=' + encodeURIComponent('Following up on your chat')
        + '&body=' + encodeURIComponent((contact.name ? 'Hi ' + contact.name + ',' : 'Hi,') + '\n\n' + (last ? 'You asked: "' + last.text.slice(0, 300) + '"\n\n' : ''));
    }
    $('#vtyping').hidden = !t.visitor_typing;
    $('#reply').disabled = !human;
    $('#reply').placeholder = human ? 'Reply as yourself' : 'Take over to reply as yourself';
    $('#send').disabled = !human;
    const box = $('#transcript');
    const atBottom = box.scrollTop + box.clientHeight >= box.scrollHeight - 20;
    box.replaceChildren(...t.messages.map((m) => {
      const d = el('div', 'msg ' + m.from);
      const who = m.from === 'person' ? (m.author ? m.author.name + (m.author.role ? ' · ' + m.author.role : '') : 'A person') : m.from === 'agent' ? agent : 'Visitor';
      d.append(el('b', '', who), document.createTextNode(m.text));
      return d;
    }));
    if (atBottom) box.scrollTop = box.scrollHeight;
  }
  const act = (path) => async () => { if (!state.selected) return; await api(path, { session_id: state.selected }); await refreshTranscript(); refreshConversations(); };
  $('#takeover').addEventListener('click', act('/ops/accept'));
  $('#handback').addEventListener('click', act('/ops/handback'));
  $('#reopen').addEventListener('click', async () => { await act('/ops/reopen')(); refreshBlocks().catch(() => {}); });
  $('#resolve').addEventListener('click', act('/ops/resolve'));
  $('#block').addEventListener('click', async () => {
    if (!state.selected || !confirm('Block this visitor? The chat closes, and their network cannot use the chat for 30 days. You can unblock them from Blocked.')) return;
    const { network } = await api('/ops/block', { session_id: state.selected });
    log('blocked ' + state.selected.slice(0, 8) + (network ? ' and their network' : ' (this browser only)'));
    await refreshTranscript(); refreshConversations(); refreshBlocks().catch(() => {});
  });
  async function send() {
    const content = $('#reply').value.trim();
    if (!content || !state.selected) return;
    $('#send').disabled = true;
    try { await api('/ops/reply', { session_id: state.selected, content }); $('#reply').value = ''; typedAt = 0; await refreshTranscript(); }
    catch (e) { log('reply failed: ' + e.message); }
    finally { $('#send').disabled = false; }
  }
  $('#send').addEventListener('click', send);
  let typedAt = 0;
  $('#reply').addEventListener('input', () => {
    if (!state.selected || !$('#reply').value.trim() || Date.now() - typedAt < 3000) return;
    typedAt = Date.now();
    api('/ops/typing', { session_id: state.selected }).catch(() => {});
  });
  $('#reply').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) send(); });

  // ---- live events for the bot on screen: long poll; the gateway answers as soon as something happens
  async function listen() {
    const gen = state.gen;
    while (gen === state.gen && state.bot) {
      try {
        const { events, latest } = await api('/ops/events?after=' + state.latest + '&wait=25');
        if (gen !== state.gen) return;
        state.latest = latest;
        for (const e of events) {
          if (e.type === 'visitor.typing' || e.type === 'person.typing' || e.type === 'person.replied') {
            if (e.session_id === state.selected) refreshTranscript().catch(() => {});
            continue;
          }
          log(e.type.replace('.', ' ') + (e.session_id ? ' · ' + e.session_id.slice(0, 8) : '') + (e.summary ? ' — ' + e.summary : '') + (e.reason ? ' — ' + e.reason : ''));
          if (e.type === 'handoff.requested' && e.staff_online !== false) alertNewChat(e.summary, botById(state.bot)?.name);
          if (e.session_id && e.session_id === state.selected) refreshTranscript().catch(() => {});
        }
        if (events.some((e) => !/typing|replied/.test(e.type))) { refreshQueue().catch(() => {}); refreshConversations().catch(() => {}); }
      } catch (e) {
        if (e.status === 403) { log('signed out: reload the page'); return; }
        await new Promise((r) => setTimeout(r, 3000));
      }
    }
  }

  // ---- online hours: one simple schedule for all bots; a bot may have its own (Advanced)
  const zoneName = (tz) => (ZONES.find(([z]) => z === tz) || [tz, tz])[1];
  const scheduleOf = (id) => (state.hours?.bots?.[id]?.own ? state.hours.bots[id].schedule : state.hours?.all) || { mode: 'off', tz: 'UTC', week: {} };
  function presetOf(s) {
    const ranges = DAYS.map((d) => JSON.stringify(s.week?.[d] || []));
    const first = (s.week && Object.values(s.week).find((r) => r && r.length)) || [['09:00', '17:00']];
    const same = (days) => DAYS.every((d) => JSON.stringify(s.week?.[d] || []) === (days.includes(d) ? JSON.stringify([first[0]]) : '[]'));
    const preset = same(PRESETS.every) ? 'every' : same(PRESETS.sunthu) ? 'sunthu' : same(PRESETS.monfri) ? 'monfri' : ranges.some((r) => r !== '[]') ? 'custom' : 'every';
    return { preset, from: first[0][0], to: first[0][1] };
  }
  function hoursLabel(s) {
    if (!s || s.mode === 'off') return 'off';
    if (s.mode === 'always') return 'always on';
    const p = presetOf(s);
    const days = { every: 'every day', sunthu: 'Sun–Thu', monfri: 'Mon–Fri', custom: 'custom days' }[p.preset];
    return p.from + '–' + p.to + ' ' + zoneName(s.tz) + ', ' + days;
  }
  async function refreshHours() {
    try { const r = await raw('/hours'); state.hours = r.hours; state.onCall = r.on_call_now || {}; } catch { return; }
    const s = state.hours.all;
    const anyOn = Object.values(state.onCall).some(Boolean);
    $('#hoursdot').className = 'dot' + (anyOn ? ' on' : '');
    $('#hourstext').textContent = s.mode === 'off' ? 'Online hours: off' : (anyOn ? 'On call now · ' : 'Off hours · ') + hoursLabel(s);
  }
  const times = () => { const out = []; for (let h = 0; h <= 24; h++) for (const m of ['00', '30']) { const t = String(h).padStart(2, '0') + ':' + m; if (t <= '24:00') out.push(t); } return out; };
  function editor(schedule, key) {
    const p = presetOf(schedule);
    const wrap = el('div');
    wrap.dataset.key = key;
    const choices = el('div', 'choices');
    const pick = { value: schedule.mode === 'hours' ? p.preset === 'custom' ? 'every' : p.preset : schedule.mode };
    const opts = [['off', 'Off'], ['every', 'Every day'], ['sunthu', 'Sun–Thu'], ['monfri', 'Mon–Fri'], ['always', 'Always on']];
    const paint = () => { for (const c of choices.children) c.classList.toggle('on', c.dataset.v === pick.value); timesRow.hidden = pick.value === 'off' || pick.value === 'always'; };
    for (const [v, t] of opts) { const c = el('button', 'choice', t); c.type = 'button'; c.dataset.v = v; c.addEventListener('click', () => { pick.value = v; paint(); }); choices.append(c); }
    const timesRow = el('div', 'times');
    const sel = (value, list, fmt = (x) => x) => { const s = el('select'); for (const x of list) { const o = el('option', '', fmt(x)); o.value = Array.isArray(x) ? x[0] : x; s.append(o); } s.value = value; return s; };
    const from = sel(p.from, times()), to = sel(p.to, times()), tz = sel(schedule.tz && ZONES.some(([z]) => z === schedule.tz) ? schedule.tz : 'Asia/Jerusalem', ZONES, (z) => z[1] + ' time');
    timesRow.append(el('span', 'note', 'From'), from, el('span', 'note', 'to'), to, tz);
    wrap.append(choices, timesRow);
    wrap.read = () => {
      const week = Object.fromEntries(DAYS.map((d) => [d, []]));
      if (pick.value === 'off' || pick.value === 'always') return { mode: pick.value, tz: tz.value, week };
      if (from.value >= to.value) throw new Error('"From" must be before "to"');
      for (const d of PRESETS[pick.value]) week[d] = [[from.value, to.value]];
      return { mode: 'hours', tz: tz.value, week };
    };
    paint();
    return wrap;
  }
  async function openHours() {
    await refreshHours();
    const form = $('#hoursform');
    const all = editor(state.hours.all, 'all');
    const intro = el('p', 'note', 'During online hours you count as available on every bot: visitors who ask for a person are offered you, and you get an alert to join. Outside them, visitors leave their email. The "I\'m available" switch on a bot works any time.');
    const adv = el('details');
    adv.append(el('summary', '', 'Advanced: a bot with its own hours'));
    const own = {};
    for (const b of state.bots.filter((x) => x.kind === 'gateway')) {
      const row = el('div', 'botrow');
      const lab = el('label', 'switch'); const cb = el('input'); cb.type = 'checkbox'; cb.checked = state.hours.bots[b.id]?.own === true;
      lab.append(cb, el('span', '', b.name + ' · ' + (isLive(b) ? 'live' : 'staging') + ' uses its own hours'));
      const ed = editor(scheduleOf(b.id), b.id); ed.hidden = !cb.checked; cb.addEventListener('change', () => { ed.hidden = !cb.checked; });
      row.append(lab); adv.append(row, ed); own[b.id] = { cb, ed };
    }
    const msg = el('span', 'note');
    const save = el('button', 'primary', 'Save'); save.type = 'button';
    const cancel = el('button', 'ghost', 'Close'); cancel.type = 'button'; cancel.addEventListener('click', () => $('#hoursdlg').close());
    save.addEventListener('click', async () => {
      try {
        const body = { all: all.read(), bots: {} };
        for (const [id, o] of Object.entries(own)) body.bots[id] = o.cb.checked ? { own: true, schedule: o.ed.read() } : { own: false };
        await raw('/hours', body);
        await refreshHours(); renderOverview(); if (state.bot) renderBotStats();
        $('#hoursdlg').close();
      } catch (e) { msg.textContent = 'Not saved: ' + e.message; }
    });
    const foot = el('div', 'row'); foot.append(msg, el('span', 'spacer')); const btns = el('div', 'actions'); btns.append(cancel, save); foot.append(btns);
    form.replaceChildren(el('h2', '', 'Online hours'), intro, all, adv, foot);
    $('#hoursdlg').showModal();
  }
  $('#hoursbtn').addEventListener('click', () => openHours().catch((e) => log('hours failed: ' + e.message)));

  (async () => {
    state.me = await raw('/me');
    $('#who').textContent = state.me.name + (state.me.on_call ? ' · on call' : '');
    state.bots = (await raw('/bots')).bots;
    await refreshHours();
    const params = new URLSearchParams(location.search);
    const asked = botById(params.get('bot') || '');
    if (asked) { state.env = asked.env; $('#envlive').classList.toggle('on', asked.env === 'production'); $('#envstage').classList.toggle('on', asked.env === 'staging'); }
    await pollSummaries();
    await showBot(asked && asked.kind === 'gateway' ? asked.id : null);
    // A phone alert links here with ?bot=ID&session=ID: open that conversation.
    const linked = params.get('session');
    if (linked && state.bot && /^[A-Za-z0-9_-]{1,128}$/.test(linked)) select(linked).catch((e) => log('could not open the linked chat: ' + e.message));
    setInterval(() => { if (state.bot) refreshQueue().catch(() => {}); }, 5000);
    setInterval(pollSummaries, 15000);
    setInterval(refreshHours, 60000);
  })().catch((e) => { document.body.prepend(el('div', 'info warm', 'Bots HQ failed to start: ' + e.message)); });
}

// Bundlers that keep function names (wrangler's esbuild does) insert calls to a
// module-level __name helper into hqApp's source; give the browser a no-op one.
export const PAGE_JS = 'var __name = (fn) => fn;\n(' + hqApp.toString() + ')();\n';
