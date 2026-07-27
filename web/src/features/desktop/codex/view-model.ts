import type { CodexResetCredit, CodexUsageWindow } from './api'

export function exactResetTime(unixSeconds: number): string {
  return Number.isFinite(unixSeconds) && unixSeconds > 0 ? new Date(unixSeconds * 1000).toLocaleString() : 'Not provided'
}

export function usageWindowLabel(seconds: number, fallback: string): string {
  if (seconds === 300 * 60) return '5-hour'
  if (seconds === 10_080 * 60) return 'Weekly'
  if (seconds > 0 && seconds % 86_400 === 0) return `${seconds / 86_400}-day`
  if (seconds > 0 && seconds % 3_600 === 0) return `${seconds / 3_600}-hour`
  return fallback
}

export function remainingUsagePercent(window: CodexUsageWindow): number {
  return Math.max(0, Math.min(100, 100 - (Number.isFinite(window.used_percent) ? window.used_percent : 0)))
}

export function resetCreditExpired(credit: CodexResetCredit, now = Date.now()): boolean {
  if (!credit.expires_at) return false
  const value = Date.parse(credit.expires_at)
  return Number.isFinite(value) && value <= now
}

export function resetCreditExpiry(credit: CodexResetCredit): string {
  if (!credit.expires_at) return 'Does not expire'
  const value = Date.parse(credit.expires_at)
  return Number.isFinite(value) ? new Date(value).toLocaleString() : credit.expires_at
}

export function resetCreditRedeemable(credit: CodexResetCredit, now = Date.now()): boolean {
  return credit.status.toLowerCase() === 'available' && !resetCreditExpired(credit, now)
}

export function sortResetCreditsByExpiry(credits: CodexResetCredit[]): CodexResetCredit[] {
  return credits.slice().sort((a, b) => {
    const left = a.expires_at ? Date.parse(a.expires_at) : Number.POSITIVE_INFINITY
    const right = b.expires_at ? Date.parse(b.expires_at) : Number.POSITIVE_INFINITY
    return (Number.isFinite(left) ? left : Number.POSITIVE_INFINITY) - (Number.isFinite(right) ? right : Number.POSITIVE_INFINITY)
  })
}
