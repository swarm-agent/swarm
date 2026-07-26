import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { BASH_APPROVAL_PROFILES, buildPrefixDenyRulePayload, normalizeBashApprovalProfile, sessionMutationDecision, type PermissionRule } from './permissions-settings-page'
import { normalizeCapabilityPolicies } from '../../../permissions/services/capability-policy'

const settingsSource = readFileSync(new URL('./permissions-settings-page.tsx', import.meta.url), 'utf8')

test('Bash approvals expose exactly four mutually exclusive profiles with safe reads recommended', () => {
  assert.deepEqual(BASH_APPROVAL_PROFILES.map((profile) => profile.label), [
    'Current rules',
    'Allow every read',
    'Allow safe reads',
    'Only critical prompts',
  ])
  assert.equal(BASH_APPROVAL_PROFILES.filter((profile) => profile.recommended).at(0)?.value, 'allow_safe_reads')
  assert.equal(normalizeBashApprovalProfile('only_critical_prompts'), 'only_critical_prompts')
  assert.equal(normalizeBashApprovalProfile('unknown'), 'current_rules')
  assert.deepEqual(BASH_APPROVAL_PROFILES.map((profile) => profile.description), [
    'Use saved permission rules and the existing default Bash behavior.',
    'Auto-approve every read, including reads marked critical.',
    'Auto-approve routine reads; prompt for critical reads and protected changes.',
    'Auto-approve noncritical reads, writes, and updates; prompt for critical operations and every delete.',
  ])
})

test('Bash approvals render immediately below global permissions as disabled radio cards while OFF', () => {
  const globalIndex = settingsSource.indexOf('Global permissions')
  const bashIndex = settingsSource.indexOf('Bash approvals', globalIndex)
  const savedRulesIndex = settingsSource.indexOf('Saved permission rules', bashIndex)
  assert(globalIndex >= 0 && bashIndex > globalIndex && savedRulesIndex > bashIndex)
  assert.match(settingsSource, /role="radiogroup" aria-label="Bash approval profile"/)
  assert.match(settingsSource, /role="radio"/)
  assert.match(settingsSource, /disabled=\{loading \|\| bashProfileBusy \|\| bypassPermissions\}/)
  assert.match(settingsSource, /This profile is preserved and will apply when permissions are turned back ON/)
})

test('Bash profile guidance explains saved-rule precedence', () => {
  assert.match(settingsSource, /Saved rules stay active in every profile/)
  assert.match(settingsSource, /Deny rules override profile auto-approvals/)
  assert.doesNotMatch(settingsSource, /Allow and Ask rules apply when the profile leaves an operation undecided/)
  assert.match(settingsSource, /Profile safety prompts still win over existing Allow rules/)
  assert.match(settingsSource, /Allow every read intentionally does not stop critical reads/)
})

test('saved-rule controls remain present and disabled without mutation while permissions are OFF', () => {
  assert.match(settingsSource, /bypassPermissions && 'opacity-50'/)
  assert.match(settingsSource, /bypassPermissions && 'pointer-events-none opacity-50'/)
  assert.match(settingsSource, /aria-disabled=\{bypassPermissions\}/)
  assert.match(settingsSource, /sortedRules\.map/)
})

test('rule composer only creates Bash prefix deny payloads', () => {
  assert.deepEqual(buildPrefixDenyRulePayload('  gcloud  '), {
    decision: 'deny',
    kind: 'bash_prefix',
    tool: 'bash',
    pattern: 'gcloud',
  })

  const composerStart = settingsSource.indexOf('Add prefix deny rule')
  const composerEnd = settingsSource.indexOf('</section>', composerStart)
  const composerSource = settingsSource.slice(composerStart, composerEnd)
  assert(composerStart >= 0 && composerEnd > composerStart)
  assert.match(composerSource, /Bash command prefix to deny/)
  assert.match(composerSource, /first executable or script name exactly matches the saved prefix/)
  assert.match(composerSource, /Following arguments are not inspected; regex and glob syntax are not supported/)
  assert.doesNotMatch(composerSource, />Decision</)
  assert.doesNotMatch(composerSource, />Match type</)
  assert.doesNotMatch(composerSource, /DECISION_OPTIONS|KIND_OPTIONS/)
})

test('request explainer UI and its supporting code are removed', () => {
  assert.doesNotMatch(settingsSource, /Explain a request|Equivalent to \/permissions explain/)
  assert.doesNotMatch(settingsSource, /PermissionExplain|explainPermission|handleExplain/)
  assert.doesNotMatch(settingsSource, /explainTool|explainArguments|explainResult|explaining/)
})

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
