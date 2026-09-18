#!/usr/bin/env node
// ==============================================================================
// Swarm First-Message Gemini Token Probe Runner
// Sends "Say hello and nothing else" to Swarm auto mode and measures exact
// Gemini API usageMetadata (promptTokenCount / input_tokens).
// ==============================================================================
import crypto from 'node:crypto'

const argv = process.argv.slice(2)
const option = (name, fallback = '') => {
  const index = argv.indexOf(name)
  return index >= 0 && index + 1 < argv.length ? argv[index + 1] : fallback
}

const apiURL = String(option('--api-url', process.env.SWARM_RUNNER_API_URL || 'http://127.0.0.1:5555')).replace(/\/$/, '')
const provider = String(option('--provider', process.env.SWARM_RUNNER_PROVIDER || 'google')).trim().toLowerCase()
const modelName = String(option('--model', process.env.SWARM_RUNNER_MODEL || 'gemini-3.6-flash')).trim()
const thinkingLevel = String(option('--thinking', process.env.SWARM_RUNNER_THINKING || 'low')).trim().toLowerCase()
const timeoutMs = Number(option('--timeout-ms', process.env.SWARM_RUNNER_TIMEOUT_MS || '120000'))
const workspacePathOverride = String(option('--workspace-path', process.env.SWARM_RUNNER_WORKSPACE_PATH || '')).trim()
const suppliedToken = String(process.env.SWARM_RUNNER_TOKEN || '').trim()

const testID = `probe-hello-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
let token = suppliedToken

async function api(method, route, body, label = route, allowError = false) {
  const headers = {
    Accept: 'application/json',
    Origin: new URL(apiURL).origin,
    Referer: `${apiURL}/app`,
    'Sec-Fetch-Site': 'same-origin',
  }
  if (token) {
    headers['X-Swarm-Token'] = token
    headers.Cookie = `swarm_desktop_session=${token}`
  }
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(new Error(`${label} timed out`)), Math.min(timeoutMs, 60000))
  try {
    const init = { method, headers, signal: controller.signal }
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json'
      init.body = JSON.stringify(body)
    }
    const response = await fetch(`${apiURL}${route}`, init)
    const text = await response.text()
    let decoded = null
    try {
      decoded = text ? JSON.parse(text) : null
    } catch {
      decoded = { raw: text }
    }
    if (!allowError && !response.ok) {
      throw new Error(`${label} failed with HTTP ${response.status}: ${text.slice(0, 1000)}`)
    }
    return { ok: response.ok, status: response.status, body: decoded, text }
  } finally {
    clearTimeout(timer)
  }
}

async function main() {
  process.stdout.write('==============================================================================\n')
  process.stdout.write('🚀 SWARM LIVE GEMINI TOKEN COUNT PROBE\n')
  process.stdout.write('==============================================================================\n')
  process.stdout.write(`  Test ID:       ${testID}\n`)
  process.stdout.write(`  API URL:       ${apiURL}\n`)
  process.stdout.write(`  Provider:      ${provider}\n`)
  process.stdout.write(`  Model:         ${modelName} (thinking: ${thinkingLevel})\n`)
  process.stdout.write(`  Prompt:        "Say hello and nothing else."\n`)
  process.stdout.write('==============================================================================\n\n')

  // 1. Authenticate
  if (!token) {
    const auth = await api('GET', '/v1/auth/desktop/session', undefined, 'desktop authentication')
    token = String(auth.body?.token || '').trim()
    if (!token) throw new Error('Could not obtain desktop session token')
  }

  // 2. Resolve Workspace & Topology
  if (workspacePathOverride) {
    await api('POST', '/v1/workspace/add', {
      path: workspacePathOverride,
      name: 'hello-probe-workspace',
      make_current: true,
      confirm_committed_only: true,
    }, 'ensure workspace binding', true)
  }

  let topology = (await api('GET', '/v1/swarm/topology', undefined, 'read topology')).body
  let bindings = Array.isArray(topology?.workspace_bindings) ? topology.workspace_bindings : []

  if (bindings.length === 0) {
    const fallbackPath = workspacePathOverride || '/workspace/swarm-primary'
    await api('POST', '/v1/workspace/add', {
      path: fallbackPath,
      name: 'hello-probe-workspace',
      make_current: true,
      confirm_committed_only: true,
    }, 'ensure fallback workspace binding', true)
    topology = (await api('GET', '/v1/swarm/topology', undefined, 'read topology again')).body
    bindings = Array.isArray(topology?.workspace_bindings) ? topology.workspace_bindings : []
  }

  const runtime = (topology?.runtimes || []).find((item) => item?.relationship === 'self') || (topology?.runtimes || [])[0]
  if (!runtime?.swarm_id) throw new Error('Topology has no runnable Swarm runtime')

  let binding = null
  if (workspacePathOverride) {
    binding = bindings.find((item) => item?.source_workspace_path === workspacePathOverride || item?.destination_workspace_path === workspacePathOverride)
  }
  if (!binding) {
    binding = bindings.find((item) => item?.state === 'bound' && item?.workspace_binding_id) || bindings[0]
  }
  if (!binding?.workspace_binding_id) throw new Error('No valid workspace binding found in topology')

  const workspacePath = String(binding?.source_workspace_path || binding?.destination_workspace_path || '').trim()
  process.stdout.write(`[1/4] Workspace resolved: ${workspacePath}\n`)

  // 3. Configure Model Settings
  const preference = { provider, model: modelName, thinking: thinkingLevel }
  await api('PATCH', '/v1/agent-model-settings', {
    swarm: { action: preference, plan: preference },
  }, 'configure probe model')
  process.stdout.write(`[2/4] Configured Swarm agent models to ${provider}/${modelName} (${thinkingLevel})\n`)

  // 4. Create Session in Auto Mode
  const sessionResp = await api('POST', '/v3/sessions', {
    client_request_id: `${testID}:create`,
    title: `Hello Token Probe ${modelName}`,
    workspace_path: workspacePath,
    workspace_name: String(binding.source_workspace_name || 'swarm-test'),
    workspace_binding_id: binding.workspace_binding_id,
    swarm_id: runtime.swarm_id,
    target_kind: 'host',
    target_relationship: 'self',
    mode: 'auto',
    agent_name: 'swarm',
    preference,
    model_profile: { use_account_default: true },
    metadata: { benchmark: 'hello-token-probe', test_id: testID },
  }, 'create session')

  const session = sessionResp.body?.session
  const sessionID = String(session?.id || '').trim()
  if (!sessionID) throw new Error('Session creation returned no session ID')
  process.stdout.write(`[3/4] Created Auto session: ${sessionID}\n`)

  // 5. Send "Say hello and nothing else." prompt
  const userPrompt = 'Say hello and nothing else.'
  process.stdout.write(`[4/4] Sending prompt "${userPrompt}" to Gemini...\n`)

  const t0 = Date.now()
  const messageResp = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, {
    client_request_id: `${testID}:msg`,
    role: 'user',
    content: userPrompt,
    metadata: { benchmark: 'hello-token-probe', test_id: testID },
  }, 'send prompt')

  const runID = String(messageResp.body?.run_intent?.run_id || messageResp.body?.run_id || '')
  process.stdout.write(`  Run started: ${runID}\n`)

  // 6. Poll for usage and response
  process.stdout.write('  Waiting for Gemini response and token usage metrics...\n')
  let latestUsage = null
  const deadline = t0 + timeoutMs

  while (Date.now() < deadline) {
    const usageResp = await api('GET', `/v1/sessions/${encodeURIComponent(sessionID)}/usage?limit=10`, undefined, 'poll usage', true)
    const records = usageResp.body?.turn_usage_records || []
    if (records.length > 0) {
      latestUsage = records[records.length - 1]
      break
    }
    await sleep(1000)
  }

  if (!latestUsage) {
    throw new Error('Timed out waiting for turn usage records from /v1/sessions/.../usage')
  }

  const durationMs = Date.now() - t0

  // 7. Read Messages from Session
  const msgResp = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/messages?limit=10`, undefined, 'read messages')
  const messages = msgResp.body?.messages || []
  const assistantMsg = messages.find((m) => m.role === 'assistant')
  const replyText = assistantMsg?.content || '(no text returned)'

  // 8. Extract Token Usage
  const inputTokens = Number(latestUsage.input_tokens || 0)
  const outputTokens = Number(latestUsage.output_tokens || 0)
  const thinkingTokens = Number(latestUsage.thinking_tokens || 0)
  const totalTokens = Number(latestUsage.total_tokens || (inputTokens + outputTokens))
  const usageSource = String(latestUsage.source || 'unknown')

  process.stdout.write('\n==============================================================================\n')
  process.stdout.write('🎯 REAL GEMINI API TOKEN RETURN REPORT\n')
  process.stdout.write('==============================================================================\n')
  process.stdout.write(`  Model:               ${provider}/${modelName} (thinking: ${thinkingLevel})\n`)
  process.stdout.write(`  Duration:            ${(durationMs / 1000).toFixed(2)}s\n`)
  process.stdout.write(`  Gemini Response:     "${replyText.trim()}"\n`)
  process.stdout.write('------------------------------------------------------------------------------\n')
  process.stdout.write(`  📊 INPUT TOKENS:     ${inputTokens.toLocaleString()} tokens\n`)
  process.stdout.write(`  📤 OUTPUT TOKENS:    ${outputTokens.toLocaleString()} tokens\n`)
  process.stdout.write(`  🧠 THINKING TOKENS:  ${thinkingTokens.toLocaleString()} tokens\n`)
  process.stdout.write(`  📈 TOTAL TOKENS:     ${totalTokens.toLocaleString()} tokens\n`)
  process.stdout.write(`  🏷️  USAGE SOURCE:     ${usageSource}\n`)
  process.stdout.write('==============================================================================\n')

  if (inputTokens === 0) {
    throw new Error('FAILED: Gemini reported 0 input tokens.')
  }
  process.stdout.write('✅ Gemini Hello Token Probe succeeded with verified live API usage return.\n\n')
}

main().catch((err) => {
  process.stderr.write(`❌ Probe Failed: ${err.message}\n`)
  process.exit(1)
})
