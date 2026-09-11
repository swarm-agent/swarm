import assert from 'node:assert/strict'
import test from 'node:test'
import { parseAutomationProposal } from './automation-proposal'
import { submitDesktopComposer } from '../../chat/services/composer-submit'

const body = { action: 'save', workspace_id: 'workspace', id: 'automation', mutation_id: 'mutation', expected_revision: 0, definition: { name: 'Daily report', session_id: 'conversation', enabled: false, plans: [{ id: 'primary', plan: { session_id: 'conversation', plan_id: 'plan', revision: 1 } }], schedule: { kind: 'manual', missed_policy: 'skip', overlap_policy: 'serialize' }, authorization: { mode: 'approval_required' } } }
const wrap = (body: unknown, path = '/v3/automations') => ({ result: { status: 'requires_user_approval', applied: false, proposal: { method: 'POST', path, body } } })
// Requirement: chat accepts only typed automation requests via mutateAutomation,
// never a URL capability from tool output. Threat: arbitrary endpoint execution or
// dropped session/CAS identity. Pure parser is the narrowest request boundary.
test('proposal preserves exact conversation and revision without mutation', () => {
  const source = wrap(body)
  const before = JSON.stringify(source)
  assert.deepEqual(parseAutomationProposal(source), body)
  assert.equal(JSON.stringify(source), before)
  assert.equal(parseAutomationProposal(wrap(body, '/v3/sessions')), null)
  assert.equal(parseAutomationProposal(wrap({ ...body, expected_revision: -1 })), null)
  assert.equal(parseAutomationProposal(wrap({ ...body, extra_authority: true })), null)
  assert.equal(parseAutomationProposal(wrap({ ...body, action: 'delete' })), null)
  assert.equal(parseAutomationProposal({ result: { ...source.result, applied: true } }), null)
})
// Requirement: approval does not execute the separate exact enable proposal.
// Threat: conflating grant creation with enabling. Parser preserves two distinct
// requests and refuses a route/action mismatch; HTTP remains authority.
test('approval and enable are distinct reviewed payloads', () => {
  const approval = { action: 'approve', workspace_id: 'workspace', id: 'automation', mutation_id: 'approve', expected_revision: 1, policy_sha256: 'a'.repeat(64) }
  assert.deepEqual(parseAutomationProposal(wrap(approval, '/v3/automations/approve')), approval)
  assert.equal(parseAutomationProposal(wrap(approval)), null)
  const enable = { ...body, expected_revision: 1, mutation_id: 'enable', definition: { ...body.definition, enabled: true, authorization: { mode: 'approved_policy', approval_reference: 'grant' } } }
  assert.deepEqual(parseAutomationProposal(wrap(enable)), enable)
})
// Requirement: a reservation conflict must retain the unsent conversation draft
// and attachments. submitDesktopComposer clears only after accepted submission.
// This isolated callback test proves no clear side effect on a rejected write.
test('automation reservation rejection retains unsent user input', async () => {
  let cleared = 0
  const attachments = [{ id: 'attachment' }]
  const result = await submitDesktopComposer({ draft: 'Discuss the report', canStop: false, attachments, clear: () => { cleared++ }, onSubmit: async (draft, media) => {
    assert.equal(draft, 'Discuss the report')
    assert.deepEqual(media, attachments)
    throw new Error('409 automation session reserved')
  } })
  assert.equal(result, 'submit-failed')
  assert.equal(cleared, 0)
})
