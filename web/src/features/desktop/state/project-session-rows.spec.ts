import test from 'node:test'
import assert from 'node:assert/strict'
import { createEmptyDesktopV3CacheState } from './desktop-v3-cache-reducer'
import { projectSessionRow, compareProjectSessionRows } from './project-session-rows'
import type { SessionSnapshot } from './desktop-v3-cache-types'

// Purpose: projectSessionRow must share canonical run/permission state, ignore
// heartbeat/projection timestamps for ordering, and clear stale tools on terminal
// runs, retaining the latest terminal status after the active intent is cleared. Pure selector tests are the narrowest proof of these ordering invariants.
test('project rows use meaningful activity and canonical attention without clock-driven reshuffling', () => {
  const state = createEmptyDesktopV3CacheState()
  const session = (id: string, activity: number) => ({ id, title: id, created_at: 1, last_message_at: activity, updated_at: 999999 } as SessionSnapshot)
  const run = session('run', 2), idle = session('idle', 9), tie = session('a', 9)
  state.currentRunIntentBySession.run = { session_id: 'run', run_id: 'work', status: 'running', started_at: 3 }
  state.liveRunsBySession.run = { work: { toolCallsByCallId: { call: { callId: 'call', toolName: 'read', status: 'running', updatedAt: 20 } } } as any }
  let row = projectSessionRow(state, run)
  assert.equal(row.label, 'read')
  assert.equal(row.active, true)
  assert.deepEqual([projectSessionRow(state, idle), row, projectSessionRow(state, tie)].sort(compareProjectSessionRows).map(r => r.session.id), ['run', 'a', 'idle'])
  state.permissionSummaryBySessionId.run = { pendingApprovalCount: 1 } as any
  assert.equal(projectSessionRow(state, run).label, 'Needs approval')
  state.permissionSummaryBySessionId.run = { pendingApprovalCount: 0 } as any
  state.currentRunIntentBySession.run = { session_id: 'run', run_id: 'work', status: 'completed', started_at: 3, completed_at: 5 }
  row = projectSessionRow(state, run)
  assert.equal(row.label, 'Completed')
  assert.equal(row.attention, false)
  assert.equal(row.active, false)
  assert.equal(row.timer?.active, false)
  assert.deepEqual([row, projectSessionRow(state, idle), projectSessionRow(state, tie)].sort(compareProjectSessionRows).map(r => r.session.id), ['a', 'idle', 'run'])
  state.currentRunIntentBySession.run.status = 'failed'
  assert.equal(projectSessionRow(state, run).attention, true)
  assert.equal(projectSessionRow(state, run).label, 'Failed')
  assert.equal(projectSessionRow(state, run, 100).label, 'Archived')
  assert.equal(projectSessionRow(state, run, 100).attention, false)
  state.runIntentsBySession.run = { work: { ...state.currentRunIntentBySession.run, duration_ms: 2000 } }
  delete state.currentRunIntentBySession.run
  assert.equal(projectSessionRow(state, run).label, 'Failed')
  assert.equal(projectSessionRow(state, run).timer?.durationMs, 2000)
  assert.equal(projectSessionRow(state, run).active, false)
})
