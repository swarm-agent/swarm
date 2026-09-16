import test from 'node:test'
import assert from 'node:assert/strict'
import { isAutomationManagementSession } from './desktop-automation-purpose'
import { createAutomationConversation } from './desktop-automation-conversations'
import { isDesktopV3NavigationHiddenSession } from './desktop-v3-session-visibility'
import { createEmptyDesktopV3CacheState, applySnapshot } from './desktop-v3-cache-reducer'
import { selectDesktopSidebarRows } from './desktop-v3-cache-selectors'
import { sessionA, sessionB, snapshotFixture } from './desktop-v3-cache.backend-fixtures'

// Purpose: canonical snapshot hydration must retain automation chats for the
// embedded renderer while ordinary navigation excludes only the complete purpose
// contract. Titles and ordinary automation bindings must not classify a session.
test('automation management survives hydration without entering ordinary navigation', () => {
  const management = { ...sessionB, metadata: { swarm_v3_session_purpose: 'automation_management', swarm_v3_purpose_workspace_id: 'workspace' } }
  assert.equal(isAutomationManagementSession(management, 'workspace'), true)
  assert.equal(isAutomationManagementSession(management, 'foreign'), false)
  assert.equal(isDesktopV3NavigationHiddenSession(management), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, title: 'Automation planning', metadata: { automation_id: 'saved' } }), false)
  assert.equal(isAutomationManagementSession({ ...management, metadata: { swarm_v3_session_purpose: 'automation_management' } }), false)
  const state = createEmptyDesktopV3CacheState()
  const snapshot = snapshotFixture({ sessions_by_id: { [sessionA.id]: sessionA, [management.id]: management }, projections_by_session: {}, messages_by_session: {}, run_intents_by_session: {}, session_order: [management.id, sessionA.id] })
  applySnapshot(state, { source: 'bootstrap', scopeId: snapshot.scope_id, snapshot })
  assert.deepEqual(selectDesktopSidebarRows(state).map(row => row.sessionId), [sessionA.id])
  assert.ok(state.sessionsById[management.id])
})

test('createAutomationConversation sends valid payload with mode plan and no forbidden fields', async () => {
  const originalFetch = globalThis.fetch
  let capturedUrl = ''
  let capturedInit: RequestInit | undefined
  let capturedBody: any = null

  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    capturedUrl = String(input)
    capturedInit = init
    if (init?.body) {
      capturedBody = JSON.parse(String(init.body))
    }
    const mockSession = {
      id: 'session-created-1',
      workspace_path: '/path/to/work',
      workspace_name: 'work',
      title: 'Automation session',
      mode: 'plan',
      preference: { provider: 'codex', model: 'plan-model', thinking: 'high', updated_at: 0 },
      metadata: {
        swarm_v3_session_purpose: 'automation_management',
        swarm_v3_purpose_workspace_id: 'ws-123',
        navigation_hidden: true,
      },
    }
    return new Response(JSON.stringify({ ok: true, session: mockSession }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as any

  try {
    const session = await createAutomationConversation(
      '/path/to/work',
      'req-client-1',
      'ws-123',
      'Custom Title',
      'binding-abc',
    )

    assert.equal(capturedUrl, '/v3/sessions')
    assert.equal(capturedInit?.method, 'POST')
    assert.ok(capturedBody)
    assert.equal(capturedBody.client_request_id, 'req-client-1')
    assert.equal(capturedBody.purpose, 'automation_management')
    assert.equal(capturedBody.workspace_path, '/path/to/work')
    assert.equal(capturedBody.workspace_binding_id, 'binding-abc')
    assert.equal(capturedBody.workspace_id, 'ws-123')
    assert.equal(capturedBody.agent_name, 'swarm')
    assert.equal(capturedBody.title, 'Custom Title')
    // Must use mode plan so Swarm uses the plan agent's model
    assert.equal(capturedBody.mode, 'plan')
    // Must NOT send forbidden / unknown fields
    assert.equal('navigation_hidden' in capturedBody, false)
    assert.equal('worktree_mode' in capturedBody, false)
    assert.equal('worktree_branch_name' in capturedBody, false)
    assert.equal('metadata' in capturedBody, false)

    assert.equal(session.id, 'session-created-1')
    assert.equal(session.mode, 'plan')
    assert.equal(session.preference?.model, 'plan-model')
  } finally {
    globalThis.fetch = originalFetch
  }
})
