import { useEffect, useMemo, useState } from 'react'
import { Outlet } from '@tanstack/react-router'
import { requestJson } from '../../../../app/api'
import { debugLog, createDebugTimer } from '../../../../lib/debug-log'
import { useDesktopUiStore } from '../../state/desktop-ui-store'
import { DesktopRealtimeBootstrap } from '../../realtime/desktop-realtime-bootstrap'
import { DesktopVaultGate } from './desktop-vault-gate'
import { DesktopOnboardingGate } from '../../onboarding/components/desktop-onboarding-gate'
import type {
  DesktopOnboardingDiscoveredSwarmWire,
  DesktopOnboardingStatus,
  DesktopOnboardingStatusWire,
  DesktopOnboardingTransportWire,
} from '../../onboarding/types'
import { DirectLANDesktopWarningScreen, getDirectLANDesktopWarning } from '../../security/direct-lan-desktop-warning'

function normalizeAPIPort(port: number | undefined): number {
  return typeof port === 'number' && Number.isFinite(port) ? port : 0
}

function normalizeBootstrapMode(mode: string | undefined): 'lan' | 'tailscale' {
  return mode === 'tailscale' ? 'tailscale' : 'lan'
}

function mapTransport(record: DesktopOnboardingTransportWire) {
  return {
    kind: String(record.kind ?? '').trim(),
    primary: String(record.primary ?? '').trim(),
    all: Array.isArray(record.all) ? record.all.map((value) => String(value).trim()).filter(Boolean) : [],
  }
}

function mapDiscoveredSwarm(record: DesktopOnboardingDiscoveredSwarmWire) {
  return {
    id: String(record.id ?? '').trim(),
    name: String(record.name ?? '').trim(),
    role: String(record.role ?? '').trim(),
    endpoint: String(record.endpoint ?? '').trim(),
    tailnetURL: String(record.tailnet_url ?? '').trim(),
    dnsName: String(record.dns_name ?? '').trim(),
    ips: Array.isArray(record.ips) ? record.ips.map((value) => String(value).trim()).filter(Boolean) : [],
    online: Boolean(record.online),
    source: String(record.source ?? '').trim(),
    running: Boolean(record.running),
    inCurrentGroup: Boolean(record.in_current_group),
    currentRelationship: String(record.current_relationship ?? '').trim(),
    transportMode: String(record.transport_mode ?? '').trim(),
    rendezvousTransports: Array.isArray(record.rendezvous_transports)
      ? record.rendezvous_transports.map(mapTransport)
      : [],
  }
}

function mapOnboardingBootstrapStatus(onboarding: DesktopOnboardingStatusWire): DesktopOnboardingStatus {
  const mode = normalizeBootstrapMode(onboarding.config?.mode)
  const child = Boolean(onboarding.config?.child)
  const rawSwarmRole = String(onboarding.config?.swarm_role ?? '').trim().toLowerCase()
  const swarmRole: DesktopOnboardingStatus['config']['swarmRole'] = rawSwarmRole === 'managed'
    ? 'managed'
    : rawSwarmRole === 'child' || child
      ? 'child'
      : rawSwarmRole === 'standalone'
        ? 'standalone'
        : 'master'
  const credentialCount = typeof onboarding.heuristics?.credential_count === 'number'
    ? onboarding.heuristics.credential_count
    : typeof onboarding.auth?.credential_count === 'number'
      ? onboarding.auth.credential_count
      : 0
  const savedWorkspaceCount = typeof onboarding.heuristics?.saved_workspace_count === 'number'
    ? onboarding.heuristics.saved_workspace_count
    : typeof onboarding.workspace?.saved_count === 'number'
      ? onboarding.workspace.saved_count
      : 0
  const tailscale = onboarding.network?.tailscale ?? onboarding.tailscale

  return {
    ok: Boolean(onboarding.ok),
    needsOnboarding: Boolean(onboarding.needs_onboarding),
    identity: {
      bootstrapped: Boolean(onboarding.identity?.bootstrapped),
      userID: String(onboarding.identity?.user_id ?? '').trim(),
      accountScopeID: String(onboarding.identity?.account_scope_id ?? '').trim(),
      username: String(onboarding.identity?.username ?? '').trim(),
      teamID: String(onboarding.identity?.team_id ?? '').trim(),
      teamDisplayName: String(onboarding.identity?.team_display_name ?? '').trim(),
      teamDefault: Boolean(onboarding.identity?.team_default),
      membershipRole: String(onboarding.identity?.membership_role ?? '').trim(),
    },
    config: {
      swarmName: String(onboarding.config?.swarm_name ?? '').trim(),
      child,
      desktopOnboardingComplete: Boolean(onboarding.config?.desktop_onboarding_complete),
      swarmRole,
      swarmID: '',
      mode,
      host: String(onboarding.config?.host ?? '').trim(),
      port: normalizeAPIPort(onboarding.config?.port),
      desktopPort: normalizeAPIPort(onboarding.config?.desktop_port ?? 5555),
      advertiseHost: String(onboarding.config?.advertise_host ?? '').trim(),
      advertisePort: normalizeAPIPort(onboarding.config?.advertise_port ?? onboarding.config?.port),
      tailscaleURL: String(onboarding.config?.tailscale_url ?? '').trim(),
      bypassPermissions: Boolean(onboarding.config?.bypass_permissions),
      devMode: Boolean(onboarding.config?.dev_mode),
      localTransportPort: normalizeAPIPort(onboarding.config?.local_transport_port ?? 7790),
      localTransportActive: Boolean(onboarding.config?.local_transport_active),
      localTransportWarning: String(onboarding.config?.local_transport_warning ?? '').trim(),
      peerTransportPort: normalizeAPIPort(onboarding.config?.peer_transport_port ?? 7791),
      restartRequired: Boolean(onboarding.config?.restart_required),
      restartReason: String(onboarding.config?.restart_reason ?? '').trim(),
    },
    heuristics: {
      missingSwarmName: Boolean(onboarding.heuristics?.missing_swarm_name),
      credentialCount,
      savedWorkspaceCount,
      vaultConfigured: Boolean(onboarding.heuristics?.vault_configured),
    },
    pairing: {
      swarmID: '',
      pairingState: '',
      parentSwarmID: '',
      activeInviteID: '',
      lastEnrollmentID: '',
      lastDecision: '',
      lastDecisionReason: '',
      lastUpdatedByRole: '',
      rendezvousTransports: [],
      managedAuthOwnerSwarmID: '',
      managedAuthSnapshotHash: '',
      managedAuthAppliedAt: 0,
      managedAuthLastAttemptAt: 0,
      managedAuthLastError: '',
    },
    network: {
      lanAddresses: Array.isArray(onboarding.network?.lan_addresses)
        ? onboarding.network.lan_addresses.map((value) => String(value).trim()).filter(Boolean)
        : [],
      tailscale: {
        available: Boolean(tailscale?.available),
        connected: Boolean(tailscale?.connected),
        dnsName: String(tailscale?.dns_name ?? '').trim(),
        tailnetName: String(tailscale?.tailnet_name ?? '').trim(),
        tailnetURL: String(tailscale?.tailnet_url ?? '').trim(),
        candidateURL: String(tailscale?.candidate_url ?? '').trim(),
        ips: Array.isArray(tailscale?.ips) ? tailscale.ips.map((value) => String(value).trim()).filter(Boolean) : [],
        authURL: String(tailscale?.auth_url ?? '').trim(),
        error: String(tailscale?.error ?? '').trim(),
        serve: {
          configured: Boolean(tailscale?.serve?.configured),
          ready: Boolean(tailscale?.serve?.ready),
          mode: String(tailscale?.serve?.mode ?? '').trim(),
          url: String(tailscale?.serve?.url ?? '').trim(),
          proxyTarget: String(tailscale?.serve?.proxy_target ?? '').trim(),
          expectedDesktopProxy: String(tailscale?.serve?.expected_desktop_proxy ?? '').trim(),
          expectedAPIProxy: String(tailscale?.serve?.expected_api_proxy ?? '').trim(),
          expectedPeerTransportProxy: String(tailscale?.serve?.expected_peer_transport_proxy ?? '').trim(),
          command: String(tailscale?.serve?.command ?? '').trim(),
          error: String(tailscale?.serve?.error ?? '').trim(),
        },
      },
    },
    currentGroupID: '',
    groups: [],
    discoveredSwarms: Array.isArray(onboarding.discovered_swarms)
      ? onboarding.discovered_swarms.map(mapDiscoveredSwarm)
      : [],
    vault: {
      enabled: Boolean(onboarding.vault?.enabled),
      unlocked: Boolean(onboarding.vault?.unlocked),
      unlockRequired: Boolean(onboarding.vault?.unlockRequired),
      storageMode: String(onboarding.vault?.storageMode ?? '').trim(),
      warning: String(onboarding.vault?.warning ?? '').trim(),
    },
    auth: {
      credentialCount,
      activeProviders: Array.isArray(onboarding.auth?.active_providers)
        ? onboarding.auth.active_providers.map((value) => String(value).trim()).filter(Boolean)
        : [],
      providers: Array.isArray(onboarding.auth?.providers) ? onboarding.auth.providers : [],
    },
    workspace: {
      savedCount: savedWorkspaceCount,
    },
  }
}

export function DesktopVaultShell() {
  debugLog('desktop-vault-shell', 'render', {
    vaultBootstrapped: useDesktopUiStore.getState().vault.bootstrapped,
  })
  const vault = useDesktopUiStore((state) => state.vault)
  const onboardingFlowRequested = useDesktopUiStore((state) => state.onboardingFlowRequested)
  const clearOnboardingFlow = useDesktopUiStore((state) => state.clearOnboardingFlow)
  const [onboardingStatus, setOnboardingStatus] = useState<DesktopOnboardingStatus | null>(null)
  const [onboardingLoading, setOnboardingLoading] = useState(true)
  const [onboardingError, setOnboardingError] = useState<string | null>(null)
  const directLANDesktopWarning = useMemo(() => getDirectLANDesktopWarning(), [])

  useEffect(() => {
    if (directLANDesktopWarning) {
      setOnboardingLoading(false)
      return
    }
    debugLog('desktop-vault-shell', 'effect:onboarding-check', {
      onboardingFlowRequested,
    })

    let cancelled = false
    const finish = createDebugTimer('desktop-vault-shell', 'fetch-onboarding-status', {
      onboardingFlowRequested,
    })
    setOnboardingLoading(true)
    setOnboardingError(null)

    void requestJson<DesktopOnboardingStatusWire>('/v1/onboarding', undefined, false)
      .then(mapOnboardingBootstrapStatus)
      .then((next) => {
        if (cancelled) {
          finish({ cancelled: true, phase: 'then' })
          return
        }
        debugLog('desktop-vault-shell', 'fetch-onboarding-status:resolved', {
          needsOnboarding: next.needsOnboarding,
          identityBootstrapped: next.identity.bootstrapped,
        })
        setOnboardingStatus(next)
      })
      .catch((error) => {
        if (cancelled) {
          finish({ cancelled: true, phase: 'catch' })
          return
        }
        debugLog('desktop-vault-shell', 'fetch-onboarding-status:rejected', {
          message: error instanceof Error ? error.message : String(error),
        })
        setOnboardingError(error instanceof Error ? error.message : 'Failed to load onboarding')
      })
      .finally(() => {
        if (!cancelled) {
          setOnboardingLoading(false)
          finish({ cancelled: false })
        }
      })

    return () => {
      cancelled = true
      debugLog('desktop-vault-shell', 'effect:onboarding-cleanup')
    }
  }, [directLANDesktopWarning, onboardingFlowRequested])

  if (directLANDesktopWarning) {
    return <DirectLANDesktopWarningScreen warning={directLANDesktopWarning} />
  }

  if (onboardingFlowRequested && onboardingError) {
    return (
      <div className="absolute inset-0 flex items-center justify-center bg-[var(--app-bg)] px-6">
        <div className="max-w-xl rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-5 py-4 text-sm text-[var(--app-danger)]">
          {onboardingError}
        </div>
      </div>
    )
  }

  if (onboardingFlowRequested && (onboardingLoading || onboardingStatus === null)) {
    return (
      <div className="absolute inset-0 flex items-center justify-center bg-[var(--app-bg)] text-sm text-[var(--app-text-muted)]">
        Loading Swarm…
      </div>
    )
  }

  if (onboardingStatus !== null && (onboardingStatus.needsOnboarding || onboardingFlowRequested)) {
    return (
      <DesktopOnboardingGate
        status={onboardingStatus}
        restart={onboardingFlowRequested}
        onReload={async () => mapOnboardingBootstrapStatus(await requestJson<DesktopOnboardingStatusWire>('/v1/onboarding', undefined, false))}
        onComplete={(next) => {
          setOnboardingStatus(next)
          clearOnboardingFlow()
        }}
      />
    )
  }

  if (vault.enabled && !vault.unlocked) {
    return <DesktopVaultGate />
  }

  return (
    <>
      <DesktopRealtimeBootstrap />
      <Outlet />
    </>
  )
}
