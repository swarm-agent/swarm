import test from 'node:test'
import assert from 'node:assert/strict'
import { projectStartupState } from './project-startup'
import { admittedConversationId } from './project-entry-policy'

// Purpose: projectStartupState and admittedConversationId own reveal/provenance.
// Optional task failure must not gate chat, while unresolved catalog/auth/route
// must never admit it. Pure policy tests are the narrowest proof of these gates.
test('task readiness is independent but route and account admission remain mandatory', () => {
  const base = { catalogLoaded: true, catalogError: '', routeError: '', projectId: 'p', tasksObserved: false }
  assert.equal(projectStartupState(base).phase, 'ready')
  assert.equal(projectStartupState({ ...base, tasksError: 'failed' }).phase, 'ready')
  assert.equal(projectStartupState({ ...base, catalogLoaded: false }).phase, 'loading')
  assert.equal(projectStartupState({ ...base, catalogError: 'unauthorized' }).phase, 'error')
  assert.equal(projectStartupState({ ...base, routeError: 'unknown project' }).phase, 'error')
  const admission = { projectId: 'p', sessionId: 'a', accountScopeId: 'account-a' }
  assert.equal(admittedConversationId(admission, 'p', 'a', 'account-a'), 'a')
  assert.equal(admittedConversationId(admission, 'p', 'b', 'account-a'), '')
  assert.equal(admittedConversationId(admission, 'q', 'a', 'account-a'), '')
  assert.equal(admittedConversationId(admission, 'p', 'a', 'account-b'), '')
  assert.equal(admittedConversationId(admission, 'p', 'a'), '')
})
