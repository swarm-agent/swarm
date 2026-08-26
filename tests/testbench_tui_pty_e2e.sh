#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: tests/testbench_tui_pty_e2e.sh [ssh-target] [options]

Runs a real TUI PTY E2E against an already rebuilt live SSH target.
This test does not mock the backend, TUI, realtime transport, or assistant output.
It launches the real remote TUI in a PTY, sends hello and a follow-up, captures
TUI transcript plus HTTP/realtime evidence, and verifies assistant output and
run lifecycle indicators.

Options:
  --primary-ssh <target>      SSH target. Required via option, positional argument, or SWARM_PRIMARY_SSH
  --api-url <url>             Remote API URL as seen from the target. Default: http://127.0.0.1:7781
  --remote-dir <path>         Remote swarm-go checkout. Default: auto-discover
  --session-id <id>           Existing V3 session to open in TUI. Default: create one through live API
  --prompt <text>             First prompt. Default: asks for TUI_E2E_HELLO_OK
  --first-marker <text>       Required first-turn assistant marker. Default: TUI_E2E_HELLO_OK
  --follow-up <text>          Follow-up prompt. Default: asks for TUI_E2E_FOLLOWUP_OK
  --follow-up-marker <text>   Required follow-up marker. Default: TUI_E2E_FOLLOWUP_OK
  --skip-follow-up            Run only the first turn
  --launch-command <command>  Start and verify a session through a real TUI /new or /task command instead of pre-creating it
  --expected-mode <mode>      Expected launched session mode for --launch-command: auto or plan
  --expected-worktree <bool>  Expected launched managed-worktree state for --launch-command
  --expected-tool-order <csv> Require exact TUI tool order, e.g. read:AGENTS.md,read:go.mod
  --provider <provider>       Provider for created session. Default: fireworks
  --model <model>             Model for created session. Default: accounts/fireworks/models/kimi-k2p6
  --thinking <level>          Thinking setting. Default: low
  --agent <name>              Agent name. Default: swarm
  --timeout-seconds <n>       Per-phase/per-turn timeout. Default: 15
  --overall-timeout-seconds <n>
                              Hard timeout for the whole remote run. Default: same as --timeout-seconds
  --tui-bin <path>            Remote TUI binary. Default: /usr/local/share/swarm/bin/swarmtui
  --artifact-dir <path>       Local artifact directory. Default: .tmp/testbench-tui-pty-e2e/<timestamp>
  --remote-work-dir <path>    Remote temp dir. Default: mktemp on target
  --keep-remote               Do not delete the remote temp dir
  --help                      Show this help
USAGE
}

log() { printf '[%(%Y-%m-%dT%H:%M:%S%z)T] testbench-tui-pty-e2e: %s\n' -1 "$*"; }
fail() { log "FAIL: $*" >&2; log "artifact root: ${ARTIFACT_DIR:-<not-created>}" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"; }

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
PRIMARY_SSH="${SWARM_PRIMARY_SSH:-}"
API_URL="${SWARM_PRIMARY_API_URL:-http://127.0.0.1:7781}"
REMOTE_DIR="${SWARM_TUI_E2E_REMOTE_DIR:-}"
SESSION_ID="${SWARM_TUI_E2E_SESSION_ID:-}"
PROMPT="${SWARM_TUI_E2E_PROMPT:-Real TUI E2E first turn. Reply with exactly TUI_E2E_HELLO_OK and do not call tools.}"
FIRST_MARKER="${SWARM_TUI_E2E_FIRST_MARKER:-TUI_E2E_HELLO_OK}"
FOLLOW_UP="${SWARM_TUI_E2E_FOLLOW_UP:-Real TUI E2E follow-up. Reply with exactly TUI_E2E_FOLLOWUP_OK and do not call tools.}"
FOLLOW_UP_MARKER="${SWARM_TUI_E2E_FOLLOW_UP_MARKER:-TUI_E2E_FOLLOWUP_OK}"
SKIP_FOLLOW_UP="${SWARM_TUI_E2E_SKIP_FOLLOW_UP:-false}"
LAUNCH_COMMAND="${SWARM_TUI_E2E_LAUNCH_COMMAND:-}"
EXPECTED_MODE="${SWARM_TUI_E2E_EXPECTED_MODE:-auto}"
EXPECTED_WORKTREE="${SWARM_TUI_E2E_EXPECTED_WORKTREE:-false}"
EXPECTED_TOOL_ORDER="${SWARM_TUI_E2E_EXPECTED_TOOL_ORDER:-}"
PROVIDER="${SWARM_TUI_E2E_PROVIDER:-fireworks}"
MODEL="${SWARM_TUI_E2E_MODEL:-accounts/fireworks/models/kimi-k2p6}"
THINKING="${SWARM_TUI_E2E_THINKING:-low}"
AGENT_NAME="${SWARM_TUI_E2E_AGENT:-swarm}"
TIMEOUT_SECONDS="${SWARM_TUI_E2E_TIMEOUT_SECONDS:-15}"
OVERALL_TIMEOUT_SECONDS="${SWARM_TUI_E2E_OVERALL_TIMEOUT_SECONDS:-}"
TUI_BIN="${SWARM_TUI_E2E_TUI_BIN:-/usr/local/share/swarm/bin/swarmtui}"
ARTIFACT_DIR="${SWARM_TUI_E2E_ARTIFACT_DIR:-${ROOT_DIR}/.tmp/testbench-tui-pty-e2e/$(date +%Y%m%d-%H%M%S)}"
REMOTE_WORK_DIR="${SWARM_TUI_E2E_REMOTE_WORK_DIR:-}"
KEEP_REMOTE="false"

if [[ $# -gt 0 && "${1:-}" != --* ]]; then
  PRIMARY_SSH="$1"
  shift
fi
while [[ $# -gt 0 ]]; do
  case "$1" in
    --primary-ssh|--ssh) PRIMARY_SSH="${2:-}"; shift 2 ;;
    --api-url|--primary-api-url) API_URL="${2:-}"; shift 2 ;;
    --remote-dir) REMOTE_DIR="${2:-}"; shift 2 ;;
    --session-id) SESSION_ID="${2:-}"; shift 2 ;;
    --prompt) PROMPT="${2:-}"; shift 2 ;;
    --first-marker) FIRST_MARKER="${2:-}"; shift 2 ;;
    --follow-up|--followup) FOLLOW_UP="${2:-}"; shift 2 ;;
    --follow-up-marker) FOLLOW_UP_MARKER="${2:-}"; shift 2 ;;
    --skip-follow-up) SKIP_FOLLOW_UP="true"; shift ;;
    --launch-command) LAUNCH_COMMAND="${2:-}"; shift 2 ;;
    --expected-mode) EXPECTED_MODE="${2:-}"; shift 2 ;;
    --expected-worktree) EXPECTED_WORKTREE="${2:-}"; shift 2 ;;
    --expected-tool-order) EXPECTED_TOOL_ORDER="${2:-}"; shift 2 ;;
    --provider) PROVIDER="${2:-}"; shift 2 ;;
    --model) MODEL="${2:-}"; shift 2 ;;
    --thinking) THINKING="${2:-}"; shift 2 ;;
    --agent|--agent-name) AGENT_NAME="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --overall-timeout-seconds|--hard-timeout-seconds) OVERALL_TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --tui-bin) TUI_BIN="${2:-}"; shift 2 ;;
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
require_command timeout

[[ -n "${PRIMARY_SSH}" ]] || fail "pass an SSH target positionally, with --primary-ssh, or via SWARM_PRIMARY_SSH"
[[ -n "${API_URL}" ]] || fail "--api-url is required"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TIMEOUT_SECONDS}" -gt 0 ]] || fail "--timeout-seconds must be a positive integer"
if [[ -z "${OVERALL_TIMEOUT_SECONDS}" ]]; then
  OVERALL_TIMEOUT_SECONDS="${TIMEOUT_SECONDS}"
fi
[[ "${OVERALL_TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${OVERALL_TIMEOUT_SECONDS}" -gt 0 ]] || fail "--overall-timeout-seconds must be a positive integer"
[[ -n "${TUI_BIN}" ]] || fail "--tui-bin is required"
[[ -n "${FIRST_MARKER}" ]] || fail "--first-marker is required"
[[ "${EXPECTED_MODE}" == "auto" || "${EXPECTED_MODE}" == "plan" ]] || fail "--expected-mode must be auto or plan"
[[ "${EXPECTED_WORKTREE}" == "true" || "${EXPECTED_WORKTREE}" == "false" ]] || fail "--expected-worktree must be true or false"
if [[ -n "${LAUNCH_COMMAND}" ]]; then
  [[ "${LAUNCH_COMMAND}" == /new* || "${LAUNCH_COMMAND}" == /task* ]] || fail "--launch-command supports only /new and /task forms"
  SKIP_FOLLOW_UP="true"
fi
if [[ "${SKIP_FOLLOW_UP}" != "true" ]]; then
  [[ -n "${FOLLOW_UP_MARKER}" ]] || fail "--follow-up-marker is required unless --skip-follow-up is set"
fi
# The Node runner owns the real overall timeout so it can write per-phase artifacts
# before exiting 124. Give the outer SSH timeout a cleanup cushion instead of
# killing the runner at the exact same instant and losing evidence.
RUN_TIMEOUT_SECONDS=$((OVERALL_TIMEOUT_SECONDS + 20))
SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=5 -o ServerAliveInterval=5 -o ServerAliveCountMax=1)
API_URL="${API_URL%/}"
mkdir -p -- "${ARTIFACT_DIR}"

RUNNER_LOCAL="${ARTIFACT_DIR}/remote-tui-runner.mjs"
REMOTE_STDOUT="${ARTIFACT_DIR}/remote-stdout.ndjson"
REMOTE_STDERR="${ARTIFACT_DIR}/remote-stderr.log"
SUMMARY_JSON="${ARTIFACT_DIR}/summary.json"

cat >"${RUNNER_LOCAL}" <<'NODE'
import crypto from 'node:crypto';
import fs from 'node:fs';
import net from 'node:net';
import path from 'node:path';

const cfg = JSON.parse(fs.readFileSync(process.env.SWARM_E2E_CONFIG, 'utf8'));
const apiURL = new URL(cfg.apiURL);
const host = apiURL.hostname;
const port = Number(apiURL.port || 80);
if (apiURL.protocol !== 'http:') throw new Error(`expected http API URL, got ${apiURL.protocol}`);
const startedAt = Date.now();
const phaseTimeoutMs = Math.max(1, Number(cfg.timeoutSeconds || 15)) * 1000;
const overallTimeoutMs = Math.max(1, Number(cfg.overallTimeoutSeconds || cfg.timeoutSeconds || 15)) * 1000;
const deadlineFor = (seconds = cfg.timeoutSeconds) => Date.now() + seconds * 1000;
const artifactDir = path.resolve(cfg.artifactDir);
fs.mkdirSync(artifactDir, { recursive: true });
const framesPath = path.join(artifactDir, 'realtime-frames.ndjson');
const requestsPath = path.join(artifactDir, 'http-requests.ndjson');
const summaryPath = path.join(artifactDir, 'summary.json');
const runnerEventsPath = path.join(artifactDir, 'runner-events.ndjson');
const phaseLogPath = path.join(artifactDir, 'phase.log');
const tuiRawPath = path.join(artifactDir, 'tui.typescript');
const tuiCleanPath = path.join(artifactDir, 'tui.cleaned.txt');
const feedLogPath = path.join(artifactDir, 'pty-feed.log');
const tuiStdoutPath = path.join(artifactDir, 'script.stdout');
const tuiStderrPath = path.join(artifactDir, 'script.stderr');
const clipboardCapturePath = path.join(artifactDir, 'chat-snapshot.txt');
const helperBinDir = path.join(artifactDir, 'bin');
fs.writeFileSync(framesPath, '');
fs.writeFileSync(requestsPath, '');
fs.writeFileSync(feedLogPath, '');
fs.writeFileSync(runnerEventsPath, '');
fs.writeFileSync(phaseLogPath, '');

let currentPhase = 'boot';
let currentPhaseStartedAt = Date.now();
let lastPhaseEvent = null;
function elapsed() { return Date.now() - startedAt; }
function emit(obj) {
  const event = { t_ms: elapsed(), phase: currentPhase, ...obj };
  const line = JSON.stringify(event);
  fs.appendFileSync(runnerEventsPath, line + '\n');
  console.log(line);
}
function logPhase(message, extra = {}) {
  lastPhaseEvent = { t_ms: elapsed(), phase: currentPhase, message, ...extra };
  fs.appendFileSync(phaseLogPath, JSON.stringify(lastPhaseEvent) + '\n');
  emit({ stage: 'phase.log', message, ...extra });
}
function beginPhase(name, extra = {}) {
  currentPhase = name;
  currentPhaseStartedAt = Date.now();
  logPhase('begin', extra);
}
function endPhase(extra = {}) {
  logPhase('end', { duration_ms: Date.now() - currentPhaseStartedAt, ...extra });
}
function remainingOverallMs() { return Math.max(0, overallTimeoutMs - elapsed()); }
function boundedTimeoutMs(requestedMs) { return Math.max(1, Math.min(requestedMs, remainingOverallMs())); }
function throwIfOverallExpired(label) { if (remainingOverallMs() <= 0) throw new Error(`overall timeout after ${Math.round(overallTimeoutMs / 1000)}s while waiting for ${label}`); }
function appendJSON(file, obj) { fs.appendFileSync(file, JSON.stringify({ t_ms: elapsed(), ...obj }) + '\n'); }
function appendLine(file, line) { fs.appendFileSync(file, `[${new Date().toISOString()}] ${line}\n`); }
function sleep(ms) { return new Promise(resolve => setTimeout(resolve, ms)); }
function fileTail(file, maxBytes = 4000) {
  try {
    const stat = fs.statSync(file);
    const size = Math.min(stat.size, maxBytes);
    const fd = fs.openSync(file, 'r');
    const buf = Buffer.alloc(size);
    fs.readSync(fd, buf, 0, size, Math.max(0, stat.size - size));
    fs.closeSync(fd);
    return buf.toString('utf8');
  } catch {
    return '';
  }
}
function writeEvidenceSummary(ok, error = '') {
  let rawTranscript = '';
  try {
    rawTranscript = fs.readFileSync(tuiRawPath, 'utf8');
    fs.writeFileSync(tuiCleanPath, stripAnsi(rawTranscript));
  } catch {}
  const summary = {
    ok,
    error,
    current_phase: currentPhase,
    current_phase_elapsed_ms: Date.now() - currentPhaseStartedAt,
    last_phase_event: lastPhaseEvent,
    session_id: sessionID,
    observed: seen,
    tui_process: tuiProc ? { pid: tuiProc.pid, exit_code: tuiProc.exitCode, signal_code: tuiProc.signalCode, killed: tuiProc.killed } : null,
    tails: {
      phase_log: fileTail(phaseLogPath),
      runner_events: fileTail(runnerEventsPath),
      feed_log: fileTail(feedLogPath),
      clean_transcript: fileTail(tuiCleanPath),
      script_stdout: fileTail(tuiStdoutPath),
      script_stderr: fileTail(tuiStderrPath),
    },
    artifacts: { transcript: tuiRawPath, clean_transcript: tuiCleanPath, chat_snapshot: clipboardCapturePath, feed_log: feedLogPath, frames: framesPath, requests: requestsPath, runner_events: runnerEventsPath, phase_log: phaseLogPath, summary: summaryPath }
  };
  fs.writeFileSync(summaryPath, JSON.stringify(summary, null, 2));
  return summary;
}
function redactHeaders(headers) {
  const out = {};
  for (const [key, value] of Object.entries(headers || {})) out[key] = /authorization|cookie|token/i.test(key) ? '<redacted>' : value;
  return out;
}
function summarizeBody(body) {
  if (!body || typeof body !== 'object') return body ?? null;
  const out = { ...body };
  for (const key of ['content', 'prompt']) if (typeof out[key] === 'string') out[key] = `${out[key].slice(0, 120)}${out[key].length > 120 ? '…' : ''}`;
  return out;
}
function summarizeResponse(value) {
  if (!value || typeof value !== 'object') return value ?? null;
  return {
    ok: value.ok,
    session_id: value.session?.id || value.session_id,
    run_id: value.run_intent?.run_id || value.run_id,
    run_status: value.run_intent?.status || value.status,
    event_seq: value.realtime_outbox?.event?.seq,
    endpoint_seq: value.realtime_outbox?.endpoint_seq,
    messages: Array.isArray(value.messages) ? value.messages.length : undefined,
    events: Array.isArray(value.events) ? value.events.length : undefined,
    runtimes: Array.isArray(value.runtimes) ? value.runtimes.length : undefined,
    workspace_bindings: Array.isArray(value.workspace_bindings) ? value.workspace_bindings.length : undefined,
  };
}
async function apiJSON(method, route, token, body = undefined, label = route) {
  logPhase('api.request.begin', { label, method, route });
  const headers = { Accept: 'application/json', Origin: cfg.apiURL, Referer: `${cfg.apiURL}/app`, 'Sec-Fetch-Site': 'same-origin' };
  if (token) headers['X-Swarm-Token'] = token;
  const controller = new AbortController();
  const abortTimer = setTimeout(() => controller.abort(new Error(`${method} ${route} timed out`)), boundedTimeoutMs(5000));
  const init = { method, headers, signal: controller.signal };
  if (body !== undefined) { headers['Content-Type'] = 'application/json'; init.body = JSON.stringify(body); }
  const before = Date.now();
  let response, text;
  try {
    response = await fetch(`${cfg.apiURL}${route}`, init);
    text = await response.text();
  } finally {
    clearTimeout(abortTimer);
  }
  let parsed = null;
  try { parsed = text ? JSON.parse(text) : null; } catch { parsed = { raw: text }; }
  appendJSON(requestsPath, { label, method, route, status: response.status, duration_ms: Date.now() - before, request_headers: redactHeaders(headers), request_body_summary: summarizeBody(body), response_summary: summarizeResponse(parsed) });
  logPhase('api.request.end', { label, method, route, status: response.status, duration_ms: Date.now() - before, response_summary: summarizeResponse(parsed) });
  if (!response.ok) throw new Error(`${method} ${route} status=${response.status} body=${text.slice(0, 1200)}`);
  return parsed;
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
    endpoint_cursor: frame.endpoint_cursor,
    error_code: frame.error_code,
    error: frame.error,
    event_seq: event?.seq,
    event_type: frame.event_type || event?.event_type,
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
  return ['GET ' + route + ' HTTP/1.1', `Host: ${host}:${port}`, 'Upgrade: websocket', 'Connection: Upgrade', `Sec-WebSocket-Key: ${key}`, 'Sec-WebSocket-Version: 13', `Origin: ${cfg.apiURL}`, 'Sec-Fetch-Site: same-origin', `X-Swarm-Token: ${token}`, `Cookie: swarm_desktop_session=${token}`, '', ''].join('\r\n');
}
function writeClientTextFrame(socket, obj) {
  const payload = Buffer.from(JSON.stringify(obj));
  let header;
  if (payload.length < 126) header = Buffer.from([0x81, payload.length | 0x80]);
  else if (payload.length < 65536) { header = Buffer.alloc(4); header[0] = 0x81; header[1] = 126 | 0x80; header.writeUInt16BE(payload.length, 2); }
  else { header = Buffer.alloc(10); header[0] = 0x81; header[1] = 127 | 0x80; header.writeBigUInt64BE(BigInt(payload.length), 2); }
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
    const socket = net.createConnection({ host, port });
    let handshake = Buffer.alloc(0), buffer = Buffer.alloc(0), upgraded = false, settled = false;
    const fail = (err) => { if (settled) return; settled = true; try { socket.destroy(); } catch {} reject(err); };
    socket.setTimeout(boundedTimeoutMs(phaseTimeoutMs), () => fail(new Error('websocket timeout')));
    socket.on('error', fail);
    socket.on('connect', () => socket.write(websocketRequestHeaders(route, token)));
    socket.on('data', (chunk) => {
      if (!upgraded) {
        handshake = Buffer.concat([handshake, chunk]);
        const marker = handshake.indexOf('\r\n\r\n');
        if (marker < 0) return;
        const head = handshake.slice(0, marker).toString('utf8');
        const rest = handshake.slice(marker + 4);
        if (!head.startsWith('HTTP/1.1 101') && !head.startsWith('HTTP/1.0 101')) return fail(new Error(`websocket upgrade failed: ${head}`));
        upgraded = true;
        appendJSON(requestsPath, { label: 'realtime.websocket.upgrade', method: 'GET', route, status: 101, request_headers: redactHeaders({ 'X-Swarm-Token': token, Cookie: `swarm_desktop_session=${token}`, Origin: cfg.apiURL }), response_summary: head.split('\r\n')[0] });
        if (!settled) { settled = true; resolve({ socket, send: (obj) => writeClientTextFrame(socket, obj), close: () => socket.end() }); }
        buffer = rest;
      } else buffer = Buffer.concat([buffer, chunk]);
      while (upgraded && buffer.length >= 2) {
        const opcode = buffer[0] & 0x0f;
        let offset = 2, len = buffer[1] & 0x7f;
        if (len === 126) { if (buffer.length < 4) return; len = buffer.readUInt16BE(2); offset = 4; }
        else if (len === 127) { if (buffer.length < 10) return; const big = buffer.readBigUInt64BE(2); if (big > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error('websocket frame too large'); len = Number(big); offset = 10; }
        const masked = (buffer[1] & 0x80) !== 0;
        let mask = null;
        if (masked) { if (buffer.length < offset + 4) return; mask = buffer.slice(offset, offset + 4); offset += 4; }
        if (buffer.length < offset + len) return;
        let payload = buffer.slice(offset, offset + len);
        buffer = buffer.slice(offset + len);
        if (masked && mask) payload = Buffer.from(payload.map((v, i) => v ^ mask[i % 4]));
        if (opcode === 0x8) { appendJSON(framesPath, { direction: 'server', frame: { kind: 'close' } }); socket.end(); return; }
        if (opcode === 0x9) { writePong(socket, payload); continue; }
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
function stripAnsi(input) {
  return input
    .replace(/\x1b\][^\x07]*(\x07|\x1b\\)/g, '')
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '')
    .replace(/\x1b[()][A-Za-z0-9]/g, '')
    .replace(/\r/g, '');
}
async function execShell(command, opts = {}) {
  const { spawn } = await import('node:child_process');
  return await new Promise((resolve, reject) => {
    const child = spawn('bash', ['-lc', command], { cwd: cfg.remoteDir || process.cwd(), env: { ...process.env, ...(opts.env || {}) }, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    child.stdout.on('data', d => stdout += d.toString());
    child.stderr.on('data', d => stderr += d.toString());
    child.on('error', reject);
    child.on('close', code => resolve({ code, stdout, stderr }));
  });
}
async function discoverRemoteDir() {
  if (cfg.remoteDir) return cfg.remoteDir;
  const candidates = ['$HOME/swarm-go', '$HOME/src/swarm-go', '$HOME/work/swarm-go'];
  for (const candidate of candidates) {
    const res = await execShell(`[ -d ${candidate}/.git ] && [ -x ${candidate}/rebuild ] && printf %s ${candidate}`.replaceAll('$HOME', '"$HOME"'), { env: {} });
    if (res.code === 0 && res.stdout.trim()) return res.stdout.trim();
  }
  const res = await execShell(`find "$HOME" /opt /srv /tmp -maxdepth 4 -type d -name swarm-go 2>/dev/null | while IFS= read -r d; do [ -d "$d/.git" ] && [ -x "$d/rebuild" ] && printf '%s\n' "$d" && exit 0; done`);
  if (res.code === 0 && res.stdout.trim()) return res.stdout.trim().split('\n')[0];
  throw new Error('could not discover remote swarm-go checkout; pass --remote-dir');
}
async function waitFor(predicate, timeoutMs, label, intervalMs = 250) {
  logPhase('wait.begin', { label, timeout_ms: timeoutMs });
  const started = Date.now();
  const end = Date.now() + boundedTimeoutMs(timeoutMs);
  let attempts = 0;
  while (Date.now() < end) {
    throwIfOverallExpired(label);
    attempts += 1;
    const result = await predicate();
    if (result) {
      logPhase('wait.end', { label, attempts, duration_ms: Date.now() - started });
      return result;
    }
    await sleep(Math.min(intervalMs, Math.max(1, end - Date.now())));
  }
  logPhase('wait.timeout', { label, attempts, duration_ms: Date.now() - started, transcript_tail: fileTail(tuiCleanPath || tuiRawPath, 2000), raw_transcript_tail: fileTail(tuiRawPath, 2000), script_stderr_tail: fileTail(tuiStderrPath, 2000) });
  throw new Error(`timed out waiting for ${label} after ${Math.ceil(timeoutMs / 1000)}s`);
}
async function waitForAssistantMarker(sessionID, marker, afterCount, label) {
  return await waitFor(async () => {
    const tail = await apiJSON('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/messages?tail=true&limit=200`, token, undefined, `messages.tail.${label}`);
    const assistants = (tail.messages || []).filter(m => m.role === 'assistant');
    const hit = assistants.find(m => String(m.content || '').includes(marker));
    if (hit && assistants.length >= afterCount) return { tail, assistants, hit };
    return null;
  }, phaseTimeoutMs, `assistant marker ${marker}`);
}
async function waitTranscriptContains(needles, label, timeoutMs = 30000) {
  return await waitFor(async () => {
    let raw = '';
    try { raw = fs.readFileSync(tuiRawPath, 'utf8'); } catch { return null; }
    const clean = stripAnsi(raw);
    fs.writeFileSync(tuiCleanPath, clean);
    const ok = needles.every(n => clean.includes(n) || raw.includes(n));
    return ok ? clean : null;
  }, timeoutMs, `TUI transcript ${label}`, 500);
}
async function waitTranscriptMatches(regexes, label, timeoutMs = 30000) {
  return await waitFor(async () => {
    let raw = '';
    try { raw = fs.readFileSync(tuiRawPath, 'utf8'); } catch { return null; }
    const clean = stripAnsi(raw);
    fs.writeFileSync(tuiCleanPath, clean);
    const ok = regexes.every(re => re.test(clean) || re.test(raw));
    return ok ? clean : null;
  }, timeoutMs, `TUI transcript ${label}`, 250);
}
function feedPTY(text, label) {
  if (!tuiProc || tuiProc.exitCode !== null) throw new Error(`cannot feed TUI ${label}: process already exited`);
  logPhase('pty.feed', { label, bytes: Buffer.byteLength(text) });
  tuiProc.stdin.write(text);
  appendLine(feedLogPath, label);
}
async function stopTUI(reason = 'stop') {
  if (!tuiProc || tuiProc.exitCode !== null) return;
  logPhase('tui.stop.begin', { reason });
  appendLine(feedLogPath, reason);
  try { tuiProc.stdin.write('/quit\r'); } catch {}
  const exitDeadline = Date.now() + boundedTimeoutMs(1000);
  while (Date.now() < exitDeadline && tuiProc.exitCode === null) await sleep(100);
  if (tuiProc.exitCode === null) { try { tuiProc.kill('TERM'); } catch {} }
  await sleep(250);
  if (tuiProc.exitCode === null) { try { tuiProc.kill('KILL'); } catch {} }
  logPhase('tui.stop.end', { reason, exit_code: tuiProc.exitCode, signal_code: tuiProc.signalCode, killed: tuiProc.killed });
}
function terminalEvent(type) { return ['session.assistant.completed', 'session.assistant.failed', 'session.run.completed', 'session.run.failed', 'session.run.cancelled', 'session.run.expired', 'session.run.interrupted'].includes(type); }
function parseSnapshotTimeline(raw) {
  const lines = String(raw || '').replaceAll('\r\n', '\n').split('\n');
  const start = lines.findIndex(line => line.startsWith('timeline_messages:'));
  if (start < 0) return [];
  const items = [];
  let current = null;
  for (const line of lines.slice(start + 1)) {
    const header = line.match(/^(\d+)\. \[[^\]]+\] (\S+)\s*$/);
    if (header) {
      if (current) items.push(current);
      current = { index: Number(header[1]), role: header[2], lines: [] };
      continue;
    }
    if (current && /^\s{3}/.test(line)) current.lines.push(line.trim());
  }
  if (current) items.push(current);
  return items.map(item => ({ ...item, text: item.lines.join('\n').trim() }));
}
function expectedTools(raw) {
  return String(raw || '').split(',').map(value => value.trim()).filter(Boolean).map(value => {
    const colon = value.indexOf(':');
    return colon < 0 ? { tool: value, target: '' } : { tool: value.slice(0, colon).trim(), target: value.slice(colon + 1).trim() };
  });
}
function toolEventTarget(event) {
  const payload = payloadObject(event) || event?.payload || {};
  let args = payload.arguments || {};
  if (typeof args === 'string') { try { args = JSON.parse(args); } catch { args = { raw: args }; } }
  return [args.path, args.query, args.command, args.raw].filter(value => value !== undefined && value !== null && String(value) !== '').map(String).join(' ');
}

let token = '', sessionID = cfg.sessionID || '', sessionSearch = cfg.sessionID || '', launchedViaTUI = Boolean(cfg.launchCommand), launchedTaskViaTUI = /^\/task(?:\s|$)/i.test(cfg.launchCommand || ''), realtime = null, tuiProc = null;
const realtimeEventTypes = [];
const seen = { hello: false, replayStarted: false, replayComplete: false, subscribed: false, userMessages: 0, assistantStarted: 0, assistantDelta: 0, assistantCompleted: 0, assistantMessageOnRealtime: 0, terminalEvents: 0, cursorErrors: [], runStatuses: [], tuiLifecycleText: false, tuiTimerText: false, tuiSwarmingText: false };
const hardStop = setTimeout(() => {
  const error = `overall timeout after ${Math.round(overallTimeoutMs / 1000)}s`;
  try { if (tuiProc && tuiProc.exitCode === null) tuiProc.kill('KILL'); } catch {}
  try { realtime?.close(); } catch {}
  try { writeEvidenceSummary(false, error); } catch {}
  emit({ stage: 'error', error, session_id: sessionID });
  process.exit(124);
}, overallTimeoutMs);
try {
  beginPhase('discover.remote.checkout');
  cfg.remoteDir = await discoverRemoteDir();
  emit({ stage: 'remote.checkout', remote_dir: cfg.remoteDir });
  endPhase({ remote_dir: cfg.remoteDir });

  beginPhase('auth.desktop.session');
  const boot = await apiJSON('GET', '/v1/auth/desktop/session', '', undefined, 'auth.desktop.session');
  token = String(boot.token || '');
  if (!token) throw new Error('auth did not return desktop token');
  emit({ stage: 'auth.ok', token_len: token.length, user_id: boot.user_id, username: boot.username });
  endPhase({ token_len: token.length, user_id: boot.user_id, username: boot.username });

  if (!sessionID && !launchedViaTUI) {
    beginPhase('session.create');
    const topology = await apiJSON('GET', '/v1/swarm/topology', token, undefined, 'topology.snapshot');
    const runtime = (topology.runtimes || []).find(item => item.relationship === 'self') || (topology.runtimes || [])[0];
    const normalizedRemoteDir = path.resolve(cfg.remoteDir);
    const bindings = topology.workspace_bindings || [];
    const binding = bindings.find(item => item.state === 'bound' && [item.source_workspace_path, item.destination_workspace_path].some(value => value && path.resolve(String(value)) === normalizedRemoteDir));
    if (!runtime?.swarm_id || !binding?.workspace_binding_id) throw new Error(`missing self runtime or bound workspace authority for ${cfg.remoteDir}`);
    const suffix = `${Date.now()}-${crypto.randomBytes(4).toString('hex')}`;
    const title = `TUI PTY E2E ${suffix}`;
    const created = await apiJSON('POST', '/v3/sessions', token, { client_request_id: `tui-e2e-create:${suffix}`, title, workspace_path: binding.source_workspace_path, workspace_name: binding.source_workspace_name || 'swarm-go', workspace_binding_id: binding.workspace_binding_id, swarm_id: runtime.swarm_id, target_kind: 'host', target_relationship: 'self', mode: 'auto', agent_name: cfg.agentName, preference: { provider: cfg.provider, model: cfg.model, thinking: cfg.thinking }, metadata: { testbench_tui_pty_e2e: suffix } }, 'sessions.v3.create');
    sessionSearch = suffix;
    sessionID = created.session?.id;
    if (!sessionID) throw new Error('create did not return session.id');
    emit({ stage: 'session.created', session_id: sessionID });
    endPhase({ session_id: sessionID });
  } else if (sessionID) {
    beginPhase('session.provided', { session_id: sessionID });
    emit({ stage: 'session.provided', session_id: sessionID });
    endPhase({ session_id: sessionID });
  } else {
    beginPhase('session.deferred.to.tui', { launch_command: cfg.launchCommand });
    emit({ stage: 'session.deferred.to.tui', launch_command: cfg.launchCommand });
    endPhase({ launch_command: cfg.launchCommand });
  }

  if (sessionID) {
    beginPhase('realtime.open');
  realtime = await openRealtime(token, (_frame, summary) => {
    emit({ stage: 'realtime.frame', ...summary });
    if (summary.kind === 'hello') seen.hello = true;
    if (summary.kind === 'replay.started') seen.replayStarted = true;
    if (summary.kind === 'replay.complete') seen.replayComplete = true;
    if (summary.kind === 'cursor.error') seen.cursorErrors.push(summary.error || summary.error_code || 'cursor.error');
    const eventType = summary.event_type || '';
    if (eventType) realtimeEventTypes.push(eventType);
    if (eventType === 'session.message.appended') seen.userMessages += 1;
    if (eventType === 'session.assistant.started') seen.assistantStarted += 1;
    if (eventType === 'session.assistant.delta') seen.assistantDelta += 1;
    if (eventType === 'session.assistant.completed') seen.assistantCompleted += 1;
    if (summary.message_role === 'assistant' && summary.message_len > 0) seen.assistantMessageOnRealtime += 1;
    if (summary.run_status) seen.runStatuses.push(summary.run_status);
    if (terminalEvent(eventType)) seen.terminalEvents += 1;
  });
  await sleep(100);
  const subscribe = { protocol: 'v3.realtime', protocol_version: 1, kind: 'subscribe.session', session_id: sessionID, subscription_id: `tui-e2e-${sessionID}` };
  appendJSON(framesPath, { direction: 'client', frame: subscribe });
  realtime.send(subscribe);
    seen.subscribed = true;
    endPhase({ subscription_id: subscribe.subscription_id });
  }

  beginPhase('tui.launch.prepare');
  fs.mkdirSync(helperBinDir, { recursive: true });
  fs.writeFileSync(path.join(helperBinDir, 'wl-copy'), '#!/usr/bin/env bash\nset -euo pipefail\ncat > "${SWARM_E2E_CLIPBOARD_CAPTURE:?}"\n', { mode: 0o755 });
  const envLines = [
    '#!/usr/bin/env bash',
    'set -euo pipefail',
    `test -x ${JSON.stringify(cfg.tuiBin)} || { echo "missing executable TUI binary: ${cfg.tuiBin}" >&2; exit 127; }`,
    `cd ${JSON.stringify(cfg.remoteDir)}`,
    `export SWARMD_URL=${JSON.stringify(cfg.apiURL)}`,
    `export SWARMD_TOKEN=${JSON.stringify(token)}`,
    `export SWARM_E2E_CLIPBOARD_CAPTURE=${JSON.stringify(clipboardCapturePath)}`,
    `export PATH=${JSON.stringify(helperBinDir)}:"$PATH"`,
    'export TERM=xterm-256color',
    'export SWARM_LANE=dev',
    'export SWARM_ROOT="$PWD"',
    `exec ${JSON.stringify(cfg.tuiBin)}`,
  ];
  const runnerPath = path.join(artifactDir, 'run-real-tui.sh');
  fs.writeFileSync(runnerPath, envLines.join('\n') + '\n', { mode: 0o755 });
  endPhase({ runner_path: runnerPath, tui_bin: cfg.tuiBin });

  beginPhase('tui.launch');
  const { spawn } = await import('node:child_process');
  tuiProc = spawn('script', ['-q', '-f', '-e', '-c', runnerPath, tuiRawPath], { cwd: cfg.remoteDir, stdio: ['pipe', 'pipe', 'pipe'] });
  tuiProc.stdout.pipe(fs.createWriteStream(tuiStdoutPath));
  tuiProc.stderr.pipe(fs.createWriteStream(tuiStderrPath));
  tuiProc.on('exit', (code, signal) => {
    logPhase('tui.exit', { code, signal });
    emit({ stage: 'tui.exit', code, signal });
  });
  appendLine(feedLogPath, `started real TUI; will open session ${sessionID}`);
  endPhase({ pid: tuiProc.pid });

  beginPhase('tui.wait.startup');
  await waitTranscriptMatches([/swarm chat|mode|workspace|sessions|chat/i], 'startup', phaseTimeoutMs);
  endPhase({ transcript_tail: fileTail(tuiCleanPath, 2000) });

  if (launchedViaTUI) {
    beginPhase('tui.launch.session');
    feedPTY(`${cfg.launchCommand}\r`, `sent launch command: ${cfg.launchCommand}`);
    const created = await waitFor(async () => {
      const response = await apiJSON('POST', '/v3/sync/bootstrap', token, { surface: 'tui', selector: { kind: 'recent', global: true, recent: { limit: 100 } }, history: { mode: 'none' }, resources: { current_run_state: true, active_plan: true }, include_active: true }, 'sync.bootstrap.launched');
      const sessions = response.sessions_by_id || {};
      for (const id of response.session_order || []) {
        const session = sessions[id] || {};
        const tail = await apiJSON('GET', `/v3/sessions/${encodeURIComponent(id)}/messages?tail=true&limit=20`, token, undefined, `messages.tail.discover.${id}`);
        if ((tail.messages || []).some(message => message.role === 'user' && String(message.content || '').includes(cfg.firstMarker))) return { id, session };
      }
      return null;
    }, phaseTimeoutMs, `launched TUI session ${cfg.firstMarker}`, 500);
    sessionID = created.id;
    if (String(created.session.mode || '').toLowerCase() !== cfg.expectedMode) throw new Error(`launched TUI session mode=${created.session.mode}, want ${cfg.expectedMode}`);
    if (Boolean(created.session.worktree_enabled) !== cfg.expectedWorktree) throw new Error(`launched TUI session worktree=${Boolean(created.session.worktree_enabled)}, want ${cfg.expectedWorktree}`);
    if (launchedTaskViaTUI) {
      const hydrated = await apiJSON('GET', `/v3/sessions/${encodeURIComponent(sessionID)}`, token, undefined, 'session.hydrate.launched.task');
      created.session = { ...created.session, ...(hydrated.session || {}), metadata: { ...(created.session.metadata || {}), ...(hydrated.session?.metadata || {}) } };
      if (created.session.metadata?.plan_mode_requested !== (cfg.expectedMode === 'plan')) throw new Error('launched TUI task persisted the wrong plan-mode intent');
    }
    const settled = await waitForAssistantMarker(sessionID, cfg.firstMarker, 1, 'after.launch');
    if (launchedTaskViaTUI) {
      sessionSearch = String(created.session.title || '').trim();
      if (!sessionSearch) throw new Error('launched TUI task has no visible title');
      feedPTY(`/sessions ${sessionSearch}\r`, `open launched task session ${sessionID}`);
      await waitTranscriptMatches([/Sessions \(0\/\d+\)|Sessions \(1\/\d+\)/], 'task sessions modal', phaseTimeoutMs);
      feedPTY('\x1b[C\x1b[C', 'switch task sessions modal to chats filter');
      await waitTranscriptMatches([/Sessions \(1\/\d+\)/], 'launched task search result', phaseTimeoutMs);
      feedPTY('\r', `confirm launched task session ${sessionID}`);
      await waitTranscriptMatches([new RegExp(sessionSearch.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), /ctx\s*100%|chat|mode/i], 'opened launched task session', phaseTimeoutMs);
    }
    await waitTranscriptContains([cfg.firstMarker], 'launch assistant marker', phaseTimeoutMs);
    endPhase({ session_id: sessionID, mode: created.session.mode, worktree: Boolean(created.session.worktree_enabled), assistant_count: settled.assistants.length, transcript_tail: fileTail(tuiCleanPath, 2000) });
  } else {
    beginPhase('tui.open.session');
    feedPTY(`/sessions ${sessionSearch}\r`, `open sessions modal for ${sessionID}`);
    await waitTranscriptMatches([/Sessions \(0\/\d+\)|Sessions \(1\/\d+\)/], 'sessions modal', phaseTimeoutMs);
    feedPTY('\x1b[C\x1b[C', 'switch sessions modal to chats filter');
    await waitTranscriptMatches([/Sessions \(1\/\d+\)/], 'target session search result', phaseTimeoutMs);
    feedPTY('\r', `confirm session ${sessionID}`);
    await waitTranscriptMatches([new RegExp(sessionSearch), /ctx\s*100%|chat|No messages yet|Type a prompt|mode/i], 'opened target session', phaseTimeoutMs);
    endPhase({ session_id: sessionID, transcript_tail: fileTail(tuiCleanPath, 2000) });
  }

  beginPhase('turn.first');
  if (!launchedViaTUI) feedPTY(`${cfg.prompt}\r`, `sent first prompt: ${cfg.prompt}`);
  await waitTranscriptContains([cfg.prompt], 'first prompt echo', Math.min(3000, phaseTimeoutMs)).catch(() => null);
  const first = launchedViaTUI
    ? await waitForAssistantMarker(sessionID, cfg.firstMarker, 1, 'after.first.launch')
    : await waitForAssistantMarker(sessionID, cfg.firstMarker, 1, 'after.first');
  await waitTranscriptContains([cfg.firstMarker], 'first assistant marker', phaseTimeoutMs);
  await waitTranscriptMatches([/(working|thinking|streaming response|winding up|running|completed)\s*(([0-9]+m)?[0-9]+s|[0-9]+:[0-9]{2})|working|thinking|streaming response|winding up/i], 'run lifecycle status', Math.min(5000, phaseTimeoutMs)).then(() => { seen.tuiLifecycleText = true; seen.tuiTimerText = true; }).catch(() => null);
  await waitTranscriptMatches([/Swarming|swarming|\[a:|swarm/i], 'swarming indicator', Math.min(5000, phaseTimeoutMs)).then(() => { seen.tuiSwarmingText = true; }).catch(() => null);
  if (!launchedViaTUI) await waitFor(() => seen.terminalEvents >= 1, phaseTimeoutMs, 'first turn terminal realtime event');
  endPhase({ assistant_marker: cfg.firstMarker, assistant_count: first.assistants.length, terminal_events: seen.terminalEvents, transcript_tail: fileTail(tuiCleanPath, 2000) });

  let second = null;
  if (!cfg.skipFollowUp) {
    beginPhase('turn.followup');
    feedPTY(`${cfg.followUp}\r`, `sent follow-up prompt: ${cfg.followUp}`);
    await waitTranscriptContains([cfg.followUp], 'follow-up prompt echo', Math.min(3000, phaseTimeoutMs)).catch(() => null);
    second = await waitForAssistantMarker(sessionID, cfg.followUpMarker, Math.max(2, first.assistants.length + 1), 'after.followup');
    await waitTranscriptContains([cfg.followUpMarker], 'follow-up assistant marker', phaseTimeoutMs);
    await waitFor(() => seen.terminalEvents >= 2, phaseTimeoutMs, 'follow-up terminal realtime event');
    endPhase({ assistant_marker: cfg.followUpMarker, assistant_count: second.assistants.length, terminal_events: seen.terminalEvents, transcript_tail: fileTail(tuiCleanPath, 2000) });
  }

  beginPhase('tui.snapshot.final');
  feedPTY('/copy\r', 'capture authoritative final chat snapshot');
  await waitFor(() => fs.existsSync(clipboardCapturePath) && fs.statSync(clipboardCapturePath).size > 0, phaseTimeoutMs, 'final chat snapshot capture');
  await waitFor(() => {
    const timeline = parseSnapshotTimeline(fs.readFileSync(clipboardCapturePath, 'utf8'));
    return timeline.some(item => item.text.includes(cfg.firstMarker)) && (cfg.skipFollowUp || timeline.some(item => item.text.includes(cfg.followUpMarker)));
  }, phaseTimeoutMs, 'final chat snapshot markers');
  endPhase({ chat_snapshot_tail: fileTail(clipboardCapturePath, 5000) });

  beginPhase('tui.shutdown');
  await stopTUI('done; sent /quit');
  endPhase();
  try { realtime?.close(); } catch {}

  let rawTranscript = '';
  try { rawTranscript = fs.readFileSync(tuiRawPath, 'utf8'); } catch {}
  const cleanTranscript = stripAnsi(rawTranscript);
  fs.writeFileSync(tuiCleanPath, cleanTranscript);
  if (!seen.tuiLifecycleText && /(working|thinking|streaming response|winding up|running|completed)\s*(([0-9]+m)?[0-9]+s|[0-9]+:[0-9]{2})/i.test(cleanTranscript)) { seen.tuiLifecycleText = true; seen.tuiTimerText = true; }
  if (!seen.tuiSwarmingText && /Swarming|swarming|\[a:|swarm/i.test(cleanTranscript)) seen.tuiSwarmingText = true;

  beginPhase('evidence.events');
  const events = await apiJSON('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/events?after_seq=0&limit=1000`, token, undefined, 'events.replay.after');
  const dbEvents = events.events || [];
  const dbEventTypes = dbEvents.map(e => e.event_type);
  const expected = expectedTools(cfg.expectedToolOrder);
  const startedTools = dbEvents.filter(e => e.event_type === 'session.tool.started');
  const completedTools = dbEvents.filter(e => e.event_type === 'session.tool.completed');
  const assistantCompleted = dbEvents.find(e => e.event_type === 'session.assistant.completed');
  const snapshotTimeline = parseSnapshotTimeline(fs.readFileSync(clipboardCapturePath, 'utf8'));
  const snapshotTools = snapshotTimeline.filter(item => item.role === 'tool');
  const firstUserIndex = snapshotTimeline.findIndex(item => item.role === 'user' && item.text.includes(launchedViaTUI ? cfg.firstMarker : cfg.prompt));
  const firstAssistantIndex = snapshotTimeline.findIndex(item => item.role === 'assistant' && item.text.includes(cfg.firstMarker));
  const followUpUserIndex = cfg.skipFollowUp ? -1 : snapshotTimeline.findIndex(item => item.role === 'user' && item.text.includes(cfg.followUp));
  const followUpAssistantIndex = cfg.skipFollowUp ? -1 : snapshotTimeline.findIndex(item => item.role === 'assistant' && item.text.includes(cfg.followUpMarker));
  const crossTurnTimelineOrderOK = cfg.skipFollowUp
    ? firstUserIndex >= 0 && firstAssistantIndex > firstUserIndex
    : firstUserIndex >= 0 && firstAssistantIndex > firstUserIndex && followUpUserIndex > firstAssistantIndex && followUpAssistantIndex > followUpUserIndex;
  const durableToolOrderOK = expected.length === 0 || Boolean(
    startedTools.length === expected.length && completedTools.length === expected.length &&
    expected.every((want, i) => {
      const startedPayload = payloadObject(startedTools[i]) || startedTools[i]?.payload || {};
      const completedPayload = payloadObject(completedTools[i]) || completedTools[i]?.payload || {};
      return String(startedPayload.tool_name || '') === want.tool && String(completedPayload.tool_name || '') === want.tool &&
        (!want.target || (toolEventTarget(startedTools[i]).includes(want.target) && toolEventTarget(completedTools[i]).includes(want.target))) &&
        startedPayload.tool_instance_id && startedPayload.tool_instance_id === completedPayload.tool_instance_id &&
        Number(startedTools[i].seq) < Number(completedTools[i].seq) &&
        (i === 0 || Number(completedTools[i - 1].seq) < Number(startedTools[i].seq));
    }) && Number(completedTools.at(-1)?.seq || 0) < Number(assistantCompleted?.seq || 0)
  );
  const snapshotToolOrderOK = expected.length === 0 || Boolean(
    snapshotTools.length === expected.length && expected.every((want, i) => {
      const text = snapshotTools[i]?.text || '';
      return text.toLowerCase().includes(want.tool.toLowerCase()) && (!want.target || text.includes(want.target));
    })
  );
  const requiredTurns = cfg.skipFollowUp ? 1 : 2;
  const followUpOK = cfg.skipFollowUp || Boolean((second?.hit?.content || '').includes(cfg.followUpMarker) && cleanTranscript.includes(cfg.followUpMarker));
  const lifecycleIndicatorsOK = expected.length > 0 || Boolean(seen.tuiLifecycleText && seen.tuiTimerText && seen.tuiSwarmingText);
  const pass = Boolean(
    sessionID && (launchedViaTUI || (seen.hello && seen.subscribed && seen.replayStarted && seen.replayComplete)) &&
    (launchedViaTUI || (seen.userMessages >= requiredTurns && seen.assistantStarted >= requiredTurns && seen.assistantCompleted >= requiredTurns && seen.assistantMessageOnRealtime >= requiredTurns && seen.cursorErrors.length === 0)) &&
    (first.hit?.content || '').includes(cfg.firstMarker) && cleanTranscript.includes(cfg.firstMarker) && followUpOK &&
    durableToolOrderOK && snapshotToolOrderOK && crossTurnTimelineOrderOK && lifecycleIndicatorsOK
  );
  const assistantPreview = second ? second.assistants : first.assistants;
  const summary = { ok: pass, primary_ssh: cfg.primarySSH, api_url: cfg.apiURL, remote_dir: cfg.remoteDir, session_id: sessionID, provider: cfg.provider, model: cfg.model, observed: seen, current_phase: currentPhase, last_phase_event: lastPhaseEvent, realtime_event_count: realtimeEventTypes.length, realtime_event_types: realtimeEventTypes, db_event_count: dbEvents.length, db_event_types: dbEventTypes, expected_tool_order: expected, durable_tool_order_ok: durableToolOrderOK, snapshot_tool_order_ok: snapshotToolOrderOK, cross_turn_timeline_order_ok: crossTurnTimelineOrderOK, snapshot_timeline: snapshotTimeline.map(item => ({ index: item.index, role: item.role, text: item.text.slice(0, 500) })), durable_started_tools: startedTools.map(e => ({ seq: e.seq, tool: (payloadObject(e) || e.payload || {}).tool_name, instance: (payloadObject(e) || e.payload || {}).tool_instance_id, target: toolEventTarget(e) })), durable_completed_tools: completedTools.map(e => ({ seq: e.seq, tool: (payloadObject(e) || e.payload || {}).tool_name, instance: (payloadObject(e) || e.payload || {}).tool_instance_id, target: toolEventTarget(e) })), snapshot_tools: snapshotTools.map(item => item.text), assistant_preview: assistantPreview.map(m => String(m.content || '').slice(0, 200)), artifacts: { transcript: tuiRawPath, clean_transcript: tuiCleanPath, chat_snapshot: clipboardCapturePath, feed_log: feedLogPath, frames: framesPath, requests: requestsPath, runner_events: runnerEventsPath, phase_log: phaseLogPath, summary: summaryPath } };
  fs.writeFileSync(summaryPath, JSON.stringify(summary, null, 2));
  endPhase({ pass, realtime_event_count: realtimeEventTypes.length, db_event_count: events.events?.length ?? 0 });
  emit({ stage: 'final.summary', ...summary, realtime_event_types: undefined, db_event_types: undefined });
  clearTimeout(hardStop);
  if (!pass) process.exitCode = 1;
  process.exit(process.exitCode || 0);
} catch (err) {
  clearTimeout(hardStop);
  await Promise.race([stopTUI('error cleanup'), sleep(1000)]);
  try { realtime?.close(); } catch {}
  const summary = writeEvidenceSummary(false, err instanceof Error ? err.message : String(err));
  emit({ stage: 'error', error: summary.error, session_id: sessionID });
  process.exit(1);
}
NODE

CONFIG_LOCAL="${ARTIFACT_DIR}/config.json"
python3 - "$CONFIG_LOCAL" "$PRIMARY_SSH" "$API_URL" "$REMOTE_DIR" "$SESSION_ID" "$PROMPT" "$FIRST_MARKER" "$FOLLOW_UP" "$FOLLOW_UP_MARKER" "$SKIP_FOLLOW_UP" "$LAUNCH_COMMAND" "$EXPECTED_MODE" "$EXPECTED_WORKTREE" "$EXPECTED_TOOL_ORDER" "$PROVIDER" "$MODEL" "$THINKING" "$AGENT_NAME" "$TIMEOUT_SECONDS" "$OVERALL_TIMEOUT_SECONDS" "$TUI_BIN" <<'PY'
import json, sys
path, primary, api, remote_dir, session_id, prompt, first_marker, follow_up, follow_up_marker, skip_follow_up, launch_command, expected_mode, expected_worktree, expected_tool_order, provider, model, thinking, agent, timeout, overall_timeout, tui_bin = sys.argv[1:]
with open(path, 'w', encoding='utf-8') as f:
    json.dump({
        'primarySSH': primary,
        'apiURL': api,
        'remoteDir': remote_dir,
        'sessionID': session_id,
        'prompt': prompt,
        'firstMarker': first_marker,
        'followUp': follow_up,
        'followUpMarker': follow_up_marker,
        'skipFollowUp': skip_follow_up == 'true',
        'launchCommand': launch_command,
        'expectedMode': expected_mode,
        'expectedWorktree': expected_worktree == 'true',
        'expectedToolOrder': expected_tool_order,
        'provider': provider,
        'model': model,
        'thinking': thinking,
        'agentName': agent,
        'timeoutSeconds': int(timeout),
        'overallTimeoutSeconds': int(overall_timeout),
        'tuiBin': tui_bin,
        'artifactDir': '',
    }, f, indent=2)
PY

log "primary=${PRIMARY_SSH} api=${API_URL} tui_bin=${TUI_BIN} phase_timeout=${TIMEOUT_SECONDS}s overall_timeout=${OVERALL_TIMEOUT_SECONDS}s run_timeout=${RUN_TIMEOUT_SECONDS}s artifacts=${ARTIFACT_DIR}"
if [[ -z "${REMOTE_WORK_DIR}" ]]; then
  REMOTE_WORK_DIR="$(timeout -k 1s 5s ssh "${SSH_OPTS[@]}" "${PRIMARY_SSH}" "mktemp -d")" || fail "failed to create remote work dir"
else
  timeout -k 1s 5s ssh "${SSH_OPTS[@]}" "${PRIMARY_SSH}" "set -euo pipefail; rm -rf -- $(printf '%q' "${REMOTE_WORK_DIR}"); mkdir -p -- $(printf '%q' "${REMOTE_WORK_DIR}")"
fi
[[ -n "${REMOTE_WORK_DIR}" ]] || fail "empty remote work dir"
timeout -k 1s 5s scp -q "${SSH_OPTS[@]}" "${RUNNER_LOCAL}" "${CONFIG_LOCAL}" "${PRIMARY_SSH}:${REMOTE_WORK_DIR}/"

remote_status=0
timeout -k 1s "${RUN_TIMEOUT_SECONDS}s" ssh "${SSH_OPTS[@]}" "${PRIMARY_SSH}" 'bash -s' -- "${REMOTE_WORK_DIR}" <<'REMOTE' >"${REMOTE_STDOUT}" 2>"${REMOTE_STDERR}" || remote_status=$?
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
SWARM_E2E_CONFIG="${remote_dir}/config.json" node "${remote_dir}/remote-tui-runner.mjs"
REMOTE

mkdir -p -- "${ARTIFACT_DIR}/remote-artifacts"
timeout -k 2s 60s scp -q -r "${SSH_OPTS[@]}" "${PRIMARY_SSH}:${REMOTE_WORK_DIR}/artifacts/." "${ARTIFACT_DIR}/remote-artifacts/" 2>"${ARTIFACT_DIR}/scp-artifacts.stderr" || true
if [[ -f "${ARTIFACT_DIR}/remote-artifacts/summary.json" ]]; then
  cp -- "${ARTIFACT_DIR}/remote-artifacts/summary.json" "${SUMMARY_JSON}"
fi
if [[ "${KEEP_REMOTE}" != "true" ]]; then
  ssh "${SSH_OPTS[@]}" -f "${PRIMARY_SSH}" "rm -rf -- $(printf '%q' "${REMOTE_WORK_DIR}")" >/dev/null 2>&1 || true
fi

if [[ "${remote_status}" != "0" ]]; then
  log "FAILED (remote status ${remote_status})"
  log "stdout: ${REMOTE_STDOUT}"
  log "stderr: ${REMOTE_STDERR}"
  [[ -s "${REMOTE_STDERR}" ]] && sed -n '1,160p' "${REMOTE_STDERR}" >&2 || true
  [[ -f "${ARTIFACT_DIR}/remote-artifacts/phase.log" ]] && { echo '--- remote phase.log ---' >&2; tail -n 80 "${ARTIFACT_DIR}/remote-artifacts/phase.log" >&2; } || true
  [[ -f "${ARTIFACT_DIR}/remote-artifacts/runner-events.ndjson" ]] && { echo '--- remote runner-events.ndjson ---' >&2; tail -n 80 "${ARTIFACT_DIR}/remote-artifacts/runner-events.ndjson" >&2; } || true
  [[ -f "${ARTIFACT_DIR}/remote-artifacts/script.stderr" ]] && { echo '--- remote script.stderr ---' >&2; tail -n 80 "${ARTIFACT_DIR}/remote-artifacts/script.stderr" >&2; } || true
  [[ -f "${ARTIFACT_DIR}/remote-artifacts/tui.cleaned.txt" ]] && { echo '--- remote tui.cleaned.txt tail ---' >&2; tail -n 80 "${ARTIFACT_DIR}/remote-artifacts/tui.cleaned.txt" >&2; } || true
  [[ -f "${SUMMARY_JSON}" ]] && jq . "${SUMMARY_JSON}" >&2 || true
  exit "${remote_status}"
fi
[[ -f "${SUMMARY_JSON}" ]] || fail "remote run succeeded but summary was not copied back"
jq '{ok, primary_ssh, api_url, remote_dir, session_id, provider, model, observed, realtime_event_count, db_event_count, expected_tool_order, durable_tool_order_ok, snapshot_tool_order_ok, cross_turn_timeline_order_ok, snapshot_timeline, durable_started_tools, durable_completed_tools, snapshot_tools, assistant_preview, artifacts}' "${SUMMARY_JSON}"
log "PASS"
log "transcript: ${ARTIFACT_DIR}/remote-artifacts/tui.cleaned.txt"
log "frames: ${ARTIFACT_DIR}/remote-artifacts/realtime-frames.ndjson"
log "requests: ${ARTIFACT_DIR}/remote-artifacts/http-requests.ndjson"
