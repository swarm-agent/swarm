import test from 'node:test'
import assert from 'node:assert/strict'
import { conversationProjectId, requireProjectConversation, projectConversationLink, projectConversationMessageMetadata } from './project-conversations'
import { orchestratorDrafts } from './integration-recovery'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from '../state/desktop-v3-cache-reducer'
import { taskAttentionPermissions, submitTaskAttentionDecision } from '../state/task-attention'
import { buildAskUserResolutionReason, parseAskUserPermission } from '../permissions/services/permission-payload'
import type { SessionSnapshot } from '../state/desktop-v3-cache-types'
import type { DesktopPermissionRecord } from '../types/realtime'

// Purpose: OrchestrateView uses this payload for sends and chat retries. The V3
// API reserves project_id for session authority; echoing it prevents messaging.
// This pure payload assertion complements the real HTTP/store rejection test.
test('project composer metadata never re-submits session project authority', () => {
  assert.deepEqual(projectConversationMessageMetadata(), { orchestrate_view: true })
})

function session(id: string, project = 'project-a'): SessionSnapshot {
  return { id, title: '', workspace_path: '', workspace_name: '', mode: 'auto', created_at: 1, updated_at: 1, message_count: 0, last_message_at: 0,
    metadata: { project_id: project, swarm_v3_project_id: project, agent_name: 'system-orchestrator' } }
}

// Purpose: project-conversations is the route admission boundary. A guessed project,
// conflicting provenance or delegated child must not mount another conversation.
// Pure boundary tests are the narrowest proof of rejection before UI hydration.
test('project deep links require consistent project-owned Orchestrator provenance', () => {
  const own = session('one')
  assert.doesNotThrow(() => requireProjectConversation('project-a', own))
  assert.throws(() => requireProjectConversation('project-b', own), /does not belong/)
  for (const metadata of [
    { ...own.metadata, task_id: 'task' },
    { ...own.metadata, parent_session_id: 'parent' },
    { ...own.metadata, swarm_v3_project_id: 'project-b' },
    { ...own.metadata, agent_name: 'system-coder' },
    { project_id: 'project-a' },
  ]) {
    assert.equal(conversationProjectId({ metadata }), '')
    assert.throws(() => requireProjectConversation('project-a', { ...own, metadata }))
  }
  assert.equal(own.metadata?.project_id, 'project-a', 'rejection never reparents history')
  assert.deepEqual(projectConversationLink('project-a', 'one'), { to: '/projects/$projectId/sessions/$sessionId', params: { projectId: 'project-a', sessionId: 'one' } })
})

// Purpose: legacy history resolves only persisted project/agent membership, never
// workspace or display names. Unowned history remains an explicit selection.
test('legacy history has no inferred project; verified legacy Orchestrator stays discoverable', () => {
  assert.equal(conversationProjectId({ metadata: { workspace_path: 'shared', title: 'project-a' } }), '')
  assert.equal(conversationProjectId({ metadata: { project_id: 'project-a', agent_name: 'system-orchestrator' } }), 'project-a')
  assert.deepEqual(projectConversationLink('project-a'), { to: '/projects/$projectId', params: { projectId: 'project-a' } })
})

// Purpose: the composer draft store must isolate unsent text by project/session;
// switching and clearing one conversation cannot overwrite another draft.
test('conversation drafts remain independent across switches', () => {
  orchestratorDrafts.set('project-a:one', 'first draft')
  orchestratorDrafts.set('project-a:two', 'second draft')
  orchestratorDrafts.set('project-b:one', 'other project')
  orchestratorDrafts.set('project-a:two', '')
  assert.equal(orchestratorDrafts.get('project-a:one').text, 'first draft')
  assert.equal(orchestratorDrafts.get('project-b:one').text, 'other project')
  assert.equal(orchestratorDrafts.get('project-a:two').text, '')
})

// Purpose: SessionPermissionAttention uses taskAttentionPermissions and the canonical
// reducer. Switching cannot consume another session's ask-user request; deny and
// stale responses cannot resurrect it. This state layer proves retained ownership.
test('pending choices survive session switching and deny rejects stale resurrection', () => {
  let state = createEmptyDesktopV3CacheState()
  for (const id of ['one', 'two']) {
    state.sessionsById[id] = { kind: 'full', needsHydrate: false, session: session(id) }
    state.permissionsBySession[id] = [{ id: 'question', sessionId: id, toolName: 'ask_user', status: 'pending', updatedAt: 1,
      toolArguments: JSON.stringify({ questions: [{ question: 'Choose', options: ['A', 'B'], custom_response: true }] }),
    } as DesktopPermissionRecord]
  }
  const pending = state.permissionsBySession.one[0]
  state.selectedSessionId = 'two'
  assert.equal(taskAttentionPermissions(state, ['one'])[0], pending)
  assert.equal(taskAttentionPermissions(state, ['two'])[0].sessionId, 'two')
  state = desktopV3CacheReducer(state, { type: 'permission.resolveResult', sessionId: 'one', permissionId: 'question', permission: { ...pending, status: 'denied', updatedAt: 5, resolvedAt: 5 } })
  state = desktopV3CacheReducer(state, { type: 'permission.resolveResult', sessionId: 'one', permissionId: 'question', permission: pending })
  assert.equal(taskAttentionPermissions(state, ['one']).length, 0)
  assert.equal(taskAttentionPermissions(state, ['two']).length, 1)
})

// Purpose: the project composer uses parseAskUserPermission, the same answer serializer
// as DesktopPermissionModal, and submitTaskAttentionDecision. This bounded state test
// proves custom answers keep their origin after selection changes, while changed
// run/call identities and mismatched server receipts cannot mutate either session.
test('project custom answers retain exact request identity across session switching', async () => {
  let state = createEmptyDesktopV3CacheState()
  const pending = {
    id: 'question', sessionId: 'one', runId: 'run-one', callId: 'ask-one',
    toolName: 'ask_user', status: 'pending', updatedAt: 1, createdAt: 1,
    toolArguments: JSON.stringify({ questions: [
      { id: 'direction', question: 'Which direction?', options: ['A', 'B'], required: true },
      { id: 'scope', question: 'Which scope?', options: ['Small', 'Large'], required: true },
    ] }),
  } as DesktopPermissionRecord
  for (const id of ['one', 'two']) {
    state.sessionsById[id] = { kind: 'full', needsHydrate: false, session: session(id) }
    state.permissionsBySession[id] = [{ ...pending, sessionId: id }]
  }
  state.selectedSessionId = 'two'
  const payload = parseAskUserPermission(pending)
  assert.ok(payload.questions.every(question => question.options.some(option => option.allowCustom)))
  assert.equal(buildAskUserResolutionReason(payload, { direction: 'Custom direction' }), null)
  const reason = buildAskUserResolutionReason(payload, { direction: 'Custom direction', scope: 'Small' })!
  assert.deepEqual(JSON.parse(reason).answers, { direction: 'Custom direction', scope: 'Small' })
  let calls = 0
  let commits = 0
  const resolved = { ...pending, status: 'approved', updatedAt: 2, resolvedAt: 2, reason }
  const deps = {
    getState: () => state,
    resolve: async (sid: string, id: string, action: string, answer: string) => {
      calls++
      assert.deepEqual([sid, id, action, answer], ['one', 'question', 'approve', reason])
      return resolved
    },
    commit: (permission: DesktopPermissionRecord | null) => {
      commits++
      state = desktopV3CacheReducer(state, { type: 'permission.resolveResult', sessionId: 'one', permissionId: 'question', permission })
    },
  }
  for (const changed of [{ runId: 'new-run' }, { callId: 'new-call' }]) {
    state.permissionsBySession.one = [{ ...pending, ...changed }]
    await assert.rejects(submitTaskAttentionDecision(pending, 'approve', reason, undefined, deps), /changed/)
  }
  assert.equal(calls, 0)
  state.permissionsBySession.one = [pending]
  for (const changed of [{ sessionId: 'two' }, { id: 'other' }, { runId: 'new-run' }, { callId: 'new-call' }]) {
    await assert.rejects(submitTaskAttentionDecision(pending, 'approve', reason, undefined, {
      ...deps, resolve: async () => ({ ...resolved, ...changed }),
    }), /resolved request/)
  }
  assert.equal(commits, 0)
  assert.equal(taskAttentionPermissions(state, ['one']).length, 1)
  await submitTaskAttentionDecision(pending, 'approve', reason, undefined, deps)
  assert.equal(calls, 1)
  assert.equal(commits, 1)
  assert.equal(taskAttentionPermissions(state, ['one']).length, 0)
  assert.equal(taskAttentionPermissions(state, ['two']).length, 1)
})
