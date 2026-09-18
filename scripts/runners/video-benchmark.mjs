#!/usr/bin/env node
// ==============================================================================
// Swarm Video Generation AI Benchmark Runner
// Measures TTFT, Time to Generation Action, Tool Selection, Duration, and Cost
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
const timeoutMs = Number(option('--timeout-ms', process.env.SWARM_RUNNER_TIMEOUT_MS || '180000'))
const maxToolCalls = Number(option('--max-tool-calls', '4'))
const workspacePathOverride = String(option('--workspace-path', process.env.SWARM_RUNNER_WORKSPACE_PATH || '')).trim()
const suppliedToken = String(process.env.SWARM_RUNNER_TOKEN || '').trim()

const testID = `benchmark-video-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`

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
  const timer = setTimeout(() => controller.abort(new Error(`${label} timed out`)), Math.min(timeoutMs, 120000))
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

function parsePayload(raw) {
  if (!raw) return {}
  if (typeof raw === 'object') return raw
  try {
    return JSON.parse(String(raw))
  } catch {
    return {}
  }
}

function extractToolArguments(payload) {
  const rawArgs = payload?.arguments || payload?.arguments_snapshot || payload?.tool_arguments || payload?.payload
  return parsePayload(rawArgs)
}

async function main() {
  process.stdout.write('==============================================================================\n')
  process.stdout.write('🎬 SWARM VIDEO GENERATION AI BENCHMARK\n')
  process.stdout.write('==============================================================================\n')
  process.stdout.write(`  Test ID:       ${testID}\n`)
  process.stdout.write(`  API URL:       ${apiURL}\n`)
  process.stdout.write(`  Provider:      ${provider}\n`)
  process.stdout.write(`  Model:         ${modelName} (thinking: ${thinkingLevel})\n`)
  process.stdout.write(`  Timeout:       ${timeoutMs / 1000}s (max tool calls: ${maxToolCalls})\n`)
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
      name: 'video-benchmark-primary',
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
      name: 'video-benchmark-primary',
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
  process.stdout.write(`[1/4] Workspace resolved: ${workspacePath} (${binding.workspace_binding_id})\n`)

  // 3. Configure Model Settings
  const preference = { provider, model: modelName, thinking: thinkingLevel }
  await api('PATCH', '/v1/agent-model-settings', {
    swarm: { action: preference, plan: preference },
  }, 'configure benchmark model')
  process.stdout.write(`[2/4] Configured Swarm agent models to ${provider}/${modelName} (${thinkingLevel})\n`)

  // 4. Create Benchmark Session in Auto Mode
  const sessionResp = await api('POST', '/v3/sessions', {
    client_request_id: `${testID}:create`,
    title: `Video Benchmark ${modelName}`,
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
    metadata: { benchmark: 'video-generation', test_id: testID },
  }, 'create session')

  const session = sessionResp.body?.session
  const sessionID = String(session?.id || '').trim()
  if (!sessionID) throw new Error('Session creation returned no session ID')
  process.stdout.write(`[3/4] Created Auto session: ${sessionID}\n`)

  // 5. Send Complicated Video Generation Prompt
  const videoPrompt = [
    'Create a 3-scene product launch video for Swarm with continuous soundtrack:',
    'Scene 1 (4 seconds): A high-tech digital grid buzzing with glowing blue sparks as autonomous AI nodes awaken. Title: "The Autonomous Age".',
    'Scene 2 (4 seconds): Smooth cinematic camera flythrough into a busy developer workstation showing code compiling instantly. Title: "Built for Scale".',
    'Scene 3 (4 seconds): Neon city skyline at dusk with the Swarm emblem glowing above the horizon. Title: "Ship Faster".',
    'Include an energetic electronic synth soundtrack that ducks under foley sound effects.',
    'Deliver the final concatenated video deliverable.',
  ].join('\n')

  process.stdout.write('\n[4/4] Sending video generation prompt and tracking execution events...\n')
  process.stdout.write('------------------------------------------------------------------------------\n')

  const t0 = Date.now()
  const messageResp = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, {
    client_request_id: `${testID}:msg`,
    role: 'user',
    content: videoPrompt,
    metadata: { benchmark: 'video-generation', test_id: testID },
  }, 'send prompt')

  const runID = String(messageResp.body?.run_intent?.run_id || messageResp.body?.run_id || '')
  process.stdout.write(`  Run started: ${runID}\n`)

  // Metrics tracking
  let tFirstEvent = null
  let tReasoningStarted = null
  let tFirstToolCall = null
  let firstToolName = null
  let firstToolAction = null
  let completedAt = null
  let runStopped = false
  let stopReason = 'natural_completion'

  const toolCalls = []
  let afterSeq = 0
  let isFinished = false
  const deadline = t0 + timeoutMs

  while (Date.now() < deadline && !isFinished) {
    const evResp = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/events?after_seq=${afterSeq}&limit=100`, undefined, 'read events', true)
    const events = evResp.body?.events || []

    for (const ev of events) {
      const eventSeq = Number(ev.seq || 0)
      if (eventSeq > afterSeq) afterSeq = eventSeq

      const evType = String(ev.event_type || '')
      const evTs = Number(ev.ts_unix_ms || Date.now())

      // Check for first event (TTFT)
      if (tFirstEvent === null && (evType === 'session.provider.first_event' || evType.startsWith('session.reasoning.') || evType.startsWith('session.assistant.delta') || evType.startsWith('session.provider_tool_call.'))) {
        tFirstEvent = evTs
        const ttft = (tFirstEvent - t0) / 1000
        process.stdout.write(`  ⚡ TTFT (Time To First Token/Event): ${ttft.toFixed(3)}s (${tFirstEvent - t0} ms)\n`)
      }

      if (evType === 'session.reasoning.started' && tReasoningStarted === null) {
        tReasoningStarted = evTs
        process.stdout.write(`  🧠 Thinking/Reasoning started: ${((tReasoningStarted - t0) / 1000).toFixed(3)}s\n`)
      }

      // Check for tool calls
      if (evType === 'session.provider_tool_call.started') {
        const payload = parsePayload(ev.payload)
        const toolName = payload?.tool_name || 'unknown'
        if (tFirstToolCall === null) {
          tFirstToolCall = evTs
          firstToolName = toolName
          process.stdout.write(`  🛠️  First Tool Call Initiated: ${toolName} at ${((tFirstToolCall - t0) / 1000).toFixed(3)}s\n`)
        }
      }

      if (evType === 'session.provider_tool_call.arguments.snapshot' || evType === 'session.provider_tool_call.completed') {
        const payload = parsePayload(ev.payload)
        const args = extractToolArguments(payload)
        const action = args?.action || 'none'
        const toolName = payload?.tool_name || 'unknown'
        const callID = payload?.call_id || ev.id

        let existing = toolCalls.find((c) => c.call_id === callID)
        if (!existing) {
          existing = {
            tool: toolName,
            action,
            call_id: callID,
            ts_ms: evTs - t0,
            args,
          }
          toolCalls.push(existing)
          process.stdout.write(`     -> Step ${toolCalls.length}: ${toolName} [action="${action}"] (${((evTs - t0) / 1000).toFixed(2)}s)\n`)
        } else if (existing.action === 'none' && action !== 'none') {
          existing.action = action
          existing.args = args
          process.stdout.write(`        (updated call ${callID}: action="${action}")\n`)
        }

        if (firstToolAction === null && (toolName === 'manage_artifact' || toolName === 'manage_video')) {
          firstToolAction = action
        }
      }

      // Check tool call limit guardrail
      if (toolCalls.length >= maxToolCalls && !isFinished) {
        process.stdout.write(`\n  ⚠️ Guardrail triggered: AI reached ${toolCalls.length} tool calls (spinning prevention). Halting run...\n`)
        stopReason = 'max_tool_calls_guardrail'
        isFinished = true
        break
      }

      // Check completion
      if (evType === 'session.assistant.completed' || evType === 'run.completed') {
        completedAt = evTs
        isFinished = true
        break
      }
    }

    if (!isFinished) {
      // Check session status directly
      const sessionCheck = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}`, undefined, 'check session status', true)
      const activeIntent = sessionCheck.body?.active_run_intent
      if (!activeIntent && afterSeq > 10) {
        completedAt = Date.now()
        isFinished = true
        break
      }
      await sleep(400)
    }
  }

  const tEnd = completedAt || Date.now()
  const totalDurationMs = tEnd - t0

  // 6. Ensure Run Stopped at the End (Mandatory Requirement)
  process.stdout.write('\n------------------------------------------------------------------------------\n')
  process.stdout.write('🛑 Verifying Run Stopped Status...\n')
  try {
    await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/run/stop`, {
      type: 'run.stop',
      run_id: runID,
      target_swarm_id: runtime.swarm_id,
      reason: 'benchmark complete',
    }, 'ensure run stopped', true)
  } catch (err) {
    // Ignore error if already stopped
  }

  // Poll until confirmed inactive (active_run_intent is null)
  for (let i = 0; i < 15; i++) {
    const sessionCheck = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}`, undefined, 'verify inactive')
    const activeIntent = sessionCheck.body?.active_run_intent
    if (!activeIntent || ['completed', 'stopped', 'failed', 'cancelled', 'interrupted'].includes(activeIntent.status)) {
      runStopped = true
      break
    }
    await sleep(500)
  }
  process.stdout.write(`  ✓ Terminal Run State: stopped=${runStopped}\n`)

  // 7. Extract Token Usage & Cost Benchmark
  const usageResp = await api('GET', `/v1/sessions/${encodeURIComponent(sessionID)}/usage?limit=50`, undefined, 'read usage', true)
  const usageRecords = usageResp.body?.turn_usage_records || []
  const latestUsage = usageRecords[usageRecords.length - 1] || {}

  const inputTokens = Number(latestUsage.input_tokens || 0)
  const outputTokens = Number(latestUsage.output_tokens || 0)
  const thinkingTokens = Number(latestUsage.thinking_tokens || 0)
  const cacheReadTokens = Number(latestUsage.cache_read_tokens || 0)
  const totalTokens = Number(latestUsage.total_tokens || (inputTokens + outputTokens))

  // Gemini Flash Pricing Formula (Standard Pay-As-You-Go):
  // Prompt input:  $0.10 / 1M tokens ($0.00000010/token)
  // Output tokens: $0.40 / 1M tokens ($0.00000040/token)
  // Context Cache: $0.025 / 1M tokens ($0.000000025/token)
  const inputCost = (inputTokens * 0.10) / 1000000
  const outputCost = (outputTokens * 0.40) / 1000000
  const cacheCost = (cacheReadTokens * 0.025) / 1000000
  const geminiFlashCostUSD = inputCost + outputCost + cacheCost

  // 8. Benchmark Results Assessment
  const ttftSeconds = tFirstEvent ? (tFirstEvent - t0) / 1000 : null
  const timeToGenSeconds = tFirstToolCall ? (tFirstToolCall - t0) / 1000 : null
  const usedVideoStory = toolCalls.some((c) => c.action === 'generate_video_story')
  const usedSingleVideo = toolCalls.some((c) => c.action === 'generate_video')
  const checkedHelp = toolCalls.some((c) => c.action === 'help')

  let benchmarkVerdict = 'FAIL'
  let verdictExplanation = ''

  if (usedVideoStory) {
    benchmarkVerdict = 'PASS'
    verdictExplanation = 'AI correctly selected atomic generate_video_story pipeline in one call.'
  } else if (checkedHelp && !usedVideoStory) {
    benchmarkVerdict = 'MISSING_PROMPT_INSTRUCTION'
    verdictExplanation = 'AI missed generate_video_story in system prompt; inspected help topic="video" and prepared multi-turn manual plan.'
  } else if (usedSingleVideo) {
    benchmarkVerdict = 'MANUAL_CHAINING_FAILURE'
    verdictExplanation = 'AI missed generate_video_story and attempted multi-step manual generate_video.'
  } else {
    benchmarkVerdict = 'NO_VIDEO_STORY_CALL'
    verdictExplanation = `AI did not invoke generate_video_story (tools called: ${toolCalls.map((c) => `${c.tool}:${c.action}`).join(', ') || 'none'}).`
  }

  // 9. Format Report Output
  process.stdout.write('\n==============================================================================\n')
  process.stdout.write('📊 BENCHMARK METRICS SUMMARY (ORIGINAL BASELINE)\n')
  process.stdout.write('==============================================================================\n')
  process.stdout.write(`  Verdict:                      ${benchmarkVerdict}\n`)
  process.stdout.write(`  Explanation:                  ${verdictExplanation}\n`)
  process.stdout.write('------------------------------------------------------------------------------\n')
  process.stdout.write(`  TTFT (Time To First Token):   ${ttftSeconds !== null ? ttftSeconds.toFixed(3) + 's' : 'N/A'} (${tFirstEvent ? tFirstEvent - t0 : 'N/A'} ms)\n`)
  process.stdout.write(`  Time To First Tool Action:    ${timeToGenSeconds !== null ? timeToGenSeconds.toFixed(3) + 's' : 'N/A'} (${tFirstToolCall ? tFirstToolCall - t0 : 'N/A'} ms)\n`)
  process.stdout.write(`  Total Execution Duration:     ${(totalDurationMs / 1000).toFixed(3)}s\n`)
  process.stdout.write(`  Stop Reason:                  ${stopReason}\n`)
  process.stdout.write(`  Run Stopped Verified:         ${runStopped ? 'YES (100% terminated)' : 'NO'}\n`)
  process.stdout.write('------------------------------------------------------------------------------\n')
  process.stdout.write(`  Tool Calls Attempted (${toolCalls.length}):\n`)
  for (const tc of toolCalls) {
    process.stdout.write(`    - [${(tc.ts_ms / 1000).toFixed(2)}s] ${tc.tool} (action: "${tc.action}")\n`)
  }
  process.stdout.write('------------------------------------------------------------------------------\n')
  process.stdout.write(`  Input Tokens:                 ${inputTokens.toLocaleString()}\n`)
  process.stdout.write(`  Output Tokens:                ${outputTokens.toLocaleString()}\n`)
  process.stdout.write(`  Thinking Tokens:              ${thinkingTokens.toLocaleString()}\n`)
  process.stdout.write(`  Cached Content Tokens:        ${cacheReadTokens.toLocaleString()}\n`)
  process.stdout.write(`  Total Tokens:                 ${totalTokens.toLocaleString()}\n`)
  process.stdout.write('------------------------------------------------------------------------------\n')
  process.stdout.write(`  Gemini Flash Incurred Cost:   $${geminiFlashCostUSD.toFixed(6)} USD\n`)
  process.stdout.write(`    - Input prompt cost:        $${inputCost.toFixed(6)}\n`)
  process.stdout.write(`    - Output cost:              $${outputCost.toFixed(6)}\n`)
  process.stdout.write(`    - Cache read cost:          $${cacheCost.toFixed(6)}\n`)
  process.stdout.write('==============================================================================\n\n')

  const benchmarkResult = {
    test_id: testID,
    session_id: sessionID,
    run_id: runID,
    model: modelName,
    thinking: thinkingLevel,
    verdict: benchmarkVerdict,
    explanation: verdictExplanation,
    ttft_seconds: ttftSeconds,
    time_to_generation_seconds: timeToGenSeconds,
    duration_seconds: totalDurationMs / 1000,
    run_stopped: runStopped,
    stop_reason: stopReason,
    tool_calls: toolCalls.map((c) => ({ tool: c.tool, action: c.action, ts_ms: c.ts_ms })),
    tokens: {
      input_tokens: inputTokens,
      output_tokens: outputTokens,
      thinking_tokens: thinkingTokens,
      cache_read_tokens: cacheReadTokens,
      total_tokens: totalTokens,
    },
    cost_usd: {
      total: geminiFlashCostUSD,
      input: inputCost,
      output: outputCost,
      cache: cacheCost,
    },
  }

  process.stdout.write(`RESULT_JSON=${JSON.stringify(benchmarkResult)}\n`)
}

main().catch((err) => {
  process.stderr.write(`\n❌ Benchmark Failed: ${err.stack || err.message}\n`)
  process.exit(1)
})
