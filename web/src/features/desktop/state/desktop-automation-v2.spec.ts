import assert from 'node:assert/strict'
import test from 'node:test'
import { validateAutomationV2, automationV2Review, type AutomationV2Settings } from './desktop-automation-v2-api'
import { reduceAutomationV2Pages, automationV2PageKey, selectAutomationV2Identity, selectPendingAutomationV2Proposals, selectPendingWorkerSidebarReviews, type AutomationV2Pages } from './desktop-automation-v2-state'
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

// Requirement: proposal and revised review appear in the global Workers sidebar
// with the exact permission, even when a different workspace is selected.
// Threat: foreign/settled/tombstoned permissions becoming actionable or a stale
// proposal remaining after revision. The canonical cache selector is the narrowest layer.
test('pending worker sidebar reviews track exact revisions across workspaces', () => {
  const state = createEmptyDesktopV3CacheState()
  const payload = (workspace: string, revision: number) => ({
    review_kind: 'worker_v2', scope: { workspace_id: workspace, account_id: 'account' },
    worker_review: { proposal_id: 'p-' + workspace, revision, digest: String(revision).repeat(64) },
    document: { title: 'Check ' + workspace, info: { goal: 'Review repository status' },
      checkpoints: [{ id: 'cp-1', title: 'Inspect', acceptance_criteria: ['Complete'] }],
      worker_v2: { schema_version: 2, schedule: { kind: 'interval', interval_seconds: 3600 }, expiration: { kind: 'indefinite' }, missed: 'skip', overlap: 'serialize', activate_on_accept: true } },
  })
  state.permissionsBySession.author = [{ id: 'permission_p-a', sessionId: 'author', status: 'pending', requirement: 'automation_v2_acceptance', toolArguments: JSON.stringify(payload('a', 1)) } as any]
  state.permissionsBySession.other = [{ id: 'permission_p-b', sessionId: 'other', status: 'pending', requirement: 'automation_v2_acceptance', toolArguments: JSON.stringify(payload('b', 1)) } as any]
  assert.deepEqual(selectPendingWorkerSidebarReviews(state).map(({ proposal }) => proposal.workspace_id), ['a', 'b'])
  state.permissionsBySession.author[0].toolArguments = JSON.stringify(payload('a', 2))
  assert.equal(selectPendingWorkerSidebarReviews(state)[0].proposal.revision, 2)
  state.permissionsBySession.other[0].status = 'denied'
  assert.deepEqual(selectPendingWorkerSidebarReviews(state).map(({ proposal }) => proposal.session_id), ['author'])
  state.tombstonesBySession.author = { session_id: 'author' } as any
  assert.equal(selectPendingWorkerSidebarReviews(state).length, 0)
})

test('selectPendingAutomationV2Proposals selects pending proposals by workspace and deduplicates', () => {
  const state = createEmptyDesktopV3CacheState()
  const propPayload = {
    review_kind: 'automation_v2',
    scope: { workspace_id: 'ws-1', account_id: 'acct-1' },
    automation_review: { proposal_id: 'prop-1', revision: 1, digest: 'a'.repeat(64) },
    document: {
      title: 'Health Check',
      info: { goal: 'Check repo status' },
      checkpoints: [{ id: 'cp-1', title: 'Check git', acceptance_criteria: ['All clean'] }],
      automation_v2: { schema_version: 2, schedule: { kind: 'interval', interval_seconds: 3600 }, expiration: { kind: 'indefinite' }, missed: 'skip', overlap: 'serialize', activate_on_accept: true },
    },
  }
  const foreignPropPayload = {
    ...propPayload,
    scope: { workspace_id: 'ws-other', account_id: 'acct-1' },
    automation_review: { proposal_id: 'prop-2', revision: 1, digest: 'b'.repeat(64) },
  }

  state.permissionsBySession.s1 = [
    { id: 'perm-1', sessionId: 's1', status: 'pending', requirement: 'automation_v2_acceptance', toolArguments: JSON.stringify(propPayload) } as any,
  ]
  state.permissionsBySession.s2 = [
    { id: 'perm-2', sessionId: 's2', status: 'pending', requirement: 'automation_v2_acceptance', toolArguments: JSON.stringify(foreignPropPayload) } as any,
  ]
  state.permissionsBySession.s3 = [
    { id: 'perm-3', sessionId: 's3', status: 'approved', requirement: 'automation_v2_acceptance', toolArguments: JSON.stringify(propPayload) } as any,
  ]

  const ws1Props = selectPendingAutomationV2Proposals(state, 'ws-1')
  assert.equal(ws1Props.length, 1)
  assert.equal(ws1Props[0].proposal_id, 'prop-1')
  assert.equal(ws1Props[0].session_id, 's1')
  assert.equal(ws1Props[0].document.title, 'Health Check')

  const wsOtherProps = selectPendingAutomationV2Proposals(state, 'ws-other')
  assert.equal(wsOtherProps.length, 1)
  assert.equal(wsOtherProps[0].proposal_id, 'prop-2')

  const allProps = selectPendingAutomationV2Proposals(state)
  assert.equal(allProps.length, 2)
  // A cached review page must not revive a settled or absent permission.
  state.automationV2Pages.old = { input: { action: 'review', workspace_id: 'ws-1', session_id: 's1' }, generation: 1, loading: false, stale: false, data: { proposal: ws1Props[0] } }
  state.permissionsBySession.s1 = []
  assert.equal(selectPendingAutomationV2Proposals(state, 'ws-1').length, 0)
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

// Requirement: pending review remains on the authoring chat, accepted independent
// workers remain in the workers list, and legacy bound workers keep their section.
// Threat: moving an ordinary chat into Workers or losing a cross-workspace review.
test('V2 sidebar keeps authoring chats ordinary while retaining legacy bound workers', () => {
  const state = createEmptyDesktopV3CacheState()
  state.sessionOrderByScope.scope = ['author', 'ordinary', 'hidden']
  for (const id of state.sessionOrderByScope.scope) state.sessionsById[id] = { kind: 'full', session: { id, title: 'Automation title is not identity' }, needsHydrate: false } as any
  state.permissionsBySession.author = [{ status: 'pending', requirement: 'automation_v2_acceptance' } as any]
  state.sessionsById.hidden = { kind: 'full', session: { id: 'hidden', navigation_hidden: true, automation_v2: { automation_id: 'a', workspace_id: 'w', digest: 'd' } }, needsHydrate: false } as any
  let rows = selectDesktopSidebarRows(state, 'scope')
  assert.deepEqual(rows.map(row => [row.sessionId, row.sidebarGroup]), [['author', 'active_chats'], ['ordinary', 'active_chats']])
  state.permissionsBySession.author = []
  assert.equal(selectDesktopSidebarRows(state, 'scope')[0].sidebarGroup, 'active_chats')
  state.automationV2Pages.accepted = { input: { action: 'list', workspace_id: 'w' }, generation: 1, stale: false, loading: false, data: { records: [{ session_id: 'author', workspace_id: 'w', automation_id: 'a' } as any] } }
  assert.equal(selectDesktopSidebarRows(state, 'scope')[0].sidebarGroup, 'active_chats')
  state.sessionsById.author = { kind: 'full', session: { id: 'author', automation_v2: { automation_id: 'a', workspace_id: 'w', digest: 'd' } }, needsHydrate: false } as any
  assert.equal(selectDesktopSidebarRows(state, 'scope')[0].sidebarGroup, 'automation')
  state.tombstonesBySession.author = { session_id: 'author' } as any
  rows = selectDesktopSidebarRows(state, 'scope')
  assert.deepEqual(rows.map(row => row.sessionId), ['ordinary'])
})

test('V2 sidebar groups accepted automation authors created with automation_management purpose', () => {
  const state = createEmptyDesktopV3CacheState()
  state.sessionOrderByScope.scope = ['author-mgmt', 'draft-mgmt']
  state.sessionsById['author-mgmt'] = {
    kind: 'full',
    session: {
      id: 'author-mgmt',
      title: 'Worker plan: test',
      automation_v2: { automation_id: 'av2_123', workspace_id: 'w', digest: 'd' },
      metadata: { swarm_v3_session_purpose: 'automation_management', swarm_v3_purpose_workspace_id: 'w', navigation_hidden: true },
    },
    needsHydrate: false,
  } as any
  state.sessionsById['draft-mgmt'] = {
    kind: 'full',
    session: {
      id: 'draft-mgmt',
      title: 'Unaccepted sidecar draft',
      metadata: { swarm_v3_session_purpose: 'automation_management', swarm_v3_purpose_workspace_id: 'w', navigation_hidden: true },
    },
    needsHydrate: false,
  } as any
  const rows = selectDesktopSidebarRows(state, 'scope')
  assert.deepEqual(rows.map(row => [row.sessionId, row.sidebarGroup]), [['author-mgmt', 'automation']])
})
