import assert from 'node:assert/strict'
import test from 'node:test'
import { sessionMutationDecision, type PermissionRule } from './permissions-settings-page'

test('session mutation settings keep commit, archive, and unarchive policies isolated', () => {
  const rules: PermissionRule[] = [
    { id: 'commit', kind: 'tool', decision: 'allow', tool: 'session_commit' },
    { id: 'archive', kind: 'tool', decision: 'deny', tool: 'session_archive' },
    { id: 'generic', kind: 'tool', decision: 'allow', tool: 'manage_sessions' },
  ]

  assert.equal(sessionMutationDecision(rules, 'session_commit'), 'allow')
  assert.equal(sessionMutationDecision(rules, 'session_archive'), 'deny')
  assert.equal(sessionMutationDecision(rules, 'session_unarchive'), 'ask')
})
