import { requestJson } from '../../../../app/api'
import { mapCodexOAuthSession } from '../types/auth'
import type { CodexOAuthSession, CodexOAuthSessionWire, StartCodexOAuthInput } from '../types/auth'

export async function startCodexOAuth(input: StartCodexOAuthInput): Promise<CodexOAuthSession> {
  const response = await requestJson<CodexOAuthSessionWire>('/v1/auth/codex/oauth/start', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  })

  return mapCodexOAuthSession(response)
}
