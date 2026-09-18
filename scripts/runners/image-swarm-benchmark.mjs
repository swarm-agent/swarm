#!/usr/bin/env node
// ==============================================================================
// Swarm Image Swarm AI Benchmark Runner
// Measures TTFT, Time to Delegation, Direct Swarm Delegation Tool Selection,
// Artifact Generation, Duration, and Cost.
// Specifically verifies that the AI directly invokes task mode=swarm with
// agent_type=image and count=N without searching codebase files or looping in help.
// ==============================================================================
import crypto from 'node:crypto'
import fs from 'node:fs'

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
const customPrompt = String(option('--prompt', 'make me an image swarm of 5 swarm agent logos')).trim()
const expectedCount = Number(option('--count', '5'))
const suppliedToken = String(process.env.SWARM_RUNNER_TOKEN || '').trim()

const testID = `benchmark-image-swarm-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`

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
  process.stdout.write('🐝 SWARM IMAGE SWARM AI BENCHMARK\n')
  process.stdout.write('==============================================================================\n')
  process.stdout.write(`  Test ID:        ${testID}\n`)
  process.stdout.write(`  API URL:        ${apiURL}\n`)
  process.stdout.write(`  Provider:       ${provider}\n`)
  process.stdout.write(`  Model:          ${modelName} (thinking: ${thinkingLevel})\n`)
  process.stdout.write(`  Prompt:         "${customPrompt}"\n`)
  process.stdout.write(`  Expected Count: ${expectedCount}\n`)
  process.stdout.write(`  Timeout:        ${timeoutMs / 1000}s (max tool calls: ${maxToolCalls})\n`)
  process.stdout.write('==============================================================================\n\n')

  // 1. Authenticate via /v1/auth/desktop/session
  if (!token) {
    const auth = await api('GET', '/v1/auth/desktop/session', undefined, 'desktop authentication')
    token = String(auth.body?.token || '').trim()
    if (!token) throw new Error('Could not obtain desktop session token')
  }

  // 2. Resolve Workspace & Topology
  if (workspacePathOverride) {
    await api('POST', '/v1/workspace/add', {
      path: workspacePathOverride,
      name: 'image-swarm-benchmark-primary',
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
      name: 'image-swarm-benchmark-primary',
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
    title: `Image Swarm Benchmark ${modelName}`,
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
    metadata: { benchmark: 'image-swarm', test_id: testID },
  }, 'create session')

  const session = sessionResp.body?.session
  const sessionID = String(session?.id || '').trim()
  if (!sessionID) throw new Error('Session creation returned no session ID')
  process.stdout.write(`[3/4] Created Auto session: ${sessionID}\n`)

  // 5. Send Prompt
  process.stdout.write(`\n[4/4] Sending image swarm prompt: "${customPrompt}"\n`)
  process.stdout.write('------------------------------------------------------------------------------\n')

  const t0 = Date.now()
  const messageResp = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, {
    client_request_id: `${testID}:msg`,
    role: 'user',
    content: customPrompt,
    metadata: { benchmark: 'image-swarm', test_id: testID },
  }, 'send prompt')

  const runID = String(messageResp.body?.run_intent?.run_id || messageResp.body?.run_id || '')
  process.stdout.write(`  Run started: ${runID}\n`)

  // Metrics tracking
  let tFirstEvent = null
  let tReasoningStarted = null
  let tFirstToolCall = null
  let firstToolName = null
  let completedAt = null
  let runStopped = false
  let stopReason = 'natural_completion'

  // Image swarm specific verification
  let calledTaskTool = false
  let isTaskModeSwarm = false
  let taskAgentType = null
  let taskCount = null
  let taskPrompt = null
  let calledSearchOrRead = false
  const unwantedToolCalls = []
  const toolErrors = []
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

      // TTFT
      if (tFirstEvent === null && (
        evType === 'session.provider.first_event' ||
        evType.startsWith('session.reasoning.') ||
        evType.startsWith('session.assistant.delta') ||
        evType.startsWith('session.provider_tool_call.')
      )) {
        tFirstEvent = evTs
        const ttft = (tFirstEvent - t0) / 1000
        process.stdout.write(`  ⚡ TTFT (Time To First Token/Event): ${ttft.toFixed(3)}s (${tFirstEvent - t0} ms)\n`)
      }

      if (evType === 'session.reasoning.started' && tReasoningStarted === null) {
        tReasoningStarted = evTs
        process.stdout.write(`  🧠 Thinking/Reasoning started: ${((tReasoningStarted - t0) / 1000).toFixed(3)}s\n`)
      }

      // First Tool Call initiated
      if (evType === 'session.provider_tool_call.started') {
        const payload = parsePayload(ev.payload)
        const toolName = payload?.tool_name || 'unknown'
        if (tFirstToolCall === null) {
          tFirstToolCall = evTs
          firstToolName = toolName
          process.stdout.write(`  🛠️  First Tool Call Initiated: ${toolName} at ${((tFirstToolCall - t0) / 1000).toFixed(3)}s\n`)
        }
      }

      // Tool Call Arguments Snapshot or Completed
      if (evType === 'session.provider_tool_call.arguments.snapshot' || evType === 'session.provider_tool_call.completed') {
        const payload = parsePayload(ev.payload)
        const args = extractToolArguments(payload)
        const action = String(args?.action || 'none')
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

          // Check if unwanted tool was called (codebase reading/searching)
          if (['search', 'find', 'read', 'list'].includes(toolName)) {
            calledSearchOrRead = true
            unwantedToolCalls.push({ tool: toolName, args })
            process.stdout.write(`        ❌ Unwanted tool called for image swarm: ${toolName}\n`)
          }

          // Check task tool call
          if (toolName === 'task') {
            calledTaskTool = true
            const mode = String(args?.mode || (args?.swarm_mode ? 'swarm' : 'regular')).toLowerCase()
            const agentType = String(args?.agent_type || args?.subagent_type || '').toLowerCase()
            const count = Number(args?.count || 0)
            const prompt = String(args?.prompt || '')

            if (mode === 'swarm') isTaskModeSwarm = true
            taskAgentType = agentType
            taskCount = count
            taskPrompt = prompt

            process.stdout.write(`        ✓ Task Call: mode="${mode}", agent_type="${agentType}", count=${count}\n`)
            if (prompt) process.stdout.write(`          Prompt: "${prompt.slice(0, 80)}..."\n`)
          }
        }
      }

      // Tool errors
      if (evType === 'session.tool.failed') {
        const payload = parsePayload(ev.payload)
        const errMsg = String(payload?.error || payload?.output || 'tool execution failed')
        const toolName = String(payload?.tool_name || 'tool')
        toolErrors.push({ tool: toolName, error: errMsg, ts_ms: evTs - t0 })
        process.stdout.write(`     ❌ Tool Error [${toolName}]: ${errMsg}\n`)
      }

      // Guardrail
      if (toolCalls.length >= maxToolCalls && !isFinished) {
        process.stdout.write(`\n  ⚠️ Guardrail reached: ${toolCalls.length} tool calls. Stopping...\n`)
        stopReason = 'max_tool_calls_guardrail'
        isFinished = true
        break
      }

      // Completion detection
      if (evType === 'session.assistant.completed' || evType === 'run.completed') {
        completedAt = evTs
        isFinished = true
        break
      }
    }

    if (!isFinished) {
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

  // 6. Ensure Run Stopped
  try {
    await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/run/stop`, {
      type: 'run.stop',
      run_id: runID,
      target_swarm_id: runtime.swarm_id,
      reason: 'benchmark complete',
    }, 'ensure run stopped', true)
  } catch {
    // Ignore error
  }

  for (let i = 0; i < 15; i++) {
    const sessionCheck = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}`, undefined, 'verify inactive')
    const activeIntent = sessionCheck.body?.active_run_intent
    if (!activeIntent || ['completed', 'stopped', 'failed', 'cancelled', 'interrupted'].includes(activeIntent.status)) {
      runStopped = true
      break
    }
    await sleep(500)
  }

  // 7. Verify Produced Artifacts
  let artifactCount = 0
  try {
    const artResp = await api('GET', `/v3/artifacts?session_id=${encodeURIComponent(sessionID)}&limit=100`, undefined, 'list artifacts', true)
    const artifacts = Array.isArray(artResp.body?.artifacts) ? artResp.body.artifacts : []
    for (const art of artifacts) {
      const isImage = art.kind === 'image' || art.presentation?.kind === 'image' || String(art.media_type || '').startsWith('image/')
      if (isImage) artifactCount++
    }
  } catch {
    // Non-fatal
  }

  // 8. Usage & Cost
  const usageResp = await api('GET', `/v1/sessions/${encodeURIComponent(sessionID)}/usage?limit=50`, undefined, 'read usage', true)
  const usageRecords = usageResp.body?.turn_usage_records || []
  const latestUsage = usageRecords[usageRecords.length - 1] || {}

  const inputTokens = Number(latestUsage.input_tokens || 0)
  const outputTokens = Number(latestUsage.output_tokens || 0)
  const thinkingTokens = Number(latestUsage.thinking_tokens || 0)
  const cacheReadTokens = Number(latestUsage.cache_read_tokens || 0)
  const totalTokens = Number(latestUsage.total_tokens || (inputTokens + outputTokens))

  const inputCost = (inputTokens * 0.10) / 1000000
  const outputCost = (outputTokens * 0.40) / 1000000
  const cacheCost = (cacheReadTokens * 0.025) / 1000000
  const geminiFlashCostUSD = inputCost + outputCost + cacheCost

  // 9. Verdict
  const ttftSeconds = tFirstEvent ? (tFirstEvent - t0) / 1000 : null
  const timeToToolSeconds = tFirstToolCall ? (tFirstToolCall - t0) / 1000 : null

  let benchmarkVerdict = 'FAIL'
  let verdictExplanation = ''

  if (firstToolName === 'task' && isTaskModeSwarm && taskAgentType === 'image' && taskCount >= expectedCount && !calledSearchOrRead) {
    benchmarkVerdict = 'PASS'
    verdictExplanation = `AI immediately called task with mode=swarm, agent_type=image, count=${taskCount} without searching codebase files.`
  } else if (calledTaskTool && isTaskModeSwarm && taskAgentType === 'image' && calledSearchOrRead) {
    benchmarkVerdict = 'PASS_WITH_UNWANTED_EXPLORATION'
    verdictExplanation = `AI called task with mode=swarm agent_type=image, but first ran unwanted search/read exploration on workspace files.`
  } else if (calledSearchOrRead && !calledTaskTool) {
    benchmarkVerdict = 'SEARCH_DISTRACTION_FAILURE'
    verdictExplanation = `AI got distracted searching/reading workspace code files instead of directly delegating an image swarm.`
  } else if (calledTaskTool && !isTaskModeSwarm) {
    benchmarkVerdict = 'WRONG_TASK_MODE'
    verdictExplanation = `AI called task tool but did not use mode=swarm.`
  } else if (calledTaskTool && taskAgentType !== 'image') {
    benchmarkVerdict = 'WRONG_AGENT_TYPE'
    verdictExplanation = `AI called task mode=swarm with agent_type="${taskAgentType}" instead of "image".`
  } else {
    benchmarkVerdict = 'NO_IMAGE_SWARM_CALL'
    verdictExplanation = `AI did not invoke task mode=swarm agent_type=image (first tool: ${firstToolName || 'none'}).`
  }

  // 10. Summary Report
  process.stdout.write('\n==============================================================================\n')
  process.stdout.write('📊 IMAGE SWARM BENCHMARK METRICS SUMMARY\n')
  process.stdout.write('==============================================================================\n')
  process.stdout.write(`  Verdict:                      ${benchmarkVerdict}\n`)
  process.stdout.write(`  Explanation:                  ${verdictExplanation}\n`)
  process.stdout.write('------------------------------------------------------------------------------\n')
  process.stdout.write(`  TTFT:                         ${ttftSeconds !== null ? ttftSeconds.toFixed(3) + 's' : 'N/A'} (${tFirstEvent ? tFirstEvent - t0 : 'N/A'} ms)\n`)
  process.stdout.write(`  Time To First Tool Action:    ${timeToToolSeconds !== null ? timeToToolSeconds.toFixed(3) + 's' : 'N/A'} (${tFirstToolCall ? tFirstToolCall - t0 : 'N/A'} ms)\n`)
  process.stdout.write(`  Total Execution Duration:     ${(totalDurationMs / 1000).toFixed(3)}s\n`)
  process.stdout.write(`  First Tool Name:              ${firstToolName || 'none'}\n`)
  process.stdout.write(`  Direct Swarm Delegated:       ${firstToolName === 'task' && isTaskModeSwarm && taskAgentType === 'image' ? 'YES (Immediate)' : 'NO'}\n`)
  process.stdout.write(`  Unwanted Code Exploration:    ${calledSearchOrRead ? 'YES (Defect)' : 'NO (Clean direct invocation)'}\n`)
  process.stdout.write(`  Swarm Agent Type:             ${taskAgentType || 'N/A'}\n`)
  process.stdout.write(`  Swarm Count:                  ${taskCount || 0} (requested: ${expectedCount})\n`)
  process.stdout.write(`  Artifacts Produced:           ${artifactCount}\n`)
  process.stdout.write('------------------------------------------------------------------------------\n')
  process.stdout.write(`  Tool Calls (${toolCalls.length}):\n`)
  for (const tc of toolCalls) {
    process.stdout.write(`    - [${(tc.ts_ms / 1000).toFixed(2)}s] ${tc.tool} (action: "${tc.action}")\n`)
  }
  process.stdout.write('------------------------------------------------------------------------------\n')
  process.stdout.write(`  Total Tokens:                 ${totalTokens.toLocaleString()} (input: ${inputTokens}, output: ${outputTokens})\n`)
  process.stdout.write(`  Gemini Flash Incurred Cost:   $${geminiFlashCostUSD.toFixed(6)} USD\n`)
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
    time_to_tool_seconds: timeToToolSeconds,
    duration_seconds: totalDurationMs / 1000,
    first_tool: firstToolName,
    called_task_tool: calledTaskTool,
    is_task_mode_swarm: isTaskModeSwarm,
    task_agent_type: taskAgentType,
    task_count: taskCount,
    called_search_or_read: calledSearchOrRead,
    unwanted_tool_calls: unwantedToolCalls,
    tool_calls_count: toolCalls.length,
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

  try {
    fs.writeFileSync('/workspace/benchmark.json', JSON.stringify(benchmarkResult, null, 2))
  } catch {
    // Non-fatal
  }

  process.stdout.write(`RESULT_JSON=${JSON.stringify(benchmarkResult)}\n`)

  if (benchmarkVerdict !== 'PASS' && benchmarkVerdict !== 'PASS_WITH_UNWANTED_EXPLORATION') {
    process.exit(1)
  }
}

main().catch((err) => {
  process.stderr.write(`\n❌ Benchmark Failed: ${err.stack || err.message}\n`)
  process.exit(1)
})
