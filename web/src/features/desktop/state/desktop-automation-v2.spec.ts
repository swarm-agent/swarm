import assert from 'node:assert/strict'
import test from 'node:test'
import { validateAutomationV2, automationV2Review, type AutomationV2Settings } from './desktop-automation-v2-api'
import { reduceAutomationV2Pages, automationV2PageKey, selectAutomationV2Identity, type AutomationV2Pages } from './desktop-automation-v2-state'
import { DesktopAutomationV2Runtime } from '../runtime/desktop-automation-v2'
import { createEmptyDesktopV3CacheState } from './desktop-v3-cache-reducer'
import { selectDesktopSidebarRows } from './desktop-v3-cache-selectors'

// Requirement: client validation preserves exact supported timing and expiration.
// Threat: silently altered cadence or hidden cutoff. The typed API validator is
// the narrowest layer; backend authorization remains independently tested in Go.
test('V2 settings preserve indefinite, exact finite and cron semantics', () => {
  const value: AutomationV2Settings = { schema_version: 2, schedule: { kind: 'interval', interval_seconds: 900 }, expiration: { kind: 'indefinite' }, missed: 'skip', overlap: 'serialize', activate_on_accept: true }
  validateAutomationV2(value, 1000)
  assert.deepEqual(value.expiration, { kind: 'indefinite' })
  validateAutomationV2({ ...value, expiration: { kind: 'at', expires_at: 123456 } }, 1000)
  for (const schedule of [{ kind: 'interval', interval_seconds: 1 }, { kind: 'interval', interval_seconds: 900, timezone: 'UTC' }, { kind: 'cron', cron: '0,30 * * * *', timezone: 'UTC' }, { kind: 'cron', cron: '0 9 * * *' }, { kind: 'cron', cron: '0 9 1 * 1', timezone: 'UTC' }]) assert.throws(() => validateAutomationV2({ ...value, schedule } as AutomationV2Settings, 1000))
  validateAutomationV2({ ...value, schedule: { kind: 'cron', cron: '*/15 * * * *', timezone: 'UTC' } }, 1000)
  assert.throws(() => automationV2Review({ proposal_id: 'p', revision: 1, digest: 'invalid' }))
})
// Requirement: canonical V3 state, initial hydration and completion-coalesced
// invalidation have one authority. Threat: late reads erase fresh records or
// chat token chatter triggers polling. Deferred transport exercises real runtime.
test('V2 runtime rejects obsolete and foreign pages and ignores unrelated chatter', async () => {
  let pages: AutomationV2Pages = {}, reads = 0
  const pending: Array<(value: any) => void> = []
  const runtime = new DesktopAutomationV2Runtime({ pages: () => pages, dispatch: action => { pages = reduceAutomationV2Pages(pages, action) }, read: async () => { reads++; return new Promise(resolve => pending.push(resolve)) }, mutate: async () => { throw new Error('denied') } })
  const input = { action: 'list' as const, workspace_id: 'workspace' }, key = automationV2PageKey(input)
  const lease = runtime.acquire(input)
  runtime.acceptFrame({ kind: 'event', event: { event_type: 'session.assistant.delta', session_id: 'other' } })
  assert.equal(reads, 1)
  runtime.invalidate('other'); assert.equal(reads, 1)
  runtime.invalidate('workspace'); runtime.invalidate('workspace')
  pending.shift()!({ records: [], next_cursor: 'obsolete' }); await lease.ready
  assert.equal(reads, 2); assert.equal(pages[key].data, undefined)
  const repair = runtime.refresh(input); pending.shift()!({ records: [], next_cursor: 'current' }); await repair
  assert.equal(pages[key].data?.next_cursor, 'current')
  const foreign = runtime.refresh(input); pending.shift()!({ records: [{ workspace_id: 'foreign' }] }); await foreign
  assert.equal(pages[key].error, 'Automation response scope mismatch'); assert.equal(pages[key].data?.next_cursor, 'current')
  lease.release(); runtime.invalidate(); assert.equal(reads, 3); assert.equal(pages[key], undefined)
})
// Requirement: pending review is labeled before an automation record exists;
// rejection removes only pending identity and accepted binding survives reload.
// Selector assertions cover canonical cache resources, not title heuristics.
test('V2 sidebar identity uses permission and accepted binding, never titles', () => {
  const state = createEmptyDesktopV3CacheState()
  assert.equal(selectAutomationV2Identity(state, 's'), undefined)
  state.permissionsBySession.s = [{ status: 'pending', requirement: 'automation_v2_acceptance' } as any]
  assert.equal(selectAutomationV2Identity(state, 's'), 'pending')
  state.permissionsBySession.s = []
  assert.equal(selectAutomationV2Identity(state, 's'), undefined)
  state.sessionsById.s = { kind: 'full', session: { id: 's', automation_v2: { automation_id: 'a', workspace_id: 'w', digest: 'd' } }, needsHydrate: false } as any
  assert.equal(selectAutomationV2Identity(state, 's'), 'accepted')
})

// Requirement: progress embeds immutable accepted snapshots belonging to the
// requested author, not the occurrence session. Threat: a mixed-scope response
// displays foreign instructions/outcomes. The runtime publication boundary is
// the narrowest layer proving rejection leaves the prior canonical page intact.
test('V2 progress rejects foreign nested accepted snapshots without replacing observed state', async () => {
  let pages: AutomationV2Pages = {}
  const record = { workspace_id: 'workspace', session_id: 'author' }
  const valid = { record, progress: { record, timezone: 'UTC', occurrences: [{ session_id: 'occurrence', accepted: record }] } } as any
  let response = valid
  const runtime = new DesktopAutomationV2Runtime({ pages: () => pages, dispatch: action => { pages = reduceAutomationV2Pages(pages, action) }, read: async () => response, mutate: async () => { throw new Error('unused') } })
  const input = { action: 'progress' as const, workspace_id: 'workspace', session_id: 'author', timezone: 'UTC' }, key = automationV2PageKey(input)
  const lease = runtime.acquire(input)
  await lease.ready
  assert.equal(pages[key].data, valid)
  for (const foreign of [{ ...record, workspace_id: 'foreign' }, { ...record, session_id: 'foreign' }]) {
    response = { ...valid, progress: { ...valid.progress, occurrences: [{ session_id: 'occurrence', accepted: foreign }] } }
    await runtime.refresh(input)
    assert.equal(pages[key].error, 'Automation response scope mismatch')
    assert.equal(pages[key].data, valid)
    assert.equal(pages[key].stale, true)
  }
  lease.release()
})

// Requirement: pending/accepted automation authors occupy the Automation section,
// not Needs Review or Active Chats. Canonical selector state proves grouping;
// rejection, hidden sidechats and tombstones must not leak or duplicate rows.
test('V2 sidebar groups automation authors without exposing hidden or removed sessions', () => {
  const state = createEmptyDesktopV3CacheState()
  state.sessionOrderByScope.scope = ['author', 'ordinary', 'hidden']
  for (const id of state.sessionOrderByScope.scope) state.sessionsById[id] = { kind: 'full', session: { id, title: 'Automation title is not identity' }, needsHydrate: false } as any
  state.permissionsBySession.author = [{ status: 'pending', requirement: 'automation_v2_acceptance' } as any]
  state.sessionsById.hidden = { kind: 'full', session: { id: 'hidden', navigation_hidden: true, automation_v2: { automation_id: 'a', workspace_id: 'w', digest: 'd' } }, needsHydrate: false } as any
  let rows = selectDesktopSidebarRows(state, 'scope')
  assert.deepEqual(rows.map(row => [row.sessionId, row.sidebarGroup]), [['author', 'automation'], ['ordinary', 'active_chats']])
  state.permissionsBySession.author = []
  assert.equal(selectDesktopSidebarRows(state, 'scope')[0].sidebarGroup, 'active_chats')
  state.sessionsById.author = { kind: 'full', session: { id: 'author', automation_v2: { automation_id: 'a', workspace_id: 'w', digest: 'd' } }, needsHydrate: false } as any
  assert.equal(selectDesktopSidebarRows(state, 'scope')[0].sidebarGroup, 'automation')
  state.tombstonesBySession.author = { session_id: 'author' } as any
  rows = selectDesktopSidebarRows(state, 'scope')
  assert.deepEqual(rows.map(row => row.sessionId), ['ordinary'])
})
