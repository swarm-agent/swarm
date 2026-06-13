#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/diagnose-v3-live-stream-e2e.sh [ssh-alias] [options]
       scripts/diagnose-v3-live-stream-e2e.sh --primary-ssh <alias> [options]

Runs a real Sessions API V3 desktop stream E2E against a live SSH testbench.
This is not a mock and not a passive socket check:
  1. SSHes to the target host.
  2. Authenticates to the live local swarmd desktop API.
  3. Resolves the live self swarm/runtime and workspace binding.
  4. Creates a real V3 primary session with Fireworks preference.
  5. Opens the canonical desktop realtime websocket: /v3/realtime/stream.
  6. Sends subscribe.session exactly like desktop runtime does.
  7. Posts a real user prompt through /v3/sessions/{id}/messages.
  8. Waits for persisted assistant output and terminal realtime events.
  9. Writes evidence plus a function-chain document explaining every hop.

Options:
  --primary-ssh <alias>      SSH alias for testbench. Default: testbench
  --api-url <url>            API URL used on remote host. Default: http://127.0.0.1:7781
  --agent <name>             V3 agent name. Default: swarm
  --provider <provider>      Model provider. Default: fireworks
  --model <model>            Model id. Default: accounts/fireworks/models/kimi-k2p6
  --thinking <level>         Thinking setting. Default: low
  --prompt <text>            Prompt to send. Default asks for LIVE_STREAM_E2E_OK
  --timeout-seconds <n>      Terminal event timeout. Default: 180
  --artifact-dir <path>      Local artifact directory. Default: .tmp/v3-live-stream-e2e/<timestamp>
  --remote-work-dir <path>   Remote temp dir. Default: created with mktemp on the target.
  --keep-remote              Keep remote runner files/logs.
  --help                     Show this help.

Environment equivalents:
  SWARM_PRIMARY_SSH, SWARM_PRIMARY_API_URL, SWARM_LIVE_STREAM_AGENT,
  SWARM_LIVE_STREAM_PROVIDER, SWARM_LIVE_STREAM_MODEL, SWARM_LIVE_STREAM_THINKING,
  SWARM_LIVE_STREAM_PROMPT, SWARM_LIVE_STREAM_TIMEOUT_SECONDS,
  SWARM_LIVE_STREAM_ARTIFACT_DIR, SWARM_LIVE_STREAM_REMOTE_WORK_DIR
EOF
}

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
PRIMARY_SSH="${SWARM_PRIMARY_SSH:-testbench}"
API_URL="${SWARM_PRIMARY_API_URL:-http://127.0.0.1:7781}"
AGENT_NAME="${SWARM_LIVE_STREAM_AGENT:-swarm}"
PROVIDER="${SWARM_LIVE_STREAM_PROVIDER:-fireworks}"
MODEL="${SWARM_LIVE_STREAM_MODEL:-accounts/fireworks/models/kimi-k2p6}"
THINKING="${SWARM_LIVE_STREAM_THINKING:-low}"
PROMPT="${SWARM_LIVE_STREAM_PROMPT:-Live stream E2E test. Reply with exactly LIVE_STREAM_E2E_OK and do not call tools.}"
TIMEOUT_SECONDS="${SWARM_LIVE_STREAM_TIMEOUT_SECONDS:-180}"
ARTIFACT_DIR="${SWARM_LIVE_STREAM_ARTIFACT_DIR:-}"
REMOTE_WORK_DIR="${SWARM_LIVE_STREAM_REMOTE_WORK_DIR:-}"
KEEP_REMOTE="false"

if [[ $# -gt 0 && "${1:-}" != --* ]]; then
  PRIMARY_SSH="$1"
  shift
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --primary-ssh|--ssh) PRIMARY_SSH="${2:-}"; shift 2 ;;
    --api-url|--primary-api-url) API_URL="${2:-}"; shift 2 ;;
    --agent|--agent-name) AGENT_NAME="${2:-}"; shift 2 ;;
    --provider) PROVIDER="${2:-}"; shift 2 ;;
    --model) MODEL="${2:-}"; shift 2 ;;
    --thinking) THINKING="${2:-}"; shift 2 ;;
    --prompt) PROMPT="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --artifact-dir|--evidence-dir) ARTIFACT_DIR="${2:-}"; shift 2 ;;
    --remote-work-dir) REMOTE_WORK_DIR="${2:-}"; shift 2 ;;
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
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TIMEOUT_SECONDS}" -gt 0 ]] || fail "--timeout-seconds must be a positive integer"
API_URL="${API_URL%/}"

if [[ -z "${ARTIFACT_DIR}" ]]; then
  ARTIFACT_DIR="${ROOT_DIR}/.tmp/v3-live-stream-e2e/$(date +%Y%m%d-%H%M%S)"
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

const cfg = JSON.parse(fs.readFileSync(process.env.SWARM_E2E_CONFIG, 'utf8'));
const apiURL = new URL(cfg.apiURL);
const host = apiURL.hostname;
const port = Number(apiURL.port || (apiURL.protocol === 'https:' ? 443 : 80));
if (apiURL.protocol !== 'http:') {
  throw new Error(`runner currently expects local http API, got ${apiURL.protocol}`);
}
const startedAt = Date.now();
const deadline = startedAt + cfg.timeoutSeconds * 1000;
const artifactDir = cfg.artifactDir;
fs.mkdirSync(artifactDir, { recursive: true });
const framesPath = path.join(artifactDir, 'realtime-frames.ndjson');
const requestsPath = path.join(artifactDir, 'http-requests.ndjson');
const summaryPath = path.join(artifactDir, 'summary.json');
const chainPath = path.join(artifactDir, 'function-chain.md');
fs.writeFileSync(framesPath, '');
fs.writeFileSync(requestsPath, '');

function elapsed() { return Date.now() - startedAt; }
function emit(obj) { console.log(JSON.stringify({ t_ms: elapsed(), ...obj })); }
function appendJSON(file, obj) { fs.appendFileSync(file, JSON.stringify({ t_ms: elapsed(), ...obj }) + '\n'); }
function redactHeaders(headers) {
  const out = {};
  for (const [key, value] of Object.entries(headers || {})) {
    if (/authorization|cookie|token/i.test(key)) out[key] = '<redacted>';
    else out[key] = value;
  }
  return out;
}

async function apiJSON(method, route, token, body = undefined, label = route) {
  const headers = {
    Accept: 'application/json',
    Origin: cfg.apiURL,
    Referer: `${cfg.apiURL}/app`,
    'Sec-Fetch-Site': 'same-origin',
  };
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
  appendJSON(requestsPath, {
    label,
    method,
    route,
    status: response.status,
    duration_ms: Date.now() - before,
    request_headers: redactHeaders(headers),
    request_body_summary: summarizeBody(body),
    response_summary: summarizeResponse(parsed),
  });
  if (!response.ok) {
    throw new Error(`${method} ${route} status=${response.status} body=${text.slice(0, 1200)}`);
  }
  return parsed;
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
    path_id: value.path_id,
    session_id: value.session?.id || value.session_id,
    run_id: value.run_intent?.run_id || value.run_id,
    run_status: value.run_intent?.status || value.status,
    event_seq: value.realtime_outbox?.event?.seq,
    endpoint_seq: value.realtime_outbox?.endpoint_seq,
    sessions: Array.isArray(value.sessions) ? value.sessions.length : undefined,
    messages: Array.isArray(value.messages) ? value.messages.length : undefined,
    events: Array.isArray(value.events) ? value.events.length : undefined,
    runtimes: Array.isArray(value.runtimes) ? value.runtimes.length : undefined,
    workspace_bindings: Array.isArray(value.workspace_bindings) ? value.workspace_bindings.length : undefined,
  };
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
function writeClientTextFrame(socket, obj) {
  const payload = Buffer.from(JSON.stringify(obj));
  let header;
  if (payload.length < 126) header = Buffer.from([0x81, payload.length | 0x80]);
  else if (payload.length < 65536) {
    header = Buffer.alloc(4);
    header[0] = 0x81; header[1] = 126 | 0x80; header.writeUInt16BE(payload.length, 2);
  } else {
    header = Buffer.alloc(10);
    header[0] = 0x81; header[1] = 127 | 0x80; header.writeBigUInt64BE(BigInt(payload.length), 2);
  }
  const mask = crypto.randomBytes(4);
  const masked = Buffer.from(payload.map((v, i) => v ^ mask[i % 4]));
  socket.write(Buffer.concat([header, mask, masked]));
}
function writePong(socket, payload) {
  const body = Buffer.from(payload);
  const header = Buffer.from([0x8a, body.length | 0x80]);
  const mask = crypto.randomBytes(4);
  const masked = Buffer.from(body.map((v, i) => v ^ mask[i % 4]));
  socket.write(Buffer.concat([header, mask, masked]));
}

function openRealtime(token, onFrame) {
  return new Promise((resolve, reject) => {
    const route = '/v3/realtime/stream';
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
    socket.setTimeout(cfg.timeoutSeconds * 1000 + 30000, () => fail(new Error('websocket timeout')));
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
          fail(new Error(`websocket upgrade failed: ${head}`));
          return;
        }
        upgraded = true;
        appendJSON(requestsPath, { label: 'realtime.websocket.upgrade', method: 'GET', route, status: 101, request_headers: { ...redactHeaders({ 'X-Swarm-Token': token, Cookie: `swarm_desktop_session=${token}` }), Origin: cfg.apiURL }, response_summary: head.split('\r\n')[0] });
        if (!settled) {
          settled = true;
          resolve({
            socket,
            send: (obj) => writeClientTextFrame(socket, obj),
            close: () => socket.end(),
          });
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
          appendJSON(framesPath, { direction: 'server', frame: { kind: 'close' } });
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
        appendJSON(framesPath, { direction: 'server', summary, frame });
        onFrame(frame, summary);
      }
    });
  });
}

const terminalEvents = new Set(['session.assistant.completed', 'session.assistant.failed', 'session.run.completed', 'session.run.failed', 'session.run.cancelled', 'session.run.expired', 'session.run.interrupted']);
let token = '';
let sessionID = '';
let runID = '';
let realtime = null;
const seen = {
  hello: false,
  replayStarted: false,
  replayComplete: false,
  subscribed: false,
  userMessage: false,
  assistantStarted: false,
  assistantDelta: false,
  assistantCompleted: false,
  assistantMessageOnRealtime: false,
  terminalEventType: '',
  cursorErrors: [],
  broadWorksetDuringStream: false,
};
const realtimeEventTypes = [];

try {
  const boot = await apiJSON('GET', '/v1/auth/desktop/session', '', undefined, 'auth.desktop.session');
  token = String(boot.token || '');
  if (!token) throw new Error('auth did not return desktop token');
  emit({ stage: 'auth.ok', user_id: boot.user_id, username: boot.username, token_len: token.length });

  const topology = await apiJSON('GET', '/v1/swarm/topology', token, undefined, 'topology.snapshot');
  const runtime = (topology.runtimes || []).find((item) => item.relationship === 'self') || (topology.runtimes || [])[0];
  const binding = (topology.workspace_bindings || []).find((item) => item.state === 'bound' && item.destination_workspace_path) || (topology.workspace_bindings || [])[0];
  if (!runtime?.swarm_id || !binding?.workspace_binding_id) throw new Error('missing runtime or workspace binding');
  emit({ stage: 'topology.ok', swarm_id: runtime.swarm_id, workspace_binding_id: binding.workspace_binding_id, workspace_path: binding.source_workspace_path });

  const suffix = `${Date.now()}-${crypto.randomBytes(4).toString('hex')}`;
  const created = await apiJSON('POST', '/v3/sessions', token, {
    client_request_id: `cp8-live-create:${suffix}`,
    title: `CP8 live stream ${suffix}`,
    workspace_path: binding.source_workspace_path,
    workspace_name: binding.source_workspace_name || 'swarm-go',
    workspace_binding_id: binding.workspace_binding_id,
    swarm_id: runtime.swarm_id,
    target_kind: 'host',
    target_relationship: 'self',
    mode: 'auto',
    agent_name: cfg.agentName,
    preference: { provider: cfg.provider, model: cfg.model, thinking: cfg.thinking },
    metadata: { cp8_live_stream_e2e: suffix },
  }, 'sessions.v3.create');
  sessionID = created.session?.id;
  if (!sessionID) throw new Error('create did not return session.id');
  emit({ stage: 'session.created', session_id: sessionID, projection_last_seq: created.projection?.last_event_seq, response_messages: created.messages?.length ?? 0, response_events: created.events?.length ?? 0 });

  const tailBefore = await apiJSON('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/messages?tail=true&limit=200`, token, undefined, 'messages.tail.before');
  emit({ stage: 'messages.tail.before', count: tailBefore.messages?.length ?? 0, has_more_older: tailBefore.has_more_older, limit: tailBefore.limit });

  realtime = await openRealtime(token, (frame, summary) => {
    emit({ stage: 'realtime.frame', ...summary });
    if (summary.kind === 'hello') seen.hello = true;
    if (summary.kind === 'replay.started') seen.replayStarted = true;
    if (summary.kind === 'replay.complete') seen.replayComplete = true;
    if (summary.kind === 'cursor.error') seen.cursorErrors.push(summary.error || summary.error_code || 'cursor.error');
    const eventType = summary.event_type || '';
    if (eventType) realtimeEventTypes.push(eventType);
    if (eventType === 'session.message.appended') seen.userMessage = true;
    if (eventType === 'session.assistant.started') seen.assistantStarted = true;
    if (eventType === 'session.assistant.delta') seen.assistantDelta = true;
    if (eventType === 'session.assistant.completed') seen.assistantCompleted = true;
    if (summary.message_role === 'assistant' && summary.message_len > 0) seen.assistantMessageOnRealtime = true;
    if (terminalEvents.has(eventType)) seen.terminalEventType = eventType;
  });
  await new Promise((resolve) => setTimeout(resolve, 100));
  const subscribe = { protocol: 'v3.realtime', protocol_version: 1, kind: 'subscribe.session', session_id: sessionID, subscription_id: `cp8-${sessionID}` };
  appendJSON(framesPath, { direction: 'client', frame: subscribe });
  realtime.send(subscribe);
  seen.subscribed = true;

  await new Promise((resolve) => setTimeout(resolve, 250));
  const message = await apiJSON('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, token, {
    client_request_id: `cp8-live-message:${suffix}`,
    role: 'user',
    content: cfg.prompt,
    metadata: { cp8_live_stream_e2e: suffix },
  }, 'sessions.v3.messages.post');
  runID = message.run_intent?.run_id || '';
  emit({ stage: 'message.posted', session_id: sessionID, run_id: runID, run_status: message.run_intent?.status, response_messages: message.messages?.length ?? 0, response_events: message.events?.length ?? 0, realtime_event_seq: message.realtime_outbox?.event?.seq });

  while (Date.now() < deadline && !seen.terminalEventType) {
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  if (realtime) realtime.close();

  const tailAfter = await apiJSON('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/messages?tail=true&limit=200`, token, undefined, 'messages.tail.after');
  const events = await apiJSON('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/events?after_seq=0&limit=500`, token, undefined, 'events.replay.after');
  const assistantMessages = (tailAfter.messages || []).filter((m) => m.role === 'assistant');
  const dbEventTypes = (events.events || []).map((e) => e.event_type);
  const pass = Boolean(
    seen.hello && seen.subscribed && seen.replayStarted && seen.replayComplete &&
    seen.userMessage && seen.assistantStarted && seen.assistantDelta && seen.assistantCompleted &&
    seen.assistantMessageOnRealtime && assistantMessages.length > 0 && seen.cursorErrors.length === 0 &&
    (tailAfter.messages?.length ?? 0) <= 200
  );
  const summary = {
    ok: pass,
    primary_ssh: cfg.primarySSH,
    api_url: cfg.apiURL,
    session_id: sessionID,
    run_id: runID,
    provider: cfg.provider,
    model: cfg.model,
    terminal_event_type: seen.terminalEventType,
    observed: seen,
    realtime_event_count: realtimeEventTypes.length,
    realtime_event_types: realtimeEventTypes,
    db_event_count: events.events?.length ?? 0,
    db_event_types: dbEventTypes,
    tail_message_count: tailAfter.messages?.length ?? 0,
    assistant_message_count: assistantMessages.length,
    assistant_preview: assistantMessages.map((m) => String(m.content || '').slice(0, 200)),
    artifacts: { frames: framesPath, requests: requestsPath, summary: summaryPath, function_chain: chainPath },
  };
  fs.writeFileSync(summaryPath, JSON.stringify(summary, null, 2));
  writeFunctionChain(chainPath, summary);
  emit({ stage: 'final.summary', ...summary, realtime_event_types: undefined, db_event_types: undefined });
  if (!pass) process.exitCode = 1;
} catch (err) {
  const summary = { ok: false, error: err instanceof Error ? err.message : String(err), session_id: sessionID, run_id: runID, observed: seen, artifacts: { frames: framesPath, requests: requestsPath, summary: summaryPath, function_chain: chainPath } };
  fs.writeFileSync(summaryPath, JSON.stringify(summary, null, 2));
  writeFunctionChain(chainPath, summary);
  emit({ stage: 'error', error: summary.error, session_id: sessionID, run_id: runID });
  process.exitCode = 1;
} finally {
  try { realtime?.close(); } catch {}
}

function writeFunctionChain(file, summary) {
  const lines = [];
  lines.push(`# V3 live desktop stream E2E function chain`);
  lines.push('');
  lines.push(`Result: ${summary.ok ? 'PASS' : 'FAIL'}`);
  lines.push(`Session: ${summary.session_id || ''}`);
  lines.push(`Run: ${summary.run_id || ''}`);
  lines.push(`Provider/model: ${cfg.provider} / ${cfg.model}`);
  if (summary.error) lines.push(`Error: ${summary.error}`);
  lines.push('');
  lines.push('## What this runner proves');
  lines.push('- It uses the live SSH host API, not mocks.');
  lines.push('- It posts a real V3 user message and waits for Fireworks-backed assistant output.');
  lines.push('- It subscribes to `/v3/realtime/stream`, the canonical desktop realtime transport.');
  lines.push('- It records request shapes and realtime frames under the artifact directory.');
  lines.push('- It verifies message tail remains bounded at <= 200 messages.');
  lines.push('');
  lines.push('## Backend chain');
  lines.push('1. `handleDesktopLocalSessionBootstrap` (`swarmd/internal/api/desktop_local_auth.go`) issues the local desktop product token used by browser/API requests.');
  lines.push('2. `handleSwarmTopologySnapshot` (`swarmd/internal/api/topology.go`) returns the self runtime and workspace binding needed for a primary V3 session.');
  lines.push('3. `handleSessionsV3PrimaryCreate` (`swarmd/internal/api/sessions_v3_primary.go`) validates topology/agent metadata and creates the V3 session through `applySessionV3PrimaryMutation`.');
  lines.push('4. `handleV3RealtimeStream` (`swarmd/internal/api/sessions_v3_realtime_ws.go`) accepts `/v3/realtime/stream`, sends `hello`, receives `subscribe.session`, replays existing session events, then tails the realtime outbox.');
  lines.push('5. `handleSessionV3PrimaryMessages` (`swarmd/internal/api/sessions_v3_primary.go`) appends the user message, creates a pending run intent, returns only the bounded mutation response, and calls `EnqueueRun`.');
  lines.push('6. `applySessionV3PrimaryMutation` (`swarmd/internal/api/sessions_v3_outbox.go`) is the canonical mutation gate: it commits through the session service, writes the durable realtime outbox row, and publishes that row to websocket subscribers.');
  lines.push('7. `sessionV3Executor.EnqueueRun` / `sessionV3Executor.run` (`swarmd/internal/api/sessions_v3_executor.go`) starts the provider turn, records `session.assistant.started`, provider/tool/delta events, and finally records `session.assistant.completed`.');
  lines.push('8. `publishCommittedV3RealtimeOutbox` wakes `/v3/realtime/stream`; `v3RealtimeCatchUpEndpointCursor` sends each committed event frame with endpoint cursor/rev data.');
  lines.push('');
  lines.push('## Frontend desktop chain');
  lines.push('1. `DesktopChatPanel` calls `ensureRunStream(sessionId, runId)` for active V3 sessions.');
  lines.push('2. `ensureRunStream` in `desktop-ui-store.ts` calls `requireV3RealtimeController().subscribeSession(...)`; it must not open the legacy per-session stream for V3 UI updates.');
  lines.push('3. `DesktopV3RealtimeController` opens `/v3/realtime/stream` and sends `subscribe.session` for the active session.');
  lines.push('4. `applyDesktopV3RealtimeFrame` normalizes each realtime frame with `normalizeV3RealtimeFrame`.');
  lines.push('5. `applyV3RuntimeEnvelope` applies events into the canonical V3 runtime store.');
  lines.push('6. `session.assistant.delta` updates the live assistant draft; `session.assistant.completed` persists the assistant message and returns live state to idle.');
  lines.push('7. `DesktopChatPanel` reads the canonical store and should update automatically without a broad workset refetch.');
  lines.push('');
  lines.push('## Observed required events');
  lines.push(`- hello: ${Boolean(summary.observed?.hello)}`);
  lines.push(`- subscribe.session sent: ${Boolean(summary.observed?.subscribed)}`);
  lines.push(`- replay.started: ${Boolean(summary.observed?.replayStarted)}`);
  lines.push(`- replay.complete: ${Boolean(summary.observed?.replayComplete)}`);
  lines.push(`- session.message.appended: ${Boolean(summary.observed?.userMessage)}`);
  lines.push(`- session.assistant.started: ${Boolean(summary.observed?.assistantStarted)}`);
  lines.push(`- session.assistant.delta: ${Boolean(summary.observed?.assistantDelta)}`);
  lines.push(`- session.assistant.completed: ${Boolean(summary.observed?.assistantCompleted)}`);
  lines.push(`- assistant message on realtime: ${Boolean(summary.observed?.assistantMessageOnRealtime)}`);
  lines.push(`- cursor errors: ${(summary.observed?.cursorErrors || []).length}`);
  lines.push(`- tail message count: ${summary.tail_message_count ?? ''}`);
  lines.push('');
  lines.push('## Artifacts');
  lines.push(`- HTTP request summaries: ${requestsPath}`);
  lines.push(`- Realtime frames: ${framesPath}`);
  lines.push(`- Summary JSON: ${summaryPath}`);
  fs.writeFileSync(file, lines.join('\n') + '\n');
}
NODE

CONFIG_LOCAL="${ARTIFACT_DIR}/config.json"
python3 - "$CONFIG_LOCAL" "$PRIMARY_SSH" "$API_URL" "$AGENT_NAME" "$PROVIDER" "$MODEL" "$THINKING" "$PROMPT" "$TIMEOUT_SECONDS" <<'PY'
import json, sys
path, primary, api, agent, provider, model, thinking, prompt, timeout = sys.argv[1:]
with open(path, 'w', encoding='utf-8') as f:
    json.dump({
        'primarySSH': primary,
        'apiURL': api,
        'agentName': agent,
        'provider': provider,
        'model': model,
        'thinking': thinking,
        'prompt': prompt,
        'timeoutSeconds': int(timeout),
        'artifactDir': '',
    }, f, indent=2)
PY

log "v3-live-stream-e2e: primary=${PRIMARY_SSH} api=${API_URL} provider=${PROVIDER} model=${MODEL}"
log "v3-live-stream-e2e: artifacts=${ARTIFACT_DIR}"

if [[ "${REMOTE_WORK_DIR_EXPLICIT}" == "true" ]]; then
  ssh "${PRIMARY_SSH}" "set -euo pipefail; rm -rf -- $(printf '%q' "${REMOTE_WORK_DIR}"); mkdir -p -- $(printf '%q' "${REMOTE_WORK_DIR}")"
else
  REMOTE_WORK_DIR="$(ssh "${PRIMARY_SSH}" "mktemp -d")"
fi
[[ -n "${REMOTE_WORK_DIR}" ]] || fail "failed to create remote work dir"
scp -q "${RUNNER_LOCAL}" "${CONFIG_LOCAL}" "${PRIMARY_SSH}:${REMOTE_WORK_DIR}/"

remote_status=0
ssh "${PRIMARY_SSH}" 'bash -s' -- "${REMOTE_WORK_DIR}" < <(cat <<'REMOTE'
set -euo pipefail
remote_dir="$1"
cd "${remote_dir}"
python3 - <<'PY'
import json
with open('config.json', 'r', encoding='utf-8') as f:
    cfg=json.load(f)
cfg['artifactDir']='artifacts'
with open('config.json', 'w', encoding='utf-8') as f:
    json.dump(cfg, f, indent=2)
PY
SWARM_E2E_CONFIG="${remote_dir}/config.json" node "${remote_dir}/remote-runner.mjs"
REMOTE
) >"${REMOTE_STDOUT}" 2>"${REMOTE_STDERR}" || remote_status=$?

# Always copy remote artifacts back for inspection.
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
  log "v3-live-stream-e2e: FAILED (remote status ${remote_status})"
  log "stdout: ${REMOTE_STDOUT}"
  log "stderr: ${REMOTE_STDERR}"
  [[ -s "${REMOTE_STDERR}" ]] && sed -n '1,120p' "${REMOTE_STDERR}" >&2 || true
  [[ -f "${SUMMARY_JSON}" ]] && jq . "${SUMMARY_JSON}" >&2 || true
  exit "${remote_status}"
fi

if [[ ! -f "${SUMMARY_JSON}" ]]; then
  fail "remote run succeeded but summary was not copied back"
fi

jq '{ok, primary_ssh, api_url, session_id, run_id, provider, model, terminal_event_type, observed, realtime_event_count, db_event_count, tail_message_count, assistant_message_count, assistant_preview, artifacts}' "${SUMMARY_JSON}"
log "v3-live-stream-e2e: PASS"
log "function chain: ${FUNCTION_CHAIN_MD}"
log "frames: ${ARTIFACT_DIR}/remote-artifacts/realtime-frames.ndjson"
log "requests: ${ARTIFACT_DIR}/remote-artifacts/http-requests.ndjson"
