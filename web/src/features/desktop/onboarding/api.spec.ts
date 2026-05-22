import assert from 'node:assert/strict'
import test from 'node:test'

import { buildDesktopOnboardingPayload } from './api'

test('desktop onboarding payload keeps username and swarm name separate', () => {
  assert.deepEqual(buildDesktopOnboardingPayload({
    username: 'alice',
    swarmName: 'Alice Laptop',
    swarmMode: true,
    child: false,
  }), {
    username: 'alice',
    swarm_name: 'Alice Laptop',
    swarm_mode: true,
    child: false,
  })
})

test('desktop onboarding payload never invents team fields', () => {
  const payload = buildDesktopOnboardingPayload({
    username: 'alice',
    swarmName: 'Alice Laptop',
  })

  assert.equal(Object.prototype.hasOwnProperty.call(payload, 'team'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(payload, 'team_id'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(payload, 'team_name'), false)
})
