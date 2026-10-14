#!/usr/bin/env node
// AI path e2e: proves an AI can use a fresh Swarm through the paths Swarm
// ships, against a real daemon and a real model (no mocks):
//   - MCP: `swarmctl mcp --socket <owner socket>` over stdio (what
//     `swarm mcp` runs), driven the way plugins/swarm/skills/* teach;
//   - the npm SDK (@swarm-agent/sdk from its npm pack) with its default
//     connection rules (SWARM_SOCKET_PATH, no token).
//
// Every run starts its own swarmd in <work-dir>/sw-<scenario>-<random>/ with
// private state, cache, runtime, config, log and home directories, its own
// free loopback ports and no Desktop listener, so several scenarios can run at
// once on one machine. The daemon and everything it started are stopped (as a
// process group) at the end, on timeout and on SIGTERM/SIGINT; the run
// directory is then removed (keep it with SWARM_E2E_KEEP_RUN_DIR=1).
//
// Binaries: --bin-dir holds swarmd, swarmctl and libfff_c.so. In the release
// layout built by scripts/build-main-dist.sh that is
// <output>/linux-amd64/swarmd/ (inside the archive:
// swarm-<version>-linux-amd64/linux-amd64/swarmd/); LD_LIBRARY_PATH is set to it.
//
// The API key (--key-file) is read into memory only and goes to the test
// daemon only, through the one-time provider key page exactly as the person's
// browser posts it. It is never printed, logged or written; receipts and logs
// are redacted. Run `node scripts/e2e/ai-path.mjs --help` for the CLI.

import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { parseArgs, USAGE, UsageError } from './lib/args.mjs'
import { Daemon, baseEnv, createRunDir, removeRunDir } from './lib/daemon.mjs'
import { KEY_LINK_PATTERN, openKeyPage, submitKeyPage } from './lib/key-page.mjs'
import { McpClient } from './lib/mcp-client.mjs'
import { Receipt, redact } from './lib/receipt.mjs'

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = path.resolve(SCRIPT_DIR, '..', '..')
const PROVIDER = 'google'
const OWNER_NAME = 'e2e-owner'
const SWARM_NAME = 'AI path e2e'
const WORKSPACE_NAME = 'e2e-ws'
const MODEL_SLOTS = [['swarm', 'action'], ['swarm', 'plan'], ['system_agents', 'compact'], ['system_agents', 'finder'], ['system_agents', 'coder'], ['system_agents', 'designer'], ['system_agents', 'router']]
const REQUIRED_TOOLS = ['swarm_list_providers', 'swarm_request_provider_key', 'swarm_list_models', 'swarm_create_workspace', 'swarm_list_workspaces', 'swarm_start_session', 'swarm_send_message', 'swarm_get_session', 'swarm_resolve_permission', 'swarm_create_project', 'swarm_list_tasks', 'swarm_approve_task', 'swarm_get_changes', 'swarm_read_file', 'swarm_integrate_task', 'swarm_accept_review']

class StepError extends Error {}
class DeadlineError extends Error {}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

// ---- context ---------------------------------------------------------------

function createContext(opts) {
  const receipt = new Receipt(opts.scenario)
  const ctx = {
    opts,
    receipt,
    step: 'start',
    deadline: Date.now() + opts.timeoutMs,
    apiKey: null,
    daemon: null,
    mcp: null,
    runDir: null,
    live: false,
    log(message) {
      process.stderr.write(`[ai-path ${opts.scenario}] ${redact(message, receipt.secrets)}\n`)
    },
    check(name, ok, detail) {
      const passed = receipt.check(name, ok, detail)
      ctx.log(`${passed ? 'ok  ' : 'FAIL'} ${name}${detail ? `: ${redact(typeof detail === 'string' ? detail : JSON.stringify(detail), receipt.secrets).slice(0, 300)}` : ''}`)
      return passed
    },
    // require records the assertion and stops the scenario when it fails.
    require(name, ok, detail) {
      if (!ctx.check(name, ok, detail)) throw new StepError(`${name} failed`)
    },
    remainingMs() {
      return ctx.deadline - Date.now()
    },
  }
  return ctx
}

function enter(ctx, step) {
  ctx.step = step
  if (ctx.remainingMs() <= 0) throw new DeadlineError(`deadline reached before ${step}`)
}

// ---- processes ---------------------------------------------------------------

// Helper processes still running (npm, swarmctl, the SDK app); cleanup kills
// their process groups before the run directory is removed.
const CHILDREN = new Set()

function killChildren() {
  const count = CHILDREN.size
  for (const pid of CHILDREN) {
    try {
      process.kill(-pid, 'SIGKILL')
    } catch {}
  }
  CHILDREN.clear()
  return count
}

function runCommand(command, args, { env, cwd, timeoutMs = 120000, shell = false } = {}) {
  return new Promise((resolve) => {
    const child = spawn(command, args, { env, cwd, shell, stdio: ['ignore', 'pipe', 'pipe'], detached: true })
    if (child.pid) CHILDREN.add(child.pid)
    let stdout = ''
    let stderr = ''
    child.stdout.on('data', (c) => {
      stdout = (stdout + c.toString('utf8')).slice(-65536)
    })
    child.stderr.on('data', (c) => {
      stderr = (stderr + c.toString('utf8')).slice(-8192)
    })
    const timer = setTimeout(() => {
      try {
        process.kill(-child.pid, 'SIGKILL')
      } catch {}
    }, timeoutMs)
    child.on('error', (err) => {
      clearTimeout(timer)
      resolve({ code: null, stdout, stderr: `${stderr}${err.message}`, timedOut: false })
    })
    child.on('close', (code, signal) => {
      clearTimeout(timer)
      CHILDREN.delete(child.pid)
      resolve({ code, signal, stdout, stderr, timedOut: signal === 'SIGKILL' })
    })
  })
}

function swarmctl(ctx, args, timeoutMs = 60000) {
  return runCommand(path.join(ctx.opts.binDir, 'swarmctl'), args, {
    env: baseEnv(ctx.runDir, ctx.opts.binDir),
    cwd: path.join(ctx.runDir, 'home'),
    timeoutMs,
  })
}

// ---- daemon and MCP ------------------------------------------------------------

async function startDaemon(ctx) {
  enter(ctx, 'daemon.start')
  ctx.runDir = createRunDir(ctx.opts.workDir, ctx.opts.scenario)
  ctx.log(`run dir ${ctx.runDir}`)
  ctx.daemon = new Daemon({ binDir: ctx.opts.binDir, runDir: ctx.runDir, log: ctx.log })
  let lastError = null
  for (let attempt = 1; attempt <= 3; attempt++) {
    try {
      await ctx.daemon.start({ readyTimeoutMs: Math.min(90000, ctx.remainingMs()) })
      lastError = null
      break
    } catch (err) {
      lastError = err
      await ctx.daemon.stop()
      // A port taken between choosing and binding is retried on new ports.
      if (!/address already in use/i.test(err.message)) break
    }
  }
  let detail = lastError ? lastError.message : `ready on loopback port ${ctx.daemon.port}, owner socket present`
  if (lastError && /forbidden root/.test(lastError.message)) {
    detail = `${detail} (swarmd treats the nearest ancestor holding a .git entry as a workspace and refuses state inside it: use a --work-dir with no .git above it)`
  }
  ctx.require('daemon.start', !lastError, detail)
}

async function connectMcp(ctx, label = 'mcp') {
  enter(ctx, `${label}.connect`)
  if (ctx.mcp) await ctx.mcp.close()
  const mcp = new McpClient({
    command: path.join(ctx.opts.binDir, 'swarmctl'),
    // The advanced session and task tools this test drives are listed only
    // with --all-tools since tools/list tells the worker story by default.
    args: ['mcp', '--all-tools', '--socket', ctx.daemon.socket],
    env: baseEnv(ctx.runDir, ctx.opts.binDir),
    cwd: path.join(ctx.runDir, 'home'),
    log: ctx.log,
  })
  ctx.mcp = mcp
  mcp.start()
  let init
  try {
    init = await mcp.initialize()
  } catch (err) {
    ctx.require(`${label}.initialize`, false, err.message)
  }
  ctx.receipt.daemonVersion = init?.serverInfo?.version || ctx.receipt.daemonVersion
  ctx.require(`${label}.initialize`, init?.serverInfo?.name === 'swarm-control', `server ${init?.serverInfo?.name} ${init?.serverInfo?.version}, protocol ${init?.protocolVersion}`)
  const tools = await mcp.listTools()
  const names = new Set(tools.map((t) => t?.name))
  const missing = REQUIRED_TOOLS.filter((name) => !names.has(name))
  ctx.require(`${label}.tools_list`, missing.length === 0, missing.length ? `missing tools: ${missing.join(', ')}` : `${tools.length} tools, all ${REQUIRED_TOOLS.length} used by this test present`)
}

// tool calls a Swarm Control tool and fails the step on a tool error.
async function tool(ctx, name, args = {}, assertion) {
  enter(ctx, `${ctx.step.split(':')[0]}:${name}`)
  const result = await ctx.mcp.call(name, args)
  if (result.isError) {
    ctx.require(assertion || `tool.${name}`, false, `tool error: ${result.text}`)
  }
  return result.data ?? {}
}

// ---- onboarding (owner socket for identity, MCP for the rest) -------------------

async function onboarding(ctx) {
  const res = await ctx.daemon.request('GET', '/v1/onboarding')
  if (res.status !== 200 || !res.body) throw new StepError(`GET /v1/onboarding returned HTTP ${res.status}`)
  return res.body
}

async function checkFreshState(ctx) {
  enter(ctx, 'onboard.fresh')
  const state = await onboarding(ctx)
  ctx.check('onboard.fresh_state', state.needs_onboarding === true && state.config?.desktop_onboarding_complete === false && state.identity?.bootstrapped === false,
    `needs_onboarding=${state.needs_onboarding} desktop_onboarding_complete=${state.config?.desktop_onboarding_complete} identity.bootstrapped=${state.identity?.bootstrapped}`)
  const status = await swarmctl(ctx, ['setup', 'status', '--socket', ctx.daemon.socket])
  ctx.check('onboard.setup_status', status.code === 0 && /identity=false onboarding_required=true credentials=0 workspaces=0/.test(status.stdout),
    status.code === 0 ? status.stdout.trim() : `swarmctl setup status exit ${status.code}: ${status.stderr.trim()}`)
}

// bootstrapIdentity names the owner through the call the SDK's
// onboarding.ensureBootstrapped makes (POST /v1/onboarding with username and
// swarm_name), over the owner socket.
async function bootstrapIdentity(ctx) {
  enter(ctx, 'onboard.identity')
  const res = await ctx.daemon.request('POST', '/v1/onboarding', { username: OWNER_NAME, swarm_name: SWARM_NAME })
  const identity = res.body?.identity
  ctx.require('onboard.identity_bootstrap', res.status === 200 && identity?.bootstrapped === true && identity?.username === OWNER_NAME,
    `HTTP ${res.status}; bootstrapped=${identity?.bootstrapped} username=${identity?.username}`)
  ctx.check('onboard.identity_not_complete', res.body?.config?.desktop_onboarding_complete === false,
    `desktop_onboarding_complete=${res.body?.config?.desktop_onboarding_complete} after bootstrap (identity alone must not finish onboarding)`)
  ctx.identity = identity
}

function providerEntry(data) {
  return (data?.providers ?? []).find((p) => p?.provider === PROVIDER || p?.id === PROVIDER)
}

async function connectProvider(ctx) {
  enter(ctx, 'key')
  const before = providerEntry(await tool(ctx, 'swarm_list_providers', {}))
  ctx.check('key.provider_listed', Boolean(before) && before.accepts_api_key === true && before.ready !== true,
    `${PROVIDER}: ${JSON.stringify(before ?? null)}`)

  const link = await tool(ctx, 'swarm_request_provider_key', { provider: PROVIDER, label: 'ai-path e2e' }, 'key.request_link')
  const match = KEY_LINK_PATTERN.exec(String(link.url ?? ''))
  ctx.require('key.request_link', Boolean(match) && Number(match?.[1]) === ctx.daemon.port && String(link.tell_the_person ?? '').includes(String(link.url)),
    match ? `one-time loopback key page on this daemon's port, expires in ${link.expires_in_seconds}s` : 'no loopback /keys/<code> link returned')
  ctx.receipt.addSecret(match[2])

  const page = await openKeyPage(link.url)
  ctx.check('key.page_open', page.status === 200 && page.hasForm && page.csp.includes("form-action 'self'"), `GET page: HTTP ${page.status}, form=${page.hasForm}`)

  if (ctx.apiKey) {
    enter(ctx, 'key.page_submit')
    const submitted = await submitKeyPage(link.url, ctx.apiKey)
    ctx.require('key.page_submit', submitted.saved, `POST form: HTTP ${submitted.status}: ${submitted.message}`)
    const after = providerEntry(await tool(ctx, 'swarm_list_providers', { provider: PROVIDER, wait_seconds: 30 }))
    ctx.require('key.provider_ready', after?.ready === true && after?.key_request?.status === 'saved',
      `ready=${after?.ready} key_request=${JSON.stringify(after?.key_request ?? null)} active=${JSON.stringify(after?.active ?? null)}`)
    return true
  }
  if (ctx.opts.loadKeyCmd) {
    enter(ctx, 'key.load_cmd')
    const command = ctx.opts.loadKeyCmd.split('{socket}').join(`'${ctx.daemon.socket.replace(/'/g, "'\\''")}'`)
    const loaded = await runCommand('/bin/sh', ['-c', command], { env: process.env, timeoutMs: 120000 })
    const ok = loaded.code === 0 && /ok['"]?\s*:\s*true/.test(loaded.stdout)
    // The loader's output is not echoed: only its outcome.
    ctx.require('key.load_cmd', ok, ok ? 'key loaded into this daemon by --load-key-cmd' : `--load-key-cmd failed (exit ${loaded.code}${/ok['"]?\s*:\s*false/.test(loaded.stdout) ? ', loader reported ok: false' : ''})`)
    ctx.check('key.page_submit_not_exercised', true, 'key page POST skipped: --load-key-cmd loaded the key directly (credential route), not through the page')
    const after = providerEntry(await tool(ctx, 'swarm_list_providers', { provider: PROVIDER }))
    ctx.require('key.provider_ready', after?.ready === true, `ready=${after?.ready} active=${JSON.stringify(after?.active ?? null)}`)
    return true
  }
  ctx.check('live.key', false, 'no --key-file or --load-key-cmd: the key page submit and every model step were not run')
  return false
}

async function agentModelSettings(ctx) {
  const res = await ctx.daemon.request('GET', '/v1/agent-model-settings')
  if (res.status !== 200) throw new StepError(`GET /v1/agent-model-settings returned HTTP ${res.status}`)
  return res.body?.agent_model_settings ?? {}
}

function slotSummary(settings) {
  return MODEL_SLOTS.map(([group, slot]) => {
    const a = settings?.[group]?.[slot] ?? {}
    return { slot, provider: a.provider ?? '', model: a.model ?? '', thinking: a.thinking ?? '' }
  })
}

async function checkFirstKeyModels(ctx) {
  enter(ctx, 'models.first_key')
  const slots = slotSummary(await agentModelSettings(ctx))
  const filled = slots.every((s) => s.provider === PROVIDER && s.model !== '' && s.thinking !== '')
  ctx.check('models.set_from_first_key', filled, `catalog chose ${slots.map((s) => `${s.slot}=${s.provider}/${s.model}:${s.thinking}`).join(' ')}`)
}

// setTestModel sets the requested model on the test daemon only, as the gate
// does (PATCH /v1/agent-model-settings), for Swarm action/plan and every
// system agent.
async function setTestModel(ctx) {
  enter(ctx, 'models.set_test_model')
  const choice = { provider: PROVIDER, model: ctx.opts.model, thinking: ctx.opts.thinking }
  // One assignment per PATCH, as `swarmctl setup model --role` sends it (the
  // route takes exactly one group, and one system-agent slot at a time).
  const refused = []
  for (const [group, slot] of MODEL_SLOTS) {
    const res = await ctx.daemon.request('PATCH', '/v1/agent-model-settings', { [group]: { [slot]: choice } })
    if (res.status !== 200) refused.push(`${group}.${slot}: HTTP ${res.status} ${res.body?.error ?? ''}`)
  }
  ctx.require('models.patch', refused.length === 0, refused.length ? `PATCH /v1/agent-model-settings refused ${refused.join('; ')}` : `${MODEL_SLOTS.length} PATCH /v1/agent-model-settings calls accepted`)
  const slots = slotSummary(await agentModelSettings(ctx))
  const wrong = slots.filter((s) => s.provider !== PROVIDER || s.model !== ctx.opts.model || s.thinking !== ctx.opts.thinking)
  ctx.require('models.test_model_set', wrong.length === 0, wrong.length ? `not set: ${wrong.map((s) => `${s.slot}=${s.model}:${s.thinking}`).join(' ')}` : `${PROVIDER}/${ctx.opts.model}:${ctx.opts.thinking} on all ${slots.length} roles`)
  const listed = await tool(ctx, 'swarm_list_models', {})
  const roles = listed.roles ?? []
  const mismatched = roles.filter((r) => r.model !== ctx.opts.model)
  ctx.check('models.list_models_roles', roles.length > 0 && mismatched.length === 0, `${roles.length} roles via swarm_list_models${mismatched.length ? `; different: ${mismatched.map((r) => r.role).join(', ')}` : ''}`)
}

async function createWorkspace(ctx) {
  enter(ctx, 'workspace.create')
  const created = await tool(ctx, 'swarm_create_workspace', { name: WORKSPACE_NAME }, 'workspace.create')
  const wsPath = String(created.workspace?.path ?? '')
  const inRun = wsPath.startsWith(fs.realpathSync(ctx.runDir) + path.sep)
  ctx.require('workspace.create', created.created === true && inRun && fs.existsSync(path.join(wsPath, '.git')),
    `created=${created.created}; git repo inside the run dir: ${inRun && fs.existsSync(path.join(wsPath, '.git'))}`)
  const listed = await tool(ctx, 'swarm_list_workspaces', {})
  ctx.require('workspace.listed', (listed.workspaces ?? []).some((w) => w.path === wsPath), `${(listed.workspaces ?? []).length} workspace(s) registered`)
  ctx.workspace = wsPath
}

// completeOnboarding finishes setup with the call the setup skill and
// headless setup use: `swarmctl setup complete` (checks identity, credential,
// workspace and all agent models, then sets desktop_onboarding_complete).
async function completeOnboarding(ctx) {
  enter(ctx, 'onboard.complete')
  const done = await swarmctl(ctx, ['setup', 'complete', '--socket', ctx.daemon.socket])
  ctx.require('onboard.setup_complete', done.code === 0, done.code === 0 ? done.stdout.trim() : `swarmctl setup complete exit ${done.code}: ${done.stderr.trim()}`)
  const state = await onboarding(ctx)
  ctx.require('onboard.complete_state', state.needs_onboarding === false && state.config?.desktop_onboarding_complete === true && state.identity?.bootstrapped === true,
    `needs_onboarding=${state.needs_onboarding} desktop_onboarding_complete=${state.config?.desktop_onboarding_complete} credentials=${state.heuristics?.credential_count} workspaces=${state.heuristics?.saved_workspace_count}`)
}

// onboard runs the whole first-run flow; it returns false when no key was
// given (the steps after the key link are then not run).
async function onboard(ctx, { fresh = false } = {}) {
  if (fresh) await checkFreshState(ctx)
  await bootstrapIdentity(ctx)
  await connectMcp(ctx)
  const keyed = await connectProvider(ctx)
  if (!keyed) {
    await createWorkspace(ctx)
    return false
  }
  await checkFirstKeyModels(ctx)
  await setTestModel(ctx)
  await createWorkspace(ctx)
  await completeOnboarding(ctx)
  ctx.live = true
  return true
}

// ---- sessions --------------------------------------------------------------------

// waitSettled waits (bounded by maxMs and the scenario deadline) until the
// session's run ends or waits on a decision, and says why the wait ended.
async function waitSettled(ctx, sessionId, maxMs) {
  const end = Math.min(ctx.deadline - 3000, Date.now() + maxMs)
  let state = null
  while (true) {
    const left = end - Date.now()
    if (left < 2000) return { reason: 'wait_bound_reached', state }
    const wait = Math.max(1, Math.min(50, Math.floor(left / 1000) - 1))
    const result = await ctx.mcp.call('swarm_get_session', { session_id: sessionId, messages: 30, detail_chars: 2000, wait_seconds: wait })
    if (result.isError) throw new StepError(`swarm_get_session: ${result.text}`)
    state = result.data
    if (state?.waited?.settled) return { reason: state.waited.reason, outcome: state.waited.outcome, state }
  }
}

// drive follows a run to its end, resolving each pending request with
// decide(permission) -> {action, reason, answer}. Returns why it stopped.
async function drive(ctx, sessionId, decide, { maxMs, maxDecisions = 12 } = {}) {
  const decisions = []
  const end = Date.now() + maxMs
  while (true) {
    const settled = await waitSettled(ctx, sessionId, end - Date.now())
    if (settled.reason !== 'waiting_on_decision') return { ...settled, decisions }
    const pending = settled.state?.pending_permissions ?? []
    if (pending.length === 0) continue
    if (decisions.length >= maxDecisions) return { reason: 'too_many_decisions', state: settled.state, decisions }
    const permission = pending[0]
    const decision = decide(permission)
    const args = { session_id: sessionId, permission_id: permission.id, action: decision.action }
    if (decision.reason) args.reason = decision.reason
    if (decision.answer) args.answer = decision.answer
    const resolved = await ctx.mcp.call('swarm_resolve_permission', args)
    if (resolved.isError) throw new StepError(`swarm_resolve_permission: ${resolved.text}`)
    decisions.push({ tool: permission.tool_name, kind: permission.kind, action: decision.action, status: resolved.data?.status, target: Boolean(decision.target) })
  }
}

function assistantTexts(state) {
  return (state?.messages ?? []).filter((m) => m?.role === 'assistant' && typeof m.content === 'string' && m.content.trim() !== '').map((m) => m.content.trim())
}

function toolMessages(state) {
  return (state?.messages ?? []).filter((m) => m?.tool).map((m) => m.tool)
}

// sessionHasFile lists the session's working folder (its worktree or
// workspace) through swarm_read_file; null when it cannot be listed.
async function sessionHasFile(ctx, sessionId, name) {
  const listed = await ctx.mcp.call('swarm_read_file', { session_id: sessionId, path: '.' })
  if (listed.isError) return null
  return (listed.data?.entries ?? []).some((e) => (typeof e === 'string' ? e : e?.name ?? e?.path) === name)
}

function lastAssistant(state) {
  const texts = assistantTexts(state)
  return texts[texts.length - 1] ?? ''
}

// ---- scenarios --------------------------------------------------------------------

async function scenarioOnboard(ctx) {
  await startDaemon(ctx)
  await onboard(ctx, { fresh: true })
}

async function startChat(ctx, prompt, title) {
  const started = await tool(ctx, 'swarm_start_session', { workspace_path: ctx.workspace, prompt, title, wait_seconds: Math.min(50, Math.max(1, Math.floor(ctx.remainingMs() / 1000) - 10)) }, 'chat.start_session')
  const sessionId = started.session?.id
  ctx.require('chat.start_session', typeof sessionId === 'string' && sessionId !== '', `session started in the workspace; model ${JSON.stringify(started.model ?? null)}`)
  return sessionId
}

async function firstReply(ctx, label) {
  const sessionId = await startChat(ctx, 'Reply with exactly one word: pong. Do not use any tools.', `e2e ${label}`)
  const run = await drive(ctx, sessionId, () => ({ action: 'deny_once', reason: 'No tools are needed for this reply.' }), { maxMs: 120000 })
  const reply = lastAssistant(run.state)
  ctx.require(`${label}.model_reply`, run.reason === 'run_ended' && run.outcome === 'finished' && reply !== '',
    `wait ended: ${run.reason}${run.outcome ? ` (${run.outcome})` : ''}; reply ${JSON.stringify(reply.slice(0, 80))}${run.state?.session?.error ? `; error ${run.state.session.error}` : ''}`)
  return { sessionId, reply }
}

async function scenarioChat(ctx) {
  await startDaemon(ctx)
  if (!(await onboard(ctx))) return
  enter(ctx, 'chat')
  const { sessionId } = await firstReply(ctx, 'chat')

  const token = `e2e${Math.random().toString(36).slice(2, 8)}`
  const command = `touch e2e-${token}.txt && echo ${token}`
  const isTarget = (p) => JSON.stringify(p?.tool_arguments ?? '').includes(token)

  enter(ctx, 'chat.deny')
  const askDeny = await tool(ctx, 'swarm_send_message', { session_id: sessionId, content: `Use your bash tool to run exactly this shell command in the workspace, nothing else: ${command}` }, 'chat.send_message')
  ctx.check('chat.send_message', askDeny.started?.accepted === true, 'follow-up accepted')
  const denied = await drive(ctx, sessionId, (p) => (isTarget(p)
    ? { action: 'deny_once', reason: 'Denied by the e2e test.', target: true }
    : p.kind === 'question' ? { action: 'allow_once', answer: `Run exactly: ${command}` } : { action: 'deny_once', reason: 'Only the exact command asked for may run.' }), { maxMs: 120000 })
  const deniedTarget = denied.decisions.filter((d) => d.target)
  // Tool records carry output and error (no status field): the command ran
  // when its output has the token on a line of its own; the file is the
  // second witness.
  // A bash tool record's output is the tool's JSON result; stdout is its
  // "output" field (a plain string is checked as is).
  const stdout = (t) => {
    const raw = String(t.output ?? '')
    try {
      const parsed = JSON.parse(raw)
      if (parsed && typeof parsed.output === 'string') return parsed.output
    } catch {}
    return raw
  }
  const echoed = (t) => new RegExp(`(^|\\n)${token}(\\n|$)`).test(stdout(t))
  const ranAnyway = toolMessages(denied.state).some(echoed)
  const fileAfterDeny = await sessionHasFile(ctx, sessionId, `e2e-${token}.txt`)
  ctx.check('chat.approval_requested', deniedTarget.length >= 1, `decisions: ${JSON.stringify(denied.decisions)}; wait ended: ${denied.reason}`)
  ctx.check('chat.denied_not_run', deniedTarget.length >= 1 && deniedTarget.every((d) => d.status === 'denied' || d.status === undefined || /den/i.test(String(d.status))) && !ranAnyway && fileAfterDeny === false && denied.reason === 'run_ended',
    `resolve status ${deniedTarget.map((d) => d.status).join(',') || 'none'}; command output seen: ${ranAnyway}; file created: ${fileAfterDeny}; run ${denied.outcome ?? denied.reason}`)

  enter(ctx, 'chat.allow')
  await tool(ctx, 'swarm_send_message', { session_id: sessionId, content: `Now run that same command again exactly once: ${command}` }, 'chat.send_message_again')
  const allowed = await drive(ctx, sessionId, (p) => (isTarget(p)
    ? { action: 'allow_once', reason: 'Allowed once by the e2e test.', target: true }
    : p.kind === 'question' ? { action: 'allow_once', answer: `Run exactly: ${command}` } : { action: 'deny_once', reason: 'Only the exact command asked for may run.' }), { maxMs: 120000 })
  const allowedTarget = allowed.decisions.filter((d) => d.target)
  const ran = toolMessages(allowed.state).filter((t) => JSON.stringify(t.arguments ?? '').includes(token))
  const ranOk = toolMessages(allowed.state).some(echoed)
  const fileAfterAllow = await sessionHasFile(ctx, sessionId, `e2e-${token}.txt`)
  ctx.check('chat.allowed_once_ran', allowedTarget.length >= 1 && ranOk && fileAfterAllow === true && allowed.reason === 'run_ended',
    `decisions: ${JSON.stringify(allowed.decisions)}; output echoed: ${ranOk}; file created: ${fileAfterAllow}; tool results ${JSON.stringify(ran.map((t) => ({ output: String(t.output ?? '').slice(0, 40), error: t.error })))}; run ${allowed.outcome ?? allowed.reason}`)
}

async function scenarioTasks(ctx) {
  await startDaemon(ctx)
  if (!(await onboard(ctx))) return
  enter(ctx, 'tasks.project')
  const line = 'hello from swarm e2e'
  const project = await tool(ctx, 'swarm_create_project', { name: 'e2e project', workspaces: [{ path: ctx.workspace }] }, 'tasks.create_project')
  const projectId = project.project?.id
  ctx.require('tasks.create_project', typeof projectId === 'string' && projectId !== '', 'project created with the workspace as primary code workspace')

  enter(ctx, 'tasks.orchestrator')
  const prompt = `Propose exactly one small code task in this project for the workspace ${ctx.workspace}: create a file named hello.txt at the repository root containing exactly this one line: ${line}. Then commit it. Propose it with auto-approval off; do not approve or start it yourself, and stop after proposing.`
  const started = await tool(ctx, 'swarm_start_session', { agent: 'system-orchestrator', project_id: projectId, prompt, title: 'e2e orchestrator', wait_seconds: 50 }, 'tasks.orchestrator_start')
  const orchestratorId = started.session?.id
  ctx.require('tasks.orchestrator_start', typeof orchestratorId === 'string', 'orchestrator session started for the project')
  const planned = await drive(ctx, orchestratorId, (p) => (p.kind === 'question'
    ? { action: 'allow_once', answer: 'Yes: one task, hello.txt with that one line, committed. Propose it now.' }
    : { action: 'allow_once', reason: 'Routine step of the requested proposal.' }), { maxMs: 180000 })
  ctx.check('tasks.orchestrator_run', planned.reason === 'run_ended' && planned.outcome === 'finished', `wait ended: ${planned.reason} ${planned.outcome ?? ''}; decisions ${JSON.stringify(planned.decisions)}; last reply ${JSON.stringify(lastAssistant(planned.state).slice(0, 120))}`)

  let tasks = (await tool(ctx, 'swarm_list_tasks', { project_id: projectId })).tasks ?? []
  const task = tasks.find((t) => t.status === 'pending_approval') ?? tasks[0]
  ctx.require('tasks.task_proposed', Boolean(task) && task.status === 'pending_approval', `${tasks.length} task(s): ${JSON.stringify(tasks.map((t) => ({ title: t.title, status: t.status, agent: t.agent })))}`)

  enter(ctx, 'tasks.approve')
  const approved = await tool(ctx, 'swarm_approve_task', { project_id: projectId, task_id: task.id, action: 'approve' }, 'tasks.approve')
  ctx.check('tasks.approve', true, `approved: ${JSON.stringify(approved.task?.status ?? approved.status ?? 'ok')}`)

  enter(ctx, 'tasks.run')
  let current = task
  let accepted = 0
  const runEnd = Math.min(ctx.deadline - 60000, Date.now() + 420000)
  while (Date.now() < runEnd) {
    tasks = (await tool(ctx, 'swarm_list_tasks', { project_id: projectId })).tasks ?? []
    current = tasks.find((t) => t.id === task.id) ?? current
    if (['needs_review', 'completed', 'failed', 'cancelled', 'rejected', 'blocked'].includes(current.status)) break
    if (!current.session_id) {
      await sleep(3000)
      continue
    }
    const run = await drive(ctx, current.session_id, (p) => (p.kind === 'question'
      ? { action: 'allow_once', answer: `Create hello.txt containing exactly: ${line} and commit it.` }
      : { action: 'allow_once', reason: 'Routine step of the approved task.' }), { maxMs: Math.max(5000, Math.min(60000, runEnd - Date.now())), maxDecisions: 20 })
    if (run.state?.plan?.status === 'waiting_review' && accepted < 3) {
      accepted += 1
      const review = await ctx.mcp.call('swarm_accept_review', { session_id: current.session_id })
      ctx.check(`tasks.accept_review_${accepted}`, !review.isError, review.isError ? review.text : 'checkpoint review accepted')
    }
    if (run.reason === 'run_ended') await sleep(2000)
  }
  ctx.require('tasks.task_finished', ['needs_review', 'completed'].includes(current.status), `task status ${current.status}${current.last_error ? `; error ${current.last_error}` : ''}${current.action_needed ? `; action needed ${current.action_needed}` : ''}`)

  enter(ctx, 'tasks.review')
  const changes = await tool(ctx, 'swarm_get_changes', { session_id: current.session_id, diff_chars: 4000 }, 'tasks.get_changes')
  const files = (changes.files ?? []).map((f) => `${f.status}:${f.path}`)
  ctx.check('tasks.get_changes', (changes.files ?? []).some((f) => f.path === 'hello.txt'), `files ${files.join(', ') || 'none'}; ${(changes.commits ?? []).length} commit(s)`)
  const read = await ctx.mcp.call('swarm_read_file', { session_id: current.session_id, path: 'hello.txt' })
  ctx.check('tasks.read_task_file', !read.isError && String(read.data?.content ?? '').includes(line), read.isError ? read.text : `hello.txt in the task worktree: ${JSON.stringify(String(read.data?.content ?? '').slice(0, 60))}`)

  enter(ctx, 'tasks.integrate')
  if (current.status === 'needs_review') {
    const integrated = await ctx.mcp.call('swarm_integrate_task', { project_id: projectId, task_id: task.id })
    ctx.require('tasks.integrate', !integrated.isError, integrated.isError ? integrated.text : `status ${integrated.data?.status}; integration ${JSON.stringify(integrated.data?.integration ?? null)}`)
  } else {
    ctx.check('tasks.integrate', current.is_integrated === true, `task ended ${current.status} without review; is_integrated=${current.is_integrated}`)
  }
  const inWorkspace = await ctx.mcp.call('swarm_read_file', { workspace_path: ctx.workspace, path: 'hello.txt' })
  const branch = current.base_branch || 'HEAD'
  const shown = await runCommand('git', ['-C', ctx.workspace, 'show', `${branch}:hello.txt`], { env: baseEnv(ctx.runDir, ctx.opts.binDir), timeoutMs: 20000 })
  ctx.check('tasks.file_in_workspace', !inWorkspace.isError && String(inWorkspace.data?.content ?? '').includes(line),
    inWorkspace.isError ? `read_file in workspace: ${inWorkspace.text}; git show ${branch}:hello.txt exit ${shown.code}` : 'hello.txt is in the workspace working tree')
  ctx.check('tasks.file_on_target_branch', shown.code === 0 && shown.stdout.includes(line), `git show ${branch}:hello.txt exit ${shown.code}`)
}

// prepareSdkPack returns a tarball path: --sdk-pack, or a pack built from a
// copy of packages/sdk inside the run dir (the checkout's dist is untouched).
async function prepareSdkPack(ctx) {
  if (ctx.opts.sdkPack) {
    const pack = path.resolve(ctx.opts.sdkPack)
    ctx.require('sdk.pack', fs.existsSync(pack), `using --sdk-pack ${path.basename(pack)}`)
    return pack
  }
  const source = path.join(REPO_ROOT, 'packages', 'sdk')
  const copy = path.join(ctx.runDir, 'sdk-src')
  fs.cpSync(source, copy, { recursive: true, filter: (p) => !/[/\\](node_modules|dist)$/.test(p) })
  const npmEnv = { ...process.env }
  for (const name of Object.keys(npmEnv)) if (/^SWARM_|_API_KEY$|^GEMINI|^GOOGLE/.test(name)) delete npmEnv[name]
  if (fs.existsSync(path.join(source, 'node_modules', 'typescript'))) {
    fs.symlinkSync(path.join(source, 'node_modules'), path.join(copy, 'node_modules'), 'dir')
  } else {
    const ci = await runCommand('npm', ['ci', '--ignore-scripts', '--no-audit', '--no-fund', '--prefer-offline'], { env: npmEnv, cwd: copy, timeoutMs: 300000 })
    ctx.require('sdk.build_deps', ci.code === 0, ci.code === 0 ? 'npm ci in the SDK copy' : `npm ci exit ${ci.code}: ${ci.stderr.trim().slice(-200)}`)
  }
  const packed = await runCommand('npm', ['pack', '--pack-destination', ctx.runDir, '--json'], { env: npmEnv, cwd: copy, timeoutMs: 300000 })
  let file = ''
  try {
    file = JSON.parse(packed.stdout.slice(packed.stdout.indexOf('[')))[0]?.filename ?? ''
  } catch {}
  const pack = path.join(ctx.runDir, path.basename(file))
  ctx.require('sdk.pack', packed.code === 0 && file !== '' && fs.existsSync(pack), packed.code === 0 ? `built and packed ${path.basename(pack)} (npm run build via prepack)` : `npm pack exit ${packed.code}: ${packed.stderr.trim().slice(-200)}`)
  return pack
}

async function scenarioSdk(ctx) {
  await startDaemon(ctx)
  const live = await onboard(ctx)
  enter(ctx, 'sdk.pack')
  const pack = await prepareSdkPack(ctx)
  enter(ctx, 'sdk.install')
  const app = path.join(ctx.runDir, 'app')
  fs.mkdirSync(app)
  fs.writeFileSync(path.join(app, 'package.json'), JSON.stringify({ name: 'swarm-sdk-e2e-app', private: true, type: 'module' }, null, 2))
  const npmEnv = { ...process.env }
  for (const name of Object.keys(npmEnv)) if (/^SWARM_|_API_KEY$|^GEMINI|^GOOGLE/.test(name)) delete npmEnv[name]
  const installed = await runCommand('npm', ['install', pack, '--no-audit', '--no-fund', '--prefer-offline', '--ignore-scripts'], { env: npmEnv, cwd: app, timeoutMs: 300000 })
  ctx.require('sdk.install', installed.code === 0 && fs.existsSync(path.join(app, 'node_modules', '@swarm-agent', 'sdk', 'package.json')),
    installed.code === 0 ? 'npm install of the pack (dependency ws)' : `npm install exit ${installed.code}: ${installed.stderr.trim().slice(-240)}`)
  fs.copyFileSync(path.join(SCRIPT_DIR, 'sdk-app.template.mjs'), path.join(app, 'app.mjs'))

  enter(ctx, 'sdk.app')
  const appTimeout = Math.max(15000, Math.min(180000, ctx.remainingMs() - 15000))
  const env = { ...baseEnv(ctx.runDir, ctx.opts.binDir), SWARM_SOCKET_PATH: ctx.daemon.socket }
  const ran = await runCommand(process.execPath, ['app.mjs', live ? 'live' : 'keyless', ctx.workspace ?? '', String(appTimeout)], { env, cwd: app, timeoutMs: appTimeout + 10000 })
  let report = null
  try {
    report = JSON.parse(ran.stdout.trim().split('\n').pop())
  } catch {}
  ctx.require('sdk.app_report', Array.isArray(report?.steps), report ? 'app reported its steps' : `app exit ${ran.code} without a report: ${ran.stderr.trim().slice(-240)}`)
  for (const s of report.steps) ctx.check(s.name, s.ok, s.detail)
}

async function scenarioRestart(ctx) {
  await startDaemon(ctx)
  const live = await onboard(ctx)
  let first = null
  if (live) {
    enter(ctx, 'restart.before')
    first = await firstReply(ctx, 'restart')
  }
  const before = await onboarding(ctx)
  const workspacesBefore = (await tool(ctx, 'swarm_list_workspaces', {})).workspaces ?? []
  const modelsBefore = live ? slotSummary(await agentModelSettings(ctx)) : null

  enter(ctx, 'restart.stop')
  await ctx.mcp.close()
  ctx.mcp = null
  const exit = await ctx.daemon.stop()
  ctx.require('restart.stop', exit && !exit.killed, `daemon stopped by SIGTERM: exit ${JSON.stringify(exit)}`)

  enter(ctx, 'restart.start')
  try {
    await ctx.daemon.start({ reusePorts: true, readyTimeoutMs: Math.min(90000, ctx.remainingMs()) })
    ctx.check('restart.start', true, 'daemon started again on the same dirs')
  } catch (err) {
    ctx.require('restart.start', false, err.message)
  }
  const after = await onboarding(ctx)
  ctx.check('restart.identity', after.identity?.bootstrapped === true && Boolean(before.identity?.user_id) && after.identity?.user_id === before.identity?.user_id && after.identity?.username === OWNER_NAME,
    `same owner: ${after.identity?.user_id === before.identity?.user_id}; username ${after.identity?.username}`)
  ctx.check('restart.onboarding_state', after.needs_onboarding === before.needs_onboarding && after.config?.desktop_onboarding_complete === before.config?.desktop_onboarding_complete && after.config?.swarm_name === SWARM_NAME,
    `needs_onboarding ${before.needs_onboarding}->${after.needs_onboarding}, desktop_onboarding_complete ${before.config?.desktop_onboarding_complete}->${after.config?.desktop_onboarding_complete}, swarm_name ${JSON.stringify(before.config?.swarm_name)}->${JSON.stringify(after.config?.swarm_name)}`)
  await connectMcp(ctx, 'restart.mcp')
  const workspacesAfter = (await tool(ctx, 'swarm_list_workspaces', {})).workspaces ?? []
  ctx.check('restart.workspace', workspacesAfter.some((w) => w.path === ctx.workspace) && workspacesAfter.length === workspacesBefore.length, `${workspacesBefore.length} -> ${workspacesAfter.length} workspace(s)`)
  if (!live) return
  const provider = providerEntry(await tool(ctx, 'swarm_list_providers', { provider: PROVIDER }))
  ctx.check('restart.provider', provider?.ready === true, `${PROVIDER} ready=${provider?.ready}`)
  const modelsAfter = slotSummary(await agentModelSettings(ctx))
  ctx.check('restart.models', JSON.stringify(modelsAfter) === JSON.stringify(modelsBefore), 'agent model settings unchanged')
  const session = await tool(ctx, 'swarm_get_session', { session_id: first.sessionId, messages: 10, detail_chars: 2000 }, 'restart.session')
  const users = (session.messages ?? []).filter((m) => m.role === 'user')
  ctx.check('restart.session', session.session?.id === first.sessionId && users.some((m) => String(m.content).includes('pong')) && assistantTexts(session).includes(first.reply),
    `session readable after restart: ${users.length} user and ${assistantTexts(session).length} assistant message(s); reply preserved: ${assistantTexts(session).includes(first.reply)}`)
}

const SCENARIO_RUNNERS = { onboard: scenarioOnboard, chat: scenarioChat, tasks: scenarioTasks, sdk: scenarioSdk, restart: scenarioRestart }

// ---- main ------------------------------------------------------------------------

function readKeyFile(ctx) {
  const file = ctx.opts.keyFile
  const stat = fs.statSync(file)
  if (!stat.isFile()) throw new StepError('--key-file is not a regular file')
  if ((stat.mode & 0o077) !== 0) throw new StepError('--key-file must not be readable by group or others (chmod 600)')
  const key = fs.readFileSync(file, 'utf8').trim()
  if (key === '' || /\s/.test(key) || key.length > 4096) throw new StepError('--key-file must hold one key with no spaces')
  ctx.receipt.addSecret(key)
  ctx.apiKey = key
}

async function cleanup(ctx) {
  if (ctx.cleaned) return
  ctx.cleaned = true
  try {
    if (ctx.mcp) await Promise.race([ctx.mcp.close(), sleep(10000)])
  } catch {}
  ctx.mcp?.kill()
  if (killChildren() > 0) await sleep(500)
  if (ctx.daemon) {
    const exit = await ctx.daemon.stop().catch(() => null)
    ctx.log(`daemon stopped: ${JSON.stringify(exit)}`)
  }
  ctx.apiKey = null
  if (ctx.runDir) {
    if (process.env.SWARM_E2E_KEEP_RUN_DIR === '1') {
      ctx.log(`kept run dir ${ctx.runDir} (SWARM_E2E_KEEP_RUN_DIR=1)`)
    } else {
      try {
        removeRunDir(ctx.runDir, ctx.opts.workDir)
      } catch (err) {
        ctx.log(`run dir not removed: ${err.message}`)
      }
    }
  }
}

function writeReceipt(ctx) {
  const receipt = ctx.receipt.toJSON()
  const out = path.resolve(ctx.opts.output)
  fs.mkdirSync(path.dirname(out), { recursive: true })
  fs.writeFileSync(out, `${JSON.stringify(receipt, null, 2)}\n`, { mode: 0o644 })
  ctx.log(`${receipt.passed ? 'PASS' : 'FAIL'} ${receipt.assertions.filter((a) => a.ok).length}/${receipt.assertions.length} assertions in ${receipt.duration_ms} ms; receipt ${out}`)
  const diagnostic = ctx.receipt.diagnosticLine()
  if (diagnostic) process.stdout.write(`${diagnostic}\n`)
  return receipt.passed
}

async function main() {
  let opts
  try {
    opts = parseArgs(process.argv.slice(2))
  } catch (err) {
    if (err instanceof UsageError) {
      process.stderr.write(`ai-path: ${err.message}\n\n${USAGE}\n`)
      process.exit(2)
    }
    throw err
  }
  if (opts.help) {
    process.stdout.write(`${USAGE}\n`)
    return
  }
  const ctx = createContext(opts)
  let finishing = null
  const finish = (code) => {
    if (!finishing) {
      finishing = (async () => {
        await cleanup(ctx)
        const passed = writeReceipt(ctx)
        process.exit(code ?? (passed ? 0 : 1))
      })()
    }
    return finishing
  }
  for (const signal of ['SIGTERM', 'SIGINT']) {
    process.on(signal, () => {
      ctx.check('scenario.interrupted', false, `${signal} during ${ctx.step}`)
      void finish(signal === 'SIGTERM' ? 143 : 130)
    })
  }
  const deadline = setTimeout(() => {
    ctx.check('scenario.deadline', false, `--timeout-ms ${opts.timeoutMs} reached during ${ctx.step}`)
    void finish(1)
  }, opts.timeoutMs)
  deadline.unref()

  try {
    for (const [flag, value] of [['--bin-dir', opts.binDir], ['--work-dir', opts.workDir]]) {
      if (!fs.statSync(value, { throwIfNoEntry: false })?.isDirectory()) throw new StepError(`${flag} is not a directory`)
    }
    for (const name of ['swarmd', 'swarmctl']) {
      if (!fs.statSync(path.join(opts.binDir, name), { throwIfNoEntry: false })?.isFile()) throw new StepError(`--bin-dir has no ${name}`)
    }
    if (opts.keyFile) readKeyFile(ctx)
    await SCENARIO_RUNNERS[opts.scenario](ctx)
    if (ctx.receipt.assertions.length === 0) ctx.check('scenario.ran', false, 'no assertions recorded')
  } catch (err) {
    if (!finishing) {
      if (err instanceof StepError && ctx.receipt.firstFailure()) {
        // The failed assertion is already recorded.
      } else {
        ctx.check(`${ctx.step}.error`, false, `${err?.name ?? 'Error'}: ${err?.message ?? err}`)
      }
    }
  }
  clearTimeout(deadline)
  await finish()
}

await main()
