import { useCallback, useEffect, useState } from 'react'
import { ShieldCheck } from 'lucide-react'
import { requestJson } from '../../../app/api'
import { Button } from '../../../components/ui/button'
import { Card } from '../../../components/ui/card'

const TAILSCALE_ONBOARDING_APPROVAL_PATH = '/v1/onboarding/tailscale-origin'

interface TailscaleOriginApprovalStatus {
  required?: boolean
  origin?: string
}

export function useTailscaleOriginApproval() {
  const [status, setStatus] = useState<TailscaleOriginApprovalStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const next = await requestJson<TailscaleOriginApprovalStatus>(TAILSCALE_ONBOARDING_APPROVAL_PATH, undefined, false)
      setStatus(next)
    } catch (loadError) {
      setStatus(null)
      setError(loadError instanceof Error ? loadError.message : 'Failed to check Tailscale access')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  return { status, loading, error, retry: load }
}

export function TailscaleOriginApprovalScreen({ origin }: { origin: string }) {
  const [approving, setApproving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const approve = async () => {
    setApproving(true)
    setError(null)
    try {
      await requestJson<TailscaleOriginApprovalStatus>(TAILSCALE_ONBOARDING_APPROVAL_PATH, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: '{}',
      }, false)
      window.location.reload()
    } catch (approvalError) {
      setError(approvalError instanceof Error ? approvalError.message : 'Failed to approve this Tailscale address')
      setApproving(false)
    }
  }

  return (
    <div className="fixed inset-0 z-[9999] grid place-items-center bg-[var(--app-bg)] px-6 text-[var(--app-text)]">
      <Card className="w-full max-w-xl border-[var(--app-border)] bg-[var(--app-surface)] p-7 shadow-[var(--shadow-panel)]">
        <div className="flex items-start gap-4">
          <div className="grid size-11 shrink-0 place-items-center rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] text-[var(--app-success)]">
            <ShieldCheck size={23} />
          </div>
          <div className="min-w-0 flex-1">
            <h1 className="text-xl font-semibold tracking-tight">Allow this Tailscale address?</h1>
            <p className="mt-2 text-sm leading-6 text-[var(--app-text-muted)]">
              Swarm verified that this address is the local machine’s Tailscale Serve route. Approving it adds only this exact HTTPS origin to Swarm’s desktop allowlist.
            </p>
            <div className="mt-4 break-all rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 font-mono text-sm">
              {origin}
            </div>
            {error ? <div role="alert" className="mt-4 text-sm text-[var(--app-danger)]">{error}</div> : null}
            <div className="mt-6 flex justify-end">
              <Button type="button" onClick={() => void approve()} disabled={approving}>
                {approving ? 'Approving…' : 'Allow and continue'}
              </Button>
            </div>
          </div>
        </div>
      </Card>
    </div>
  )
}
