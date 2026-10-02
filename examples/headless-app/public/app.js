const $ = id => document.getElementById(id);
let csrf = '', providers = [], models = [], workspaces = [], selected = null, sessionId = '', stream, runId = '', loginId = '', repository, review, pendingMessage;
function fail(error) { $('error').textContent = error.message || 'Operation failed.'; $('error').hidden = false; }
function act(id, fn, event = 'click') { $(id).addEventListener(event, async e => { e.preventDefault(); $('error').hidden = true; try { await fn(e); } catch (error) { fail(error); } }); }
async function request(path, data, signal) {
  return fetch(path, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body: JSON.stringify(data), signal });
}
async function api(op, data = {}) {
  const res = await request('/api', { ...data, op }); const value = await res.json();
  if (!res.ok) throw new Error(value.error); return value;
}
function options(id, items, placeholder = 'Use provider default') {
  $(id).replaceChildren(new Option(placeholder, ''), ...items.map(i => new Option(i.label ?? i, i.value ?? i)));
}
function showJSON(id, value) { $(id).textContent = JSON.stringify(value, null, 2); }
async function settings() {
  const result = await api('settings'); providers = result.providers;
  options('provider', providers.map(p => ({ value: p.id, label: p.id + (p.ready ? ' · ready' : ' · setup needed') })), 'Choose provider');
  showJSON('credentials', result.credentials); showJSON('assignments', result.settings?.agent_model_settings ?? { status: 'Not configured. Connect a provider, then choose model assignments.' });
}
async function refresh() {
  const onboard = await api('onboarding');
  $('owner-status').textContent = onboard.identity.bootstrapped ? `Owner: ${onboard.identity.username}` : 'Create an owner to begin.';
  $('owner-form').hidden = onboard.identity.bootstrapped;
  if (!onboard.identity.bootstrapped) return;
  await settings(); await refreshWorkspaces();
}
async function refreshWorkspaces() {
  workspaces = await api('workspaces');
  options('workspaces', workspaces.map((w, i) => ({ value: String(i), label: w.name || w.workspace_name || w.path })), 'Choose workspace');
}
async function sessions() {
  if (!selected) return;
  const list = await api('sessions', { workspace_id: selected.id || selected.workspace_id });
  $('sessions').replaceChildren(...list.map(s => {
    const button = document.createElement('button'); button.textContent = s.title || s.id;
    button.onclick = () => { sessionId = s.id; $('session-title').textContent = s.title || 'Conversation'; pendingMessage = null; watch().catch(fail); }; return button;
  }));
}
act('login-form', async e => {
  const input = e.target.elements.secret;
  const res = await request('/login', { secret: input.value }); input.value = '';
  const data = await res.json(); if (!res.ok) throw new Error(data.error);
  csrf = data.csrf; $('login').hidden = true; $('app').hidden = false; $('logout').hidden = false;
  $('connection').textContent = 'Authenticated · local HTTPS'; await refresh();
}, 'submit');
act('logout', async () => { stream?.abort(); await request('/logout', {}); location.reload(); });
act('theme', () => document.body.classList.toggle('light'));
act('refresh', refresh);
act('owner-form', async e => { await api('owner', Object.fromEntries(new FormData(e.target))); await refresh(); }, 'submit');
act('provider', async () => {
  const p = providers.find(p => p.id === $('provider').value); if (!p) return;
  $('provider-status').textContent = p.ready ? 'Connected. Choose a model below.' : (p.reason || 'Connection required.');
  options('credential-type', (p.auth_methods || []).filter(m => m.credential_type).map(m => ({ label: m.label, value: m.credential_type })), 'Choose credential type');
  models = (await api('catalog', { provider: p.id })).records;
  options('model', models.map(m => ({ label: m.display_name || m.model, value: m.model })), 'Choose catalog model');
  for (const id of ['thinking', 'tier', 'context']) options(id, []);
}, 'change');
act('model', () => {
  const m = models.find(m => m.model === $('model').value);
  options('thinking', m?.thinking_options || []); options('tier', m?.service_tiers || []);
  options('context', (m?.context_modes || []).map(c => ({ label: c.label || c.mode, value: c.mode })));
}, 'change');
act('credential-form', async e => {
  const input = e.target.elements.key, key = input.value; input.value = '';
  const result = await api('credential', { provider: $('provider').value, type: $('credential-type').value, key });
  await settings();
  if (!result.connected || result.defaultsError) throw new Error('Credential stored, but inference/default model readiness is not confirmed. Select canonical model settings and check provider status.');
}, 'submit');
function displayLogin(s) {
  $('codex-login').replaceChildren();
  const p = document.createElement('p'); p.textContent = `Status: ${s.status}${s.user_code ? ' · Code: ' + s.user_code : ''}`; $('codex-login').append(p);
  if (s.verification_url || s.auth_url) {
    const url = new URL(s.verification_url || s.auth_url);
    if (url.protocol === 'https:') { const a = document.createElement('a'); a.href = url.href; a.target = '_blank'; a.rel = 'noopener noreferrer'; a.textContent = 'Open provider sign-in'; $('codex-login').append(a); }
  }
  $('codex-status').hidden = ['success', 'error'].includes(s.status);
  $('codex-complete').hidden = s.method !== 'manual' || ['success', 'error'].includes(s.status);
  if (s.failed) throw new Error('Codex sign-in failed or expired. Start a new device login.');
}
act('codex-start', async () => { const s = await api('codex-start', { method: $('codex-method').value }); loginId = s.session_id; displayLogin(s); });
act('codex-complete', async e => { const input = e.target.elements.callback, callback = input.value; input.value = ''; const s = await api('codex-complete', { id: loginId, callback }); displayLogin(s); if (s.status === 'success') await settings(); }, 'submit');
act('codex-status', async () => { const s = await api('codex-status', { id: loginId }); displayLogin(s); if (s.status === 'success') await settings(); });
act('save-model', async () => {
  const result = await api('model', { provider: $('provider').value, model: $('model').value, thinking: $('thinking').value,
    service_tier: $('tier').value, context_mode: $('context').value, slot: $('slot').value });
  showJSON('assignments', result.agent_model_settings);
});
act('folder-form', async e => {
  const folder = await api('folder', Object.fromEntries(new FormData(e.target))); $('folder-path').value = folder.path;
  repository = null; review = null; $('baseline-files').replaceChildren(); $('repository-status').textContent = 'Folder created; inspect and explicitly initialize Git next.';
}, 'submit');
async function inspect() { repository = await api('repository', { path: $('folder-path').value }); $('repository-status').textContent = repository.message || repository.state; }
act('inspect', inspect);
act('git-init', async () => { if (!repository) throw new Error('Inspect the folder first.'); await api('git-init', { path: $('folder-path').value, expected_path: repository.path, confirm: $('git-confirm').checked }); $('git-confirm').checked = false; await inspect(); });
act('review', async () => {
  review = await api('review', { path: $('folder-path').value });
  $('baseline-files').replaceChildren(...review.files.map(f => { const label = document.createElement('label'), input = document.createElement('input'); input.type = 'checkbox'; input.value = f.path; input.disabled = !f.selectable; label.append(input, document.createTextNode(f.path)); return label; }));
});
act('baseline', async () => {
  if (!review) throw new Error('Review the files first.');
  await api('baseline', { path: $('folder-path').value, expected_path: review.repository.path, digest: review.digest,
    paths: [...$('baseline-files').querySelectorAll('input:checked')].map(i => i.value), confirm: $('baseline-confirm').checked, confirm_omissions: $('baseline-confirm').checked });
  review = null; $('baseline-files').replaceChildren(); $('baseline-confirm').checked = false; await inspect();
});
act('register', async () => { await api('register', { path: $('folder-path').value }); await refreshWorkspaces(); });
act('workspaces', async () => { selected = workspaces[Number($('workspaces').value)]; if ($('workspaces').value === '') selected = null; stream?.abort(); sessionId = ''; $('send').disabled = true; $('stop').disabled = true; await sessions(); }, 'change');
act('session-form', async e => {
  if (!selected) throw new Error('Select a registered workspace first.');
  const result = await api('session', { path: selected.path || selected.workspace_path, title: e.target.elements.title.value, request_id: crypto.randomUUID() });
  sessionId = result.id; $('session-title').textContent = result.title; await sessions(); void watch().catch(fail);
}, 'submit');
function render(state) {
  $('stream-status').textContent = state.status; $('send').disabled = state.status !== 'live';
  $('messages').replaceChildren(...state.messages.map(m => {
    const div = document.createElement('article'), role = document.createElement('strong'); div.className = 'message'; div.dataset.role = m.role; div.dataset.messageId = m.id;
    role.textContent = m.role; div.append(role, document.createTextNode(m.content)); return div;
  }));
  $('live').replaceChildren(...state.live.map(l => { const p = document.createElement('article'); p.className = 'draft'; p.dataset.stream = JSON.stringify([l.runId, l.streamId]); p.textContent = l.text; return p; }));
  const snap = state.snapshot, view = snap.session_views_by_id?.[state.sessionId] || {}, run = snap.current_run_state_by_session?.[state.sessionId] || view.current_run_state;
  runId = run?.active ? run.run_id : ''; $('stop').disabled = !runId;
  showJSON('activity', { run, tools: (snap.events_by_session?.[state.sessionId] || []).filter(e => e.event_type.includes('tool')).slice(-30) });
  $('history-status').textContent = snap.omissions?.length ? 'Some history/resources omitted. See run details; this is a bounded tail.' : 'Durable 200-message tail + separate live draft';
  $('permissions').replaceChildren(...(view.pending_permissions || []).map(p => {
    const box = document.createElement('div'); box.className = 'permission'; const title = document.createElement('strong'), details = document.createElement('pre');
    title.textContent = `Approval required · ${p.tool_name}`; details.textContent = `Call: ${p.call_id}\nPermission: ${p.id}\n${p.tool_arguments}`; box.append(title, details);
    for (const [action, label] of [['allow_once', 'Allow this call once'], ['deny', 'Deny']]) {
      const button = document.createElement('button'); button.textContent = label;
      button.onclick = async () => { button.disabled = true; try { await api('permission', { id: state.sessionId, permission_id: p.id, action }); } catch (e) { fail(e); button.disabled = false; } }; box.append(button);
    } return box;
  }));
}
async function watch() {
  stream?.abort(); if (!sessionId) return;
  const controller = new AbortController(); stream = controller; $('stream-status').textContent = 'Connecting'; $('send').disabled = true;
  try {
    const response = await request('/watch', { id: sessionId }, controller.signal);
    if (!response.ok) throw new Error((await response.json()).error);
    const reader = response.body.pipeThrough(new TextDecoderStream()).getReader(); let buffer = '';
    while (true) {
      const { done, value } = await reader.read(); if (done) break; buffer += value;
      if (buffer.length > 9 * 1024 * 1024) throw new Error('Stream view exceeds app limit.');
      let newline; while ((newline = buffer.indexOf('\n')) >= 0) {
        const event = JSON.parse(buffer.slice(0, newline)); buffer = buffer.slice(newline + 1);
        if (event.error) throw new Error(event.error); if (event.state && stream === controller) render(event.state);
      }
    }
  } catch (e) { if (!controller.signal.aborted) fail(e); }
  finally { controller.abort(); if (stream === controller) { $('stream-status').textContent = 'Disconnected · click Reconnect'; $('send').disabled = true; $('stop').disabled = true; } }
}
act('reconnect', watch);
act('message-form', async () => {
  const content = $('message').value;
  if (!pendingMessage || pendingMessage.content !== content || pendingMessage.id !== sessionId) pendingMessage = { content, id: sessionId, request_id: crypto.randomUUID() };
  $('send').disabled = true;
  try { await api('message', pendingMessage); $('message').value = ''; pendingMessage = null; }
  finally { $('send').disabled = $('stream-status').textContent !== 'live'; }
}, 'submit');
act('stop', async () => { await api('stop', { id: sessionId, run_id: runId }); });
window.addEventListener('pagehide', () => stream?.abort());
