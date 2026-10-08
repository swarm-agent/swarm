const $ = id => document.getElementById(id);
let csrf = '', account = null, setupState = null, showSecurity = false;
let workspaces = [], selected = null, sessionId = '', stream, runId = '', pendingMessage;
let providers = [], models = [], codexTimer = 0, remoteTimer = 0, remoteUntil = 0;

// ---- plumbing --------------------------------------------------------------

function fail(error) { $('error').textContent = error.message || 'Something went wrong.'; $('error').hidden = false; }
// busy: disable the pressed button while the action runs (no double submits).
function act(id, fn, event = 'click', busy = true) {
  $(id).addEventListener(event, async e => {
    e.preventDefault(); $('error').hidden = true;
    const button = busy ? e.submitter || (e.target?.tagName === 'BUTTON' ? e.target : null) : null;
    if (button) button.disabled = true;
    try { await fn(e); } catch (error) { fail(error); } finally { if (button) button.disabled = false; }
  });
}
async function request(path, data = {}, signal) {
  return fetch(path, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body: JSON.stringify(data), signal });
}
async function call(path, data) {
  const res = await request(path, data); const value = await res.json();
  if (!res.ok) throw new Error(value.error); return value;
}
const api = (op, data = {}) => call('/api', { ...data, op });
function el(tag, props = {}, ...children) {
  const node = document.createElement(tag); Object.assign(node, props); node.append(...children); return node;
}
function options(id, items, placeholder) {
  $(id).replaceChildren(...(placeholder ? [new Option(placeholder, '')] : []), ...items.map(i => new Option(i.label ?? i, i.value ?? i)));
}
const SCREENS = ['signup', 'signin', 'setup', 'twofa', 'home', 'settings'];
function show(screen) {
  for (const id of SCREENS) $(id).hidden = id !== screen;
  $('topnav').hidden = !csrf;
  if (screen !== 'home') stream?.abort();
}

// ---- sign-in ---------------------------------------------------------------

async function boot() {
  const status = await call('/auth/status');
  if (!status.registered) {
    show('signup'); $('signup-form').hidden = !status.can_register; $('signup-blocked').hidden = status.can_register; return;
  }
  $('signin-code').hidden = !status.two_factor; $('signin-form').elements.code.required = status.two_factor;
  show('signin');
}
async function signedIn(result, fresh = false) {
  csrf = result.csrf; account = await call('/auth/me');
  showSecurity = fresh && !account.two_factor;
  await openSetup(true);
}
act('signup-form', async e => {
  const f = e.target.elements;
  if (f.password.value !== f.repeat.value) throw new Error('The two passwords are different.');
  const result = await call('/auth/register', { username: f.username.value, password: f.password.value });
  f.password.value = ''; f.repeat.value = '';
  await signedIn(result, true);
}, 'submit');
act('signin-form', async e => {
  const f = e.target.elements;
  const result = await call('/auth/login', { username: f.username.value, password: f.password.value, code: f.code.value });
  f.password.value = ''; f.code.value = '';
  await signedIn(result);
}, 'submit');
act('logout', async () => { stream?.abort(); clearTimeout(codexTimer); clearTimeout(remoteTimer); await request('/auth/logout'); location.reload(); });

// ---- two-factor ------------------------------------------------------------

function drawQR({ size, cells }) {
  const ns = 'http://www.w3.org/2000/svg', svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', `0 0 ${size} ${size}`); svg.setAttribute('role', 'img'); svg.setAttribute('aria-label', 'QR code for your authenticator');
  svg.setAttribute('shape-rendering', 'crispEdges');
  for (let i = 0; i < cells.length; i++) {
    if (cells[i] !== '1') continue;
    const r = document.createElementNS(ns, 'rect');
    r.setAttribute('x', i % size); r.setAttribute('y', Math.floor(i / size)); r.setAttribute('width', 1); r.setAttribute('height', 1); r.setAttribute('fill', '#000');
    svg.append(r);
  }
  $('twofa-qr').replaceChildren(svg);
}
let twofaReturn = 'setup';
async function beginTwoFactor(from) {
  twofaReturn = from;
  const setup = await call('/auth/2fa/start');
  drawQR(setup.qr); $('twofa-key').value = setup.secret; $('twofa-form').elements.code.value = '';
  show('twofa');
}
async function leaveTwoFactor() {
  if (twofaReturn === 'settings') await openSettings(); else { showSecurity = false; await openSetup(); }
}
act('twofa-begin', () => beginTwoFactor('setup'));
act('twofa-on', () => beginTwoFactor('settings'));
act('twofa-skip', async () => { showSecurity = false; await openSetup(); });
act('twofa-cancel', leaveTwoFactor);
act('twofa-form', async e => {
  await call('/auth/2fa/confirm', { code: e.target.elements.code.value });
  account = await call('/auth/me'); await leaveTwoFactor();
}, 'submit');
act('twofa-off', async e => {
  const f = e.target.elements;
  await call('/auth/2fa/disable', { password: f.password.value, code: f.code.value });
  f.password.value = ''; f.code.value = ''; account = await call('/auth/me'); await openSettings();
}, 'submit');
act('password-form', async e => {
  const f = e.target.elements;
  await call('/auth/password', { current: f.current.value, next: f.next.value, code: f.code.value });
  f.current.value = ''; f.next.value = ''; f.code.value = '';
  $('account-status').textContent = 'Password changed. Other browsers were signed out.';
}, 'submit');

// ---- setup -----------------------------------------------------------------

const STEPS = [
  ['security', 'Login'], ['provider', 'AI provider'], ['models', 'Models'], ['workspace', 'Workspace'], ['agents', 'Agents'], ['claude', 'Claude'],
];
function nextStep() {
  if (showSecurity) return 'security';
  return STEPS.slice(1).map(([key]) => key).find(key => !setupState.steps[key]) || 'done';
}
async function openSetup(fromSignIn = false) {
  setupState = await api('setup');
  const next = nextStep();
  // Returning owners with a finished setup go straight to their work.
  if (fromSignIn && next === 'done') { await openHome(); return; }
  show('setup');
  $('progress').replaceChildren(...STEPS.map(([key, label]) => {
    const done = key === 'security' ? account?.two_factor || !showSecurity : setupState.steps[key];
    return el('li', { className: key === next ? 'current' : done ? 'done' : '', textContent: label });
  }));
  for (const key of [...STEPS.map(([k]) => k), 'done']) $(`step-${key}`).hidden = key !== next;
  $('setup-later').hidden = next === 'done';
  if (next === 'provider') await loadProviders();
  if (next === 'claude') renderRemote(setupState.remote, setupState.defaults);
}
act('nav-setup', () => openSetup());
act('setup-later', openHome);
act('go-home', openHome);

async function loadProviders() {
  const settings = await api('settings'); providers = settings.providers;
  options('provider', providers.map(p => ({ value: p.id, label: p.id })), 'Choose a provider');
  options('model-provider', providers.filter(p => p.ready).map(p => ({ value: p.id, label: p.id })), 'Choose a provider');
  $('assignments').textContent = JSON.stringify(settings.settings?.agent_model_settings ?? 'Not set yet', null, 2);
}
act('provider', () => {
  const p = providers.find(p => p.id === $('provider').value);
  options('credential-type', (p?.auth_methods || []).filter(m => m.credential_type).map(m => ({ label: m.label, value: m.credential_type })));
}, 'change');
act('credential-form', async e => {
  const input = e.target.elements.key, key = input.value; input.value = '';
  const result = await api('credential', { provider: $('provider').value, type: $('credential-type').value, key });
  if (!result.connected) throw new Error('The key was saved, but the provider did not confirm it. Check the key and try again.');
  await openSetup();
}, 'submit');
act('codex-start', async () => {
  clearTimeout(codexTimer);
  const s = await api('codex-start', { method: 'device' });
  const url = new URL(s.verification_url || s.auth_url);
  if (url.protocol !== 'https:') throw new Error('The provider returned an unexpected sign-in address.');
  $('codex-link').href = url.href; $('codex-link').textContent = url.host + url.pathname;
  $('codex-code').textContent = s.user_code || ''; $('codex-login').hidden = false;
  const until = Date.now() + 15 * 60_000;
  const poll = async () => {
    try {
      const status = await api('codex-status', { id: s.session_id });
      if (status.status === 'success') { $('codex-login').hidden = true; await openSetup(); return; }
      if (status.failed || status.status === 'error') throw new Error('Sign-in failed or expired. Press "Sign in with ChatGPT" to try again.');
      if (Date.now() < until) codexTimer = setTimeout(poll, 3000);
      else $('codex-wait').textContent = 'Still waiting. Press "Sign in with ChatGPT" to get a new code.';
    } catch (error) { fail(error); }
  };
  codexTimer = setTimeout(poll, 3000);
});
act('models-recommended', async () => { await api('models-recommended'); await openSetup(); });
act('workspace-form', async e => { await api('workspace-create', { name: e.target.elements.name.value }); await openSetup(); }, 'submit');
act('agents-form', async e => { await api('agents-mode', { mode: e.target.elements.mode.value }); await openSetup(); }, 'submit');

// ---- Claude connection -----------------------------------------------------

function renderRemote(remote, defaults = {}) {
  const f = $('claude-form').elements;
  if (!f.relay_url.value) f.relay_url.value = remote?.relay_url || defaults.relay_url || '';
  if (!f.device_name.value) f.device_name.value = remote?.device_name || defaults.device_name || '';
  const pairing = remote?.pairing_code && remote.pairing_expires_at > Date.now();
  $('claude-form').hidden = !!remote?.enabled; $('pairing').hidden = !pairing;
  const text = !remote ? '' : remote.connected ? `Connected as "${remote.device_name}". Claude can use this server.`
    : pairing ? 'Waiting for Claude to pair…'
    : remote.enabled ? `Connecting to ${remote.relay_url}…${remote.last_error ? ' (' + remote.last_error + ')' : ''}`
    : remote.configured ? `Set up for ${remote.relay_url}, currently off.` : 'Not connected.';
  $('claude-status').textContent = text; $('settings-claude').textContent = text;
  $('claude-disable').hidden = !remote?.enabled; $('claude-reset').hidden = !remote?.configured;
  if (pairing) {
    $('pairing-say').textContent = `Pair my Swarm machine with code ${remote.pairing_code}`;
    $('pairing-expiry').textContent = `Code expires at ${new Date(remote.pairing_expires_at).toLocaleTimeString()}; a new one appears if it does.`;
  }
  $('consents').replaceChildren(...(remote?.consents || []).map(c => {
    const box = el('div', { className: 'item' }, el('div', {}, el('b', { textContent: `${c.client_name || 'An AI client'} asks for access` }),
      el('p', { textContent: `Code ${c.code} · ${c.redirect_host}. Approve only if Claude shows the same code.` })));
    for (const [approve, label] of [[true, 'Approve'], [false, 'Deny']]) {
      const button = el('button', { type: 'button', textContent: label });
      button.onclick = async () => { button.disabled = true; try { renderRemote(await api('remote-consent', { code: c.code, approve })); } catch (e) { fail(e); button.disabled = false; } };
      box.append(button);
    }
    return box;
  }));
  // Refresh on a bounded timer only while waiting to connect or pair.
  clearTimeout(remoteTimer);
  if (remote?.enabled && !remote.connected && Date.now() < remoteUntil) {
    remoteTimer = setTimeout(async () => {
      try {
        const next = await api('remote-status'); renderRemote(next);
        if (next.connected && !$('setup').hidden) await openSetup();
      } catch (error) { fail(error); }
    }, 3000);
  }
}
act('claude-form', async e => {
  const f = e.target.elements; remoteUntil = Date.now() + 20 * 60_000;
  renderRemote(await api('remote-connect', { relay_url: f.relay_url.value, device_name: f.device_name.value,
    allow_write: f.allow_write.checked, allow_manage: f.allow_manage.checked, allow_approve: f.allow_approve.checked }));
}, 'submit');
act('claude-disable', async () => { remoteUntil = 0; renderRemote(await api('remote-disable')); });
act('claude-reset', async () => {
  if (!window.confirm('Forget this server\'s relay key? Claude loses access until you connect and pair again.')) return;
  remoteUntil = 0; renderRemote(await api('remote-reset', { confirm: true }));
});

// ---- settings --------------------------------------------------------------

async function openSettings() {
  show('settings');
  account = await call('/auth/me');
  $('account-status').textContent = `Signed in as ${account.username}. Two-factor is ${account.two_factor ? 'on' : 'off'}.`;
  $('twofa-on').hidden = account.two_factor; $('twofa-off-box').hidden = !account.two_factor;
  $('password-code').hidden = !account.two_factor; $('password-form').elements.username.value = account.username;
  await loadProviders();
  renderAgents((await api('setup')).agents);
  renderRemote(await api('remote-status'));
  renderAI(await api('ai-keys'));
}
act('nav-settings', openSettings);
function renderAgents(mode) {
  $('agents-status').textContent = mode === 'auto'
    ? 'Agents work on their own: no permission prompts (they still stop for plans and questions).'
    : 'Agents ask before running commands or changing files.';
  $('agents-toggle').textContent = mode === 'auto' ? 'Make agents ask first' : 'Let agents work on their own';
  $('agents-toggle').dataset.next = mode === 'auto' ? 'ask' : 'auto';
}
act('agents-toggle', async () => {
  const next = $('agents-toggle').dataset.next;
  if (next === 'auto' && !window.confirm('Agents will run commands and change files in this server\'s container without asking. Continue?')) return;
  renderAgents((await api('agents-mode', { mode: next })).mode);
});

function renderAI(state) {
  $('ai-form').hidden = !state.url;
  $('ai-status').textContent = !state.url ? 'Not available: the installer did not publish the AI gateway on this server.'
    : `AI clients on your tailnet reach this server at ${state.url} with a key. ${state.keys.length || 'No'} active key${state.keys.length === 1 ? '' : 's'}.`;
  const address = state.url || 'https://<server>.<tailnet>.ts.net:8444/mcp';
  $('ai-client').textContent = [
    '# Claude Code on a device in your tailnet:',
    `claude mcp add --transport http swarm ${address} --header "Authorization: Bearer <key>"`,
  ].join('\n');
  $('ai-keys').replaceChildren(...state.keys.map(k => {
    const button = el('button', { type: 'button', textContent: 'Revoke' });
    button.onclick = async () => {
      if (!window.confirm(`Revoke "${k.name}"? Clients using it lose access immediately.`)) return;
      button.disabled = true; try { $('ai-new').hidden = true; renderAI(await api('ai-key-revoke', { id: k.id })); } catch (e) { fail(e); button.disabled = false; }
    };
    return el('div', { className: 'item' }, el('div', {}, el('b', { textContent: `${k.name} · ${k.access === 'write' ? 'read and write' : 'read only'}` }),
      el('p', { textContent: `${k.hint} · expires ${new Date(k.expires_at).toLocaleDateString()} · ${k.last_used_at ? 'last used ' + new Date(k.last_used_at).toLocaleString() : 'never used'}` })), button);
  }));
}
act('ai-form', async e => {
  const f = e.target.elements;
  const created = await api('ai-key-create', { name: f.name.value, access: f.access.value, days: Number(f.days.value) });
  $('ai-token').textContent = created.token; $('ai-new').hidden = false; renderAI(await api('ai-keys'));
}, 'submit');

act('model-provider', async () => {
  models = $('model-provider').value ? (await api('catalog', { provider: $('model-provider').value })).records : [];
  options('model', models.map(m => ({ label: m.display_name || m.model, value: m.model })), 'Choose a model');
  options('thinking', []);
}, 'change');
act('model', () => {
  const m = models.find(m => m.model === $('model').value);
  options('thinking', m?.thinking_options || []);
  if (m?.default_thinking) $('thinking').value = m.default_thinking;
}, 'change');
act('save-model', async () => {
  if (!$('model').value) throw new Error('Choose a provider and model first.');
  const slots = $('slot').value === 'all' ? ['action', 'plan', 'compact', 'finder', 'coder', 'designer', 'router'] : [$('slot').value];
  let result;
  for (const slot of slots) result = await api('model', { provider: $('model-provider').value, model: $('model').value, thinking: $('thinking').value, slot });
  $('assignments').textContent = JSON.stringify(result.agent_model_settings, null, 2);
});
act('models-reset', async () => { const result = await api('models-recommended'); $('assignments').textContent = JSON.stringify(result.agent_model_settings, null, 2); });

// ---- home: workspaces and conversations -------------------------------------

async function openHome() {
  show('home');
  workspaces = await api('workspaces');
  if (!selected || !workspaces.some(w => w.workspace_id === selected.workspace_id)) selected = workspaces[0] || null;
  renderWorkspaces(); await sessions();
}
act('nav-home', openHome);
function renderWorkspaces() {
  $('workspace-list').replaceChildren(...workspaces.map(w => {
    const button = el('button', { type: 'button', textContent: w.name || w.workspace_name || w.path, className: w === selected ? 'selected' : '' });
    button.onclick = async () => { selected = w; stream?.abort(); sessionId = ''; renderWorkspaces(); await sessions().catch(fail); };
    return button;
  }));
  if (!workspaces.length) $('workspace-list').append(el('p', { className: 'muted', textContent: 'No workspaces yet. Add one below.' }));
}
async function sessions() {
  if (!selected) { $('sessions').replaceChildren(); return; }
  const list = await api('sessions', { workspace_id: selected.workspace_id || selected.id });
  $('sessions').replaceChildren(...list.map(s => {
    const button = el('button', { type: 'button', textContent: s.title || s.id, className: s.id === sessionId ? 'selected' : '' });
    button.onclick = () => { sessionId = s.id; $('session-title').textContent = s.title || 'Conversation'; pendingMessage = null; void sessions(); watch().catch(fail); };
    return button;
  }));
}
act('workspace-new', async e => {
  const created = await api('workspace-create', { name: e.target.elements.name.value });
  e.target.elements.name.value = '';
  workspaces = await api('workspaces');
  selected = workspaces.find(w => w.workspace_id === created.workspace_id) || selected;
  renderWorkspaces(); await sessions();
}, 'submit');
act('session-form', async e => {
  if (!selected) throw new Error('Create or pick a workspace first.');
  const result = await api('session', { path: selected.path || selected.workspace_path, title: e.target.elements.title.value, request_id: crypto.randomUUID() });
  e.target.elements.title.value = '';
  sessionId = result.id; $('session-title').textContent = result.title; await sessions(); void watch().catch(fail);
}, 'submit');

function render(state) {
  $('stream-status').textContent = state.status === 'live' ? 'Live' : state.status; $('send').disabled = state.status !== 'live';
  $('messages').replaceChildren(...state.messages.map(m => {
    const div = document.createElement('article'), role = document.createElement('strong'); div.className = 'message'; div.dataset.role = m.role; div.dataset.messageId = m.id;
    role.textContent = m.role === 'user' ? 'You' : m.role; div.append(role, document.createTextNode(m.content)); return div;
  }));
  $('live').replaceChildren(...state.live.map(l => { const p = document.createElement('article'); p.className = 'draft'; p.dataset.stream = JSON.stringify([l.runId, l.streamId]); p.textContent = l.text; return p; }));
  const snap = state.snapshot, view = snap.session_views_by_id?.[state.sessionId] || {}, run = snap.current_run_state_by_session?.[state.sessionId] || view.current_run_state;
  runId = run?.active ? run.run_id : ''; $('stop').disabled = !runId;
  $('activity').textContent = JSON.stringify({ run, tools: (snap.events_by_session?.[state.sessionId] || []).filter(e => e.event_type.includes('tool')).slice(-30) }, null, 2);
  $('history-status').textContent = snap.omissions?.length ? 'Showing the latest part of a long history.' : '';
  $('permissions').replaceChildren(...(view.pending_permissions || []).map(p => {
    const box = document.createElement('div'); box.className = 'permission'; const title = document.createElement('strong'), details = document.createElement('pre');
    title.textContent = `The agent wants to use ${p.tool_name}`; details.textContent = `Call: ${p.call_id}\nPermission: ${p.id}\n${p.tool_arguments}`; box.append(title, details);
    for (const [action, label] of [['allow_once', 'Allow once'], ['deny', 'Deny']]) {
      const button = document.createElement('button'); button.textContent = label;
      button.onclick = async () => { button.disabled = true; try { await api('permission', { id: state.sessionId, permission_id: p.id, action }); } catch (e) { fail(e); button.disabled = false; } }; box.append(button);
    } return box;
  }));
}
async function watch() {
  stream?.abort(); if (!sessionId) return;
  const controller = new AbortController(); stream = controller; $('stream-status').textContent = 'Connecting…'; $('send').disabled = true;
  try {
    const response = await request('/watch', { id: sessionId }, controller.signal);
    if (!response.ok) throw new Error((await response.json()).error);
    const reader = response.body.pipeThrough(new TextDecoderStream()).getReader(); let buffer = '';
    while (true) {
      const { done, value } = await reader.read(); if (done) break; buffer += value;
      if (buffer.length > 9 * 1024 * 1024) throw new Error('This conversation view is too large for the app.');
      let newline; while ((newline = buffer.indexOf('\n')) >= 0) {
        const event = JSON.parse(buffer.slice(0, newline)); buffer = buffer.slice(newline + 1);
        if (event.error) throw new Error(event.error); if (event.state && stream === controller) render(event.state);
      }
    }
  } catch (e) { if (!controller.signal.aborted) fail(e); }
  finally { controller.abort(); if (stream === controller) { $('stream-status').textContent = 'Disconnected · press Reconnect'; $('send').disabled = true; $('stop').disabled = true; } }
}
act('reconnect', watch);
act('message-form', async () => {
  const content = $('message').value;
  if (!pendingMessage || pendingMessage.content !== content || pendingMessage.id !== sessionId) pendingMessage = { content, id: sessionId, request_id: crypto.randomUUID() };
  $('send').disabled = true;
  try { await api('message', pendingMessage); $('message').value = ''; pendingMessage = null; }
  finally { $('send').disabled = $('stream-status').textContent !== 'Live'; }
}, 'submit', false);
act('stop', async () => { await api('stop', { id: sessionId, run_id: runId }); }, 'click', false);
window.addEventListener('pagehide', () => stream?.abort());

if (typeof fetch === 'function') boot().catch(fail);
