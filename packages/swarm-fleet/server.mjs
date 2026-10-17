// swarm-fleet: one MCP server (stdio) for every Swarm machine you reach over
// your tailnet. Each machine serves Swarm Control at /mcp; this process lists
// the union of their tools with a `machine` argument and forwards each call to
// that machine. Its only state is one fleet-wide agent model table (see
// "One model table for the fleet" in README.md), kept in SWARM_FLEET_STATE.
//
// Machines come from the tailnet itself: every peer carrying one of
// FLEET_TAGS (default tag:swarm) is a machine at https://NAME:8444/mcp. The
// tailnet policy decides what this device may do on each (the
// swarmagent.dev/cap/swarm grant), so no key is needed. SWARM_FLEET can add
// machines or override one, optionally with an AI key:
//
//   SWARM_FLEET='[{"name":"box","url":"https://box.tailnet.ts.net:8444/mcp","token":"swk_..."}]'
//   FLEET_PROXY=http://127.0.0.1:1055       (tailscaled's outbound proxy, userspace mode)
//   FLEET_TAILSCALE_SOCKET=/path/to/socket  (that tailscaled's socket; default: the system one)
//   FLEET_TAGS=tag:swarm,tag:swarm-protected
//
// Each machine enforces its own limits; this server adds no authority.
import { request as httpsRequest } from 'node:https';
import { request as httpRequest } from 'node:http';
import { connect as tlsConnect } from 'node:tls';
import { execFile } from 'node:child_process';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';
import { mkdirSync, readFileSync, renameSync, writeFileSync, chmodSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { homedir } from 'node:os';

const PROTOCOL_VERSIONS = ['2025-11-25', '2025-06-18', '2025-03-26'];
const CALL_TIMEOUT_MS = 330_000; // Swarm tools wait at most 300 s for a reply.
const LIST_TIMEOUT_MS = 15_000;

/** A call that asks the machine to wait gets that wait plus 30 s, not the full 330 s. */
export function callTimeoutMs(args) {
  const wait = Number(args?.wait_seconds);
  return Number.isFinite(wait) && wait > 0 ? Math.min(CALL_TIMEOUT_MS, (Math.min(wait, 300) + 30) * 1000) : CALL_TIMEOUT_MS;
}
const DISCOVER_EVERY_MS = 20_000;
const MAX_RESPONSE_BYTES = 8 << 20;
const MACHINE_PROP = { type: 'string', maxLength: 64, description: 'Machine name from swarm_list_machines. Required when several are configured.' };
const INSTRUCTIONS = 'swarm-fleet reaches your Swarm machines over your private tailnet. ' +
  'Call swarm_list_machines (or swarm_fleet_status for one view of every machine) first; pass `machine` to every other tool when more than one machine is configured. ' +
  'Each machine allows only what your tailnet policy (or its AI key) grants this device: read only, or read and write. ' +
  'Every machine runs its roles on Swarm\'s recommended models; swarm_fleet_set_models overrides them fleet-wide (advanced, only when the person asks).';

// machineInstructions joins the fleet's own text with what a machine says
// about itself (its initialize instructions), so an AI on the fleet is told
// the same way of working as an AI on the machine.
export function machineInstructions(own) {
  const text = String(own || '').trim();
  return text ? `${INSTRUCTIONS} Each machine says: ${text}` : INSTRUCTIONS;
}

const NAME_RE = /^[a-z0-9][a-z0-9-]{0,40}$/;

/** Parses and validates SWARM_FLEET (may be empty). Tokens never appear in errors. */
export function parseFleet(raw) {
  if (!raw || !String(raw).trim()) return [];
  let list;
  try { list = JSON.parse(raw); } catch { throw new Error('SWARM_FLEET must be a JSON array of {name, url, token?}'); }
  if (!Array.isArray(list) || list.length > 50) throw new Error('SWARM_FLEET must list at most 50 machines');
  const seen = new Set();
  return list.map((m, i) => {
    const name = String(m?.name ?? '');
    if (!NAME_RE.test(name) || seen.has(name)) throw new Error(`SWARM_FLEET entry ${i}: name must be unique lowercase letters, digits and dashes`);
    seen.add(name);
    let url;
    try { url = new URL(String(m?.url ?? '')); } catch { throw new Error(`SWARM_FLEET entry ${name}: url is invalid`); }
    const loopback = ['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname);
    if (!(url.protocol === 'https:' || (url.protocol === 'http:' && loopback)) || url.username || url.password || url.search || url.hash) {
      throw new Error(`SWARM_FLEET entry ${name}: url must be https (http only on loopback) with no credentials or query`);
    }
    const token = m?.token === undefined || m?.token === null || m?.token === '' ? '' : String(m.token);
    if (token && !/^swk_[A-Za-z0-9_-]{16,200}$/.test(token)) throw new Error(`SWARM_FLEET entry ${name}: token must be a Swarm AI key (swk_...) or left out`);
    return { name, url, token, source: 'configured' };
  });
}

/** Machines from `tailscale status --json`: tagged peers (and self) as Swarm gateways. */
export function machinesFromStatus(status, { tags = ['tag:swarm'], port = 8444 } = {}) {
  const nodes = [status?.Self, ...Object.values(status?.Peer || {})].filter(Boolean);
  const out = [], seen = new Set();
  for (const node of nodes) {
    const nodeTags = Array.isArray(node.Tags) ? node.Tags : [];
    const tag = tags.find(t => nodeTags.includes(t));
    const dns = String(node.DNSName || '').replace(/\.$/, '');
    if (!tag || !dns.endsWith('.ts.net')) continue;
    const name = dns.split('.')[0].toLowerCase().replace(/[^a-z0-9-]/g, '-').replace(/^-+/, '').slice(0, 41);
    if (!NAME_RE.test(name) || seen.has(name)) continue;
    seen.add(name);
    out.push({ name, url: new URL(`https://${dns}:${port}/mcp`), token: '', source: tag, online: node.Online !== false || node === status?.Self });
  }
  return out;
}

/** Runs `tailscale status --json` against the given (or the system) tailscaled. */
export function tailnetDiscoverer({ socket = '', tags, port } = {}) {
  return () => new Promise(resolve => {
    const args = [...(socket ? [`--socket=${socket}`] : []), 'status', '--json'];
    execFile('tailscale', args, { timeout: 10_000, maxBuffer: 16 << 20 }, (error, stdout) => {
      if (error) { resolve({ machines: [], error: `tailscale status failed: ${String(error.message || error).slice(0, 160)}` }); return; }
      try { resolve({ machines: machinesFromStatus(JSON.parse(stdout), { tags, port }) }); }
      catch (e) { resolve({ machines: [], error: `tailscale status was not JSON: ${String(e.message || e).slice(0, 120)}` }); }
    });
  });
}

/** Opens a TCP tunnel through an HTTP CONNECT proxy (tailscaled's outbound proxy). */
function tunnel(proxy, host, port) {
  return new Promise((resolve, reject) => {
    const req = httpRequest({ host: proxy.hostname, port: proxy.port || 80, method: 'CONNECT', path: `${host}:${port}`, headers: { Host: `${host}:${port}` } });
    req.once('connect', (res, socket) => {
      if (res.statusCode !== 200) { socket.destroy(); reject(new Error(`tailnet proxy refused ${host}:${port} (${res.statusCode})`)); return; }
      resolve(socket);
    });
    req.once('error', reject);
    req.setTimeout(LIST_TIMEOUT_MS, () => req.destroy(new Error('tailnet proxy timed out')));
    req.end();
  });
}

/** POSTs one JSON-RPC message to a machine's /mcp; resolves the parsed JSON body. */
export async function post(machine, message, { proxy = '', timeoutMs = CALL_TIMEOUT_MS } = {}) {
  const { url, token } = machine;
  const body = JSON.stringify(message);
  const port = Number(url.port || (url.protocol === 'https:' ? 443 : 80));
  const headers = { 'Content-Type': 'application/json', Accept: 'application/json', 'Content-Length': Buffer.byteLength(body), 'MCP-Protocol-Version': PROTOCOL_VERSIONS[1] };
  if (token) headers.Authorization = `Bearer ${token}`;
  const options = { method: 'POST', hostname: url.hostname, port, path: url.pathname, headers };
  if (proxy && url.protocol === 'https:') {
    const socket = await tunnel(new URL(proxy), url.hostname, port);
    // No agent: with createConnection and no agent, Node uses only this socket
    // (agent:false would make a fresh agent that dials by DNS instead).
    options.createConnection = () => tlsConnect({ socket, servername: url.hostname });
  }
  const send = url.protocol === 'https:' ? httpsRequest : httpRequest;
  return new Promise((resolve, reject) => {
    const req = send(options, res => {
      const chunks = []; let size = 0;
      res.on('data', c => { size += c.length; if (size > MAX_RESPONSE_BYTES) { req.destroy(new Error('machine response too large')); return; } chunks.push(c); });
      res.on('end', () => {
        const text = Buffer.concat(chunks).toString('utf8');
        if (res.statusCode === 202) { resolve(null); return; }
        let parsed;
        try { parsed = JSON.parse(text); } catch { reject(new Error(`machine answered HTTP ${res.statusCode} without JSON`)); return; }
        if (res.statusCode !== 200) { reject(new Error(`machine answered HTTP ${res.statusCode}: ${String(parsed?.error ?? parsed?.message ?? '').slice(0, 200)}`)); return; }
        resolve(parsed);
      });
    });
    req.once('error', reject);
    // setTimeout on a request is an idle timer that restarts on every byte; this one is a
    // total deadline, so a machine that keeps the connection busy cannot hold a call forever.
    const deadline = setTimeout(() => req.destroy(new Error('machine did not answer in time')), timeoutMs);
    req.once('close', () => clearTimeout(deadline));
    req.setTimeout(timeoutMs, () => req.destroy(new Error('machine did not answer in time')));
    req.end(body);
  });
}

const rpcResult = (id, result) => ({ jsonrpc: '2.0', id, result });
const rpcError = (id, code, message) => ({ jsonrpc: '2.0', id, error: { code, message } });
const toolText = (value, isError = false) => ({ content: [{ type: 'text', text: typeof value === 'string' ? value : JSON.stringify(value) }], ...(isError ? { isError: true } : {}) });
const short = error => String(error?.message || error).slice(0, 200);

/**
 * What a machine lets this device do, from the tool names it lists (null when
 * unreached). Exact for AI keys and tailnet grants, whose level decides the
 * list; a plain scoped token lists every tool, so for it this is an upper bound.
 */
export function accessLevel(names) {
  if (!names) return 'none';
  // A machine lists its default tools unless asked for all of them, so the
  // worker and job tools count as much as the advanced ones.
  if (names.includes('swarm_request_provider_key') || names.includes('swarm_create_worker') || names.includes('swarm_manage_worker')) return 'full control';
  if (names.includes('swarm_resolve_permission')) return 'read, write and approve';
  return names.includes('swarm_start_session') || names.includes('swarm_give_job') ? 'read and write' : 'read only';
}

/** The JSON a Swarm tool returned as text, or null. */
function toolJSON(result) {
  if (!result || result.isError) return null;
  try { return JSON.parse(result.content?.[0]?.text ?? ''); } catch { return null; }
}


// ---- One model table for the fleet -----------------------------------------
// Every machine runs the same role → provider/model/thinking table; a machine
// differs only by an explicit override. The table is the fleet's only state.
const FLEET_ROLES = {
  coder: 'system-coder', finder: 'system-finder', designer: 'system-designer',
  compact: 'system-compact', router: 'system-router',
  action: 'swarm', plan: 'system-orchestrator', // advanced; the defaults are the point
};
const ROLE_OF_MACHINE_ROLE = Object.fromEntries(Object.entries(FLEET_ROLES).map(([k, v]) => [v, k]));
const MAX_STATE_BYTES = 256 << 10;

/** Default state file: $SWARM_FLEET_STATE, else $XDG_CONFIG_HOME/swarm-fleet/fleet.json, else ~/.config/swarm-fleet/fleet.json. */
export function defaultStatePath(env = process.env) {
  if (env.SWARM_FLEET_STATE) return env.SWARM_FLEET_STATE;
  const base = env.XDG_CONFIG_HOME || join(homedir(), '.config');
  return join(base, 'swarm-fleet', 'fleet.json');
}

/** Validates `{role: {provider, model, thinking?}}`; returns a normalized copy. */
export function validateModels(models) {
  if (!models || typeof models !== 'object' || Array.isArray(models)) throw new Error('models must be an object of role → {provider, model, thinking?}');
  const out = {};
  for (const [role, value] of Object.entries(models)) {
    if (!FLEET_ROLES[role]) throw new Error(`unknown role ${JSON.stringify(String(role).slice(0, 40))}; roles are ${Object.keys(FLEET_ROLES).join(', ')}`);
    const provider = String(value?.provider ?? '').trim(), model = String(value?.model ?? '').trim(), thinking = String(value?.thinking ?? '').trim();
    if (!provider || !model || provider.length > 64 || model.length > 200 || thinking.length > 32) throw new Error(`role ${role}: provider and model are required`);
    out[role] = { provider, model, thinking, ...(value?.fast === true ? { fast: true } : {}) };
  }
  return out;
}

function normalizeState(raw) {
  const state = { models: {}, overrides: {} };
  if (raw?.models) state.models = validateModels(raw.models);
  for (const [machine, models] of Object.entries(raw?.overrides || {})) {
    if (!NAME_RE.test(machine)) throw new Error(`override for an invalid machine name`);
    const v = validateModels(models);
    if (Object.keys(v).length) state.overrides[machine] = v;
  }
  return state;
}

/** The state file: read on every use, written atomically, mode 0600. */
export function fleetState(path) {
  return {
    path,
    read() {
      let text;
      try { text = readFileSync(path, 'utf8'); } catch (e) { if (e.code === 'ENOENT') return { models: {}, overrides: {} }; throw e; }
      if (text.length > MAX_STATE_BYTES) throw new Error('fleet state file is too large');
      return normalizeState(JSON.parse(text));
    },
    write(state) {
      const next = normalizeState(state);
      mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
      const tmp = `${path}.${process.pid}.tmp`;
      writeFileSync(tmp, JSON.stringify(next, null, 2) + '\n', { mode: 0o600 });
      chmodSync(tmp, 0o600);
      renameSync(tmp, path);
      return next;
    },
  };
}

/** What one machine should run: the table, with its override on top. */
export function desiredFor(state, machine) {
  return { ...state.models, ...(state.overrides[machine] || {}) };
}

/** Compares a machine's assignment with the desired one (thinking only when the table names one; fast always). */
export function roleState(desired, actual, overridden) {
  if (!desired) return 'unset';
  if (!actual?.provider || !actual?.model) return 'drift';
  const same = actual.provider === desired.provider && actual.model === desired.model && (!desired.thinking || actual.thinking === desired.thinking) && !!actual.fast === !!desired.fast;
  return same ? (overridden ? 'override' : 'in_sync') : 'drift';
}

const FLEET_TOOLS = [
  {
    name: 'swarm_list_machines', title: 'List Swarm machines',
    description: 'List the Swarm machines in this fleet (found on your tailnet by tag, plus any configured), whether each is reachable now, and what this device may do there. Call first; pass `machine` to other tools when several are listed.',
    inputSchema: { type: 'object', additionalProperties: false, properties: {} },
    annotations: { readOnlyHint: true, destructiveHint: false, idempotentHint: true, openWorldHint: false },
  },
  {
    name: 'swarm_fleet_status', title: 'Fleet status',
    description: 'One view of every Swarm machine: reachable, access, exact counts per machine (schedules and how many are active, runs today: succeeded, failed, unfinished, running now, with the failed runs\' job ids), workspaces, sessions (running and recent), and today\'s usage and limits. Today is the UTC day unless you pass timezone.',
    inputSchema: { type: 'object', additionalProperties: false, properties: { timezone: { type: 'string', maxLength: 64, description: 'IANA timezone for "today" (for example Europe/Helsinki). Default UTC.' } } },
    annotations: { readOnlyHint: true, destructiveHint: false, idempotentHint: true, openWorldHint: false },
  },
  {
    name: 'swarm_fleet_models', title: 'The fleet\'s model table',
    description: 'The one agent model table every machine follows (role → provider, model, thinking for coder, finder, designer, compact, router; action and plan are advanced), the per-machine overrides, and per machine what it actually runs: in_sync, override, drift (differs from the table), unset (nothing in the table) or unknown (not reachable). Start here before changing models.',
    inputSchema: { type: 'object', additionalProperties: false, properties: {} },
    annotations: { readOnlyHint: true, destructiveHint: false, idempotentHint: true, openWorldHint: false },
  },
  {
    name: 'swarm_fleet_set_models', title: 'Set the fleet\'s model table',
    description: 'Change the model table for the whole fleet and apply it to every machine, one role at a time. Without `machine`, `models` merges into the table and goes to every machine (a machine keeps its own override for a role). With `machine`, `models` becomes that machine\'s override for those roles only, or `clear_override` removes its override and the table applies again. Overrides are the exception: everything should run the same table unless there is a reason. A machine admitted only by a tailnet read/write grant cannot be managed; it needs a full AI key (`token`) in SWARM_FLEET. Machines that refuse are listed in `applied` with ok:false, nothing is thrown. thinking may be left out: the machine then uses its catalog default. fast: true runs a role on priority serving (faster, costs more) where the model offers it.',
    inputSchema: {
      type: 'object', additionalProperties: false,
      properties: {
        models: {
          type: 'object', description: 'role → {provider, model, thinking?, fast?}',
          additionalProperties: { type: 'object', additionalProperties: false, required: ['provider', 'model'], properties: { provider: { type: 'string', maxLength: 64 }, model: { type: 'string', maxLength: 200 }, thinking: { type: 'string', maxLength: 32 }, fast: { type: 'boolean', description: 'Priority serving (faster, costs more) where the model offers it' } } },
        },
        machine: { type: 'string', maxLength: 64, description: 'Set or clear an override for this machine only' },
        clear_override: { type: 'boolean', description: 'With machine: drop its override and apply the table to it' },
        apply: { type: 'boolean', description: 'Apply to the machines now (default true); false only records the table' },
      },
    },
    annotations: { readOnlyHint: false, destructiveHint: false, idempotentHint: true, openWorldHint: false },
  },
];

export function createFleet(configured, { proxy = '', notify = () => {}, send = post, discover = null, now = Date.now, statePath = '' } = {}) {
  const state = fleetState(statePath || defaultStatePath());
  let machines = configured.slice();
  let discoveryError = '';
  let discoveredAt = -Infinity;
  const tools = new Map(); // machine name -> tools/list result (array) once reached
  const instructions = new Map(); // machine name -> its initialize instructions
  const errors = new Map();
  let advertised = 0;

  async function rediscover(force = false) {
    if (!discover || (!force && now() - discoveredAt < DISCOVER_EVERY_MS)) return;
    discoveredAt = now();
    const { machines: found = [], error = '' } = await discover();
    discoveryError = error;
    // Configured entries win over a discovered machine of the same name.
    const byName = new Map(found.map(m => [m.name, m]));
    for (const m of configured) byName.set(m.name, m);
    machines = [...byName.values()].sort((a, b) => a.name.localeCompare(b.name));
    for (const name of [...tools.keys()]) if (!byName.has(name)) { tools.delete(name); errors.delete(name); instructions.delete(name); }
  }
  async function refresh(machine) {
    if (machine.online === false) { errors.set(machine.name, 'offline on the tailnet'); return; }
    try {
      const body = await send(machine, { jsonrpc: '2.0', id: 1, method: 'tools/list' }, { proxy, timeoutMs: LIST_TIMEOUT_MS });
      const list = Array.isArray(body?.result?.tools) ? body.result.tools : [];
      tools.set(machine.name, list); errors.delete(machine.name);
    } catch (error) {
      tools.delete(machine.name);
      errors.set(machine.name, short(error));
      return;
    }
    if (instructions.has(machine.name)) return;
    try {
      const init = await send(machine, { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: PROTOCOL_VERSIONS[0], capabilities: {}, clientInfo: { name: 'swarm-fleet', version: '0.2.0' } } }, { proxy, timeoutMs: LIST_TIMEOUT_MS });
      const text = init?.result?.instructions;
      if (typeof text === 'string' && text.trim()) instructions.set(machine.name, text.trim());
    } catch { /* the tools still work; the machine's text is optional */ }
  }
  // The text of the first reachable machine (they run the same product).
  const ownInstructions = () => machines.map(m => instructions.get(m.name)).find(Boolean) || '';
  const refreshAll = async (force = false) => { await rediscover(force); await Promise.all(machines.map(refresh)); };
  const access = name => accessLevel(tools.get(name)?.map(t => t.name) ?? null);
  function union() {
    const byName = new Map();
    for (const machine of machines) for (const tool of tools.get(machine.name) || []) if (tool?.name && !byName.has(tool.name)) byName.set(tool.name, tool);
    const list = FLEET_TOOLS.slice();
    for (const tool of byName.values()) {
      if (FLEET_TOOLS.some(t => t.name === tool.name)) continue;
      const schema = structuredClone(tool.inputSchema || { type: 'object', properties: {} });
      schema.properties = { ...(schema.properties || {}), machine: MACHINE_PROP };
      list.push({ ...tool, inputSchema: schema });
    }
    return list;
  }
  // Tools appear once a machine answers; tell the client when the list changes.
  let retry;
  function watch() {
    clearTimeout(retry);
    retry = setTimeout(async () => {
      await refreshAll();
      const count = union().length;
      if (count !== advertised) { advertised = count; notify({ jsonrpc: '2.0', method: 'notifications/tools/list_changed' }); }
      watch();
    }, machines.length && machines.every(m => tools.has(m.name)) ? 60_000 : 15_000);
    retry.unref?.();
  }
  const callOn = (machine, name, args) => send(machine, { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name, arguments: args } }, { proxy, timeoutMs: callTimeoutMs(args) });

  // A machine's run counts for today, exact (the daemon's run index), with the
  // failed runs' job ids; their reasons stay on the machine (swarm_get_job).
  function runsToday(runs, workers) {
    const failed = Array.isArray(runs.failed_runs) ? runs.failed_runs : [];
    return {
      day: workers?.day ?? null, timezone: workers?.timezone ?? null,
      total: runs.total ?? 0, succeeded: runs.succeeded ?? 0, failed: runs.failed ?? 0, cancelled: runs.cancelled ?? 0,
      unfinished: runs.unfinished ?? 0, running_now: runs.running_now ?? 0,
      failed_runs: failed.slice(0, 10).map(r => ({ job_id: `worker/${r.worker_id}/${r.run_id}`, worker: r.worker_name || r.worker_id, at: r.created_at })),
    };
  }

  async function machineStatus(machine, timezone = '') {
    const base = { machine: machine.name, found_by: machine.source, reachable: tools.has(machine.name), access: access(machine.name) };
    if (!tools.has(machine.name)) return { ...base, error: errors.get(machine.name) || 'not reached' };
    const has = name => tools.get(machine.name).some(t => t.name === name);
    const [status, sessions, usage] = await Promise.all([
      has('swarm_status') ? callOn(machine, 'swarm_status', timezone ? { timezone } : {}).then(b => toolJSON(b?.result), () => null) : null,
      has('swarm_list_sessions') ? callOn(machine, 'swarm_list_sessions', { limit: 50 }).then(b => toolJSON(b?.result), () => null) : null,
      has('swarm_get_usage') ? callOn(machine, 'swarm_get_usage', { period: 'today' }).then(b => toolJSON(b?.result), () => null) : null,
    ]);
    const list = Array.isArray(sessions?.sessions) ? sessions.sessions : null;
    return {
      ...base,
      ...(status ? { status: status.status ?? null, reasons: status.reasons ?? [], attention: status.attention ?? null } : {}),
      ...(status?.runs ? { runs_today: runsToday(status.runs, status.workers) } : {}),
      ...(status?.automations ? { automations: { schedules: status.automations.schedules, active: status.automations.active, next_run_at: status.automations.next_run_at || null } } : {}),
      ...(status?.workspaces?.total !== undefined ? { workspaces: status.workspaces.total } : {}),
      ...(status?.machine?.version ? { version: status.machine.version } : {}),
      ...(list ? { sessions_recent: list.length, sessions_running: list.filter(s => s?.running).map(s => ({ id: s.id, title: s.title, agent: s.agent })) } : {}),
      ...(usage ? { usage_today: usage.summary ?? null, limits: usage.limits ?? null } : {}),
    };
  }


  // The model table: what each machine runs against what it should.
  async function machineModels(machine, table) {
    const base = { machine: machine.name, access: access(machine.name) };
    const desired = desiredFor(table, machine.name);
    const overrides = table.overrides[machine.name] || {};
    const roles = Object.keys(FLEET_ROLES);
    // swarm_list_models is an advanced tool: not listed by default, still
    // callable with read access.
    if (!tools.has(machine.name)) {
      return { ...base, roles: roles.map(role => ({ role, ...(desired[role] || {}), state: 'unknown' })), error: errors.get(machine.name) || 'cannot list models' };
    }
    let listed;
    try { listed = toolJSON((await callOn(machine, 'swarm_list_models', {}))?.result); } catch (error) { listed = null; base.error = short(error); }
    const actual = {};
    for (const r of Array.isArray(listed?.roles) ? listed.roles : []) {
      const role = ROLE_OF_MACHINE_ROLE[r?.role];
      if (role) actual[role] = { provider: r.provider || '', model: r.model || '', thinking: r.thinking || '', ...(r.fast === true ? { fast: true } : {}) };
    }
    return {
      ...base,
      roles: roles.map(role => ({
        role,
        ...(actual[role] || desired[role] || {}),
        ...(desired[role] && actual[role] && roleState(desired[role], actual[role], !!overrides[role]) === 'drift' ? { wanted: desired[role] } : {}),
        state: listed ? roleState(desired[role], actual[role], !!overrides[role]) : 'unknown',
      })),
    };
  }

  // One set_agent_model per role, sequential on a machine; machines in parallel.
  async function applyModels(table, targets) {
    const applied = [];
    await Promise.all(targets.map(async ({ machine, roles }) => {
      for (const role of roles) {
        const want = desiredFor(table, machine.name)[role];
        if (!want) continue;
        const entry = { machine: machine.name, role, ok: false };
        if (!tools.has(machine.name)) { applied.push({ ...entry, error: errors.get(machine.name) || 'not reached' }); continue; }
        if (access(machine.name) === 'read only') { applied.push({ ...entry, error: 'read-only access on this machine: setting models needs a write or full AI key in SWARM_FLEET' }); continue; }
        try {
          const body = await callOn(machine, 'swarm_set_agent_model', { role: FLEET_ROLES[role], provider: want.provider, model: want.model, ...(want.thinking ? { thinking: want.thinking } : {}), ...(want.fast ? { fast: true } : {}) });
          if (body?.error) applied.push({ ...entry, error: String(body.error.message || 'error').slice(0, 200) });
          else if (body?.result?.isError) applied.push({ ...entry, error: String(body.result.content?.[0]?.text || 'refused').slice(0, 200) });
          else applied.push({ ...entry, ok: true });
        } catch (error) { applied.push({ ...entry, error: short(error) }); }
      }
    }));
    return applied.sort((a, b) => a.machine.localeCompare(b.machine) || a.role.localeCompare(b.role));
  }

  async function setModels(args) {
    const table = state.read();
    const wanted = typeof args.machine === 'string' ? args.machine : '';
    const changes = args.models === undefined ? {} : validateModels(args.models);
    const targets = [];
    if (wanted) {
      const machine = machines.find(m => m.name === wanted);
      if (!machine) return toolText(`Unknown machine ${wanted}; see swarm_list_machines.`, true);
      if (args.clear_override) {
        const roles = Object.keys(table.overrides[wanted] || {});
        delete table.overrides[wanted];
        targets.push({ machine, roles });
      } else {
        if (!Object.keys(changes).length) return toolText('Give models to override on that machine, or clear_override.', true);
        table.overrides[wanted] = { ...(table.overrides[wanted] || {}), ...changes };
        targets.push({ machine, roles: Object.keys(changes) });
      }
    } else {
      if (!Object.keys(changes).length) return toolText('Give models (role → {provider, model, thinking?}) for the table.', true);
      table.models = { ...table.models, ...changes };
      for (const machine of machines) {
        const roles = Object.keys(changes).filter(role => !table.overrides[machine.name]?.[role]);
        if (roles.length) targets.push({ machine, roles });
      }
    }
    const saved = state.write(table);
    const applied = args.apply === false ? [] : await applyModels(saved, targets);
    return toolText({ models: saved.models, overrides: saved.overrides, applied });
  }

  async function callTool(params) {
    const name = String(params?.name || '');
    const args = params?.arguments && typeof params.arguments === 'object' && !Array.isArray(params.arguments) ? { ...params.arguments } : {};
    if (name === 'swarm_list_machines' || name === 'swarm_fleet_status') {
      await refreshAll(true);
      const note = machines.length ? {} : { note: discoveryError || 'No Swarm machines found: tag them (default tag:swarm) in the Tailscale admin console, or set SWARM_FLEET.' };
      if (name === 'swarm_fleet_status') {
        const timezone = typeof args.timezone === 'string' ? args.timezone.trim().slice(0, 64) : '';
        return toolText({ machines: await Promise.all(machines.map(m => machineStatus(m, timezone))), ...note });
      }
      return toolText({ machines: machines.map(m => ({
        machine: m.name, found_by: m.source, reachable: tools.has(m.name), access: access(m.name),
        ...(errors.has(m.name) ? { error: errors.get(m.name) } : {}),
      })), ...note });
    }
    if (name === 'swarm_fleet_models' || name === 'swarm_fleet_set_models') {
      await refreshAll(true);
      let table;
      try { table = state.read(); } catch (error) { return toolText(`The fleet state file could not be read: ${short(error)}`, true); }
      if (name === 'swarm_fleet_models') return toolText({ models: table.models, overrides: table.overrides, machines: await Promise.all(machines.map(m => machineModels(m, table))) });
      try { return await setModels(args); } catch (error) { return toolText(short(error), true); }
    }
    const wanted = typeof args.machine === 'string' ? args.machine : '';
    delete args.machine;
    if (!machines.some(m => m.name === wanted)) await rediscover();
    const target = wanted ? machines.find(m => m.name === wanted) : machines.length === 1 ? machines[0] : null;
    if (!target) return toolText(wanted ? `Unknown machine ${wanted}; see swarm_list_machines.` : 'Several machines are configured: pass `machine` (see swarm_list_machines).', true);
    let body;
    try { body = await callOn(target, name, args); }
    catch (error) { return toolText(`Machine ${target.name} could not be reached: ${short(error)}`, true); }
    if (body?.error) return toolText(`Machine ${target.name} rejected the call: ${String(body.error.message || 'error').slice(0, 300)}`, true);
    return body?.result ?? toolText(`Machine ${target.name} returned no result.`, true);
  }
  return {
    async handle(message) {
      const { id, method } = message || {};
      if (id === undefined || id === null) return null; // notification
      switch (method) {
        case 'initialize': {
          const requested = message.params?.protocolVersion;
          if (!machines.some(m => instructions.has(m.name))) await refreshAll();
          return rpcResult(id, {
            protocolVersion: PROTOCOL_VERSIONS.includes(requested) ? requested : PROTOCOL_VERSIONS[0],
            capabilities: { tools: { listChanged: true } },
            serverInfo: { name: 'swarm-fleet', title: 'Swarm fleet', version: '0.2.0' },
            instructions: machineInstructions(ownInstructions()),
          });
        }
        case 'ping': return rpcResult(id, {});
        case 'tools/list': {
          if (!machines.some(m => tools.has(m.name))) await refreshAll();
          const list = union(); advertised = list.length; watch();
          return rpcResult(id, { tools: list });
        }
        case 'tools/call': return rpcResult(id, await callTool(message.params));
        default: return rpcError(id, -32601, 'method not found');
      }
    },
    machines: () => machines.map(m => m.name),
    stop() { clearTimeout(retry); },
  };
}

// stdio transport: one JSON-RPC message per line on stdin/stdout. Logs go to stderr.
export function serveStdio(fleet, input = process.stdin, output = process.stdout) {
  const write = message => { if (message) output.write(JSON.stringify(message) + '\n'); };
  const lines = createInterface({ input, crlfDelay: Infinity });
  lines.on('line', async line => {
    if (!line.trim()) return;
    let message;
    try { message = JSON.parse(line); } catch { write(rpcError(null, -32700, 'parse error')); return; }
    if (Array.isArray(message)) { write(rpcError(null, -32600, 'batching is not supported')); return; }
    try { write(await fleet.handle(message)); }
    catch (error) { write(rpcError(message.id ?? null, -32603, 'internal error')); console.error('swarm-fleet:', error?.message || error); }
  });
  lines.on('close', () => { fleet.stop(); });
  return write;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  let configured;
  try { configured = parseFleet(process.env.SWARM_FLEET); }
  catch (error) { console.error(`swarm-fleet: ${error.message}`); process.exit(2); }
  const tags = String(process.env.FLEET_TAGS || 'tag:swarm').split(',').map(t => t.trim()).filter(t => /^tag:[a-z0-9][a-z0-9-]{0,40}$/.test(t));
  const port = Number(process.env.FLEET_PORT || 8444);
  const discover = process.env.FLEET_DISCOVER === 'off' ? null : tailnetDiscoverer({ socket: process.env.FLEET_TAILSCALE_SOCKET || '', tags, port });
  let write = () => {};
  const fleet = createFleet(configured, { proxy: process.env.FLEET_PROXY || '', notify: m => write(m), discover, statePath: defaultStatePath() });
  write = serveStdio(fleet);
  console.error(`swarm-fleet: ${configured.length} configured machine(s)${discover ? `; discovering ${tags.join(', ')} on the tailnet` : ''}`);
}
