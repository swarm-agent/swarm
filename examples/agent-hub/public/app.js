const $ = id => document.getElementById(id);
let csrf = '', selected = null, conversation = '', tab = 'context', cursor = '';
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
function action(fn) { return async event => { event?.preventDefault(); $('status').textContent = 'Working…'; try { await fn(); $('status').textContent = 'Saved / refreshed.'; } catch (error) { $('status').textContent = error.message; } }; }
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
  selected = await api('agent', { id }); conversation = '';
  $('heading').textContent = selected.name;
  for (const [element, value] of Object.entries({ 'agent-id': selected.id, name: selected.name, instructions: selected.instructions, 'agent-context': selected.context, 'project-id': selected.project_id || '', 'worker-ids': (selected.worker_ids || []).join(', ') })) $(element).value = value;
  $('agent-id').readOnly = true; $('revision').textContent = `Revision ${selected.revision}`;
  await loadTab();
}
async function loadConversation() {
  if (!conversation) return;
  const state = await api('conversation', { id: requireAgent(), session_id: conversation });
  $('conversation-heading').textContent = state.session?.title || 'Conversation';
  $('messages').replaceChildren();
  for (const message of state.messages || []) card($('messages'), message.role, message.content);
}
async function loadTab() {
  document.querySelectorAll('nav button').forEach(b => b.classList.toggle('active', b.dataset.tab === tab));
  for (const id of ['context', 'conversations', 'tasks', 'workers']) $(id).hidden = id !== tab;
  if (!selected || tab === 'context') return;
  const id = selected.id;
  if (tab === 'conversations') {
    const state = await api('conversations', { id }); $('history').replaceChildren();
    for (const session of state.sessions) $('history').append(button(session.title || session.id, async () => { conversation = session.id; await loadConversation(); }));
    if (state.scan_limit_reached) card($('history'), 'Recent history', 'Showing a bounded recent view. Older IDs remain accessible through the SDK.');
    await loadConversation();
  }
  if (tab === 'tasks') {
    $('task-list').replaceChildren();
    if (!selected.project_id) return card($('task-list'), 'No project linked', 'Link an existing project in Context.');
    for (const task of (await api('tasks', { id })).tasks || []) card($('task-list'), task.title || task.id, task);
  }
  if (tab === 'workers') {
    $('worker-list').replaceChildren();
    for (const worker_id of selected.worker_ids || []) {
      const { worker } = await api('worker', { id, worker_id });
      card($('worker-list'), worker.name, { state: worker.lifecycle_state, revision: worker.revision, automations: worker.automations });
      const runs = await api('runs', { id, worker_id });
      for (const run of runs.runs || []) card($('worker-list'), `${run.request_source} · ${run.status}`, run);
    }
  }
}
$('login-form').onsubmit = action(async () => { const token = $('token').value; $('token').value = ''; csrf = (await api('', { token }, '/login')).csrf; $('login').hidden = true; $('hub').hidden = false; $('logout').hidden = false; await agents(); });
$('logout').onclick = action(async () => { await api('', {}, '/logout'); location.reload(); });
$('new').onclick = () => { selected = null; conversation = ''; $('agent-form').reset(); $('agent-id').readOnly = false; $('revision').textContent = ''; $('heading').textContent = 'New agent'; tab = 'context'; void loadTab(); };
$('more').onclick = action(() => agents(true));
$('refresh').onclick = action(async () => { await agents(); if (selected) { const saved = conversation; await select(selected.id); conversation = saved; await loadTab(); } });
for (const b of document.querySelectorAll('nav button')) b.onclick = action(async () => { tab = b.dataset.tab; await loadTab(); });
$('agent-form').onsubmit = action(async () => {
  const agent = await api('save', { id: $('agent-id').value, name: $('name').value, instructions: $('instructions').value, context: $('agent-context').value, expected_revision: selected?.revision || 0, project_id: $('project-id').value, worker_ids: $('worker-ids').value.split(',').map(s => s.trim()).filter(Boolean) });
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
