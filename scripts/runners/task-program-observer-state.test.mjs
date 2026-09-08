// Purpose: deterministic narrow regression for observer early-return decisions.
// No provider, live daemon, credentials or sleep is needed. Reject stale progress
// while preserving successful siblings and ignoring historical blocked programs.
import test from 'node:test'
import assert from 'node:assert/strict'
import { observerStopReason, createProgressWatch } from './task-program-observer-state.mjs'
const input = output => ({ sessionID: 'parent', runID: 'current', programIDs: ['new'], snapshot: { messages_by_session: { parent: [{ role: 'tool', content: JSON.stringify({ run_id: 'current', tool_name: 'task', output: JSON.stringify(output) }) }] } } })
test('blocked child returns even when parent and program still say running', () => {
  const reason = observerStopReason(input({ program_id: 'new', program_state: 'running', jobs: [{ job_id: 'done', state: 'integrated' }, { job_id: 'confirm', state: 'blocked', blocker: { message: 'missing report' } }] }))
  assert.equal(reason.kind, 'child_stopped'); assert.equal(reason.job_id, 'confirm')
})
test('duplicate program tool error returns before checkpoint terminalizes', () => {
  const i = input({}); i.snapshot.messages_by_session.parent[0].content = JSON.stringify({ run_id: 'current', metadata: { error: 'program already exists' } })
  assert.equal(observerStopReason(i).kind, 'tool_failed')
})
test('historical blocked program does not stop a new recovery', () => {
  const i = input({ program_id: 'old', program_state: 'blocked' })
  i.snapshot.messages_by_session.parent[0].content = JSON.stringify({ run_id: 'old-run', output: JSON.stringify({ program_id: 'old', program_state: 'blocked' }) })
  assert.equal(observerStopReason(i), null)
})
test('blocked program reports preserved completed jobs without mutation', () => {
  const i = input({ program_id: 'new', program_state: 'blocked', jobs: [{ job_id: 'write', state: 'integrated' }, { job_id: 'audit', state: 'completed' }] })
  const before = JSON.stringify(i)
  assert.deepEqual(observerStopReason(i).preserved_jobs, ['write', 'audit']); assert.equal(JSON.stringify(i), before)
})
test('terminal without required outputs and explicit child BLOCKED both yield', () => {
  const i = input({}); i.snapshot.run_intents_by_session = { parent: [{ run_id: 'current', status: 'completed' }] }
  assert.equal(observerStopReason(i).kind, 'terminal_without_evidence')
  i.childReports = [{ program_id: 'new', session_id: 'child', text: 'BLOCKED: report absent' }]
  assert.equal(observerStopReason(i).kind, 'child_stopped')
})
test('progress uses injected clock and never extends total deadline', () => {
  let t = 0; const watch = createProgressWatch({ now: () => t, stallMs: 10, timeoutMs: 30 })
  assert.equal(watch('a'), null); t = 9; assert.equal(watch('a'), null); t = 10; assert.equal(watch('a').kind, 'stalled')
  t = 11; assert.equal(watch('b'), null); t = 30; assert.equal(watch('c').kind, 'deadline')
})

test('streamed child block returns before final task result', () => {
  const i = input({ program_id: 'new', program_state: 'running' })
  i.snapshot.events_by_session = { parent: [{ event_type: 'session.tool.delta', payload: { run_id: 'current', output: JSON.stringify({ program_id: 'new', event: 'launch.patch', launch: { child_session_id: 'confirm', phase: 'completed', report: 'BLOCKED: missing handoff' } }) } }] }
  assert.equal(observerStopReason(i).kind, 'child_stopped')
})
