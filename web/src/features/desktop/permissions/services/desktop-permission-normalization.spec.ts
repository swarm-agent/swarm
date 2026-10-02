import assert from 'node:assert/strict'
import test from 'node:test'
import { normalizeDesktopPermission, normalizeDesktopPendingPermissions } from './desktop-permission-normalization'

// Purpose: exact resolve responses must retain terminal status/identity for
// submitTaskAttentionDecision while snapshot lists remain pending-only. Testing
// the shared normalizer is the narrowest proof against wrong-session clearing,
// unknown status acceptance, or resolved requests reappearing in pending lists.
test('resolution normalization preserves terminal identity without broadening pending lists', () => {
  const base = { id: 'source', session_id: 'parent', tool_name: 'read', requirement: 'design_source_sensitive_read', updated_at: 5, resolved_at: 5 }
  for (const status of ['approved', 'denied', 'cancelled', 'expired']) {
    const wire = { ...base, status }
    assert.equal(normalizeDesktopPermission(wire, 'parent'), null)
    assert.deepEqual(normalizeDesktopPendingPermissions([wire], 'parent'), [])
    const resolved = normalizeDesktopPermission(wire, 'parent', { includeResolved: true })
    assert.equal(resolved?.id, 'source')
    assert.equal(resolved?.sessionId, 'parent')
    assert.equal(resolved?.status, status)
    assert.equal(resolved?.resolvedAt, 5)
    assert.equal(normalizeDesktopPermission(wire, 'other', { includeResolved: true }), null)
  }
  assert.equal(normalizeDesktopPermission({ ...base, status: 'unknown' }, 'parent', { includeResolved: true }), null)
  assert.equal(normalizeDesktopPermission({ ...base, id: '', status: 'approved' }, 'parent', { includeResolved: true }), null)
  assert.equal(normalizeDesktopPermission({ ...base, status: 'pending', resolved_at: 0 }, 'parent')?.status, 'pending')
})
