import test from 'node:test'
import assert from 'node:assert/strict'
import { conversationProjectId, requireProjectConversation, projectConversationLink } from './project-conversations'
import { orchestratorDrafts } from './integration-recovery'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from '../state/desktop-v3-cache-reducer'
import { taskAttentionPermissions } from '../state/task-attention'
import type { SessionSnapshot } from '../state/desktop-v3-cache-types'
import type { DesktopPermissionRecord } from '../types/realtime'

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
