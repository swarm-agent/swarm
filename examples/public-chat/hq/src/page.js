// The Bots HQ page: the staff console for several bots. consoleApp runs in the
// browser (served as /hq.js, so the page needs no inline script); it talks only
// to this Worker: /bots, /bots/summary, /hours and the /ops routes (with ?bot=).
// Everything from visitors or the model is rendered as text, never HTML.

export const PAGE_HTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Bots HQ</title>
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
  button.danger { border-color:#f1c4c4; color:#b42318; }
  button:disabled { opacity:.45; cursor:default; }
  a.button { display:inline-block; padding:6px 12px; border-radius:8px; border:1px solid var(--accent); color:var(--accent); text-decoration:none; }
  #contact { margin:6px 0 10px; padding:8px 10px; background:#f0f4ff; border-radius:8px; }
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
  #card { margin:6px 0 10px; padding:8px 10px; background:#fff7ed; border-radius:8px; }
  #vtyping { font-size:12px; color:var(--mute); padding:2px 4px 0; display:flex; align-items:center; gap:6px; }
  #vtyping[hidden] { display:none; }
  #vtyping i { width:5px; height:5px; border-radius:50%; background:var(--mute); animation:tdot 1.1s infinite; }
  #vtyping i:nth-child(2) { animation-delay:.15s; } #vtyping i:nth-child(3) { animation-delay:.3s; }
  @keyframes tdot { 0%,60%,100% { opacity:.3; } 30% { opacity:1; } }
  textarea { width:100%; min-height:70px; font:inherit; padding:8px; border:1px solid var(--line); border-radius:8px; resize:vertical; }
  .empty { color:var(--mute); }
  nav#tabs { display:flex; gap:6px; flex-wrap:wrap; padding:10px 20px 0; }
  .tab { padding:6px 12px; border:1px solid var(--line); border-radius:999px; background:var(--card); cursor:pointer; display:inline-flex; gap:6px; align-items:center; }
  .tab.sel { border-color:var(--accent); color:var(--accent); }
  .tab .env { font-size:11px; color:var(--mute); }
  .badge { min-width:18px; padding:0 6px; border-radius:999px; background:var(--warn); color:#fff; font-size:11px; text-align:center; }
  .badge:empty { display:none; }
  #overview { display:grid; grid-template-columns:repeat(auto-fill,minmax(260px,1fr)); gap:16px; padding:16px 20px; }
  .bot h3 { font-size:15px; display:flex; justify-content:space-between; gap:8px; align-items:center; }
  .bot dl { display:grid; grid-template-columns:auto 1fr; gap:4px 12px; margin-top:10px; }
  .bot dt { color:var(--mute); } .bot dd { margin:0; text-align:right; }
  .up { color:var(--ok); } .down { color:#b42318; }
  #hourspanel { padding:16px 20px; }
  #hourspanel table { border-collapse:collapse; margin:8px 0; }
  #hourspanel td { padding:4px 8px; vertical-align:top; }
  #hourspanel input[type=text] { width:150px; font:inherit; padding:4px 6px; border:1px solid var(--line); border-radius:6px; }
  #hourspanel select { font:inherit; padding:4px; }
  .hint { color:var(--mute); font-size:12px; }
  @media (max-width:800px) { main { grid-template-columns:1fr; } }
</style></head>
<body>
<header>
  <h1>Bots HQ</h1>
  <span id="who" class="empty"></span>
  <label class="pill" id="availpill" hidden><span id="dot" class="dot"></span><input id="avail" type="checkbox"> Available on this bot</label>
  <button id="hoursbtn">Online hours</button>
  <button id="alerts">Enable alerts</button>
</header>
<nav id="tabs"></nav>
<div id="overview"></div>
<section id="hourspanel" hidden></section>
<main id="botview" hidden>
  <div class="stack">
    <section><h2>Waiting for a person</h2><div id="queue" class="empty">Nobody waiting.</div></section>
    <section><h2>Recent conversations</h2><div id="convs" class="empty">None yet.</div></section>
    <section><h2>Blocked</h2><div id="blocks" class="empty">Nobody blocked.</div></section>
  </div>
  <div class="stack">
    <section>
      <div class="row"><h2 id="title">Select a conversation</h2>
        <span><button id="takeover" disabled>Take over</button> <button id="handback" disabled>Hand back</button> <button id="resolve" disabled>Resolve</button> <button id="block" class="danger" disabled>Block</button> <button id="reopen" disabled>Reopen</button> <a id="mail" class="button" hidden>Email them</a></span></div>
      <div id="contact" class="empty" hidden></div>
      <div id="card" hidden></div>
      <div id="transcript"></div>
      <div id="vtyping" hidden><i></i><i></i><i></i> Visitor is typing…</div>
      <div style="margin-top:10px"><textarea id="reply" placeholder="Reply as yourself (take over first)" disabled></textarea>
      <div class="row" style="margin-top:8px"><small id="state" class="empty"></small><button id="send" class="primary" disabled>Send</button></div></div>
    </section>
    <section><h2>Live events</h2><div id="log"></div></section>
  </div>
</main>
<script src="/hq.js" defer></script>
</body></html>`;

function consoleApp() {
  const $ = (s) => document.querySelector(s);
  const ENDED = { resolved: 'Resolved', blocked: 'Blocked', lead_saved: 'Left their details', abuse: 'Ended by the AI (abuse)', cannot_help: 'Ended by the AI' };
  const state = { latest: 0, selected: null, available: false, beat: null, me: null, queue: [], audio: null, bot: 'all', bots: [], gen: 0, waiting: {}, hours: null };
  // Every /ops call goes to the bot on screen: ?bot= on reads, "bot" in the body on writes.
  const withBot = (path) => path.startsWith('/ops/') ? path + (path.includes('?') ? '&' : '?') + 'bot=' + encodeURIComponent(state.bot) : path;
  const raw = async (path, body) => {
    const res = await fetch(path, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw Object.assign(new Error(data.error || String(res.status)), { status: res.status });
    return data;
  };
  const api = async (path, body) => {
    if (path.startsWith('/ops/') && body !== undefined) return raw(path, { ...body, bot: state.bot });
    const res = await fetch(withBot(path), body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
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
    if (state.available) fetch('/ops/presence', { method: 'POST', keepalive: true, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ available: false, bot: state.bot }) });
  });

  // ---- alerts: desktop notification + a short tone when someone asks for a person
  $('#alerts').addEventListener('click', async () => {
    state.audio = state.audio || new AudioContext();
    if ('Notification' in window) await Notification.requestPermission();
    $('#alerts').textContent = Notification.permission === 'granted' ? 'Alerts on' : 'Sound only';
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
      if (v.ended) tags.push(ENDED[v.ended.reason] || 'ended: ' + v.ended.reason);
      if (v.contact) tags.push('email left');
      item.append(el('div', '', 'Visitor ' + v.visitor_id.slice(0, 8)), el('small', '', tags.join(' · ')));
      item.addEventListener('click', () => select(v.session_id));
      c.append(item);
    }
  }

  // ---- blocked networks: each lapses on its own after 30 days, or unblock here
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
      item.append(undo);
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
    if (!state.selected) return;
    const t = await api('/ops/transcript?session_id=' + encodeURIComponent(state.selected));
    const human = t.takeover?.state === 'human';
    $('#title').textContent = 'Conversation ' + state.selected.slice(0, 8);
    const agent = t.agent_label || 'AI worker';
    $('#state').textContent = t.ended ? (ENDED[t.ended.reason] || 'Ended (' + t.ended.reason + ')') + (t.ended.by ? ' by ' + t.ended.by : '') + (t.blocked ? ' · network blocked' : '')
      : human ? 'You hold this conversation' : agent + ' is answering';
    $('#takeover').disabled = human || Boolean(t.ended);
    $('#handback').disabled = !human;
    $('#resolve').disabled = Boolean(t.ended);
    $('#block').disabled = t.ended?.reason === 'blocked';
    $('#reopen').disabled = !t.ended;
    // The card the agent filled in, while it waits for the visitor to send it.
    $('#card').hidden = !t.proposal;
    if (t.proposal) $('#card').textContent = 'Waiting for the visitor to send the card: ' + [t.proposal.name, t.proposal.email, t.proposal.need].filter(Boolean).join(' · ');
    // Contact details the visitor left (form or the agent's capture_lead): reply by email.
    const contact = t.contact;
    $('#contact').hidden = !contact;
    $('#mail').hidden = !contact;
    if (contact) {
      $('#contact').textContent = [contact.name, contact.email, contact.need].filter(Boolean).join(' · ');
      const last = t.messages.filter((m) => m.from === 'visitor').slice(-1)[0];
      $('#mail').href = 'mailto:' + encodeURIComponent(contact.email) + '?subject=' + encodeURIComponent('Following up on your chat')
        + '&body=' + encodeURIComponent((contact.name ? 'Hi ' + contact.name + ',' : 'Hi,') + '\n\n' + (last ? 'You asked: "' + last.text.slice(0, 300) + '"\n\n' : ''));
    }
    $('#vtyping').hidden = !t.visitor_typing;
    $('#reply').disabled = !human;
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
    if (!state.selected || !confirm('Block this visitor? The chat closes, and their network cannot use the chat for 30 days. You can unblock them from the Blocked list.')) return;
    const { network } = await api('/ops/block', { session_id: state.selected });
    log('blocked ' + state.selected.slice(0, 8) + (network ? ' and their network' : ' (no network known: this browser only)'));
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
  // While you type a reply, the visitor sees "<your name> is typing" (sent at most every 3 s).
  let typedAt = 0;
  $('#reply').addEventListener('input', () => {
    if (!state.selected || !$('#reply').value.trim() || Date.now() - typedAt < 3000) return;
    typedAt = Date.now();
    api('/ops/typing', { session_id: state.selected }).catch(() => {});
  });
  $('#reply').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) send(); });

  // ---- live events: long poll; the gateway answers as soon as something happens
  async function listen() {
    const gen = state.gen;
    while (gen === state.gen) {
      try {
        const { events, latest } = await api('/ops/events?after=' + state.latest + '&wait=25');
        if (gen !== state.gen) return;
        state.latest = latest;
        for (const e of events) {
          // Typing marks only refresh the open conversation; they are not worth a log line.
          if (e.type === 'visitor.typing' || e.type === 'person.typing' || e.type === 'person.replied') {
            if (e.session_id === state.selected) refreshTranscript().catch(() => {});
            continue;
          }
          log(e.type + (e.session_id ? ' ' + e.session_id.slice(0, 8) : '') + (e.summary ? ' — ' + e.summary : '') + (e.reason ? ' — ' + e.reason : ''));
          if (e.type === 'handoff.requested' && e.staff_online !== false) alertNewChat(e.summary, botName(state.bot));
          if (e.session_id && e.session_id === state.selected) refreshTranscript().catch(() => {});
        }
        if (events.some((e) => !/typing|replied/.test(e.type))) { refreshQueue().catch(() => {}); refreshConversations().catch(() => {}); }
      } catch (e) {
        if (e.status === 403) { log('signed out: reload the page'); setAvailable(false); return; }
        await new Promise((r) => setTimeout(r, 3000));
      }
    }
  }

  // ---- bots: tabs, the overview, badges and notifications across all bots
  const botName = (id) => (state.bots.find((b) => b.id === id) || {}).name || id;
  const envLabel = (b) => b.env === 'staging' ? 'staging' : 'live';
  function renderTabs() {
    const nav = $('#tabs');
    const tab = (id, label, env) => {
      const t = el('button', 'tab' + (state.bot === id ? ' sel' : ''));
      t.append(el('span', '', label));
      if (env) t.append(el('span', 'env', env));
      const badge = el('span', 'badge', state.waiting[id] ? String(state.waiting[id]) : '');
      if (id === 'all') badge.textContent = String(Object.values(state.waiting).reduce((a, b) => a + b, 0) || '');
      t.append(badge);
      t.addEventListener('click', () => showBot(id));
      return t;
    };
    nav.replaceChildren(tab('all', 'All bots'), ...state.bots.map((b) => tab(b.id, b.name, envLabel(b))));
  }
  const money = (n) => '$' + Number(n || 0).toFixed(Number(n) < 1 ? 4 : 2);
  function renderOverview(list) {
    const box = $('#overview');
    box.replaceChildren(...list.map((b) => {
      const card = el('section', 'bot');
      const h = el('h3');
      h.append(el('span', '', b.name + ' · ' + envLabel(b)), el('span', b.up ? 'up' : 'down', b.up ? 'up' : 'down'));
      card.append(h);
      const dl = el('dl');
      const row = (k, v) => dl.append(el('dt', '', k), el('dd', '', v));
      const s = b.summary || {};
      if (!b.up) row('problem', b.error || 'unreachable');
      if (b.kind === 'status') {
        for (const [k, v] of Object.entries(s).slice(0, 10)) if (typeof v !== 'object') row(k.replace(/_/g, ' '), String(v));
      } else if (b.up) {
        row('waiting for a person', String(s.conversations?.waiting_for_person ?? 0));
        row('chats today', String(s.conversations?.today ?? 0));
        row('chats kept', String(s.conversations?.total ?? 0));
        row('staff online', String(s.staff_online ?? 0));
        row('errors today', String(s.errors_today ?? 0));
        row('spend today', s.spend ? money(s.spend.today_usd) + (s.spend.cap_on ? ' of ' + money(s.spend.cap_usd) : ' (no cap!)') + (s.spend.over ? ' · OVER CAP' : '') : 'unknown');
        row('running since', s.started_at ? new Date(s.started_at).toLocaleString() : '?');
        if (state.hours) row('online hours', describe(scheduleOf(b.id)));
      }
      if (b.kind === 'gateway') { const open = el('button', '', 'Open'); open.style.marginTop = '10px'; open.addEventListener('click', () => showBot(b.id)); card.append(open); }
      return card;
    }));
  }
  async function pollSummaries() {
    try {
      const { bots } = await raw('/bots/summary');
      for (const b of bots) {
        const w = b.summary?.conversations?.waiting_for_person ?? 0;
        if (w > (state.waiting[b.id] ?? 0)) alertNewChat('Waiting on ' + b.name + (b.env === 'staging' ? ' (staging)' : ''), b.name);
        state.waiting[b.id] = w;
      }
      renderTabs();
      if (state.bot === 'all') renderOverview(bots);
    } catch (e) { log('summary failed: ' + e.message); }
  }
  async function showBot(id) {
    state.gen++;
    if (state.available) await setAvailable(false);
    state.bot = id; state.selected = null; state.latest = 0;
    history.replaceState(null, '', id === 'all' ? '/' : '/?bot=' + encodeURIComponent(id));
    renderTabs();
    $('#hourspanel').hidden = true;
    const all = id === 'all';
    $('#overview').hidden = !all; $('#botview').hidden = all; $('#availpill').hidden = all;
    if (all) { pollSummaries(); return; }
    $('#transcript').replaceChildren(); $('#title').textContent = 'Select a conversation'; $('#log').textContent = '';
    try {
      const { latest } = await api('/ops/events?after=0&wait=0');
      state.latest = latest;
      await Promise.all([refreshQueue(), refreshConversations(), refreshBlocks()]);
    } catch (e) { log(botName(id) + ' unreachable: ' + e.message); }
    listen();
  }

  // ---- online hours: one schedule for all bots, or a bot's own
  const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
  const scheduleOf = (id) => (state.hours?.bots?.[id]?.own ? state.hours.bots[id].schedule : state.hours?.all) || { mode: 'off' };
  const describe = (s) => s.mode === 'off' ? 'off (toggle only)' : s.mode === 'always' ? 'always on' : 'scheduled (' + s.tz + ')';
  function scheduleEditor(schedule, prefix) {
    const wrap = el('div');
    const mode = el('select'); mode.id = prefix + '-mode';
    for (const [v, t] of [['off', 'Off: only the "Available" switch'], ['hours', 'During these hours'], ['always', 'Always online']]) { const o = el('option', '', t); o.value = v; mode.append(o); }
    mode.value = schedule.mode;
    const tz = el('input'); tz.type = 'text'; tz.id = prefix + '-tz'; tz.value = schedule.tz || Intl.DateTimeFormat().resolvedOptions().timeZone;
    const top = el('div'); top.append(mode, document.createTextNode(' time zone '), tz);
    const table = el('table');
    for (const d of DAYS) {
      const tr = el('tr'); const input = el('input'); input.type = 'text'; input.id = prefix + '-' + d; input.placeholder = 'e.g. 09:00-17:00';
      input.value = (schedule.week?.[d] || []).map((r) => r.join('-')).join(', ');
      tr.append(el('td', '', d), el('td')); tr.lastChild.append(input); table.append(tr);
    }
    wrap.append(top, table);
    return wrap;
  }
  function readEditor(prefix) {
    const week = {};
    for (const d of DAYS) {
      const text = $('#' + prefix + '-' + d).value.trim();
      week[d] = text ? text.split(',').map((r) => r.trim().split('-').map((t) => t.trim().padStart(5, '0'))) : [];
    }
    return { mode: $('#' + prefix + '-mode').value, tz: $('#' + prefix + '-tz').value.trim(), week };
  }
  async function openHours() {
    const panel = $('#hourspanel');
    if (!panel.hidden) { panel.hidden = true; return; }
    const { hours, on_call_now } = await raw('/hours');
    state.hours = hours;
    panel.replaceChildren();
    const card = el('section');
    card.append(el('h2', '', 'Online hours'), el('p', 'hint', 'During online hours the on-call team counts as available, so visitors are offered a person and you get a phone alert to join. Outside them, visitors leave their email. The "Available" switch on a bot works at any time.'));
    card.append(el('h3', '', 'All bots'), scheduleEditor(hours.all, 'all'));
    for (const b of state.bots.filter((x) => x.kind === 'gateway')) {
      const own = hours.bots[b.id]?.own === true;
      const box = el('div'); box.style.marginTop = '14px';
      const label = el('label'); const cb = el('input'); cb.type = 'checkbox'; cb.id = 'own-' + b.id; cb.checked = own;
      label.append(cb, document.createTextNode(' ' + b.name + ' (' + envLabel(b) + ') uses its own hours · on call now: ' + (on_call_now[b.id] ? 'yes' : 'no')));
      const editor = scheduleEditor(own ? hours.bots[b.id].schedule : hours.all, 'b-' + b.id);
      editor.hidden = !own; cb.addEventListener('change', () => { editor.hidden = !cb.checked; });
      box.append(label, editor); card.append(box);
    }
    const save = el('button', 'primary', 'Save hours'); save.style.marginTop = '14px';
    const msg = el('span', 'hint'); msg.style.marginLeft = '10px';
    save.addEventListener('click', async () => {
      try {
        const body = { all: readEditor('all'), bots: {} };
        for (const b of state.bots.filter((x) => x.kind === 'gateway')) body.bots[b.id] = $('#own-' + b.id).checked ? { own: true, schedule: readEditor('b-' + b.id) } : { own: false };
        const r = await raw('/hours', body); state.hours = r.hours; msg.textContent = 'Saved. The schedule applies within a minute.';
      } catch (e) { msg.textContent = 'Not saved: ' + e.message; }
    });
    card.append(save, msg);
    panel.append(card); panel.hidden = false;
  }
  $('#hoursbtn').addEventListener('click', () => openHours().catch((e) => log('hours failed: ' + e.message)));

  (async () => {
    state.me = await api('/me');
    $('#who').textContent = state.me.name + ' · ' + state.me.role + (state.me.on_call ? ' · on call' : '');
    state.bots = (await raw('/bots')).bots;
    try { state.hours = (await raw('/hours')).hours; } catch { state.hours = null; }
    const asked = new URLSearchParams(location.search).get('bot');
    await showBot(state.bots.some((b) => b.id === asked && b.kind === 'gateway') ? asked : 'all');
    // A phone alert links here with ?bot=ID&session=ID: open that conversation (Take over accepts it).
    const linked = new URLSearchParams(location.search).get('session');
    if (linked && state.bot !== 'all' && /^[A-Za-z0-9_-]{1,128}$/.test(linked)) select(linked).catch((e) => log('could not open the linked chat: ' + e.message));
    setInterval(() => { if (state.bot !== 'all') refreshQueue().catch(() => {}); }, 5000);
    setInterval(pollSummaries, 15000);
    pollSummaries();
  })().catch((e) => log('Bots HQ failed to start: ' + e.message));
}

// Bundlers that keep function names (wrangler's esbuild does) insert calls to a
// module-level __name helper into consoleApp's source; give the browser a no-op one.
export const PAGE_JS = 'var __name = (fn) => fn;\n(' + consoleApp.toString() + ')();\n';
