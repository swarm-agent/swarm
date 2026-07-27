import { requestJson } from '../../../app/api'

export interface CodexUsageWindow {
  used_percent: number
  limit_window_seconds: number
  reset_after_seconds: number
  reset_at: number
}

export interface CodexAccountUsage {
  plan_type: string
  rate_limit?: {
    allowed: boolean
    limit_reached: boolean
    primary_window?: CodexUsageWindow
    secondary_window?: CodexUsageWindow
  }
  rate_limit_reset_credits?: { available_count: number }
}

export interface CodexResetCredit {
  id: string
  reset_type: string
  status: string
  granted_at: string
  expires_at?: string | null
  title?: string | null
  description?: string | null
}

export interface CodexResetCredits {
  credits: CodexResetCredit[]
  available_count: number
}

export type CodexConsumeOutcome = 'reset' | 'already_redeemed' | 'nothing_to_reset' | 'no_credit'

export interface CodexConsumeResetCreditResponse {
  code: CodexConsumeOutcome
  windows_reset: number
}

export function fetchCodexAccountUsage(): Promise<CodexAccountUsage> {
  return requestJson<CodexAccountUsage>('/v1/codex/account/usage')
}

export function fetchCodexResetCredits(): Promise<CodexResetCredits> {
  return requestJson<CodexResetCredits>('/v1/codex/account/reset-credits')
}

export function consumeCodexResetCredit(creditID: string, idempotencyKey: string): Promise<CodexConsumeResetCreditResponse> {
  return requestJson<CodexConsumeResetCreditResponse>('/v1/codex/account/reset-credits/consume', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ credit_id: creditID, idempotency_key: idempotencyKey }),
  })
}
