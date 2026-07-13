import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { LoaderCircle, RefreshCcw } from 'lucide-react'
import { Button } from '../../../components/ui/button'
import { Card } from '../../../components/ui/card'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import {
  consumeCodexResetCredit,
  fetchCodexAccountUsage,
  fetchCodexResetCredits,
  type CodexAccountUsage,
  type CodexResetCredit,
  type CodexResetCredits,
  type CodexUsageWindow,
} from './api'
import {
  exactResetTime,
  remainingUsagePercent,
  resetCreditExpiry,
  resetCreditExpired,
  resetCreditRedeemable,
  sortResetCreditsByExpiry,
  usageWindowLabel,
} from './view-model'

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function LimitCard({ window, fallback }: { window?: CodexUsageWindow; fallback: string }) {
  if (!window) return <Card className="p-5 text-sm text-[var(--app-text-muted)]">{fallback} usage is not supplied for this plan.</Card>
  const remaining = remainingUsagePercent(window)
  return (
    <Card className="space-y-4 p-5">
      <div className="flex items-baseline justify-between gap-3">
        <h3 className="font-semibold text-[var(--app-text)]">{usageWindowLabel(window.limit_window_seconds, fallback)}</h3>
        <span className="text-2xl font-semibold text-[var(--app-text)]">{remaining}%</span>
      </div>
      <div className="h-2 overflow-hidden rounded-full bg-[var(--app-surface-hover)]" aria-label={`${remaining}% remaining`}>
        <div className="h-full rounded-full bg-[var(--app-primary)] transition-[width]" style={{ width: `${remaining}%` }} />
      </div>
      <div className="text-xs text-[var(--app-text-muted)]">Remaining · exact reset {exactResetTime(window.reset_at)}</div>
    </Card>
  )
}

export function DesktopCodexUsageModal({ open, onOpenChange, onOpenAuthSettings }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onOpenAuthSettings: () => void
}) {
  const [usage, setUsage] = useState<CodexAccountUsage | null>(null)
  const [credits, setCredits] = useState<CodexResetCredits | null>(null)
  const [usageError, setUsageError] = useState<string | null>(null)
  const [creditsError, setCreditsError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [busyCredit, setBusyCredit] = useState<string | null>(null)
  const [consumeMessage, setConsumeMessage] = useState<Record<string, string>>({})
  const redeemKeys = useRef(new Map<string, string>())

  const refresh = useCallback(async () => {
    setLoading(true)
    setUsageError(null)
    setCreditsError(null)
    const [usageResult, creditsResult] = await Promise.allSettled([fetchCodexAccountUsage(), fetchCodexResetCredits()])
    if (usageResult.status === 'fulfilled') setUsage(usageResult.value)
    else setUsageError(errorText(usageResult.reason))
    if (creditsResult.status === 'fulfilled') setCredits(creditsResult.value)
    else setCreditsError(errorText(creditsResult.reason))
    setLoading(false)
  }, [])

  useEffect(() => {
    if (open) void refresh()
  }, [open, refresh])

  const sortedCredits = useMemo(() => sortResetCreditsByExpiry(credits?.credits ?? []), [credits])

  const consume = useCallback(async (credit: CodexResetCredit) => {
    if (busyCredit) return
    let key = redeemKeys.current.get(credit.id)
    if (!key) {
      key = typeof crypto !== 'undefined' && 'randomUUID' in crypto ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`
      redeemKeys.current.set(credit.id, key)
    }
    setBusyCredit(credit.id)
    setConsumeMessage((current) => ({ ...current, [credit.id]: '' }))
    try {
      const result = await consumeCodexResetCredit(credit.id, key)
      if (result.code === 'reset' || result.code === 'already_redeemed') {
        redeemKeys.current.delete(credit.id)
        setConsumeMessage((current) => ({ ...current, [credit.id]: result.code === 'reset' ? 'Usage window reset.' : 'This reset was already used.' }))
        await refresh()
      } else {
        setConsumeMessage((current) => ({ ...current, [credit.id]: result.code === 'nothing_to_reset' ? 'No reached usage window needs resetting.' : 'This reset is no longer available. Refresh for current credits.' }))
      }
    } catch (error) {
      setConsumeMessage((current) => ({ ...current, [credit.id]: `${errorText(error)} Retry uses the same redemption key.` }))
    } finally {
      setBusyCredit(null)
    }
  }, [busyCredit, refresh])

  if (!open) return null
  const authRequired = `${usageError ?? ''} ${creditsError ?? ''}`.toLowerCase().includes('oauth')
  const initialLoading = !usage && !credits && !usageError && !creditsError
  const weeklyWindow = [usage?.rate_limit?.primary_window, usage?.rate_limit?.secondary_window]
    .find((window) => window?.limit_window_seconds === 7 * 24 * 60 * 60)
  return (
    <Dialog role="dialog" aria-modal="true" aria-label="Codex usage" className="z-[85] p-3 sm:p-6">
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="max-h-[calc(100dvh-24px)] w-[min(900px,calc(100vw-24px))] gap-0 overflow-hidden rounded-2xl p-0 sm:max-h-[calc(100dvh-48px)]">
        <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-5 py-4 sm:px-6">
          <div><h2 className="text-lg font-semibold text-[var(--app-text)]">Codex usage</h2><p className="mt-1 text-sm text-[var(--app-text-muted)]">ChatGPT plan: {usage?.plan_type || 'Unavailable'}</p></div>
          <div className="flex gap-2"><Button variant="secondary" size="sm" onClick={() => void refresh()} disabled={loading}><RefreshCcw size={14} className={loading ? 'animate-spin' : ''} /> Refresh</Button><Button variant="ghost" size="sm" onClick={() => onOpenChange(false)}>Close</Button></div>
        </div>
        <div className="min-h-0 overflow-y-auto p-5 sm:p-6">
          {initialLoading ? (
            <div className="grid min-h-[280px] place-items-center" role="status" aria-label="Loading Codex account usage">
              <LoaderCircle size={36} strokeWidth={1.8} className="animate-spin text-[var(--app-primary)]" aria-hidden="true" />
            </div>
          ) : (
            <>
          {usageError ? <Card className="mb-4 border-[var(--app-error)] p-4 text-sm text-[var(--app-error)]">Usage could not be loaded: {usageError} <Button className="ml-2" variant="secondary" size="sm" onClick={authRequired ? onOpenAuthSettings : () => void refresh()}>{authRequired ? 'Open Auth settings' : 'Retry'}</Button></Card> : null}
          <LimitCard window={weeklyWindow} fallback="Weekly" />
          <div className="mt-7 flex items-baseline justify-between gap-3"><h3 className="text-base font-semibold text-[var(--app-text)]">Usage-limit resets</h3><span className="text-xs text-[var(--app-text-muted)]">{credits?.available_count ?? usage?.rate_limit_reset_credits?.available_count ?? 0} available</span></div>
          {creditsError ? <Card className="mt-3 border-[var(--app-error)] p-4 text-sm text-[var(--app-error)]">Resets could not be loaded: {creditsError} <Button className="ml-2" variant="secondary" size="sm" onClick={authRequired ? onOpenAuthSettings : () => void refresh()}>{authRequired ? 'Open Auth settings' : 'Retry'}</Button></Card> : null}
          {!creditsError && !loading && sortedCredits.length === 0 ? <Card className="mt-3 p-5 text-sm text-[var(--app-text-muted)]">No reset credits are available for this account.</Card> : null}
          <div className="mt-3 space-y-3">
            {sortedCredits.map((credit) => {
              const expired = resetCreditExpired(credit)
              const available = resetCreditRedeemable(credit)
              const busy = busyCredit === credit.id
              return <Card key={credit.id} className="p-4"><div className="flex flex-col justify-between gap-4 sm:flex-row sm:items-center"><div className="min-w-0"><div className="flex flex-wrap items-center gap-2"><span className="font-medium text-[var(--app-text)]">{credit.title || 'Usage-limit reset'}</span><span className="text-xs uppercase text-[var(--app-text-subtle)]">{expired ? 'expired' : credit.status}</span></div><p className="mt-1 text-sm text-[var(--app-text-muted)]">{credit.description || credit.reset_type}</p><p className="mt-2 text-xs text-[var(--app-text-subtle)]">Expires: {resetCreditExpiry(credit)}</p>{consumeMessage[credit.id] ? <p className="mt-2 text-xs text-[var(--app-text-muted)]" role="status">{consumeMessage[credit.id]}</p> : null}</div><Button size="sm" className="shrink-0" disabled={!available || busy || Boolean(busyCredit && !busy)} onClick={() => void consume(credit)}>{busy ? 'Using…' : 'Use reset'}</Button></div></Card>
            })}
          </div>
            </>
          )}
        </div>
      </DialogPanel>
    </Dialog>
  )
}
