// Tiny application for the ai-path e2e sdk scenario. ai-path.mjs copies it into
// a temp app directory where @swarm-agent/sdk was installed from its npm pack,
// so the import below resolves the packed SDK exactly as a user's app would.
// It connects with the SDK's default connection rules (SWARM_SOCKET_PATH, no
// token) and prints one JSON line of step results on stdout. Nothing secret
// passes through it.
//
// Usage: node app.mjs <keyless|live> <workspace_path> <timeout_ms>

import { SwarmClient } from '@swarm-agent/sdk'

const [mode = 'keyless', workspacePath = '', timeoutArg = '120000'] = process.argv.slice(2)
const timeoutMs = Number(timeoutArg) || 120000
const steps = []
const step = (name, ok, detail = '') => steps.push({ name, ok: Boolean(ok), detail: String(detail).slice(0, 300) })
const finish = (code) => {
  process.stdout.write(`${JSON.stringify({ steps })}\n`)
  process.exit(code)
}

const hardStop = setTimeout(() => {
  step('sdk.deadline', false, `the app did not finish within ${timeoutMs} ms`)
  finish(1)
}, timeoutMs)
hardStop.unref()

try {
  const client = new SwarmClient()
  step('sdk.default_connection', Boolean(client.config.socketPath) && !client.config.token, `socket from SWARM_SOCKET_PATH, no token`)

  const status = await client.onboarding.get()
  step('sdk.onboarding_read', status?.identity?.bootstrapped === true, `bootstrapped=${status?.identity?.bootstrapped} needs_onboarding=${status?.needs_onboarding}`)

  const before = await client.sessions.list({ limit: 50 })
  step('sdk.list_conversations', Array.isArray(before), `${Array.isArray(before) ? before.length : 'no'} conversations`)

  if (mode === 'live') {
    const session = await client.chat.createSession({ workspace_path: workspacePath, title: 'sdk e2e chat' })
    const sessionId = session?.id
    step('sdk.start_chat', typeof sessionId === 'string' && sessionId !== '', 'conversation created in the workspace')

    let chunks = 0
    let streamed = ''
    let completed = null
    let streamError = null
    let resolveDone
    const done = new Promise((resolve) => {
      resolveDone = resolve
    })
    const watch = await client.chat.stream(sessionId, {
      onText: (delta, fullText) => {
        if (delta) chunks += 1
        streamed = fullText
      },
      onComplete: (messages, run) => {
        completed = { messages, run }
        resolveDone()
      },
      onError: (err) => {
        streamError = err
      },
    })
    await watch.ready
    await client.chat.sendMessage(sessionId, 'Reply with exactly one word: pong. Do not use any tools.')
    const waitMs = Math.max(10000, timeoutMs - 20000)
    const timer = new Promise((resolve) => setTimeout(() => resolve('timeout'), waitMs))
    const outcome = await Promise.race([done.then(() => 'complete'), timer])
    watch.dispose()
    const reply = completed?.messages?.filter((m) => m?.role === 'assistant').map((m) => String(m.content ?? '')).join('').trim() ?? ''
    step('sdk.stream_text', chunks > 0 && streamed.trim() !== '', `${chunks} onText chunks, ${streamed.length} chars streamed${streamError ? `; stream error: ${streamError.message}` : ''}`)
    step('sdk.stream_complete', outcome === 'complete' && completed?.run?.status === 'completed' && reply !== '',
      outcome === 'complete' ? `run ${completed?.run?.status}; reply ${JSON.stringify(reply.slice(0, 60))}` : `no onComplete within ${waitMs} ms`)

    const renamed = await client.sessions.rename(sessionId, 'sdk e2e renamed')
    const detail = await client.sessions.get(sessionId)
    step('sdk.rename', renamed?.ok !== false && detail?.title === 'sdk e2e renamed', `title now ${JSON.stringify(detail?.title)}`)

    const after = await client.sessions.list({ limit: 50 })
    step('sdk.listed_after', after.some((s) => s?.id === sessionId), `${after.length} conversations, new one listed`)
  }
  finish(steps.every((s) => s.ok) ? 0 : 1)
} catch (err) {
  step('sdk.error', false, `${err?.name || 'Error'}: ${err?.message || err}`)
  finish(1)
}
