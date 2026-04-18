import { requestJson } from '../../../../app/api'
import { mapCodexOAuthSession } from '../types/auth'
import type { CodexOAuthSession, CodexOAuthSessionWire } from '../types/auth'

export async function getCodexOAuthStatus(sessionID: string): Promise<CodexOAuthSession> {
  const params = new URLSearchParams({ session_id: sessionID.trim() })
  const response = await requestJson<CodexOAuthSessionWire>(`/v1/auth/codex/oauth/status?${params.toString()}`)
  return mapCodexOAuthSession(response)
}
