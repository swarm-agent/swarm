// Requirement: /integrate binds the current session and orders confirmed AI commit before
// exact-HEAD promotion. Threat: wrong-lane writes, stale targets, duplicate invocations,
// and swallowed commit failures. Authority: inspectIntegration/runIntegration and the
// existing review-worktrees API. Injected API tests are the narrowest workflow layer;
// backend ownership and atomic Git application remain server responsibilities.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { inspectIntegration, runIntegration, integrationRepairPrompt, type IntegrationAPI } from './integrate-command'
import type { SessionRepository } from './types'
import type { ReviewWorktreesResponse } from '../session-v3/review-worktrees-api'
import { buildDesktopSlashPaletteState } from '../chat/services/slash-commands'

function fixture(dirty = false) {
  const calls: string[] = []
  const row = { session_id: 'session', active: true, default: false, attached: false, kind: 'parent', availability: 'available', files_truncated: false, source_path: '/repo', workspace_path: '/lane', branch: 'agent/change', status: { workspace_path: '/lane', has_git: true, clean: !dirty, head_oid: 'a'.repeat(40) } } as SessionRepository
  const review = { ok: true, checkout_dirty: false, current_target_branch: 'dev', current_target_head: 'b'.repeat(40), retained: [{ session_id: 'session', worktree_path: '/lane', worktree_branch: row.branch, commit_eligible: dirty, integrate_eligible: !dirty }], done: [] } as unknown as ReviewWorktreesResponse
  const api: IntegrationAPI = {
    repositories: async () => { calls.push('repositories'); return { ok: true, items: [structuredClone(row)], history_coverage: 'complete' } },
    review: async input => { calls.push(input?.promoteSessionIds ? 'promote' : 'review'); if (input?.promoteSessionIds) { assert.deepEqual(input.promoteSessionIds, ['session']); assert.equal(input.targetHead, 'b'.repeat(40)); assert.equal(input.sourceHeadBySessionId?.session, row.status?.head_oid) }; return structuredClone(review) },
    suggest: async input => { calls.push('suggest'); assert.equal(input.sessionId, 'session'); assert.equal(input.workspacePath, '/lane'); return { ok: true, message: 'change', cwd: '/lane', workspace_path: '/lane' } },
    commit: async input => { calls.push('commit'); assert.equal(input.sessionId, 'session'); assert.equal(input.workspacePath, '/lane'); row.status!.clean = true; row.status!.head_oid = 'c'.repeat(40); review.retained[0].integrate_eligible = true; return { ok: true } },
  }
  return { api, calls, row, review }
}

test('integrate is available without developer mode', () => {
  assert.equal(buildDesktopSlashPaletteState('/integrate').exactMatch?.action.kind, 'integrate-session')
})
test('clean source promotes exactly once without AI commit', async () => {
  const f = fixture(); const selected = await inspectIntegration('session', f.api)
  const result = await runIntegration(selected, () => {}, f.api)
  assert.equal(result.integrated, true); assert.equal(result.committed, false)
  assert.equal(f.calls.filter(x => x === 'promote').length, 1); assert.ok(!f.calls.includes('suggest'))
})
test('dirty source commits, refreshes source HEAD, then promotes', async () => {
  const f = fixture(true); const selected = await inspectIntegration('session', f.api); f.calls.length = 0
  const result = await runIntegration(selected, () => {}, f.api)
  assert.equal(result.integrated, true); assert.equal(result.committed, true)
  assert.deepEqual(f.calls, ['repositories', 'review', 'suggest', 'commit', 'repositories', 'review', 'promote'])
})
test('missing, foreign, and ambiguous current worktrees reject without mutation', async () => {
  for (const kind of ['source', 'worker']) { const f = fixture(); f.row.kind = kind; await assert.rejects(inspectIntegration('session', f.api)); assert.deepEqual(f.calls, ['repositories']) }
  const f = fixture(); f.row.session_id = 'foreign'; await assert.rejects(inspectIntegration('session', f.api)); assert.ok(!f.calls.includes('commit'))
  await assert.rejects(inspectIntegration('', f.api))
  const ambiguous = fixture()
  ambiguous.api.repositories = async () => ({ ok: true, items: [ambiguous.row, { ...ambiguous.row }], history_coverage: 'complete' })
  await assert.rejects(inspectIntegration('session', ambiguous.api), /unambiguous/)
})
test('dirty target and stale confirmed target stop before any commit', async () => {
  const f = fixture(true); const selected = await inspectIntegration('session', f.api)
  f.review.current_target_head = 'd'.repeat(40)
  assert.match((await runIntegration(selected, () => {}, f.api)).error!, /changed/)
  f.review.checkout_dirty = true
  await assert.rejects(inspectIntegration('session', f.api), /clean/)
  assert.ok(!f.calls.includes('commit')); assert.ok(!f.calls.includes('promote'))
})
test('AI and commit failures prevent promotion including nonthrowing failure responses', async () => {
  for (const failure of ['suggest', 'commit']) {
    const f = fixture(true); const selected = await inspectIntegration('session', f.api)
    if (failure === 'suggest') f.api.suggest = async () => { throw new Error('provider unavailable') }
    else f.api.commit = async () => ({ ok: false, error: 'commit rejected' })
    const result = await runIntegration(selected, () => {}, f.api)
    assert.ok(result.error); assert.equal(result.committed, false); assert.equal(result.integrated, false); assert.ok(!f.calls.includes('promote'))
  }
})
test('integration failure preserves successful commit and bounded explicit repair evidence', async () => {
  const f = fixture(true); const selected = await inspectIntegration('session', f.api)
  const review = f.api.review; f.api.review = async input => { if (input?.promoteSessionIds) throw new Error('conflict'); return review(input) }
  const result = await runIntegration(selected, () => {}, f.api)
  assert.equal(result.committed, true); assert.equal(result.integrated, false); assert.equal(result.error, 'conflict')
  assert.match(integrationRepairPrompt(selected, result), /Source commit confirmed: true/)
  assert.ok(integrationRepairPrompt(selected, { ...result, error: 'x'.repeat(20000) }).length < 7000)
})
test('concurrent invocation is rejected, not queued or retried', async () => {
  const f = fixture(); const selected = await inspectIntegration('session', f.api)
  let release!: () => void; const gate = new Promise<void>(resolve => { release = resolve })
  const repositories = f.api.repositories; f.api.repositories = async (...args) => { await gate; return repositories(...args) }
  const first = runIntegration(selected, () => {}, f.api)
  await assert.rejects(runIntegration(selected, () => {}, f.api), /already running/)
  release(); assert.equal((await first).integrated, true)
  assert.equal(f.calls.filter(x => x === 'promote').length, 1)
})

// Rebuild extension: no build before successful integration; failure preserves Git success.
test('dev build registration is gated and exact in dev mode', () => {
  assert.notEqual(buildDesktopSlashPaletteState('/integrate build').exactMatch?.id, 'integrate-build')
  assert.equal(buildDesktopSlashPaletteState('/integrate build', { developerMode: true }).exactMatch?.id, 'integrate-build')
})
test('build eligibility failure prevents all Git mutations', async () => {
  const f = fixture(true); const selection = await inspectIntegration('session', f.api); f.calls.length = 0
  const result = await runIntegration(selection, () => {}, f.api, { check: async () => { throw new Error('wrong dev root') }, run: async () => { assert.fail('must not build') } })
  assert.equal(result.integrated, false); assert.match(result.error!, /wrong dev root/); assert.deepEqual(f.calls, [])
})
test('rebuild follows promotion and failure retains integration success without retry', async () => {
  for (const fail of [false, true]) {
    const f = fixture(true); const selection = await inspectIntegration('session', f.api)
    const result = await runIntegration(selection, () => {}, f.api, { check: async () => {}, run: async path => { assert.equal(path, '/repo'); assert.equal(f.calls.at(-1), 'promote'); f.calls.push('build'); if (fail) throw new Error('build failed') } })
    assert.equal(result.integrated, true); assert.equal(result.committed, true); assert.equal(Boolean(result.rebuilt), !fail)
    assert.equal(f.calls.filter(x => x === 'promote').length, 1); assert.equal(f.calls.filter(x => x === 'build').length, 1)
    if (fail) assert.match(integrationRepairPrompt(selection, result), /Integration confirmed: true/)
  }
})
test('integration error never starts rebuild', async () => {
  const f = fixture(); const selection = await inspectIntegration('session', f.api); const review = f.api.review
  f.api.review = async input => { if (input?.promoteSessionIds) throw new Error('conflict'); return review(input) }
  const result = await runIntegration(selection, () => {}, f.api, { check: async () => {}, run: async () => { assert.fail('must not build') } })
  assert.equal(result.integrated, false); assert.match(result.error!, /conflict/)
})

// Requirement: managed lanes are not catalog attachments; paged history must not
// hide the active lane or another candidate. Authority: repository inventory and
// inspectIntegration/repositoryMutationSupported. Injected pages prove selection
// and zero downstream calls on incomplete, ambiguous or unavailable evidence.
test('active managed lane does not require catalog attachment or default flags', async () => {
  const f = fixture()
  assert.equal(f.row.attached, false); assert.equal(f.row.default, false)
  assert.equal((await inspectIntegration('session', f.api)).repository.workspace_path, '/lane')
  assert.deepEqual(f.calls, ['repositories', 'review'])
})
test('active lane on a later page is found using the exact opaque cursor', async () => {
  const f = fixture(); const cursors: string[] = []
  f.api.repositories = async (_session, cursor = '') => {
    cursors.push(cursor)
    return { ok: true, items: cursor ? [f.row] : [{ ...f.row, kind: 'source', active: false, attached: true, default: true }], next_cursor: cursor ? undefined : 'opaque+/=', history_coverage: 'complete' }
  }
  assert.equal((await inspectIntegration('session', f.api)).repository.workspace_path, '/lane')
  assert.deepEqual(cursors, ['', 'opaque+/=']); assert.deepEqual(f.calls, ['review'])
})
test('later ambiguity and incomplete pagination reject before review or mutation', async () => {
  for (const mode of ['ambiguous', 'cycle', 'limit', 'failed']) {
    const f = fixture(); let pages = 0
    f.api.repositories = async () => {
      pages++
      return { ok: mode !== 'failed', items: mode === 'ambiguous' || pages === 1 ? [f.row] : [], next_cursor: mode === 'ambiguous' && pages === 2 ? undefined : mode === 'cycle' ? 'same' : `opaque-${pages}`, history_coverage: 'complete' }
    }
    await assert.rejects(inspectIntegration('session', f.api), /unambiguous|incomplete|unavailable/)
    assert.ok(pages <= 10); assert.deepEqual(f.calls, [])
  }
})
test('inactive and unavailable lanes cannot gain authority from attachment flags', async () => {
  for (const defect of ['inactive', 'unavailable', 'truncated', 'wrong-status-path', 'wrong-branch']) {
    const f = fixture(); f.row.attached = true; f.row.default = true
    if (defect === 'inactive') f.row.active = false
    if (defect === 'unavailable') f.row.availability = 'unavailable'
    if (defect === 'truncated') f.row.files_truncated = true
    if (defect === 'wrong-status-path') f.row.status!.workspace_path = '/another-lane'
    if (defect === 'wrong-branch') f.row.status!.branch = 'agent/another'
    await assert.rejects(inspectIntegration('session', f.api))
    assert.deepEqual(f.calls, ['repositories'])
  }
})
