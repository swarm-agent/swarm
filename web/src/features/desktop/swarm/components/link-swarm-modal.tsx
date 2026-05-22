import { useEffect, useMemo, useState } from 'react'
import { Check, Clipboard, Loader2, RefreshCw } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import {
  Dialog,
  DialogBackdrop,
  DialogPanel,
} from '../../../../components/ui/dialog'
import {
  fetchRemoteSwarmCandidates,
  fetchSwarmState,
  removeManagedHostLink,
  startRemoteSwarmPairing,
  type RemoteSwarmCandidate,
  type RemoteSwarmPairingStartResult,
  type SwarmLocalState,
} from '../../onboarding/api'
import type { DesktopOnboardingStatus } from '../../onboarding/types'

interface LinkSwarmModalProps {
  open: boolean
  onboardingStatus: DesktopOnboardingStatus | null
  onOpenChange: (open: boolean) => void
  onPairingSent?: (message: string) => Promise<void> | void
  onOnboardingStatusChange?: (status: DesktopOnboardingStatus) => void
}

type LinkStatus = 'idle' | 'pairing' | 'pending' | 'paired' | 'error'

function normalizePairingCode(value: string): string {
  return String(value ?? '')
    .trim()
    .replace(/[^a-fA-F0-9]/g, '')
    .toUpperCase()
    .slice(0, 6)
}

function formatPairingCode(value: string): string {
  const normalized = normalizePairingCode(value)
  return normalized
    ? (normalized.match(/.{1,3}/g)?.join(' ') ?? normalized)
    : ''
}

function endpointHostLabel(endpoint: string): string {
  const trimmed = String(endpoint ?? '').trim()
  if (!trimmed) return ''
  try {
    const parsed = new URL(trimmed)
    return parsed.host || trimmed
  } catch {
    return trimmed
  }
}

function localSwarmRoleFromState(state: SwarmLocalState | null): DesktopOnboardingStatus['config']['swarmRole'] | null {
  const role = String(state?.node.role ?? '').trim().toLowerCase()
  if (role === 'managed') return 'managed'
  if (role === 'child') return 'child'
  if (role === 'standalone') return 'standalone'
  if (role === 'master' || role === 'controller' || role === 'parent') return 'master'
  return null
}

function localTailscaleServeCommand(status: DesktopOnboardingStatus | null): string {
  const desktopPort = status?.config.desktopPort || 5555
  return status?.network.tailscale.serve.command || `tailscale serve --bg http://127.0.0.1:${desktopPort}`
}

function remoteTailscaleServeCommand(candidate: RemoteSwarmCandidate | null): string {
  const endpoint = selectCandidateEndpoint(candidate)
  const apiPort = candidate?.endpointCandidates.find((item) => item.url === endpoint)?.port
    || candidate?.endpointCandidates.find((item) => item.kind.includes('api') && item.port > 0)?.port
    || 7781
  return `tailscale serve --bg http://127.0.0.1:${apiPort}`
}

function localServeAdvisoryDetail(status: DesktopOnboardingStatus | null): string {
  if (!status) return 'Checking this swarm’s Tailscale Serve status…'
  const tailscale = status.network.tailscale
  const serve = tailscale.serve
  if (!tailscale.connected) {
    return tailscale.error || 'Tailscale is not connected on this host.'
  }
  if (serve.error) {
    return `Tailscale Serve status could not be checked: ${serve.error}`
  }
  if (serve.ready) {
    return 'This host is already served on Tailscale.'
  }
  if (serve.configured) {
    return `Tailscale Serve points to ${serve.proxyTarget || 'another target'}; confirm it is Swarm.`
  }
  return 'This host is not served on its Tailscale URL.'
}

function selectCandidateEndpoint(
  candidate: RemoteSwarmCandidate | null,
): string {
  if (!candidate) return ''
  const apiEndpoint = candidate.endpointCandidates.find(
    (item) => String(item.kind ?? '').includes('api') && item.url.trim() !== '',
  )
  return (
    apiEndpoint?.url.trim() ||
    candidate.endpoint.trim() ||
    candidate.endpointCandidates
      .find((item) => item.url.trim() !== '')
      ?.url.trim() ||
    ''
  )
}

export function LinkSwarmModal({
  open,
  onboardingStatus,
  onOpenChange,
  onPairingSent,
  onOnboardingStatusChange: _onOnboardingStatusChange,
}: LinkSwarmModalProps) {
  // Direction is intentional: this modal runs on the requester, which becomes the Managed Host.
  // The selected remote Swarm is the existing primary/Manager that receives and approves the request.
  const [status, setStatus] = useState<LinkStatus>('idle')
  const [candidates, setCandidates] = useState<RemoteSwarmCandidate[]>([])
  const [candidatesLoading, setCandidatesLoading] = useState(false)
  const [candidatesError, setCandidatesError] = useState<string | null>(null)
  const [selectedCandidateID, setSelectedCandidateID] = useState('')
  const [pairingResult, setPairingResult] =
    useState<RemoteSwarmPairingStartResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [listenError, setListenError] = useState<string | null>(null)
  const [serveRefreshing, setServeRefreshing] = useState(false)
  const [detachBusy, setDetachBusy] = useState(false)
  const [serveCopyState, setServeCopyState] = useState<'idle' | 'copied' | 'error'>('idle')
  const [modalSwarmState, setModalSwarmState] = useState<SwarmLocalState | null>(null)
  const effectiveOnboardingStatus = onboardingStatus
  const modalSwarmRole = localSwarmRoleFromState(modalSwarmState)
  const effectiveSwarmRole = modalSwarmRole ?? effectiveOnboardingStatus?.config.swarmRole ?? ''

  const alreadyLinkedManagedHost = effectiveSwarmRole === 'managed'
  const linkedManagerID = String(modalSwarmState?.pairing.parent_swarm_id ?? '').trim() || effectiveOnboardingStatus?.pairing.parentSwarmID || ''
  const linkedPairingState = String(modalSwarmState?.pairing.pairing_state ?? '').trim() || effectiveOnboardingStatus?.pairing.pairingState || ''
  const selectedCandidate = useMemo(
    () =>
      candidates.find((candidate) => candidate.id === selectedCandidateID) ??
      null,
    [candidates, selectedCandidateID],
  )
  const selectedEndpoint = useMemo(
    () => selectCandidateEndpoint(selectedCandidate),
    [selectedCandidate],
  )
  const ceremonyCode = normalizePairingCode(
    pairingResult?.ceremony.code || pairingResult?.request.ceremony_code || '',
  )
  const selectedName =
    selectedCandidate?.name ||
    selectedCandidate?.dnsName ||
    endpointHostLabel(selectedEndpoint) ||
    'primary Manager'
  const busy = status === 'pairing' || status === 'pending'
  const serveAdvisoryDetail = localServeAdvisoryDetail(effectiveOnboardingStatus)
  const serveCommand = localTailscaleServeCommand(effectiveOnboardingStatus)
  const selectedRemoteServeCommand = remoteTailscaleServeCommand(selectedCandidate)

  const refreshRequesterServeStatus = async () => {
    setServeRefreshing(true)
    setError(null)
    try {
      const next = await fetchSwarmState()
      setModalSwarmState(next)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to refresh swarm state')
    } finally {
      setServeRefreshing(false)
    }
  }

  const copyServeCommand = async () => {
    try {
      await navigator.clipboard.writeText(serveCommand)
      setServeCopyState('copied')
      window.setTimeout(() => setServeCopyState('idle'), 1200)
    } catch {
      setServeCopyState('error')
    }
  }

  const forceDetachManagedHost = async () => {
    setDetachBusy(true)
    setError(null)
    try {
      await removeManagedHostLink({
        managerSwarmID: linkedManagerID,
        propagate: false,
        reason: 'Force detached locally from Managed Hosting modal',
      })
      const next = await fetchSwarmState()
      setModalSwarmState(next)
      await onPairingSent?.('Detached this host locally from its stale Manager link. You can link it again now.')
      if (localSwarmRoleFromState(next) !== 'managed') {
        void loadCandidates()
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to detach Managed Host link')
    } finally {
      setDetachBusy(false)
    }
  }

  const loadCandidates = async () => {
    setCandidatesLoading(true)
    setCandidatesError(null)
    try {
      const result = await fetchRemoteSwarmCandidates()
      setCandidates(result.candidates)
      setSelectedCandidateID((current) =>
        current &&
        result.candidates.some((candidate) => candidate.id === current)
          ? current
          : (result.candidates[0]?.id ?? ''),
      )
      if (!result.tailscale.connected) {
        setCandidatesError(
          result.tailscale.error || 'Tailscale is not connected on this host.',
        )
      }
    } catch (err) {
      setCandidates([])
      setSelectedCandidateID('')
      setCandidatesError(
        err instanceof Error ? err.message : 'Failed to load Tailscale devices',
      )
    } finally {
      setCandidatesLoading(false)
    }
  }

  useEffect(() => {
    if (!open) return
    setStatus('idle')
    setCandidates([])
    setCandidatesError(null)
    setSelectedCandidateID('')
    setPairingResult(null)
    setError(null)
    setListenError(null)
    setServeCopyState('idle')
    setModalSwarmState(null)
    void fetchSwarmState()
      .then((next) => {
        setModalSwarmState(next)
        if (localSwarmRoleFromState(next) !== 'managed') {
          void loadCandidates()
        }
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : 'Failed to load swarm state')
      })
  }, [open])

  useEffect(() => {
    if (!open || status !== 'pending' || !pairingResult) return
    let cancelled = false

    const pollPairingState = async () => {
      try {
        const state = await fetchSwarmState()
        const pairingState = String(state.pairing?.pairing_state ?? '').trim().toLowerCase()
        if (pairingState === 'paired') {
          if (cancelled) return
          setListenError(null)
          setStatus('paired')
          const displayName = selectedName
          setModalSwarmState(state)
          void onPairingSent?.(`This host is now linked as a Managed Host. You can use it from primary Manager ${displayName}.`)
        } else if (!cancelled) {
          setListenError(null)
        }
      } catch (err) {
        if (!cancelled) {
          setListenError(err instanceof Error ? err.message : 'Still waiting for pairing status')
        }
      }
    }

    void pollPairingState()
    const timer = window.setInterval(pollPairingState, 2_000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [open, status, pairingResult, selectedName, onPairingSent])

  const closeModal = () => {
    if (status !== 'pairing') onOpenChange(false)
  }

  const startPairing = async () => {
    if (alreadyLinkedManagedHost) {
      setError('This host is already linked to a Manager.')
      setStatus('error')
      return
    }
    if (!selectedCandidate || !selectedEndpoint) {
      setError('Select an online Tailscale device running swarmd.')
      setStatus('error')
      return
    }
    setStatus('pairing')
    setError(null)
    setListenError(null)
    setPairingResult(null)
    try {
      const result = await startRemoteSwarmPairing({
        endpoint: selectedEndpoint,
        dnsName: selectedCandidate.dnsName,
        ips: selectedCandidate.ips,
        managedName: selectedName,
        rendezvousTransports: selectedCandidate.rendezvousTransports,
      })
      setPairingResult(result)
      setStatus('pending')
      const displayName = selectedName
      await onPairingSent?.(
        `Request sent to ${displayName}. Approve on the primary host. Code: ${formatPairingCode(result.ceremony.code || result.request.ceremony_code)}.`,
      )
    } catch (err) {
      setError(
        err instanceof Error ? err.message : 'Failed to send pairing request',
      )
      setStatus('error')
    }
  }

  if (!open) return null

  const panelClassName =
    'mx-auto mt-[6vh] flex w-[min(720px,calc(100vw-24px))] max-w-[720px] flex-col overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(720px,calc(100vw-48px))]'
  const sectionClassName =
    'grid gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 shadow-none'
  const optionClassName = (active: boolean) =>
    `rounded-lg border px-3 py-2 text-left transition ${active ? 'border-[var(--app-primary)] bg-transparent text-[var(--app-text)]' : 'border-[var(--app-border)] bg-transparent text-[var(--app-text-muted)]'} ${busy ? 'opacity-70' : ''}`

  return (
    <Dialog>
      <DialogBackdrop onClick={closeModal} />
      <DialogPanel data-testid="link-swarm-modal" className={panelClassName}>
        <div className="border-b border-[var(--app-border)] px-5 py-4">
          <h2 className="text-xl font-semibold text-[var(--app-text)]">
            Link as Managed Host
          </h2>
          <p className="mt-1 text-sm text-[var(--app-text-muted)]">
            Run this on the host you want managed. It sends a request to the primary host for approval.
          </p>
        </div>

        <div className="flex max-h-[min(76vh,720px)] flex-col gap-3 overflow-y-auto px-5 py-4">
          {error ? (
            <Card className="whitespace-pre-wrap border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] p-4 text-sm text-[var(--app-danger)]">
              {error}
            </Card>
          ) : null}

          {alreadyLinkedManagedHost ? (
            <Card className="grid gap-2 border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
              <div className="font-semibold text-[var(--app-text)]">
                Already linked as a Managed Host
              </div>
              <div>
                This host is already linked to a Manager{linkedManagerID ? ` (${linkedManagerID})` : ''}. Pairing is {linkedPairingState || 'paired'}.
              </div>
              <div>Managed sync is enabled by this link. This host pulls credentials/API keys, agents, custom tools, skills, and permissions from its Manager until it is detached.</div>
              <div className="flex justify-end">
                <Button type="button" variant="outline" onClick={() => void forceDetachManagedHost()} disabled={detachBusy}>
                  {detachBusy ? <Loader2 size={14} className="animate-spin" /> : null}
                  Force detach locally
                </Button>
              </div>
            </Card>
          ) : null}

          {!alreadyLinkedManagedHost ? (
            <Card className="grid gap-3 border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-4 text-sm text-[var(--app-warning-text)]">
              <div className="font-semibold text-[var(--app-text)]">
                Tailscale Serve required
              </div>
              <div>{serveAdvisoryDetail}</div>
              <div className="grid gap-2">
                <div>
                  <div className="mb-1 text-xs font-semibold uppercase tracking-[0.12em] text-[var(--app-text-muted)]">This host</div>
                  <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                    <code className="min-w-0 flex-1 overflow-x-auto rounded-md border border-[var(--app-border)] bg-[var(--app-code-bg)] px-2 py-1 text-xs text-[var(--app-text)]">
                      {serveCommand}
                    </code>
                    <Button type="button" variant="outline" onClick={() => void copyServeCommand()}>
                      <Clipboard size={14} />
                      {serveCopyState === 'copied' ? 'Copied' : 'Copy'}
                    </Button>
                  </div>
                </div>
                <div>
                  <div className="mb-1 text-xs font-semibold uppercase tracking-[0.12em] text-[var(--app-text-muted)]">Primary host</div>
                  <code className="block min-w-0 overflow-x-auto rounded-md border border-[var(--app-border)] bg-[var(--app-code-bg)] px-2 py-1 text-xs text-[var(--app-text)]">
                    {selectedRemoteServeCommand}
                  </code>
                </div>
              </div>
              {serveCopyState === 'error' ? (
                <div className="text-xs">Copy failed; select and copy the command manually.</div>
              ) : null}
              <div className="flex justify-end">
                <Button type="button" variant="outline" onClick={() => void refreshRequesterServeStatus()} disabled={serveRefreshing}>
                  {serveRefreshing ? <Loader2 size={14} className="animate-spin" /> : <RefreshCw size={14} />}
                  Refresh status
                </Button>
              </div>
            </Card>
          ) : null}

          {!alreadyLinkedManagedHost ? (
          <Card className={sectionClassName}>
            <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
              <div>
                <div className="text-sm font-semibold text-[var(--app-text)]">
                  Primary host
                </div>
                <div className="text-xs text-[var(--app-text-muted)]">
                  Select the primary that should manage this host.
                </div>
              </div>
              <Button
                type="button"
                variant="outline"
                onClick={() => void loadCandidates()}
                disabled={busy || candidatesLoading}
              >
                {candidatesLoading ? (
                  <Loader2 size={14} className="animate-spin" />
                ) : (
                  <RefreshCw size={14} />
                )}
                Refresh
              </Button>
            </div>
            {candidatesError ? (
              <div className="rounded-lg border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-3 text-sm text-[var(--app-warning-text)]">
                {candidatesError}
              </div>
            ) : null}
            {candidates.length === 0 ? (
              <div className="rounded-lg border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">
                {candidatesLoading
                  ? 'Loading Tailscale devices…'
                  : 'No reachable primary hosts found. Start swarmd on the primary, confirm Tailscale, then refresh.'}
              </div>
            ) : (
              <div className="grid gap-2">
                {candidates.map((candidate) => {
                  const selected = candidate.id === selectedCandidateID
                  const endpoint = selectCandidateEndpoint(candidate)
                  return (
                    <button
                      key={candidate.id}
                      type="button"
                      className={optionClassName(selected)}
                      onClick={() => {
                        if (busy) return
                        setSelectedCandidateID(candidate.id)
                        setPairingResult(null)
                        setError(null)
                        setListenError(null)
                        setStatus('idle')
                      }}
                      disabled={busy}
                    >
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0">
                          <div className="truncate text-sm font-semibold text-[var(--app-text)]">
                            {candidate.name ||
                              candidate.dnsName ||
                              endpointHostLabel(endpoint) ||
                              'Tailscale device'}
                          </div>
                          <div className="mt-1 truncate text-xs text-[var(--app-text-muted)]">
                            {endpoint ||
                              candidate.dnsName ||
                              candidate.ips.join(', ')}
                          </div>
                          <div className="mt-1 text-xs text-[var(--app-text-muted)]">
                            {candidate.os || 'unknown OS'} ·{' '}
                            {candidate.transportMode || 'tailscale'}
                          </div>
                        </div>
                        {selected ? (
                          <Check
                            size={16}
                            className="shrink-0 text-[var(--app-primary)]"
                          />
                        ) : null}
                      </div>
                    </button>
                  )
                })}
              </div>
            )}
          </Card>
          ) : null}

          {pairingResult ? (
            <Card className={sectionClassName}>
              <div className="flex items-start gap-3">
                {status === 'paired' ? (
                  <Check size={18} className="mt-0.5 text-[var(--app-success)]" />
                ) : (
                  <Loader2 size={18} className="mt-0.5 animate-spin text-[var(--app-primary)]" />
                )}
                <div className="min-w-0">
                  <div className="text-sm font-semibold text-[var(--app-text)]">
                    {status === 'paired' ? 'Swarm linked' : 'Waiting for Manager approval'}
                  </div>
                  <div className="mt-1 text-sm text-[var(--app-text-muted)]">
                    {status === 'paired'
                      ? 'Linked. Use this host from the primary.'
                      : `Approve on the primary host. Code: ${formatPairingCode(ceremonyCode)}.`}
                  </div>
                  {listenError && status === 'pending' ? (
                    <div className="mt-2 text-xs text-[var(--app-warning-text)]">
                      Still listening; latest status check failed: {listenError}
                    </div>
                  ) : null}
                </div>
              </div>
            </Card>
          ) : null}
        </div>

        <div className="flex flex-col gap-3 border-t border-[var(--app-border)] px-6 py-5 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-sm text-[var(--app-text-muted)]">
            {status === 'pending'
              ? 'Keep this open; it is listening for approval.'
              : status === 'paired'
                ? 'Linked.'
                : alreadyLinkedManagedHost
                  ? 'This host is already linked to a Manager.'
                  : 'Select a primary host.'}
          </div>
          <div className="flex gap-3">
            <Button
              type="button"
              variant="outline"
              onClick={closeModal}
              disabled={status === 'pairing'}
            >
              {status === 'paired' ? 'Done' : status === 'pending' ? 'Close' : 'Cancel'}
            </Button>
            {status === 'pending' || status === 'paired' ? (
              <Button type="button" disabled={status === 'pending'} onClick={status === 'paired' ? closeModal : undefined}>
                {status === 'paired' ? (
                  <Check size={14} />
                ) : (
                  <Loader2 size={14} className="animate-spin" />
                )}
                {status === 'paired' ? 'Linked' : 'Listening…'}
              </Button>
            ) : (
              <Button
                type="button"
                data-testid="link-swarm-submit"
                onClick={() => void startPairing()}
                disabled={
                  status === 'pairing' ||
                  candidatesLoading ||
                  alreadyLinkedManagedHost ||
                  !selectedCandidate ||
                  !selectedEndpoint
                }
              >
                {status === 'pairing' ? (
                  <Loader2 size={14} className="animate-spin" />
                ) : (
                  <Check size={14} />
                )}
                {status === 'pairing' ? 'Requesting…' : 'Request'}
              </Button>
            )}
          </div>
        </div>
      </DialogPanel>
    </Dialog>
  )
}
