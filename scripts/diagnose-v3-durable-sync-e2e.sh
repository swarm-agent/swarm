#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/diagnose-v3-durable-sync-e2e.sh [ssh-alias] [options]
       scripts/diagnose-v3-durable-sync-e2e.sh --primary-ssh <alias> [options]

Runs a live durable V3 sync E2E against an SSH testbench. The harness uses the
real backend, real Pebble state/outbox, real websocket transport, and a real
Fireworks-backed V3 session turn.

It verifies:
  - legacy Desktop/TUI workset and reconnect responses return opaque v3c1 cursors
  - /v3/realtime/stream accepts a valid v3c1 cursor and emits v3c1 cursors
  - tampered, wrong-scope, and ahead-of-head cursors fail closed with machine-readable WS errors
  - a v3c1 cursor remains valid across a swarm service restart
  - live cursors are backed by a persistent v3sync-* key, not the default dev key
  - a Fireworks-backed assistant turn converges through realtime and durable replay

Options:
  --primary-ssh <alias>      SSH alias for testbench. Default: testbench
  --api-url <url>            API URL used on remote host. Default: http://127.0.0.1:7781
  --service <unit>           systemd service to restart for key persistence. Default: swarm.service
  --data-dir <path>          Remote swarmd data dir containing v3-sync-cursor.key. Default: /var/lib/swarmd or SWARMD_DATA_DIR
  --agent <name>             V3 agent name. Default: swarm
  --provider <provider>      Model provider. Default: fireworks
  --model <model>            Model id. Default: accounts/fireworks/models/kimi-k2p6
  --thinking <level>         Thinking setting. Default: low
  --prompt <text>            Prompt to send to Fireworks-backed session.
  --timeout-seconds <n>      Terminal event timeout. Default: 240
  --artifact-dir <path>      Local artifact directory. Default: .tmp/v3-durable-sync-e2e/<timestamp>
  --remote-work-dir <path>   Remote temp dir. Default: created with mktemp on target.
  --skip-restart             Skip restart/key-persistence check.
  --keep-remote              Keep remote runner files/logs.
  --help                     Show this help.

Environment equivalents:
  SWARM_PRIMARY_SSH, SWARM_PRIMARY_API_URL, SWARM_SERVICE_UNIT,
  SWARM_LIVE_STREAM_AGENT, SWARM_LIVE_STREAM_PROVIDER,
  SWARM_LIVE_STREAM_MODEL, SWARM_LIVE_STREAM_THINKING,
  SWARM_DURABLE_SYNC_PROMPT, SWARM_DURABLE_SYNC_TIMEOUT_SECONDS,
  SWARM_DURABLE_SYNC_ARTIFACT_DIR, SWARM_DURABLE_SYNC_REMOTE_WORK_DIR
EOF
}

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
PRIMARY_SSH="${SWARM_PRIMARY_SSH:-testbench}"
API_URL="${SWARM_PRIMARY_API_URL:-http://127.0.0.1:7781}"
SERVICE_UNIT="${SWARM_SERVICE_UNIT:-swarm.service}"
DATA_DIR="${SWARMD_DATA_DIR:-/var/lib/swarmd}"
AGENT_NAME="${SWARM_LIVE_STREAM_AGENT:-swarm}"
PROVIDER="${SWARM_LIVE_STREAM_PROVIDER:-fireworks}"
MODEL="${SWARM_LIVE_STREAM_MODEL:-accounts/fireworks/models/kimi-k2p6}"
THINKING="${SWARM_LIVE_STREAM_THINKING:-low}"
PROMPT="${SWARM_DURABLE_SYNC_PROMPT:-Durable V3 sync E2E. Reply with exactly V3_DURABLE_SYNC_E2E_OK and do not call tools.}"
TIMEOUT_SECONDS="${SWARM_DURABLE_SYNC_TIMEOUT_SECONDS:-240}"
ARTIFACT_DIR="${SWARM_DURABLE_SYNC_ARTIFACT_DIR:-}"
REMOTE_WORK_DIR="${SWARM_DURABLE_SYNC_REMOTE_WORK_DIR:-}"
KEEP_REMOTE="false"
SKIP_RESTART="false"

if [[ $# -gt 0 && "${1:-}" != --* ]]; then
  PRIMARY_SSH="$1"
  shift
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --primary-ssh|--ssh) PRIMARY_SSH="${2:-}"; shift 2 ;;
    --api-url|--primary-api-url) API_URL="${2:-}"; shift 2 ;;
    --service) SERVICE_UNIT="${2:-}"; shift 2 ;;
    --data-dir) DATA_DIR="${2:-}"; shift 2 ;;
    --agent|--agent-name) AGENT_NAME="${2:-}"; shift 2 ;;
    --provider) PROVIDER="${2:-}"; shift 2 ;;
    --model) MODEL="${2:-}"; shift 2 ;;
    --thinking) THINKING="${2:-}"; shift 2 ;;
    --prompt) PROMPT="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --artifact-dir|--evidence-dir) ARTIFACT_DIR="${2:-}"; shift 2 ;;
    --remote-work-dir) REMOTE_WORK_DIR="${2:-}"; shift 2 ;;
    --skip-restart) SKIP_RESTART="true"; shift ;;
    --keep-remote) KEEP_REMOTE="true"; shift ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

require_command ssh
require_command scp
require_command jq
require_command python3

[[ -n "${PRIMARY_SSH}" ]] || fail "--primary-ssh is required"
[[ -n "${API_URL}" ]] || fail "--api-url is required"
[[ -n "${SERVICE_UNIT}" ]] || fail "--service is required"
[[ -n "${DATA_DIR}" ]] || fail "--data-dir is required"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TIMEOUT_SECONDS}" -gt 0 ]] || fail "--timeout-seconds must be a positive integer"
API_URL="${API_URL%/}"

if [[ -z "${ARTIFACT_DIR}" ]]; then
  ARTIFACT_DIR="${ROOT_DIR}/.tmp/v3-durable-sync-e2e/$(date +%Y%m%d-%H%M%S)"
fi
mkdir -p -- "${ARTIFACT_DIR}"

REMOTE_WORK_DIR_EXPLICIT="true"
if [[ -z "${REMOTE_WORK_DIR}" ]]; then
  REMOTE_WORK_DIR_EXPLICIT="false"
fi

RUNNER_LOCAL="${ARTIFACT_DIR}/remote-runner.mjs"
REMOTE_STDOUT="${ARTIFACT_DIR}/remote-stdout.ndjson"
REMOTE_STDERR="${ARTIFACT_DIR}/remote-stderr.log"
SUMMARY_JSON="${ARTIFACT_DIR}/summary.json"
FUNCTION_CHAIN_MD="${ARTIFACT_DIR}/function-chain.md"

cat >"${RUNNER_LOCAL}" <<'NODE'
import crypto from 'node:crypto';
import fs from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { execFileSync, spawnSync } from 'node:child_process';

const cfg = JSON.parse(fs.readFileSync(process.env.SWARM_E2E_CONFIG, 'utf8'));
const apiURL = new URL(cfg.apiURL);
const host = apiURL.hostname;
const port = Number(apiURL.port || (apiURL.protocol === 'https:' ? 443 : 80));
if (apiURL.protocol !== 'http:') throw new Error(`runner expects local http API, got ${apiURL.protocol}`);

const startedAt = Date.now();
const deadline = startedAt + cfg.timeoutSeconds * 1000;
const artifactDir = cfg.artifactDir;
fs.mkdirSync(artifactDir, { recursive: true });
const framesPath = path.join(artifactDir, 'realtime-frames.ndjson');
const requestsPath = path.join(artifactDir, 'http-requests.ndjson');
const restartPath = path.join(artifactDir, 'restart.log');
const summaryPath = path.join(artifactDir, 'summary.json');
const chainPath = path.join(artifactDir, 'function-chain.md');
fs.writeFileSync(framesPath, '');
fs.writeFileSync(requestsPath, '');
fs.writeFileSync(restartPath, '');

function elapsed() { return Date.now() - startedAt; }
function emit(obj) { console.log(JSON.stringify({ t_ms: elapsed(), ...obj })); }
function appendJSON(file, obj) { fs.appendFileSync(file, JSON.stringify({ t_ms: elapsed(), ...obj }) + '\n'); }
function sleep(ms) { return new Promise(resolve => setTimeout(resolve, ms)); }
function assert(condition, message) { if (!condition) throw new Error(message); }
function isV3Cursor(value) { return typeof value === 'string' && value.startsWith('v3c1.'); }
function tamperCursor(cursor) {
  assert(isV3Cursor(cursor), 'cannot tamper non-v3 cursor');
  const last = cursor.at(-1);
  return cursor.slice(0, -1) + (last === 'A' ? 'B' : 'A');
}
function decodeCursorPayload(cursor) {
  assert(isV3Cursor(cursor), 'cannot decode non-v3 cursor');
  const parts = cursor.slice('v3c1.'.length).split('.');
  assert(parts.length === 2 && parts[0], 'malformed v3 cursor body');
  return JSON.parse(Buffer.from(parts[0], 'base64url').toString('utf8'));
}
function readPersistentCursorKey() {
  const keyPath = path.join(cfg.dataDir, 'v3-sync-cursor.key');
  try {
    return fs.readFileSync(keyPath, 'utf8').trim();
  } catch (err) {
    const sudo = spawnSync('sudo', ['-n', 'cat', keyPath], { encoding: 'utf8' });
    if (sudo.status === 0 && sudo.stdout.trim()) return sudo.stdout.trim();
    throw new Error(`read persistent cursor key ${keyPath}: ${err instanceof Error ? err.message : String(err)}; sudo=${sudo.stderr || sudo.status}`);
  }
}
function cursorWithEndpointSeqSignedByDataDirKey(cursor, afterEndpointSeq) {
  const encodedKey = readPersistentCursorKey();
  const key = Buffer.from(encodedKey, 'base64url');
  assert(key.length >= 32, `persistent cursor key is too short: ${key.length}`);
  const payload = decodeCursorPayload(cursor);
  payload.after_endpoint_seq = afterEndpointSeq;
  const body = Buffer.from(JSON.stringify(payload)).toString('base64url');
  const sig = crypto.createHmac('sha256', key).update(body).digest('base64url');
  return `v3c1.${body}.${sig}`;
}
function redactHeaders(headers) {
  const out = {};
  for (const [key, value] of Object.entries(headers || {})) {
    out[key] = /authorization|cookie|token/i.test(key) ? '<redacted>' : value;
  }
  return out;
}
function summarizeBody(body) {
  if (!body || typeof body !== 'object') return body ?? null;
  const out = { ...body };
  if (typeof out.content === 'string') out.content = `${out.content.slice(0, 120)}${out.content.length > 120 ? '…' : ''}`;
  if (typeof out.prompt === 'string') out.prompt = `${out.prompt.slice(0, 120)}${out.prompt.length > 120 ? '…' : ''}`;
  return out;
}
function summarizeResponse(value) {
  if (!value || typeof value !== 'object') return value ?? null;
  return {
    ok: value.ok,
    code: value.code,
    error: value.error,
    path_id: value.path_id,
    session_id: value.session?.id || value.session_id,
    run_id: value.run_intent?.run_id || value.run_id,
    run_status: value.run_intent?.status || value.status,
    endpoint_seq: value.realtime_outbox?.endpoint_seq,
    event_seq: value.realtime_outbox?.event?.seq,
    snapshot_endpoint_cursor: isV3Cursor(value.snapshot_endpoint_cursor) ? `${value.snapshot_endpoint_cursor.slice(0, 16)}…` : value.snapshot_endpoint_cursor,
    sessions: Array.isArray(value.sessions) ? value.sessions.length : undefined,
    sessions_by_id: value.sessions_by_id && typeof value.sessions_by_id === 'object' ? Object.keys(value.sessions_by_id).length : undefined,
    messages: Array.isArray(value.messages) ? value.messages.length : undefined,
    events: Array.isArray(value.events) ? value.events.length : undefined,
    subscriptions: Array.isArray(value.subscriptions) ? value.subscriptions.length : undefined,
  };
}
async function apiJSON(method, route, token, body = undefined, label = route, allowError = false) {
  const headers = { Accept: 'application/json', Origin: cfg.apiURL, Referer: `${cfg.apiURL}/app`, 'Sec-Fetch-Site': 'same-origin' };
  if (token) headers['X-Swarm-Token'] = token;
  const init = { method, headers };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  const before = Date.now();
  const response = await fetch(`${cfg.apiURL}${route}`, init);
  const text = await response.text();
  let parsed = null;
  try { parsed = text ? JSON.parse(text) : null; } catch { parsed = { raw: text }; }
  appendJSON(requestsPath, { label, method, route, status: response.status, duration_ms: Date.now() - before, request_headers: redactHeaders(headers), request_body_summary: summarizeBody(body), response_summary: summarizeResponse(parsed) });
  if (!response.ok && !allowError) throw new Error(`${method} ${route} status=${response.status} body=${text.slice(0, 1200)}`);
  return { status: response.status, body: parsed, ok: response.ok };
}
async function authToken() {
  const boot = await apiJSON('GET', '/v1/auth/desktop/session', '', undefined, 'auth.desktop.session');
  const token = String(boot.body.token || '');
  assert(token, 'auth did not return desktop token');
  return { token, boot: boot.body };
}
async function waitForAPI() {
  let last = null;
  for (let i = 0; i < 80; i++) {
    try {
      const boot = await apiJSON('GET', '/v1/auth/desktop/session', '', undefined, `auth.wait.${i}`, true);
      if (boot.ok && boot.body?.token) return String(boot.body.token);
      last = JSON.stringify(boot.body);
    } catch (err) {
      last = err instanceof Error ? err.message : String(err);
    }
    await sleep(250);
  }
  throw new Error(`API did not become healthy after restart: ${last || 'unknown'}`);
}
function websocketRequestHeaders(route, token) {
  const key = crypto.randomBytes(16).toString('base64');
  return {
    key,
    text: [
      `GET ${route} HTTP/1.1`,
      `Host: ${host}:${port}`,
      'Upgrade: websocket',
      'Connection: Upgrade',
      `Sec-WebSocket-Key: ${key}`,
      'Sec-WebSocket-Version: 13',
      `Origin: ${cfg.apiURL}`,
      'Sec-Fetch-Site: same-origin',
      `X-Swarm-Token: ${token}`,
      `Cookie: swarm_desktop_session=${token}`,
      '',
      '',
    ].join('\r\n'),
  };
}
function writePong(socket, payload) {
  const body = Buffer.from(payload);
  const header = Buffer.from([0x8a, body.length | 0x80]);
  const mask = crypto.randomBytes(4);
  const masked = Buffer.from(body.map((v, i) => v ^ mask[i % 4]));
  socket.write(Buffer.concat([header, mask, masked]));
}
function payloadObject(raw) {
  if (!raw || typeof raw !== 'object') return null;
  if (raw.payload && typeof raw.payload === 'object' && !Array.isArray(raw.payload)) return raw.payload;
  return null;
}
function summarizeRealtimeFrame(frame) {
  const event = frame.event || null;
  const payload = payloadObject(event);
  const message = payload?.message && typeof payload.message === 'object' ? payload.message : null;
  const runIntent = payload?.run_intent && typeof payload.run_intent === 'object' ? payload.run_intent : null;
  const summary = {
    kind: frame.kind,
    type: frame.type,
    protocol: frame.protocol,
    session_id: frame.session_id,
    subscription_id: frame.subscription_id,
    rev: frame.rev,
    prevRev: frame.prevRev,
    last_seq: frame.last_seq,
    high_watermark_seq: frame.high_watermark_seq,
    endpoint_cursor: frame.endpoint_cursor,
    bootstrap_required: frame.bootstrap_required,
    oldest_available_endpoint_seq: frame.oldest_available_endpoint_seq,
    latest_endpoint_seq: frame.latest_endpoint_seq,
    error_code: frame.error_code,
    error: frame.error,
    event_seq: event?.seq,
    event_type: frame.event_type || event?.event_type,
    payload_keys: payload ? Object.keys(payload).sort() : [],
    message_id: message?.id,
    message_role: message?.role,
    message_len: typeof message?.content === 'string' ? message.content.length : undefined,
    message_preview: typeof message?.content === 'string' ? message.content.slice(0, 160) : undefined,
    delta_len: typeof payload?.delta === 'string' ? payload.delta.length : undefined,
    delta_preview: typeof payload?.delta === 'string' ? payload.delta.slice(0, 160) : undefined,
    run_id: runIntent?.run_id || payload?.run_id,
    run_status: runIntent?.status || payload?.status,
  };
  return Object.fromEntries(Object.entries(summary).filter(([, value]) => value !== undefined && value !== null && value !== ''));
}
function openRealtime(route, token, onFrame) {
  return new Promise((resolve, reject) => {
    const { text } = websocketRequestHeaders(route, token);
    const socket = net.createConnection({ host, port });
    let handshake = Buffer.alloc(0);
    let buffer = Buffer.alloc(0);
    let upgraded = false;
    let settled = false;
    const fail = (err) => {
      if (settled) return;
      settled = true;
      try { socket.destroy(); } catch {}
      reject(err);
    };
    socket.setTimeout(cfg.timeoutSeconds * 1000 + 30000, () => fail(new Error(`websocket timeout for ${route}`)));
    socket.on('error', fail);
    socket.on('connect', () => socket.write(text));
    socket.on('data', (chunk) => {
      if (!upgraded) {
        handshake = Buffer.concat([handshake, chunk]);
        const marker = handshake.indexOf('\r\n\r\n');
        if (marker < 0) return;
        const head = handshake.slice(0, marker).toString('utf8');
        const rest = handshake.slice(marker + 4);
        if (!head.startsWith('HTTP/1.1 101') && !head.startsWith('HTTP/1.0 101')) {
          fail(new Error(`websocket upgrade failed for ${route}: ${head}`));
          return;
        }
        upgraded = true;
        appendJSON(requestsPath, { label: 'realtime.websocket.upgrade', method: 'GET', route, status: 101, request_headers: redactHeaders({ 'X-Swarm-Token': token, Cookie: `swarm_desktop_session=${token}`, Origin: cfg.apiURL }), response_summary: head.split('\r\n')[0] });
        if (!settled) {
          settled = true;
          resolve({ socket, close: () => socket.end() });
        }
        buffer = rest;
      } else {
        buffer = Buffer.concat([buffer, chunk]);
      }
      while (upgraded && buffer.length >= 2) {
        const opcode = buffer[0] & 0x0f;
        let offset = 2;
        let len = buffer[1] & 0x7f;
        if (len === 126) {
          if (buffer.length < 4) return;
          len = buffer.readUInt16BE(2);
          offset = 4;
        } else if (len === 127) {
          if (buffer.length < 10) return;
          const big = buffer.readBigUInt64BE(2);
          if (big > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error('websocket frame too large');
          len = Number(big);
          offset = 10;
        }
        const masked = (buffer[1] & 0x80) !== 0;
        let mask = null;
        if (masked) {
          if (buffer.length < offset + 4) return;
          mask = buffer.slice(offset, offset + 4);
          offset += 4;
        }
        if (buffer.length < offset + len) return;
        let payload = buffer.slice(offset, offset + len);
        buffer = buffer.slice(offset + len);
        if (masked && mask) payload = Buffer.from(payload.map((v, i) => v ^ mask[i % 4]));
        if (opcode === 0x8) {
          appendJSON(framesPath, { direction: 'server', route, frame: { kind: 'close' } });
          socket.end();
          return;
        }
        if (opcode === 0x9) {
          writePong(socket, payload);
          continue;
        }
        if (opcode !== 0x1) continue;
        const textFrame = payload.toString('utf8');
        let frame;
        try { frame = JSON.parse(textFrame); } catch { frame = { kind: 'unparsed', raw: textFrame }; }
        const summary = summarizeRealtimeFrame(frame);
        appendJSON(framesPath, { direction: 'server', route, summary, frame });
        onFrame(frame, summary);
      }
    });
  });
}
async function collectRealtime(route, token, ms, stopWhen = () => false) {
  const frames = [];
  let stopped = false;
  const ws = await openRealtime(route, token, (frame, summary) => {
    frames.push({ frame, summary });
    if (!stopped && stopWhen(frame, summary)) {
      stopped = true;
      setTimeout(() => { try { ws.close(); } catch {} }, 25);
    }
  });
  const started = Date.now();
  while (Date.now() - started < ms && !stopped) await sleep(50);
  try { ws.close(); } catch {}
  await sleep(100);
  return frames;
}
function restartService() {
  if (cfg.skipRestart) {
    fs.appendFileSync(restartPath, 'restart skipped by config\n');
    return;
  }
  fs.appendFileSync(restartPath, `restarting ${cfg.serviceUnit}\n`);
  const user = spawnSync('systemctl', ['--user', 'restart', cfg.serviceUnit], { encoding: 'utf8' });
  fs.appendFileSync(restartPath, `$ systemctl --user restart ${cfg.serviceUnit}\nstdout=${user.stdout}\nstderr=${user.stderr}\nstatus=${user.status}\n`);
  if (user.status === 0) return;
  const sudo = spawnSync('sudo', ['-n', 'systemctl', 'restart', cfg.serviceUnit], { encoding: 'utf8' });
  fs.appendFileSync(restartPath, `$ sudo -n systemctl restart ${cfg.serviceUnit}\nstdout=${sudo.stdout}\nstderr=${sudo.stderr}\nstatus=${sudo.status}\n`);
  if (sudo.status !== 0) throw new Error(`failed to restart ${cfg.serviceUnit}: user=${user.stderr || user.status}, sudo=${sudo.stderr || sudo.status}`);
}

const terminalEvents = new Set(['session.assistant.completed', 'session.assistant.failed', 'session.run.completed', 'session.run.failed', 'session.run.cancelled', 'session.run.expired', 'session.run.interrupted']);
let sessionID = '';
let runID = '';
const observed = {
  desktopWorksetCursor: false,
  tuiWorksetCursor: false,
  reconnectCursor: false,
  discoveryCursor: false,
  validWSCursorAccepted: false,
  wsHelloCursor: false,
  wsReplayCursor: false,
  tamperedCursorRejected: false,
  wrongScopeCursorRejected: false,
  restartCursorAccepted: cfg.skipRestart,
  fireworksAssistantCompleted: false,
  realtimeEventCursors: 0,
  aheadCursorRejected: false,
  persistentKeyKid: false,
  cursorErrorsDuringValidStreams: [],
};
const cursorSamples = {};
const cursorKids = {};
const realtimeEventTypes = [];

try {
  let { token, boot } = await authToken();
  emit({ stage: 'auth.ok', user_id: boot.user_id, username: boot.username, token_len: token.length });

  const topology = (await apiJSON('GET', '/v1/swarm/topology', token, undefined, 'topology.snapshot')).body;
  const runtime = (topology.runtimes || []).find((item) => item.relationship === 'self') || (topology.runtimes || [])[0];
  const binding = (topology.workspace_bindings || []).find((item) => item.state === 'bound' && item.destination_workspace_path) || (topology.workspace_bindings || [])[0];
  assert(runtime?.swarm_id && binding?.workspace_binding_id, 'missing runtime or workspace binding');
  emit({ stage: 'topology.ok', swarm_id: runtime.swarm_id, workspace_binding_id: binding.workspace_binding_id, workspace_path: binding.source_workspace_path });

  const suffix = `${Date.now()}-${crypto.randomBytes(4).toString('hex')}`;
  const created = (await apiJSON('POST', '/v3/sessions', token, {
    client_request_id: `durable-sync-create:${suffix}`,
    title: `Durable sync E2E ${suffix}`,
    workspace_path: binding.source_workspace_path,
    workspace_name: binding.source_workspace_name || 'swarm-go',
    workspace_binding_id: binding.workspace_binding_id,
    swarm_id: runtime.swarm_id,
    target_kind: 'host',
    target_relationship: 'self',
    mode: 'auto',
    agent_name: cfg.agentName,
    preference: { provider: cfg.provider, model: cfg.model, thinking: cfg.thinking },
    metadata: { durable_sync_e2e: suffix },
  }, 'sessions.v3.create')).body;
  sessionID = created.session?.id || '';
  assert(sessionID, 'create did not return session.id');
  emit({ stage: 'session.created', session_id: sessionID, projection_last_seq: created.projection?.last_event_seq });

  const desktopWorkset = (await apiJSON('POST', '/v3/sessions:workset', token, {
    global: true,
    recent: { limit: 20 },
    history: { mode: 'none' },
    resources: { run_intents: true },
  }, 'sessions.workset.desktop')).body;
  const desktopCursor = desktopWorkset.snapshot_endpoint_cursor || '';
  observed.desktopWorksetCursor = isV3Cursor(desktopCursor);
  assert(observed.desktopWorksetCursor, `desktop workset cursor is not v3c1: ${desktopCursor}`);
  cursorSamples.desktop_workset = `${desktopCursor.slice(0, 32)}…`;
  const desktopCursorPayload = decodeCursorPayload(desktopCursor);
  cursorKids.desktop_workset = desktopCursorPayload.kid || '';
  observed.persistentKeyKid = typeof desktopCursorPayload.kid === 'string' && desktopCursorPayload.kid.startsWith('v3sync-') && desktopCursorPayload.kid !== 'dev-v3-sync-cursor';
  assert(observed.persistentKeyKid, `desktop workset cursor kid is not persistent v3sync-* kid: ${desktopCursorPayload.kid || '<missing>'}`);

  const tuiWorkset = (await apiJSON('POST', '/v3/tui/sessions:workset', token, {
    scope: { workspace_path: binding.source_workspace_path },
    recent: { limit: 20 },
    history: { mode: 'none' },
  }, 'sessions.workset.tui')).body;
  const tuiCursor = tuiWorkset.snapshot_endpoint_cursor || '';
  observed.tuiWorksetCursor = isV3Cursor(tuiCursor);
  assert(observed.tuiWorksetCursor, `tui workset cursor is not v3c1: ${tuiCursor}`);
  cursorSamples.tui_workset = `${tuiCursor.slice(0, 32)}…`;

  const reconnect = (await apiJSON('POST', '/v3/sessions:reconnect', token, {}, 'sessions.reconnect')).body;
  const reconnectCursor = reconnect.snapshot_endpoint_cursor || '';
  observed.reconnectCursor = isV3Cursor(reconnectCursor);
  assert(observed.reconnectCursor, `reconnect cursor is not v3c1: ${reconnectCursor}`);
  cursorSamples.reconnect = `${reconnectCursor.slice(0, 32)}…`;

  const discovery = (await apiJSON('POST', '/v3/sessions:discover', token, { global: true, recent: { limit: 20 } }, 'sessions.discover')).body;
  const discoveryCursor = discovery.snapshot_endpoint_cursor || '';
  observed.discoveryCursor = isV3Cursor(discoveryCursor);
  assert(observed.discoveryCursor, `discovery cursor is not v3c1: ${discoveryCursor}`);
  cursorSamples.discovery = `${discoveryCursor.slice(0, 32)}…`;

  const validRoute = `/v3/realtime/stream?endpoint_cursor=${encodeURIComponent(desktopCursor)}&sessions=${encodeURIComponent(sessionID)}`;
  const validFrames = await collectRealtime(validRoute, token, 900);
  const validErrors = validFrames.filter(({ summary }) => summary.kind === 'cursor.error').map(({ summary }) => summary.error_code || summary.error || 'cursor.error');
  observed.cursorErrorsDuringValidStreams.push(...validErrors);
  observed.validWSCursorAccepted = validErrors.length === 0 && validFrames.some(({ summary }) => summary.kind === 'hello');
  observed.wsHelloCursor = validFrames.some(({ summary }) => summary.kind === 'hello' && isV3Cursor(summary.endpoint_cursor));
  observed.wsReplayCursor = validFrames.some(({ summary }) => String(summary.kind || '').startsWith('replay.') && isV3Cursor(summary.endpoint_cursor));
  assert(observed.validWSCursorAccepted && observed.wsHelloCursor && observed.wsReplayCursor, `valid WS cursor did not produce v3c1 hello/replay; errors=${validErrors.join(',')}`);

  const tamperedRoute = `/v3/realtime/stream?endpoint_cursor=${encodeURIComponent(tamperCursor(desktopCursor))}`;
  const tamperedFrames = await collectRealtime(tamperedRoute, token, 1200, (_frame, summary) => summary.kind === 'cursor.error');
  observed.tamperedCursorRejected = tamperedFrames.some(({ summary }) => summary.kind === 'cursor.error' && summary.error_code === 'endpoint_cursor_tampered');
  assert(observed.tamperedCursorRejected, `tampered cursor was not rejected with endpoint_cursor_tampered: ${JSON.stringify(tamperedFrames.map(f => f.summary))}`);

  const wrongScopeRoute = `/v3/realtime/stream?endpoint_cursor=${encodeURIComponent(tuiCursor)}`;
  const wrongScopeFrames = await collectRealtime(wrongScopeRoute, token, 1200, (_frame, summary) => summary.kind === 'cursor.error');
  observed.wrongScopeCursorRejected = wrongScopeFrames.some(({ summary }) => summary.kind === 'cursor.error' && summary.error_code === 'endpoint_cursor_scope_mismatch');
  assert(observed.wrongScopeCursorRejected, `wrong-scope cursor was not rejected with endpoint_cursor_scope_mismatch: ${JSON.stringify(wrongScopeFrames.map(f => f.summary))}`);

  const currentHead = Number(desktopCursorPayload.after_endpoint_seq || 0);
  assert(Number.isSafeInteger(currentHead) && currentHead > 0, `desktop cursor missing usable after_endpoint_seq: ${desktopCursorPayload.after_endpoint_seq}`);
  const aheadCursor = cursorWithEndpointSeqSignedByDataDirKey(desktopCursor, currentHead + 1);
  const aheadRoute = `/v3/realtime/stream?endpoint_cursor=${encodeURIComponent(aheadCursor)}`;
  const aheadFrames = await collectRealtime(aheadRoute, token, 1200, (_frame, summary) => summary.kind === 'cursor.error');
  observed.aheadCursorRejected = aheadFrames.some(({ summary }) => summary.kind === 'cursor.error' && summary.error_code === 'endpoint_cursor_ahead' && Number(summary.latest_endpoint_seq || 0) === currentHead);
  assert(observed.aheadCursorRejected, `ahead cursor was not rejected with endpoint_cursor_ahead/latest head ${currentHead}: ${JSON.stringify(aheadFrames.map(f => f.summary))}`);

  if (!cfg.skipRestart) {
    emit({ stage: 'restart.begin', service: cfg.serviceUnit });
    restartService();
    token = await waitForAPI();
    emit({ stage: 'restart.ok', service: cfg.serviceUnit });
    const restartFrames = await collectRealtime(validRoute, token, 900);
    const restartErrors = restartFrames.filter(({ summary }) => summary.kind === 'cursor.error').map(({ summary }) => summary.error_code || summary.error || 'cursor.error');
    observed.cursorErrorsDuringValidStreams.push(...restartErrors);
    observed.restartCursorAccepted = restartErrors.length === 0 && restartFrames.some(({ summary }) => summary.kind === 'hello' && isV3Cursor(summary.endpoint_cursor));
    assert(observed.restartCursorAccepted, `cursor was not accepted after restart; errors=${restartErrors.join(',')}`);
  }

  let realtime = null;
  try {
    const liveRoute = `/v3/realtime/stream?endpoint_cursor=${encodeURIComponent(desktopCursor)}&sessions=${encodeURIComponent(sessionID)}`;
    realtime = await openRealtime(liveRoute, token, (_frame, summary) => {
      emit({ stage: 'realtime.frame', ...summary });
      if (summary.kind === 'cursor.error') observed.cursorErrorsDuringValidStreams.push(summary.error_code || summary.error || 'cursor.error');
      if (isV3Cursor(summary.endpoint_cursor)) {
        if (summary.kind === 'event') observed.realtimeEventCursors += 1;
        if (!cursorSamples.realtime_event) cursorSamples.realtime_event = `${summary.endpoint_cursor.slice(0, 32)}…`;
      }
      const eventType = summary.event_type || '';
      if (eventType) realtimeEventTypes.push(eventType);
      if (eventType === 'session.assistant.completed') observed.fireworksAssistantCompleted = true;
      if (terminalEvents.has(eventType) && eventType !== 'session.assistant.completed') observed.fireworksAssistantCompleted = false;
    });
    await sleep(250);
    const message = (await apiJSON('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, token, {
      client_request_id: `durable-sync-message:${suffix}`,
      role: 'user',
      content: cfg.prompt,
      metadata: { durable_sync_e2e: suffix },
    }, 'sessions.v3.messages.post')).body;
    runID = message.run_intent?.run_id || '';
    emit({ stage: 'message.posted', session_id: sessionID, run_id: runID, run_status: message.run_intent?.status, endpoint_seq: message.realtime_outbox?.endpoint_seq });
    while (Date.now() < deadline && !realtimeEventTypes.some(type => terminalEvents.has(type))) await sleep(250);
  } finally {
    try { realtime?.close(); } catch {}
  }

  const tailAfter = (await apiJSON('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/messages?tail=true&limit=200`, token, undefined, 'messages.tail.after')).body;
  const events = (await apiJSON('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/events?after_seq=0&limit=500`, token, undefined, 'events.replay.after')).body;
  const assistantMessages = (tailAfter.messages || []).filter((m) => m.role === 'assistant');
  const assistantPreview = assistantMessages.map((m) => String(m.content || '').slice(0, 200));
  observed.fireworksAssistantCompleted = observed.fireworksAssistantCompleted && assistantMessages.length > 0;

  const pass = Boolean(
    observed.desktopWorksetCursor && observed.tuiWorksetCursor && observed.reconnectCursor && observed.discoveryCursor &&
    observed.validWSCursorAccepted && observed.wsHelloCursor && observed.wsReplayCursor &&
    observed.tamperedCursorRejected && observed.wrongScopeCursorRejected && observed.aheadCursorRejected && observed.restartCursorAccepted &&
    observed.persistentKeyKid && observed.fireworksAssistantCompleted && observed.realtimeEventCursors > 0 && observed.cursorErrorsDuringValidStreams.length === 0
  );
  const summary = {
    ok: pass,
    primary_ssh: cfg.primarySSH,
    api_url: cfg.apiURL,
    service_unit: cfg.serviceUnit,
    data_dir: cfg.dataDir,
    session_id: sessionID,
    run_id: runID,
    provider: cfg.provider,
    model: cfg.model,
    observed,
    cursor_samples: cursorSamples,
    cursor_kids: cursorKids,
    realtime_event_count: realtimeEventTypes.length,
    realtime_event_types: realtimeEventTypes,
    db_event_count: events.events?.length ?? 0,
    tail_message_count: tailAfter.messages?.length ?? 0,
    assistant_message_count: assistantMessages.length,
    assistant_preview: assistantPreview,
    artifacts: { frames: framesPath, requests: requestsPath, restart: restartPath, summary: summaryPath, function_chain: chainPath },
  };
  fs.writeFileSync(summaryPath, JSON.stringify(summary, null, 2));
  writeFunctionChain(chainPath, summary);
  emit({ stage: 'final.summary', ...summary, realtime_event_types: undefined });
  if (!pass) process.exitCode = 1;
} catch (err) {
  const summary = { ok: false, error: err instanceof Error ? err.message : String(err), session_id: sessionID, run_id: runID, observed, cursor_samples: cursorSamples, cursor_kids: cursorKids, artifacts: { frames: framesPath, requests: requestsPath, restart: restartPath, summary: summaryPath, function_chain: chainPath } };
  fs.writeFileSync(summaryPath, JSON.stringify(summary, null, 2));
  writeFunctionChain(chainPath, summary);
  emit({ stage: 'error', error: summary.error, session_id: sessionID, run_id: runID });
  process.exitCode = 1;
}

function writeFunctionChain(file, summary) {
  const lines = [];
  lines.push('# V3 durable sync E2E function chain');
  lines.push('');
  lines.push(`Result: ${summary.ok ? 'PASS' : 'FAIL'}`);
  lines.push(`Session: ${summary.session_id || ''}`);
  lines.push(`Run: ${summary.run_id || ''}`);
  lines.push(`Provider/model: ${cfg.provider} / ${cfg.model}`);
  if (summary.error) lines.push(`Error: ${summary.error}`);
  lines.push('');
  lines.push('## What this runner proves');
  lines.push('- It uses the live SSH host API and real swarmd service, not mocks.');
  lines.push('- It checks Desktop workset, TUI workset, reconnect, and discovery cursor shape.');
  lines.push('- It checks valid, tampered, wrong-scope, and ahead-of-head websocket cursor behavior.');
  lines.push('- It decodes cursor metadata to prove the live server uses a persistent v3sync-* key ID, not the default dev key.');
  lines.push('- It restarts the swarm service and reuses a pre-restart cursor to prove key persistence.');
  lines.push('- It posts a real V3 user message and waits for Fireworks-backed assistant output over realtime.');
  lines.push('');
  lines.push('## Observed gates');
  for (const [key, value] of Object.entries(summary.observed || {})) lines.push(`- ${key}: ${Array.isArray(value) ? value.join(',') : value}`);
  lines.push('');
  lines.push('## Cursor samples');
  for (const [key, value] of Object.entries(summary.cursor_samples || {})) lines.push(`- ${key}: ${value}`);
  lines.push('');
  lines.push('## Cursor key IDs');
  for (const [key, value] of Object.entries(summary.cursor_kids || {})) lines.push(`- ${key}: ${value}`);
  lines.push('');
  lines.push('## Artifacts');
  lines.push(`- HTTP request summaries: ${requestsPath}`);
  lines.push(`- Realtime frames: ${framesPath}`);
  lines.push(`- Restart log: ${restartPath}`);
  lines.push(`- Summary JSON: ${summaryPath}`);
  fs.writeFileSync(file, lines.join('\n') + '\n');
}
NODE

CONFIG_LOCAL="${ARTIFACT_DIR}/config.json"
python3 - "$CONFIG_LOCAL" "$PRIMARY_SSH" "$API_URL" "$SERVICE_UNIT" "$DATA_DIR" "$AGENT_NAME" "$PROVIDER" "$MODEL" "$THINKING" "$PROMPT" "$TIMEOUT_SECONDS" "$SKIP_RESTART" <<'PY'
import json, sys
path, primary, api, service, data_dir, agent, provider, model, thinking, prompt, timeout, skip_restart = sys.argv[1:]
with open(path, 'w', encoding='utf-8') as f:
    json.dump({
        'primarySSH': primary,
        'apiURL': api,
        'serviceUnit': service,
        'dataDir': data_dir,
        'agentName': agent,
        'provider': provider,
        'model': model,
        'thinking': thinking,
        'prompt': prompt,
        'timeoutSeconds': int(timeout),
        'skipRestart': skip_restart == 'true',
        'artifactDir': '',
    }, f, indent=2)
PY

log "v3-durable-sync-e2e: primary=${PRIMARY_SSH} api=${API_URL} provider=${PROVIDER} model=${MODEL} restart=$([[ "${SKIP_RESTART}" == "true" ]] && printf skipped || printf ${SERVICE_UNIT})"
log "v3-durable-sync-e2e: artifacts=${ARTIFACT_DIR}"

if [[ "${REMOTE_WORK_DIR_EXPLICIT}" == "true" ]]; then
  ssh "${PRIMARY_SSH}" "set -euo pipefail; rm -rf -- $(printf '%q' "${REMOTE_WORK_DIR}"); mkdir -p -- $(printf '%q' "${REMOTE_WORK_DIR}")"
else
  REMOTE_WORK_DIR="$(ssh "${PRIMARY_SSH}" "mktemp -d")"
fi
[[ -n "${REMOTE_WORK_DIR}" ]] || fail "failed to create remote work dir"
scp -q "${RUNNER_LOCAL}" "${CONFIG_LOCAL}" "${PRIMARY_SSH}:${REMOTE_WORK_DIR}/"

remote_status=0
ssh "${PRIMARY_SSH}" 'bash -s' -- "${REMOTE_WORK_DIR}" <<'REMOTE' >"${REMOTE_STDOUT}" 2>"${REMOTE_STDERR}" || remote_status=$?
set -euo pipefail
remote_dir="$1"
cd "${remote_dir}"
python3 - <<'PY'
import json
with open('config.json', 'r', encoding='utf-8') as f:
    cfg = json.load(f)
cfg['artifactDir'] = 'artifacts'
with open('config.json', 'w', encoding='utf-8') as f:
    json.dump(cfg, f, indent=2)
PY
SWARM_E2E_CONFIG="${remote_dir}/config.json" node "${remote_dir}/remote-runner.mjs"
REMOTE

mkdir -p -- "${ARTIFACT_DIR}/remote-artifacts"
scp -q -r "${PRIMARY_SSH}:${REMOTE_WORK_DIR}/artifacts/." "${ARTIFACT_DIR}/remote-artifacts/" 2>/dev/null || true
if [[ -f "${ARTIFACT_DIR}/remote-artifacts/summary.json" ]]; then
  cp -- "${ARTIFACT_DIR}/remote-artifacts/summary.json" "${SUMMARY_JSON}"
fi
if [[ -f "${ARTIFACT_DIR}/remote-artifacts/function-chain.md" ]]; then
  cp -- "${ARTIFACT_DIR}/remote-artifacts/function-chain.md" "${FUNCTION_CHAIN_MD}"
fi

if [[ "${KEEP_REMOTE}" != "true" ]]; then
  ssh "${PRIMARY_SSH}" "rm -rf -- $(printf '%q' "${REMOTE_WORK_DIR}")" >/dev/null 2>&1 || true
fi

if [[ "${remote_status}" != "0" ]]; then
  log "v3-durable-sync-e2e: FAILED (remote status ${remote_status})"
  log "stdout: ${REMOTE_STDOUT}"
  log "stderr: ${REMOTE_STDERR}"
  [[ -s "${REMOTE_STDERR}" ]] && sed -n '1,160p' "${REMOTE_STDERR}" >&2 || true
  [[ -f "${SUMMARY_JSON}" ]] && jq . "${SUMMARY_JSON}" >&2 || true
  exit "${remote_status}"
fi

if [[ ! -f "${SUMMARY_JSON}" ]]; then
  fail "remote run succeeded but summary was not copied back"
fi

jq '{ok, primary_ssh, api_url, service_unit, data_dir, session_id, run_id, provider, model, observed, cursor_samples, cursor_kids, realtime_event_count, db_event_count, tail_message_count, assistant_message_count, assistant_preview, artifacts}' "${SUMMARY_JSON}"
log "v3-durable-sync-e2e: PASS"
log "function chain: ${FUNCTION_CHAIN_MD}"
log "frames: ${ARTIFACT_DIR}/remote-artifacts/realtime-frames.ndjson"
log "requests: ${ARTIFACT_DIR}/remote-artifacts/http-requests.ndjson"
log "restart: ${ARTIFACT_DIR}/remote-artifacts/restart.log"
