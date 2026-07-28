import { useEffect, useMemo, useState } from 'react'
import { AlertTriangle, CheckCircle2, Network, RefreshCw, ShieldCheck, Terminal, Trash2 } from 'lucide-react'
import { Button } from '../../../../../components/ui/button'
import { Input } from '../../../../../components/ui/input'
import { cn } from '../../../../../lib/cn'
import { approveTailscaleOrigin } from '../mutations/approve-tailscale-origin'
import { revokeTailscaleOrigin } from '../mutations/revoke-tailscale-origin'
import { getTailscaleSettings } from '../queries/get-tailscale-settings'
import { normalizeTailscaleOriginInput, verifiedRouteForInput, type TailscaleRouteClassification, type TailscaleSettingsRoute, type TailscaleSettingsStatus } from '../types'

const CLASSIFICATION_COPY: Record<TailscaleRouteClassification, { label: string; detail: string; tone: 'success' | 'warning' | 'danger' | 'muted' }> = {
  verified_swarm_desktop: { label: 'Verified Swarm desktop', detail: 'This exact HTTPS route proxies to the running Swarm desktop listener and Funnel is off.', tone: 'success' },
  wrong_target: { label: 'Wrong target', detail: 'This route proxies to another local port or service, not this Swarm desktop.', tone: 'danger' },
  funnel_enabled: { label: 'Funnel enabled', detail: 'Public Funnel traffic is not accepted for Swarm desktop access.', tone: 'danger' },
  unsupported_handler: { label: 'Unsupported handler', detail: 'The authority must have one root HTTP proxy handler and no additional handler behavior.', tone: 'warning' },
  invalid: { label: 'Invalid authority', detail: 'The authority is not the local node’s exact HTTPS Tailscale origin.', tone: 'danger' },
  unconfigured: { label: 'Not configured', detail: 'No HTTPS Serve route exists for this node.', tone: 'muted' },
  incompatible: { label: 'Incompatible configuration', detail: 'The installed Tailscale schema or listener configuration cannot be safely verified.', tone: 'warning' },
  unavailable: { label: 'Inspection unavailable', detail: 'Swarm could not inspect Tailscale. Existing approvals remain revocable.', tone: 'warning' },
}

function toneClasses(tone: 'success' | 'warning' | 'danger' | 'muted'): string {
  if (tone === 'success') return 'border-[var(--app-success-border)] bg-[var(--app-success-bg)] text-[var(--app-success)]'
  if (tone === 'danger') return 'border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] text-[var(--app-danger)]'
  if (tone === 'warning') return 'border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] text-[var(--app-warning)]'
  return 'border-[var(--app-border)] bg-[var(--app-bg-alt)] text-[var(--app-text-muted)]'
}

function inferredEffectiveTarget(status: TailscaleSettingsStatus | null): string {
  if (!status) return ''
  if (status.effective_target) return status.effective_target
  const marker = 'tailscale serve --bg '
  return status.remediation?.startsWith(marker) ? status.remediation.slice(marker.length).trim() : ''
}

function RouteCard({ route, busy, onApprove }: { route: TailscaleSettingsRoute; busy: boolean; onApprove: (origin: string) => void }) {
  const copy = CLASSIFICATION_COPY[route.classification]
  const verified = route.classification === 'verified_swarm_desktop'
  return (
    <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="break-all text-sm font-medium text-[var(--app-text)]">{route.origin || route.authority}</div>
          <div className={cn('mt-2 inline-flex rounded-full border px-2.5 py-1 text-xs font-medium', toneClasses(copy.tone))}>{copy.label}</div>
          <p className="mt-2 text-xs leading-5 text-[var(--app-text-muted)]">{route.reason || copy.detail}</p>
          {route.proxy_target ? <p className="mt-2 break-all font-mono text-xs text-[var(--app-text-subtle)]">Proxy target: {route.proxy_target}</p> : null}
        </div>
        {verified ? (
          <Button
            variant={route.approved ? 'outline' : 'primary'}
            size="sm"
            className="shrink-0"
            disabled={route.approved || busy || !route.origin}
            onClick={() => onApprove(route.origin)}
          >
            {route.approved ? 'Approved' : busy ? 'Approving…' : 'Approve'}
          </Button>
        ) : null}
      </div>
    </div>
  )
}

export function TailscaleSettingsPage() {
  const [status, setStatus] = useState<TailscaleSettingsStatus | null>(null)
  const [manualOrigin, setManualOrigin] = useState('')
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [busyOrigin, setBusyOrigin] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState<string | null>(null)

  const load = async (refresh = false) => {
    if (refresh) setRefreshing(true)
    else setLoading(true)
    setError(null)
    try {
      setStatus(await getTailscaleSettings())
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to inspect Tailscale')
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  const verifiedRoutes = useMemo(
    () => status?.routes.filter((route) => route.classification === 'verified_swarm_desktop') ?? [],
    [status],
  )
  const problemRoutes = useMemo(
    () => status?.routes.filter((route) => route.classification !== 'verified_swarm_desktop') ?? [],
    [status],
  )
  const manualNormalized = normalizeTailscaleOriginInput(manualOrigin)
  const manualRoute = verifiedRouteForInput(status, manualOrigin)
  const manualError = manualOrigin.trim() && !manualNormalized
    ? 'Enter an exact HTTPS .ts.net origin with no path, query, fragment, credentials, or non-443 port.'
    : manualOrigin.trim() && !manualRoute
      ? 'That exact origin is not a currently verified non-Funnel Swarm desktop route.'
      : null
  const effectiveTarget = inferredEffectiveTarget(status)

  const approve = async (origin: string) => {
    setBusyOrigin(origin)
    setError(null)
    setSuccess(null)
    try {
      setStatus(await approveTailscaleOrigin(origin))
      setSuccess(`Approved ${origin}`)
      setManualOrigin('')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to approve Tailscale origin')
    } finally {
      setBusyOrigin(null)
    }
  }

  const revoke = async (origin: string) => {
    setBusyOrigin(origin)
    setError(null)
    setSuccess(null)
    try {
      setStatus(await revokeTailscaleOrigin(origin))
      setSuccess(`Revoked ${origin}`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to revoke Tailscale origin')
    } finally {
      setBusyOrigin(null)
    }
  }

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <h1 className="text-xl font-semibold text-[var(--app-text)]">Tailscale</h1>
          <p className="mt-1 max-w-2xl text-sm leading-6 text-[var(--app-text-muted)]">
            Inspect Tailscale Serve and approve exact private HTTPS routes to this running Swarm desktop. Swarm never changes Serve or Funnel configuration.
          </p>
        </div>
        <Button variant="outline" onClick={() => void load(true)} disabled={loading || refreshing || busyOrigin !== null}>
          <RefreshCw size={15} className={refreshing ? 'animate-spin' : ''} />
          {refreshing ? 'Inspecting…' : 'Refresh status'}
        </Button>
      </div>

      {error ? <div role="alert" className="mb-4 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">{error}</div> : null}
      {success ? <div role="status" className="mb-4 rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-4 py-3 text-sm text-[var(--app-success)]">{success}</div> : null}

      <div className="grid gap-6 pb-12">
        <section className="rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-5 shadow-sm">
          <div className="flex items-start gap-3">
            <Network size={18} className="mt-0.5 shrink-0 text-[var(--app-primary)]" />
            <div className="min-w-0">
              <div className="text-sm font-semibold text-[var(--app-text)]">Effective Swarm desktop target</div>
              {loading ? (
                <p className="mt-2 text-sm text-[var(--app-text-muted)]">Inspecting Tailscale status, Serve routes, and Funnel state…</p>
              ) : (
                <>
                  <p className="mt-2 break-all font-mono text-sm text-[var(--app-text)]">{effectiveTarget || 'Unavailable while Tailscale inspection is unavailable'}</p>
                  {status?.self_dns_name ? <p className="mt-2 text-xs text-[var(--app-text-muted)]">Node DNS: {status.self_dns_name}. A DNS name alone does not prove that Swarm is being served.</p> : null}
                </>
              )}
            </div>
          </div>
          {status?.detection_error ? (
            <div className={cn('mt-4 rounded-xl border px-4 py-3 text-sm', toneClasses(status.state === 'incompatible' ? 'warning' : 'danger'))}>
              <div className="font-medium">{status.state === 'incompatible' ? 'Tailscale schema is incompatible' : 'Tailscale CLI inspection is unavailable'}</div>
              <div className="mt-1 break-words text-xs leading-5">{status.detection_error}</div>
            </div>
          ) : null}
        </section>

        {!loading && verifiedRoutes.length === 0 ? (
          <section className="rounded-2xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-5">
            <div className="flex items-start gap-3">
              <Terminal size={18} className="mt-0.5 shrink-0 text-[var(--app-warning)]" />
              <div className="min-w-0 flex-1">
                <div className="text-sm font-semibold text-[var(--app-warning)]">No verified Swarm Serve route</div>
                <p className="mt-1 text-xs leading-5 text-[var(--app-warning)]">Review the detected problems below. If Serve is not configured, run this command yourself; Swarm will not execute it.</p>
                {status?.remediation ? <pre className="mt-3 overflow-x-auto rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-3 font-mono text-xs text-[var(--app-text)]"><code>{status.remediation}</code></pre> : <p className="mt-3 text-xs text-[var(--app-warning)]">A remediation command is unavailable until the Tailscale CLI returns a compatible status.</p>}
              </div>
            </div>
          </section>
        ) : null}

        <section className="rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-5 shadow-sm">
          <div className="flex items-start gap-3">
            <ShieldCheck size={18} className="mt-0.5 shrink-0 text-[var(--app-success)]" />
            <div>
              <div className="text-sm font-semibold text-[var(--app-text)]">Verified Swarm routes</div>
              <p className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">Only routes in this list can be approved. Approval performs another fresh backend verification before saving.</p>
            </div>
          </div>
          <div className="mt-4 grid gap-3">
            {loading ? <div className="text-sm text-[var(--app-text-muted)]">Loading verified routes…</div> : verifiedRoutes.length === 0 ? <div className="rounded-xl border border-dashed border-[var(--app-border)] px-4 py-5 text-sm text-[var(--app-text-muted)]">No verified route detected.</div> : verifiedRoutes.map((route) => <RouteCard key={`${route.authority}:${route.classification}`} route={route} busy={busyOrigin === route.origin} onApprove={(origin) => void approve(origin)} />)}
          </div>
        </section>

        <section className="rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-5 shadow-sm">
          <div className="flex items-start gap-3">
            <AlertTriangle size={18} className="mt-0.5 shrink-0 text-[var(--app-warning)]" />
            <div>
              <div className="text-sm font-semibold text-[var(--app-text)]">Detected configuration problems</div>
              <p className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">Wrong targets, non-root handlers, incompatible listeners, and Funnel routes are never approvable.</p>
            </div>
          </div>
          <div className="mt-4 grid gap-3">
            {loading ? <div className="text-sm text-[var(--app-text-muted)]">Checking route classifications…</div> : problemRoutes.length === 0 ? <div className="rounded-xl border border-dashed border-[var(--app-border)] px-4 py-5 text-sm text-[var(--app-text-muted)]">No mismatched routes detected.</div> : problemRoutes.map((route) => <RouteCard key={`${route.authority}:${route.classification}`} route={route} busy={false} onApprove={() => undefined} />)}
          </div>
        </section>

        <section className="rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-5 shadow-sm">
          <div className="text-sm font-semibold text-[var(--app-text)]">Select a verified origin manually</div>
          <p className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">Manual entry selects an exact route from the verified list. It cannot bypass active-target or Funnel verification.</p>
          <div className="mt-4 flex flex-col gap-3 sm:flex-row">
            <Input aria-label="Tailscale HTTPS origin" value={manualOrigin} onChange={(event) => setManualOrigin(event.target.value)} placeholder="https://node.tailnet.ts.net" disabled={loading || busyOrigin !== null} />
            <Button variant="primary" className="shrink-0" disabled={!manualRoute || manualRoute.approved || busyOrigin !== null} onClick={() => manualRoute && void approve(manualRoute.origin)}>
              {busyOrigin === manualRoute?.origin ? 'Approving…' : manualRoute?.approved ? 'Already approved' : 'Approve verified origin'}
            </Button>
          </div>
          {manualError ? <p role="alert" className="mt-2 text-xs text-[var(--app-danger)]">{manualError}</p> : manualRoute ? <p className="mt-2 flex items-center gap-1.5 text-xs text-[var(--app-success)]"><CheckCircle2 size={14} /> Exact verified route selected.</p> : null}
        </section>

        <section className="rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-5 shadow-sm">
          <div className="text-sm font-semibold text-[var(--app-text)]">Persisted approvals</div>
          <p className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">Revocation only updates Swarm’s machine-global allowlist and remains available when Tailscale inspection fails.</p>
          <div className="mt-4 divide-y divide-[var(--app-border)] rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)]">
            {status?.approvals.length ? status.approvals.map((origin) => {
              const route = status.routes.find((candidate) => candidate.origin === origin)
              const unavailable = Boolean(status.detection_error)
              const label = unavailable ? (status.state === 'incompatible' ? 'Verification incompatible' : 'Verification unavailable') : route?.active ? 'Active' : route?.classification === 'funnel_enabled' ? 'Funnel enabled' : route ? 'Mismatched' : 'Inactive'
              const tone = route?.active ? 'success' : route?.classification === 'funnel_enabled' || route && !unavailable ? 'danger' : 'warning'
              return (
                <div key={origin} className="flex flex-col gap-3 px-4 py-4 sm:flex-row sm:items-center sm:justify-between">
                  <div className="min-w-0"><div className="break-all text-sm font-medium text-[var(--app-text)]">{origin}</div><div className={cn('mt-1 inline-flex rounded-full border px-2 py-0.5 text-xs', toneClasses(tone))}>{label}</div></div>
                  <Button variant="ghost" size="sm" className="self-start text-[var(--app-danger)] hover:text-[var(--app-danger)]" disabled={busyOrigin === origin} onClick={() => void revoke(origin)}><Trash2 size={14} />{busyOrigin === origin ? 'Revoking…' : 'Revoke'}</Button>
                </div>
              )
            }) : <div className="px-4 py-5 text-sm text-[var(--app-text-muted)]">No approved Tailscale origins.</div>}
          </div>
        </section>
      </div>
    </div>
  )
}
