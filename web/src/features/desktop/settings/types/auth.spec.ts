import assert from 'node:assert/strict'
import test from 'node:test'

import { mapCodexOAuthSession } from './auth'

test('maps Codex device-code session fields from the authenticated API', () => {
  assert.deepEqual(mapCodexOAuthSession({
    session_id: 'oauth-session',
    provider: 'codex',
    method: 'device',
    verification_url: 'https://auth.openai.com/codex/device',
    user_code: 'ABCD-EFGH',
    expires_at: 1_785_000_000,
    status: 'waiting',
  }), {
    sessionID: 'oauth-session',
    provider: 'codex',
    method: 'device',
    label: '',
    active: false,
    authURL: '',
    verificationURL: 'https://auth.openai.com/codex/device',
    userCode: 'ABCD-EFGH',
    expiresAt: 1_785_000_000,
    status: 'waiting',
    error: '',
    credential: undefined,
  })
})
