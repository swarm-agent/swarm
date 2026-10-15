// Minimal embeddable chat widget for the public chat edge. Usage:
//   <script src="chat.js" defer data-endpoint="https://chat.example.com"
//           data-title="Ask us" data-fallback="Email hello@example.com"></script>
// Replies are rendered as text only: visitor and model output is untrusted.
(() => {
  const tag = document.currentScript;
  const endpoint = (tag?.dataset.endpoint || '').replace(/\/$/, '');
  const title = tag?.dataset.title || 'Ask us anything';
  const fallback = tag?.dataset.fallback || 'The assistant is unavailable right now. Please try again later.';
  const store = (() => { try { return window.localStorage; } catch { return undefined; } })();
  const remember = (key, value) => { try { store?.setItem(key, value); } catch { /* private mode */ } };
  let visitorId = (() => { try { return store?.getItem('chat_visitor_id'); } catch { return null; } })();
  if (!visitorId) {
    visitorId = (crypto.randomUUID?.() || `${Date.now()}${Math.random()}`).replace(/[^A-Za-z0-9]/g, '').slice(0, 32).padEnd(16, '0');
    remember('chat_visitor_id', visitorId);
  }
  let ticket = (() => { try { return store?.getItem('chat_ticket') || undefined; } catch { return undefined; } })();
  let lastSeq = 0;
  let polling;

  const root = document.createElement('div');
  root.style.cssText = 'position:fixed;right:16px;bottom:16px;z-index:2147483000;font:14px/1.45 system-ui,sans-serif';
  root.innerHTML = '<button type="button" style="padding:12px 16px;border-radius:999px;border:0;background:#111;color:#fff;cursor:pointer"></button>' +
    '<div hidden style="width:min(360px,calc(100vw - 32px));height:min(520px,calc(100vh - 96px));background:#fff;color:#111;border:1px solid #ddd;border-radius:14px;box-shadow:0 20px 60px rgba(0,0,0,.25);display:flex;flex-direction:column;overflow:hidden">' +
    '<div style="padding:12px 14px;border-bottom:1px solid #eee;font-weight:600"></div>' +
    '<div role="log" aria-live="polite" style="flex:1;overflow:auto;padding:12px;display:flex;flex-direction:column;gap:8px"></div>' +
    '<form style="display:flex;gap:8px;padding:10px;border-top:1px solid #eee"><input maxlength="2000" aria-label="Your message" style="flex:1;padding:9px;border:1px solid #ccc;border-radius:8px"><button style="padding:9px 12px;border:0;border-radius:8px;background:#111;color:#fff">Send</button></form></div>';
  const [launch, panel] = root.children;
  const [header, log, form] = panel.children;
  const input = form.querySelector('input');
  launch.textContent = title;
  header.textContent = title;
  document.body.appendChild(root);
  launch.addEventListener('click', () => { panel.hidden = !panel.hidden; panel.style.display = panel.hidden ? 'none' : 'flex'; if (!panel.hidden) input.focus(); });
  panel.style.display = 'none';

  const say = (from, text) => {
    const line = document.createElement('div');
    line.textContent = text;
    line.style.cssText = 'max-width:85%;padding:8px 11px;border-radius:10px;white-space:pre-wrap;' +
      (from === 'you' ? 'align-self:flex-end;background:#111;color:#fff' : from === 'person' ? 'background:#e8f0ff' : 'background:#f2f2f2');
    log.appendChild(line);
    log.scrollTop = log.scrollHeight;
  };

  // A person stepping in: offered -> waiting -> joined, or missed if nobody accepts in time.
  let personShown = '';
  const showPerson = (person) => {
    if (!person || personShown === person.state) return;
    personShown = person.state;
    if (person.state === 'waiting') say('agent', `Connecting you with ${person.name}…`);
    if (person.state === 'joined') say('agent', `${person.name} joined the chat.`);
    if (person.state === 'missed') say('agent', `${person.name} couldn't join just now. Leave your email and they will get back to you.`);
  };

  const pollReplies = () => {
    clearInterval(polling);
    polling = setInterval(async () => {
      try {
        const res = await fetch(`${endpoint}/api/chat/replies?visitor_id=${visitorId}&after=${lastSeq}`);
        const data = await res.json();
        showPerson(data.person);
        for (const r of data.replies || []) { lastSeq = Math.max(lastSeq, r.seq); say(r.from, r.from === 'person' && data.person ? `${data.person.name}: ${r.text}` : r.text); }
        if (!data.with_person && data.person?.state !== 'waiting') clearInterval(polling);
      } catch { clearInterval(polling); }
    }, 3000);
  };

  // The agent offered a person who is online: one Yes button, no model run when pressed.
  const offerPerson = (person) => {
    const yes = document.createElement('button');
    yes.type = 'button';
    yes.textContent = `Yes, talk to ${person.name}`;
    yes.style.cssText = 'align-self:flex-start;padding:7px 12px;border:0;border-radius:999px;background:#111;color:#fff;cursor:pointer';
    yes.addEventListener('click', async () => {
      yes.remove();
      const res = await fetch(`${endpoint}/api/chat/person`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ visitor_id: visitorId, accept: true }) }).catch(() => undefined);
      const data = res ? await res.json().catch(() => ({})) : {};
      if (data.status === 'waiting' || data.status === 'joined') { showPerson(data.person); pollReplies(); } else say('agent', 'Nobody is online right now. ' + fallback);
    });
    log.appendChild(yes);
    log.scrollTop = log.scrollHeight;
  };

  // Header: whether someone from the team is online.
  fetch(`${endpoint}/api/chat/status`).then((r) => r.json()).then((data) => {
    if (data.staff?.[0]) header.textContent = `${title} · ${data.staff[0].name} is online`;
  }).catch(() => {});

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    const message = input.value.trim();
    if (!message) return;
    input.value = '';
    say('you', message);
    try {
      const res = await fetch(`${endpoint}/api/chat`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ visitor_id: visitorId, message, ticket }) });
      const data = await res.json();
      if (data.ticket) { ticket = data.ticket; remember('chat_ticket', ticket); }
      if (Number.isFinite(data.seq)) lastSeq = Math.max(lastSeq, data.seq);
      if (data.status === 'answered') {
        say('agent', data.reply);
        if (data.person?.state === 'offered') offerPerson(data.person);
        if (data.person?.state === 'waiting') { showPerson(data.person); pollReplies(); }
      }
      else if (data.status === 'ended') {
        if (data.reply) say('agent', data.reply);
        say('agent', 'This chat has ended.');
        for (const el of form.elements) el.disabled = true;
      }
      else if (data.status === 'with_person') { showPerson(data.person || { state: 'joined', name: 'A person' }); pollReplies(); }
      else if (data.status === 'slow_down') say('agent', 'Please wait a moment before sending more.');
      else if (data.status === 'busy') say('agent', 'Lots of visitors right now. Please try again in a few seconds.');
      else if (data.status === 'conversation_full') say('agent', 'Let us continue this with a person: ' + (data.booking_url || fallback));
      else say('agent', fallback);
    } catch {
      say('agent', fallback);
    }
  });
})();
