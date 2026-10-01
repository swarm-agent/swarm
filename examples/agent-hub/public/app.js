const $ = id => document.getElementById(id);
let csrf = '', selected = null, conversation = '', tab = 'context', cursor = '', selectionVersion = 0, viewVersion = 0, providerVersion = 0, actionVersion = 0;
const pendingKeys = new Map();
const stableKey = (kind, payload) => {
  const key = JSON.stringify([kind, payload]);
  if (!pendingKeys.has(key)) pendingKeys.set(key, crypto.randomUUID());
  return [key, pendingKeys.get(key)];
};
async function api(op, data = {}, path = '/api') {
  const response = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body: JSON.stringify({ op, ...data }) });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error);
  return result;
}
function action(fn) { return async event => { event?.preventDefault(); const version = ++actionVersion; $('status').textContent = 'Working…'; try { await fn(); if (version === actionVersion) $('status').textContent = 'Saved / refreshed.'; } catch (error) { if (version === actionVersion) $('status').textContent = error.message; } }; }
function button(label, fn) { const el = document.createElement('button'); el.textContent = label; el.onclick = action(fn); return el; }
function card(target, title, data) { const el = document.createElement('div'); el.className = 'card'; const h = document.createElement('strong'); h.textContent = title; const pre = document.createElement('pre'); pre.textContent = typeof data === 'string' ? data : JSON.stringify(data, null, 2); el.append(h, pre); target.append(el); }
function requireAgent() { if (!selected) throw new Error('Save or select an agent first.'); return selected.id; }
async function agents(append = false) {
  const result = await api('agents', append ? { cursor } : {});
  if (!append) $('agents').replaceChildren();
  for (const agent of result.agents) $('agents').append(button(agent.name, () => select(agent.id)));
  cursor = result.next_cursor || ''; $('more').hidden = !cursor;
}
async function select(id) {
  stopStream();
  const version = ++selectionVersion; viewVersion++;
  const next = await api('agent', { id });
  if (version !== selectionVersion) return;
  selected = next; conversation = '';
  $('heading').textContent = selected.name;
  for (const [element, value] of Object.entries({ 'agent-id': selected.id, name: selected.name, instructions: selected.instructions, 'agent-context': selected.context, 'project-id': selected.project_id || '' })) $(element).value = value;
  for (const option of $('worker-ids').options) option.selected = (selected.worker_ids || []).includes(option.value);
  $('agent-id').readOnly = true; $('revision').textContent = `Revision ${selected.revision}`;
  await loadTab();
}
async function loadConversation() {
  if (!conversation) return;
  const id = requireAgent(), session_id = conversation, version = ++viewVersion;
  const state = await api('conversation', { id, session_id });
  if (version !== viewVersion || selected?.id !== id || conversation !== session_id || tab !== 'conversations') return;
  $('conversation-heading').textContent = state.session?.title || 'Conversation';
  $('messages').replaceChildren();
  for (const message of state.messages || []) card($('messages'), message.role, message.content);
  void streamView();
}
async function loadTab() {
  stopStream();
  const version = ++viewVersion;
  document.querySelectorAll('nav button').forEach(b => b.classList.toggle('active', b.dataset.tab === tab));
  for (const id of ['context', 'conversations', 'tasks', 'workers']) $(id).hidden = id !== tab;
  if (!selected || tab === 'context') return;
  const id = selected.id;
  if (tab === 'conversations') {
    const state = await api('conversations', { id });
    if (version !== viewVersion || selected?.id !== id || tab !== 'conversations') return;
    $('history').replaceChildren();
    for (const session of state.sessions) $('history').append(button(session.title || session.id, async () => { conversation = session.id; await loadConversation(); }));
    if (state.scan_limit_reached) card($('history'), 'Recent history', 'Showing a bounded recent view. Older IDs remain accessible through the SDK.');
    await loadConversation();
  }
  if (tab === 'tasks') {
    $('task-list').replaceChildren();
    if (!selected.project_id) return card($('task-list'), 'No project linked', 'Link an existing project in Context.');
    const result = await api('tasks', { id });
    if (version !== viewVersion || selected?.id !== id || tab !== 'tasks') return;
    for (const task of result.tasks || []) card($('task-list'), task.title || task.id, task);
  }
  if (tab === 'workers') {
    $('worker-list').replaceChildren();
    const items = [];
    for (const worker_id of selected.worker_ids || []) {
      const { worker } = await api('worker', { id, worker_id });

      const runs = await api('runs', { id, worker_id });
      items.push({ worker, runs: runs.runs });
    }
    if (version !== viewVersion || selected?.id !== id || tab !== 'workers') return;
    renderWorkers(items);
  }
  if (tab !== 'conversations') void streamView();
}
$('login-form').onsubmit = action(async () => { const token = $('token').value; $('token').value = ''; csrf = (await api('', { token }, '/login')).csrf; $('login').hidden = true; $('hub').hidden = false; $('logout').hidden = false; await agents(); await discovery(); });
$('logout').onclick = action(async () => { stopStream(); selectionVersion++; viewVersion++; providerVersion++; clearTimeout(providerExpiry); $('provider-callback').value = ''; $('provider-link').hidden = true; $('provider-code').textContent = ''; await api('', {}, '/logout'); location.reload(); });
$('new').onclick = () => { stopStream(); selectionVersion++; viewVersion++; selected = null; conversation = ''; $('agent-form').reset(); $('agent-id').readOnly = false; $('revision').textContent = ''; $('heading').textContent = 'New agent'; tab = 'context'; void loadTab(); };
$('more').onclick = action(() => agents(true));
$('refresh').onclick = action(async () => { await agents(); await discovery(); if (selected) { const saved = conversation; await select(selected.id); conversation = saved; await loadTab(); } });
for (const b of document.querySelectorAll('nav button')) b.onclick = action(async () => { tab = b.dataset.tab; await loadTab(); });
$('agent-form').onsubmit = action(async () => {
  const agent = await api('save', { id: $('agent-id').value, name: $('name').value, instructions: $('instructions').value, context: $('agent-context').value, expected_revision: selected?.revision || 0, project_id: $('project-id').value, worker_ids: [...$('worker-ids').selectedOptions].map(o => o.value) });
  await agents(); await select(agent.id);
});
$('open-form').onsubmit = action(async () => {
  const data = { id: requireAgent(), revision: selected.revision, workspace_id: $('workspace-id').value, title: $('conversation-title').value };
  const [key, request_id] = stableKey('open', data); const result = await api('open', { ...data, request_id }); pendingKeys.delete(key); conversation = result.id; await loadTab();
});
$('message-form').onsubmit = action(async () => {
  if (!conversation) throw new Error('Open a conversation first.');
  const data = { id: requireAgent(), session_id: conversation, content: $('message').value };
  const [key, request_id] = stableKey('message', data); await api('message', { ...data, request_id }); pendingKeys.delete(key); $('message').value = ''; await loadConversation();
});
$('task-form').onsubmit = action(async () => {
  const data = { id: requireAgent(), title: $('task-title').value, content: $('task-content').value };
  const [key, request_id] = stableKey('task', data); await api('task', { ...data, request_id }); pendingKeys.delete(key); await loadTab();
});

// One selected-view stream. Selection changes abort reads and suppress stale updates.
let activeStream;
function stopStream() { activeStream?.abort(); activeStream = undefined; }
async function streamView() {
  stopStream();
  if (!selected || tab === 'context' || (tab === 'conversations' && !conversation)) return;
  const controller = new AbortController(); activeStream = controller;
  const id = selected.id, session_id = tab === 'conversations' ? conversation : undefined;
  try {
    const response = await fetch('/stream', { method: 'POST', signal: controller.signal,
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body: JSON.stringify({ id, session_id }) });
    if (!response.ok || !response.body) throw new Error('Stream unavailable. Refresh to reconnect.');
    const reader = response.body.getReader(), decoder = new TextDecoder(); let buffer = '';
    while (!controller.signal.aborted) {
      const { value, done } = await reader.read(); if (done) break;
      buffer += decoder.decode(value, { stream: true });
      if (buffer.length > 1024 * 1024) throw new Error('Result too large. Use the SDK to read details.');
      let newline;
      while ((newline = buffer.indexOf('\n')) >= 0) {
        const update = JSON.parse(buffer.slice(0, newline)); buffer = buffer.slice(newline + 1);
        if (controller.signal.aborted || selected?.id !== id || (session_id && conversation !== session_id)) return;
        if (update.conversation) {
          $('messages').replaceChildren();
          for (const m of update.conversation.messages) card($('messages'), m.role, m.content);
          for (const m of update.conversation.live) card($('messages'), 'Assistant · writing', m.text);
        } else if (update.results && tab === 'tasks') {
          $('task-list').replaceChildren();
          for (const task of update.results.tasks) card($('task-list'), task.title, task);
        } else if (update.results && tab === 'workers') {
          // Do not destroy a configuration form while the user is editing it.
          if (!$('worker-list').contains(document.activeElement)) renderWorkers(update.results.workers);
        }
      }
    }
    if (!controller.signal.aborted) $('status').textContent = 'Stream ended. Refresh to reconnect.';
  } catch { if (!controller.signal.aborted) $('status').textContent = 'Stream unavailable. Refresh to reconnect.'; }
  finally { controller.abort(); }
}
function renderWorkers(items) {
  const target = $('worker-list'); target.replaceChildren();
  for (const { worker, runs } of items) {
    card(target, worker.name, `State: ${worker.lifecycle_state} · Revision ${worker.revision}`);
    if (worker.pending_review) {
      card(target, 'Changes awaiting approval', 'Your configuration was saved for review in Orchestrator. Approved jobs keep running unchanged.');
      for (const automation of worker.pending_review.automations || []) card(target, automation.name, { activation: automation.activation_mode, schedule: automation.schedule });
    }
    for (const automation of worker.automations || []) {
      const form = document.createElement('form');
      const heading = document.createElement('h3'); heading.textContent = automation.name; form.append(heading);
      const field = (label, element) => { const wrapper = document.createElement('label'); wrapper.append(document.createTextNode(label), element); form.append(wrapper); return element; };
      const mode = field('Activation', document.createElement('select'));
      for (const value of ['manual', 'interval', 'cron', ...(automation.trigger ? ['external_trigger'] : [])]) { const option = document.createElement('option'); option.value = value; option.textContent = value.replaceAll('_', ' '); mode.append(option); }
      mode.value = automation.activation_mode;
      const seconds = field('Interval seconds', document.createElement('input')); seconds.type = 'number'; seconds.min = '60'; seconds.value = automation.schedule?.interval_seconds || 3600;
      const cron = field('Cron schedule', document.createElement('input')); cron.value = automation.schedule?.cron || '0 9 * * *';
      const timezone = field('Timezone', document.createElement('input')); timezone.value = automation.schedule?.timezone || 'UTC';
      const save = document.createElement('button'); save.textContent = 'Submit configuration for review'; form.append(save);
      const agentId = selected.id;
      form.onsubmit = action(async () => {
        await api('configure', { id: agentId, worker_id: worker.id, automation_id: automation.id, revision: worker.revision, mode: mode.value, seconds: Number(seconds.value), cron: cron.value, timezone: timezone.value });
        await loadTab();
      });
      target.append(form);
      if (automation.activation_mode === 'external_trigger') {
        const delivery = document.createElement('form');
        const label = document.createElement('label'); label.textContent = 'Event message (must match the configured trigger input)';
        const message = document.createElement('textarea'); message.required = true; label.append(message);
        const submit = document.createElement('button'); submit.textContent = 'Submit event'; delivery.append(label, submit);
        delivery.onsubmit = action(async () => {
          const data = { id: agentId, worker_id: worker.id, automation_id: automation.id, content: message.value };
          const [key, request_id] = stableKey('trigger', data);
          await api('trigger', { ...data, request_id }); pendingKeys.delete(key); message.value = '';
        });
        target.append(delivery);
      }
    }
    for (const run of runs || []) card(target, `${run.request_source} · ${run.status}`, run);
  }
}
window.addEventListener('pagehide', () => { stopStream(); providerVersion++; clearTimeout(providerExpiry); $('provider-callback').value = ''; });

async function discovery() {
  let failed = false;
  for (const [op, element] of [['projects', 'project-id'], ['workers', 'worker-ids'], ['workspaces', 'workspace-id']]) {
    try {
      const result = await api(op), items = Array.isArray(result) ? result : result.workers;
      $(element).replaceChildren();
      if (element === 'project-id') { const empty = document.createElement('option'); empty.value = ''; empty.textContent = 'No project'; $(element).append(empty); }
      for (const item of items || []) { const option = document.createElement('option'); option.value = item.id; option.textContent = item.name || item.title || item.path || item.id; $(element).append(option); }
    } catch { failed = true; }
  }
  if (failed) throw new Error('Some resources are unavailable. Check account permissions.');
}

let providerExpiry;
async function providerAction(op, data = {}) {
  clearTimeout(providerExpiry);
  const version = ++providerVersion;
  // Clear links/codes immediately on cancel, expiry or network failure.
  $('provider-link').hidden = true; $('provider-link').removeAttribute('href');
  $('provider-code').textContent = ''; $('provider-complete').hidden = true;
  const state = await api(op, data, '/provider');
  if (version !== providerVersion) return;
  const pending = ['waiting', 'authorizing'].includes(state.status);
  $('provider-state').textContent = `${state.status}${state.defaults_applied ? ' · Account defaults initialized/preserved by Swarm.' : ''}${pending ? ' · Expires ' + new Date(state.expires_at).toLocaleTimeString() : ''}`;
  $('provider-check').hidden = !pending; $('provider-cancel').hidden = state.status === 'forgotten';
  $('provider-complete').hidden = !pending || state.method !== 'manual';
  if (state.url) { $('provider-link').href = state.url; $('provider-link').hidden = false; }
  $('provider-code').textContent = state.user_code || '';
  if (pending) providerExpiry = setTimeout(() => {
    if (version !== providerVersion) return;
    $('provider-link').hidden = true; $('provider-link').removeAttribute('href');
    $('provider-code').textContent = ''; $('provider-complete').hidden = true;
    $('provider-check').hidden = true; $('provider-state').textContent = 'Sign-in expired. Start again.';
  }, Math.max(0, state.expires_at - Date.now()));
}
$('provider-start').onsubmit = action(() => providerAction('start', { method: $('provider-method').value, consent: $('provider-consent').checked }));
$('provider-check').onclick = action(() => providerAction('status'));
$('provider-cancel').onclick = action(() => providerAction('cancel'));
$('provider-complete').onsubmit = action(async () => {
  const callback = $('provider-callback').value; $('provider-callback').value = '';
  await providerAction('complete', { callback });
});
