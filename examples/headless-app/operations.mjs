import { realpath } from 'node:fs/promises';
import { SwarmNotFoundError } from '@swarmagent/sdk';
import { reject, text } from './boundary.mjs';

/** @param {import('@swarmagent/sdk').SwarmClient} sdk */
export function operations(sdk, project = '/project') {
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
  return {
    session,
    async run(op, b) {
      if (op === 'onboarding') { const s = await sdk.onboarding.get(); return { identity: s.identity, needs_onboarding: s.needs_onboarding }; }
      if (op === 'owner') {
        const s = await sdk.onboarding.get();
        if (s.identity.bootstrapped) reject(409, 'Owner already exists.');
        await sdk.onboarding.update({ username: text(b.username, 64), swarm_name: text(b.name, 80) });
        return { ok: true };
      }
      if (!(await sdk.onboarding.get()).identity.bootstrapped) reject(409, 'Create the installation owner first.');
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
        default: reject(404, 'Unknown application operation.');
      }
    },
  };
}
