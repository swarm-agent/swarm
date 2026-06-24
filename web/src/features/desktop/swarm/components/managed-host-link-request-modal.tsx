import { Link2, X } from 'lucide-react'
import { useMemo } from 'react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import type { RemoteSwarmPendingPairing } from '../../onboarding/api'
import type { SwarmTarget } from '../api/swarm-targets'
import { ManagedHostWorkspaceLinkPanel } from './managed-host-workspace-replication-panel'

interface ManagedHostLinkRequestModalProps {
  open: boolean
  requests: RemoteSwarmPendingPairing[]
  busyID: string | null
  confirmations: Record<string, boolean>
  error: string | null
  status: string | null
  now: number
  linkReviewTarget: SwarmTarget | null
  linkReviewBusy?: boolean
  onOpenChange: (open: boolean) => void
  onConfirmationChange: (requestID: string, confirmed: boolean) => void
  onDecision: (request: RemoteSwarmPendingPairing, approve: boolean) => void
  onLinkReviewComplete: (message: string) => Promise<void> | void
  onLinkReviewSkip: (message: string) => Promise<void> | void
}

function normalizePairingCode(value: string | null | undefined): string {
  return String(value ?? '').trim().replace(/[^a-zA-Z0-9]/g, '').toUpperCase().slice(0, 6)
}

function formatPairingCode(value: string | null | undefined): string {
  const normalized = normalizePairingCode(value)
  return normalized.length === 6 ? `${normalized.slice(0, 3)}-${normalized.slice(3)}` : normalized
}

function formatRelativeTime(timestampMs: number, nowMs: number): string {
  if (!Number.isFinite(timestampMs) || timestampMs <= 0) return 'recently'
  const diffMs = Math.max(0, nowMs - timestampMs)
  const minutes = Math.floor(diffMs / 60_000)
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return `${days}d ago`
}

export function activePendingPairings(items: RemoteSwarmPendingPairing[]): RemoteSwarmPendingPairing[] {
  return items.filter((item) => item.status === 'pending_approval' || item.status === '')
}

export function managedHostTargetFromPairingResult(input: {
  request: RemoteSwarmPendingPairing
  result: { status?: string; routing?: { managed_swarm_id?: string; managed_name?: string; backend_url?: string } }
}): SwarmTarget | null {
  const managedSwarmID = (input.result.routing?.managed_swarm_id || input.request.managed_swarm_id || '').trim()
  if (!managedSwarmID) return null
  const managedName = (input.result.routing?.managed_name || input.request.managed_name || managedSwarmID).trim()
  const backendURL = (input.result.routing?.backend_url || input.request.managed_endpoint || '').trim()
  return {
    swarm_id: managedSwarmID,
    name: managedName,
    role: 'managed',
    relationship: 'managed',
    kind: 'host',
    attach_status: input.result.status || 'paired',
    online: true,
    selectable: true,
    current: false,
    backend_url: backendURL,
  }
}

export function ManagedHostLinkRequestModal({
  open,
  requests,
  busyID,
  confirmations,
  error,
  status,
  now,
  linkReviewTarget,
  linkReviewBusy = false,
  onOpenChange,
  onConfirmationChange,
  onDecision,
  onLinkReviewComplete,
  onLinkReviewSkip,
}: ManagedHostLinkRequestModalProps) {
  const title = linkReviewTarget ? `Link workspaces on ${linkReviewTarget.name || linkReviewTarget.swarm_id}` : 'Link request'
  const visibleRequests = useMemo(() => activePendingPairings(requests), [requests])

  if (!open) return null
  return (
    <Dialog>
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="mx-auto mt-[4vh] flex w-[min(920px,calc(100vw-24px))] max-w-[920px] flex-col overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-[var(--shadow-panel)]">
        <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-5 py-4">
          <div>
            <div className="flex items-center gap-2 text-lg font-semibold text-[var(--app-text)]">
              <Link2 size={18} className="text-[var(--app-primary)]" />
              {title}
            </div>
            <p className="mt-1 text-sm text-[var(--app-text-muted)]">
              {linkReviewTarget
                ? 'Managed Host linked. Review its live inventory, then link existing workspaces or transfer only missing git workspaces.'
                : 'Confirm the ceremony code on both machines before approving.'}
            </p>
          </div>
          <Button variant="ghost" size="sm" className="h-8 w-8 min-w-8 p-0" onClick={() => onOpenChange(false)} aria-label="Close link requests">
            <X size={16} />
          </Button>
        </div>
        <div className="grid max-h-[78vh] gap-3 overflow-y-auto px-5 py-4">
          {error ? <Card className="border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] p-3 text-sm text-[var(--app-danger)]">{error}</Card> : null}
          {status ? <Card className="border-[var(--app-success-border)] bg-[var(--app-success-bg)] p-3 text-sm text-[var(--app-success)]">{status}</Card> : null}

          {linkReviewTarget ? (
            <ManagedHostWorkspaceLinkPanel
              target={linkReviewTarget}
              busy={linkReviewBusy}
              onComplete={onLinkReviewComplete}
              onSkip={onLinkReviewSkip}
            />
          ) : visibleRequests.length === 0 ? (
            <Card className="border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">
              No pending Managed Host link requests.
            </Card>
          ) : visibleRequests.map((request) => {
            const requestID = request.request_id.trim()
            const busy = busyID === requestID
            const code = normalizePairingCode(request.ceremony_code)
            const confirmed = confirmations[requestID] === true
            return (
              <Card key={requestID || request.managed_swarm_id || request.managed_name} className="grid gap-3 border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-4">
                <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                  <div className="min-w-0">
                    <div className="font-semibold text-[var(--app-text)]">{request.managed_name || 'Managed Host'}</div>
                    <div className="mt-1 break-all text-sm text-[var(--app-text-muted)]">{request.managed_endpoint || request.managed_swarm_id || requestID}</div>
                    {request.managed_fingerprint ? <div className="mt-1 break-all text-xs text-[var(--app-text-muted)]">Fingerprint: {request.managed_fingerprint}</div> : null}
                    {request.created_at ? <div className="mt-1 text-xs text-[var(--app-text-subtle)]">Requested {formatRelativeTime(request.created_at * 1000, now)}</div> : null}
                  </div>
                  <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 text-center">
                    <div className="text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-muted)]">Code</div>
                    <div className="mt-1 font-mono text-2xl font-semibold tracking-[0.18em] text-[var(--app-text)]">{formatPairingCode(code) || '—'}</div>
                  </div>
                </div>
                <label className="flex items-center gap-2 text-sm text-[var(--app-text-muted)]">
                  <input
                    type="checkbox"
                    checked={confirmed}
                    disabled={Boolean(busyID)}
                    onChange={(event) => onConfirmationChange(requestID, event.target.checked)}
                  />
                  <span>I confirm this code matches on both machines</span>
                </label>
                <div className="flex flex-wrap justify-between gap-2 sm:justify-end">
                  <Badge tone="warning">Pending</Badge>
                  <div className="flex flex-wrap gap-2">
                    <Button size="sm" variant="outline" disabled={Boolean(busyID)} onClick={() => onDecision(request, false)}>{busy ? 'Working…' : 'Reject'}</Button>
                    <Button size="sm" disabled={Boolean(busyID) || code.length !== 6 || !confirmed} onClick={() => onDecision(request, true)}>{busy ? 'Working…' : 'Approve'}</Button>
                  </div>
                </div>
              </Card>
            )
          })}
        </div>
        <div className="flex justify-end gap-2 border-t border-[var(--app-border)] px-5 py-3">
          <Button variant="ghost" onClick={() => onOpenChange(false)}>Close</Button>
        </div>
      </DialogPanel>
    </Dialog>
  )
}
