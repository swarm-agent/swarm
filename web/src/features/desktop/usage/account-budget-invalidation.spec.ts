// Purpose: updateUsageLimits is the Desktop account-policy mutation boundary.
// Worker allowance views must repair after success OR a lost/error response,
// without inventing policy/usage state. Hermetic HTTP plus runtime spy is the
// narrowest proof of post-mutation invalidation, not live budget enforcement.
import test from 'node:test'
import assert from 'node:assert/strict'
import { ensureDesktopSession } from '../../../app/api'
import { desktopUsage } from '../runtime/desktop-usage'
import { updateUsageLimits } from './services/usage-api'
test('account policy writes repair demanded budgets even on ambiguous failure', { timeout: 2000 }, async () => {
  const originalFetch = globalThis.fetch, originalInvalidate = desktopUsage.invalidate
  const calls: unknown[][] = []
  let fail = false
  globalThis.fetch = async input => {
    const url = String(input)
    if (url.includes('/v1/auth/desktop/session')) return new Response(JSON.stringify({ user_id: 'fixture-user', account_scope_id: 'fixture-account' }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    return new Response(JSON.stringify(fail ? { error: 'Fixture response lost' } : { ok: true, limits: {} }), { status: fail ? 503 : 200, headers: { 'Content-Type': 'application/json' } })
  }
  desktopUsage.invalidate = (...args) => { calls.push(args) }
  try {
    await ensureDesktopSession(true)
    await updateUsageLimits({ enabled: false, daily_cost_limit_usd: 0 })
    fail = true
    await assert.rejects(updateUsageLimits({ enabled: true, daily_cost_limit_usd: 1 }))
    assert.deepEqual(calls, [['fixture-account', undefined, true], ['fixture-account', undefined, true]])
  } finally { globalThis.fetch = originalFetch; desktopUsage.invalidate = originalInvalidate }
})
