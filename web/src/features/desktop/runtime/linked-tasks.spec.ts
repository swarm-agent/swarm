import test from 'node:test'
import assert from 'node:assert/strict'

// Purpose: the Desktop reads the home's linked-task records and the
// read-only remote session view only through their one route
// (/v3/fleet/linked-tasks), with ids encoded, and surfaces a refusal (an
// unreachable machine, a missing task) as an error instead of an empty list.
// The runtime module owns these requests; a fetch stub is the narrowest layer
// that proves the request shape and the error path without a daemon.

type Call = { input: RequestInfo | URL; init?: RequestInit }

async function withFetch(respond: (url: string) => Response, run: (calls: Call[]) => Promise<void>): Promise<void> {
  const calls: Call[] = []
  const original = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    return respond(String(input))
  }) as typeof fetch
  try {
    await run(calls)
  } finally {
    globalThis.fetch = original
  }
}

const json = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status, headers: { 'Content-Type': 'application/json' } })

test('linked tasks are listed by session or project through the one route', async () => {
  const row = { machine: 'box', project_id: 'rp', task_id: 't1', title: 'Audit', home_session_id: 's/1', created_at: 1, status: 'running' }
  await withFetch(() => json({ linked_tasks: [row], untrusted_content: true }), async calls => {
    const { fetchLinkedTasks, linkedTaskStatusLabel } = await import('./linked-tasks')
    const bySession = await fetchLinkedTasks({ sessionId: 's/1' })
    assert.equal(String(calls[0]?.input), '/v3/fleet/linked-tasks?session_id=s%2F1')
    assert.equal(bySession[0]?.machine, 'box')
    assert.equal(linkedTaskStatusLabel(bySession[0]!), 'running')
    await fetchLinkedTasks({ projectId: 'home project' })
    assert.equal(String(calls[1]?.input), '/v3/fleet/linked-tasks?project_id=home%20project')
    assert.equal(linkedTaskStatusLabel({ ...row, missing: true }), 'missing on machine')
    assert.equal(linkedTaskStatusLabel({ ...row, archived: true }), 'running (archived)')
  })
})

test('the remote session view is a read and a refusal is an error', async () => {
  await withFetch(url => url.includes('task_id=gone')
    ? json({ error: 'task gone not found on box' }, 404)
    : json({ machine: 'box', project_id: 'rp', task_id: 't1', session_id: 'rs', messages: [{ id: 'm', role: 'assistant', content: 'hi', global_seq: 1, created_at: 1 }], read_only: true, untrusted_content: true }), async calls => {
    const { fetchLinkedTaskSession } = await import('./linked-tasks')
    const view = await fetchLinkedTaskSession({ machine: 'box', project_id: 'rp', task_id: 't1' })
    assert.equal(String(calls[0]?.input), '/v3/fleet/linked-tasks/session?machine=box&project_id=rp&task_id=t1')
    assert.equal((calls[0]?.init?.method || 'GET').toUpperCase(), 'GET')
    assert.equal(view.messages[0]?.content, 'hi')
    await assert.rejects(fetchLinkedTaskSession({ machine: 'box', project_id: 'rp', task_id: 'gone' }), /not found on box/)
  })
})
