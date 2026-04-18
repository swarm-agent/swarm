import { requestJson } from '../../../../app/api'
import { mapCodexOAuthSession } from '../types/auth'
import type { CodexOAuthSession, CodexOAuthSessionWire, CompleteCodexOAuthInput } from '../types/auth'

export async function completeCodexOAuth(input: CompleteCodexOAuthInput): Promise<CodexOAuthSession> {
  const response = await requestJson<CodexOAuthSessionWire>('/v1/auth/codex/oauth/complete', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  })

  return mapCodexOAuthSession(response)
}
