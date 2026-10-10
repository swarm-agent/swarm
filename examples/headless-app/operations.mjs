import { readFile, realpath, rename, writeFile } from 'node:fs/promises';
import { SwarmApiError, SwarmNotFoundError } from '@swarm-agent/sdk';
import { reject, text } from './boundary.mjs';

/**
 * @param {import('@swarm-agent/sdk').SwarmClient} sdk
 * @param {{ relayUrl?: string, deviceName?: string, aiUrl?: string, aiIp?: string, settingsFile?: string }} defaults installer-supplied relay connection
 *   defaults, the tailnet address of this machine's AI gateway (empty when the installer did not publish it) and
 *   the app's own settings file (setup choices; no secrets)
 */
export function operations(sdk, project = '/project', defaults = {}) {
  async function workspace(path) {
    text(path, 512);
    const root = await realpath(project), resolved = await realpath(path);
    if (!resolved.startsWith(root + '/')) reject(403, 'Workspace must be inside the project volume.');
    return resolved;
  }
  async function session(id) {
    text(id);
    const s = await sdk.sessions.get(id);
    // Session runtime paths may be managed worktrees in daemon data. Authorize
    // through the registered source workspace, not its isolated execution path.
    const registered = (await sdk.workspaces.list({ limit: 100 })).find(w => w.workspace_id === s.workspace_id);
    if (!registered) reject(403, 'Session source workspace is not available to this app.');
    await workspace(registered.path);
    return s;
  }
  const credentialStatus = c => ({ id: c.id, provider: c.provider, active: c.active, auth_type: c.auth_type,
    connected: c.connection?.connected === true, defaultsApplied: c.auto_defaults?.applied === true,
    defaultsError: !!c.auto_defaults?.error });
  const loginStatus = c => ({ session_id: c.session_id, method: c.method, status: c.status,
    verification_url: c.verification_url, auth_url: c.auth_url, user_code: c.user_code, expires_at: c.expires_at,
    failed: !!c.error });
  // Relay status for the browser: no keys or tokens exist in it; consents keep
  // only what the owner needs to recognise the requesting client.
  const remoteStatus = r => ({ configured: r.configured, enabled: r.enabled, connected: r.connected,
    relay_url: r.relay_url, device_name: r.device_name, allow_write: r.allow_write, allow_approve: r.allow_approve,
    allow_manage: r.allow_manage, last_error: r.last_error, pairing_code: r.pairing_code, pairing_expires_at: r.pairing_expires_at,
    consents: (r.pending_consents || []).map(c => ({ code: c.code, client_name: c.client_name, redirect_host: c.redirect_host,
      scopes: c.scopes, expires_at: c.expires_at })) });
  // Owner-only relay administration returns key-free messages (bad relay URL,
  // already configured); show those instead of the generic daemon error.
  async function remote(call) {
    try { return remoteStatus(await call()); }
    catch (error) { if (error instanceof SwarmApiError && error.status === 400 && error.message) reject(400, error.message.slice(0, 300)); throw error; }
  }
  // AI keys: scoped tokens carrying Swarm Control levels. The browser sees
  // each key's hint and dates; the full key only once, when it is created.
  const aiLevel = scopes => { const s = scopes || []; return s.includes('swarm:manage') || s.includes('swarm:approve') || s.includes('admin') ? 'full' : s.includes('swarm:write') ? 'write' : 'read'; };
  const aiKey = t => ({ id: t.id, name: t.name, access: aiLevel(t.scopes), hint: t.token_hint,
    created_at: t.created_at, expires_at: t.expires_at, last_used_at: t.last_used_at || 0 });
  async function aiKeys() {
    const tokens = await sdk.auth.listScopedTokens();
    const keys = tokens.filter(t => !t.revoked && (t.scopes || []).some(s => s.startsWith('swarm:'))
      && !(t.expires_at && t.expires_at <= Date.now())).map(aiKey);
    return { url: defaults.aiUrl || '', ip: defaults.aiIp || '', keys };
  }
  const assigned = a => typeof a?.provider === 'string' && !!a.provider && typeof a.model === 'string' && !!a.model;
  // Guided setup: which step comes next. Reads only; each step keeps its own
  // explicit operation below.
  // Setup choices the daemon does not record itself (that the owner has
  // chosen how agents work). No secrets; absent until the first choice.
  async function appSettings() {
    if (!defaults.settingsFile) return {};
    try { return JSON.parse(await readFile(defaults.settingsFile, 'utf8')); }
    catch (error) { if (error.code === 'ENOENT') return {}; throw error; }
  }
  async function saveAppSettings(values) {
    if (!defaults.settingsFile) return;
    const temp = `${defaults.settingsFile}.tmp`;
    await writeFile(temp, JSON.stringify({ ...(await appSettings()), ...values }), { mode: 0o600 });
    await rename(temp, defaults.settingsFile);
  }
  // Agents work on their own (no ordinary permission prompts) or ask first.
  const agentsMode = async () => (await sdk.permissions.bypass()) ? 'auto' : 'ask';
  // The app's owner account doubles as the Swarm owner; created once, on the
  // first sign-up (or the first request after it, if that one failed).
  async function ensureOwner(username) {
    const status = await sdk.onboarding.get();
    if (status.identity?.bootstrapped) return;
    await sdk.onboarding.update({ username: text(username, 64), swarm_name: text(defaults.deviceName || 'swarm', 80) });
  }
  async function setup() {
    const status = await sdk.onboarding.get();
    const steps = { provider: false, models: false, workspace: false, agents: false, claude: false };
    let relay = null, mode = 'ask';
    if (status.identity?.bootstrapped === true) {
      // A saved key counts only once its provider works: a rejected or
      // unverifiable key must not move setup past the provider step.
      steps.provider = (await sdk.settings.providers()).some(p => p.ready && p.runnable);
      let settings = null;
      try { settings = (await sdk.settings.agentModels()).agent_model_settings; }
      catch (error) { if (!(error instanceof SwarmNotFoundError)) throw error; }
      const agents = Object.values(settings?.system_agents || {});
      steps.models = !!settings && assigned(settings.swarm?.action) && assigned(settings.swarm?.plan) && agents.length >= 5 && agents.every(assigned);
      steps.workspace = (await sdk.workspaces.list({ limit: 100 })).some(w => (w.path || w.workspace_path || '').startsWith(project + '/'));
      relay = remoteStatus(await sdk.remote.status());
      steps.claude = relay.connected === true;
      mode = await agentsMode();
      steps.agents = mode === 'auto' || (await appSettings()).agents_chosen === true;
    }
    return { steps, agents: mode, remote: relay, defaults: { relay_url: defaults.relayUrl || '', device_name: defaults.deviceName || '' } };
  }
  return {
    session,
    ensureOwner,
    async run(op, b, username = '') {
      if (op === 'onboarding') { const s = await sdk.onboarding.get(); return { identity: s.identity, needs_onboarding: s.needs_onboarding }; }
      if (username) await ensureOwner(username);
      else if (!(await sdk.onboarding.get()).identity.bootstrapped) reject(409, 'Create the owner account first.');
      if (op === 'setup') return setup();
      switch (op) {
        case 'settings': {
          let settings = null;
          try { settings = await sdk.settings.agentModels(); }
          catch (error) { if (!(error instanceof SwarmNotFoundError)) throw error; }
          return { providers: await sdk.settings.providers(), settings,
            credentials: (await sdk.auth.credentials.list()).records.map(credentialStatus) };
        }
        case 'catalog': return sdk.settings.models(text(b.provider));
        case 'credential': {
          const p = (await sdk.settings.providers()).find(p => p.id === b.provider);
          if (!p?.auth_methods?.some(m => m.credential_type === b.type)) reject(400, 'Select an advertised credential type.');
          return credentialStatus(await sdk.auth.credentials.save({ provider: p.id, type: text(b.type), api_key: text(b.key, 16000), active: true, label: 'Headless app' }));
        }
        case 'codex-start': {
          if (!['device', 'manual'].includes(b.method)) reject(400, 'Select device or manual sign-in.');
          return loginStatus(await sdk.auth.codex.start({ method: b.method, active: true }));
        }
        case 'codex-status': return loginStatus(await sdk.auth.codex.status(text(b.id)));
        case 'codex-complete': return loginStatus(await sdk.auth.codex.complete(text(b.id), text(b.callback, 16000)));
        case 'model': {
          const records = (await sdk.settings.models(text(b.provider))).records;
          const model = records.find(m => m.model === b.model);
          if (!model) reject(400, 'Select a catalog model.');
          const thinking = typeof b.thinking === 'string' ? b.thinking : '';
          if (thinking && !model.thinking_options?.includes(thinking)) reject(400, 'Unsupported thinking option.');
          if (b.service_tier && !model.service_tiers?.includes(b.service_tier)) reject(400, 'Unsupported service tier.');
          if (b.context_mode && !model.context_modes?.some(m => m.mode === b.context_mode)) reject(400, 'Unsupported context mode.');
          const assignment = { provider: b.provider, model: b.model, thinking,
            ...(b.service_tier ? { service_tier: b.service_tier } : {}), ...(b.context_mode ? { context_mode: b.context_mode } : {}) };
          if (['action', 'plan'].includes(b.slot)) return sdk.settings.setSwarmModel(b.slot, assignment);
          if (['compact', 'finder', 'coder', 'designer', 'router'].includes(b.slot)) return sdk.settings.setSystemAgentModel(b.slot, assignment);
          reject(400, 'Invalid role.');
          break;
        }
        case 'models-recommended': return sdk.settings.restoreDefaults();
        case 'agents-mode': {
          if (!['auto', 'ask'].includes(b.mode)) reject(400, 'Choose whether agents work on their own or ask first.');
          await sdk.permissions.setBypass(b.mode === 'auto');
          await saveAppSettings({ agents_chosen: true });
          return { mode: await agentsMode() };
        }
        case 'workspace-create': {
          const name = text(b.name, 63);
          if (!/^[a-z0-9][a-z0-9_-]*$/.test(name)) reject(400, 'Use lowercase letters, digits, dashes and underscores, e.g. social-bot.');
          return sdk.workspaces.create({ parent_path: await realpath(project), name });
        }
        case 'workspaces': return (await sdk.workspaces.list({ limit: 100 })).filter(w => w.path?.startsWith(project + '/') || w.workspace_path?.startsWith(project + '/'));
        case 'folder': {
          const name = text(b.name, 64);
          if (!/^[a-zA-Z0-9][a-zA-Z0-9_-]*$/.test(name)) reject(400, 'Use letters, digits, hyphens and underscores.');
          return sdk.workspaces.createFolder(project, name);
        }
        case 'repository': return sdk.workspaces.inspectRepository(await workspace(b.path));
        case 'git-init': {
          if (b.confirm !== true) reject(400, 'Explicit Git initialization approval required.');
          const path = await workspace(b.path);
          return sdk.workspaces.setupRepository(path, text(b.expected_path, 512));
        }
        case 'review': return sdk.workspaces.reviewRepository(await workspace(b.path));
        case 'baseline': {
          if (b.confirm !== true || b.confirm_omissions !== true || !Array.isArray(b.paths) || b.paths.length > 500) reject(400, 'Explicit baseline and omission approval required.');
          return sdk.workspaces.baselineRepository({ path: await workspace(b.path), expected_resolved_path: text(b.expected_path, 512), review_digest: text(b.digest),
            selected_paths: b.paths.map(p => text(p, 512)), confirm_baseline: true, confirm_omissions: true });
        }
        case 'register': return sdk.workspaces.add({ path: await workspace(b.path), make_current: false });
        case 'sessions': return sdk.sessions.list({ workspace_id: text(b.workspace_id), limit: 100 });
        case 'session': return sdk.sessions.create({ workspace_path: await workspace(b.path), title: text(b.title, 120), mode: 'auto', client_request_id: text(b.request_id) });
        case 'message': await session(b.id); return sdk.sessions.sendMessage(b.id, { content: text(b.content, 32000), client_request_id: text(b.request_id) });
        case 'stop': await session(b.id); return { ok: await sdk.sessions.stopRun(b.id, { run_id: text(b.run_id) }) };
        case 'permission': {
          await session(b.id);
          const snapshot = await sdk.realtime.hydrate({ session_ids: [b.id], resources: { session_view: true } });
          const pending = snapshot.session_views_by_id?.[b.id]?.pending_permissions;
          if (!Array.isArray(pending) || !pending.some(p => p.id === b.permission_id && p.session_id === b.id)) reject(409, 'Permission is no longer pending. Refresh the session.');
          if (b.action === 'allow_once') return sdk.sessions.approvePermissionOnce(b.id, text(b.permission_id));
          if (b.action === 'deny') return sdk.sessions.denyPermission(b.id, text(b.permission_id));
          reject(400, 'Only allow-once or deny is supported.'); break;
        }
        case 'ai-keys': return aiKeys();
        case 'ai-key-create': {
          const name = text(b.name, 64);
          if (!['read', 'write'].includes(b.access)) reject(400, 'Choose read only or read and write.');
          const days = Number(b.days);
          if (![1, 7, 30, 90, 365].includes(days)) reject(400, 'Choose how long the key lasts.');
          if ((await aiKeys()).keys.length >= 20) reject(409, 'Revoke an old key first (at most 20).');
          const created = await sdk.auth.createAIKey({ name, access: b.access, expires_in_seconds: days * 86400 });
          return { token: created.token, key: aiKey(created.record), url: defaults.aiUrl || '' };
        }
        case 'ai-key-revoke': {
          const id = text(b.id, 200);
          if (!(await aiKeys()).keys.some(k => k.id === id)) reject(404, 'That key is not an active AI key.');
          await sdk.auth.revokeScopedToken(id);
          return aiKeys();
        }
        case 'remote-status': return remote(() => sdk.remote.status());
        case 'remote-connect': {
          const relayUrl = text(b.relay_url, 200);
          let origin;
          try { origin = new URL(relayUrl).origin; } catch { reject(400, 'Enter the relay URL, e.g. https://swarm-relay.example.workers.dev'); }
          const current = await sdk.remote.status();
          if (!current.configured) {
            await remote(() => sdk.remote.init({ relay_url: relayUrl, device_name: text(b.device_name, 80),
              allow_write: b.allow_write === true, allow_approve: b.allow_approve === true, allow_manage: b.allow_manage === true }));
          } else if (current.relay_url !== origin) {
            reject(409, `This machine is set up for ${current.relay_url}. Reset the connection first to use another relay.`);
          }
          return remote(() => sdk.remote.enable());
        }
        case 'remote-disable': return remote(() => sdk.remote.disable());
        case 'remote-reset':
          if (b.confirm !== true) reject(400, 'Confirm that this machine should forget its relay key.');
          return remote(() => sdk.remote.reset());
        case 'remote-consent': {
          const code = text(b.code, 16);
          try { await sdk.remote.decideConsent(code, b.approve === true); }
          catch (error) { if (error instanceof SwarmApiError && error.status === 400) reject(409, 'That request is no longer pending. Refresh.'); throw error; }
          return remote(() => sdk.remote.status());
        }
        default: reject(404, 'Unknown application operation.');
      }
    },
  };
}
