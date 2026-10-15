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

  const pollReplies = () => {
    clearInterval(polling);
    polling = setInterval(async () => {
      try {
        const res = await fetch(`${endpoint}/api/chat/replies?visitor_id=${visitorId}&after=${lastSeq}`);
        const data = await res.json();
        for (const r of data.replies || []) { lastSeq = Math.max(lastSeq, r.seq); say(r.from, r.text); }
        if (!data.with_person) clearInterval(polling);
      } catch { clearInterval(polling); }
    }, 4000);
  };

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
      if (data.status === 'answered') say('agent', data.reply);
      else if (data.status === 'ended') {
        if (data.reply) say('agent', data.reply);
        say('agent', 'This chat has ended.');
        for (const el of form.elements) el.disabled = true;
      }
      else if (data.status === 'with_person') { say('agent', 'A person is with you now.'); pollReplies(); }
      else if (data.status === 'slow_down') say('agent', 'Please wait a moment before sending more.');
      else if (data.status === 'busy') say('agent', 'Lots of visitors right now. Please try again in a few seconds.');
      else if (data.status === 'conversation_full') say('agent', 'Let us continue this with a person: ' + (data.booking_url || fallback));
      else say('agent', fallback);
    } catch {
      say('agent', fallback);
    }
  });
})();
