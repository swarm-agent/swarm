import assert from 'node:assert/strict'
import test from 'node:test'
import { sessionMutationDecision, type PermissionRule } from './permissions-settings-page'
import { normalizeCapabilityPolicies } from '../../../permissions/services/capability-policy'

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

test('capability settings default session deployment and plan acceptance to ask', () => {
  assert.deepEqual(normalizeCapabilityPolicies(null), {
    session_deploy: { mode: 'ask', automatic_deployments_per_parent_run: 0, over_limit_action: 'ask' },
    plan_acceptance: { mode: 'ask' },
  })
})

test('capability settings preserve bounded deployment and always-allow plan acceptance payloads', () => {
  assert.deepEqual(normalizeCapabilityPolicies({
    session_deploy: { mode: 'bounded', automatic_deployments_per_parent_run: 3, over_limit_action: 'deny' },
    plan_acceptance: { mode: 'always_allow' },
  }), {
    session_deploy: { mode: 'bounded', automatic_deployments_per_parent_run: 3, over_limit_action: 'deny' },
    plan_acceptance: { mode: 'always_allow' },
  })
})
