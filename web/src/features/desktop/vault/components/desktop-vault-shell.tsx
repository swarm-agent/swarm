import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Outlet } from '@tanstack/react-router'
import { StartupScreen } from '../../../../app/startup-recovery'
import { DesktopV3RuntimeProvider } from '../../runtime/desktop-v3-runtime-provider'
import { requestStartupJson } from '../../../../app/api'
import { DesktopVaultGate } from './desktop-vault-gate'
import { DesktopOnboardingGate } from '../../onboarding/components/desktop-onboarding-gate'
import type { DesktopOnboardingStatus, DesktopOnboardingStatusWire } from '../../onboarding/types'
import { DirectLANDesktopWarningScreen, getDirectLANDesktopWarning } from '../../security/direct-lan-desktop-warning'
import { TailscaleOriginApprovalScreen, useTailscaleOriginApproval } from '../../security/tailscale-origin-approval'
import { DesktopModelCatalogSync } from '../../models/desktop-model-catalog-sync'

function mapOnboardingBootstrapStatus(onboarding: DesktopOnboardingStatusWire): DesktopOnboardingStatus {
  const credentialCount = typeof onboarding.heuristics?.credential_count === 'number'
    ? onboarding.heuristics.credential_count
    : typeof onboarding.auth?.credential_count === 'number'
      ? onboarding.auth.credential_count
      : 0
  const agentCount = typeof onboarding.heuristics?.agent_count === 'number'
    ? onboarding.heuristics.agent_count
    : 0
  const savedWorkspaceCount = typeof onboarding.heuristics?.saved_workspace_count === 'number'
    ? onboarding.heuristics.saved_workspace_count
    : typeof onboarding.workspace?.saved_count === 'number'
      ? onboarding.workspace.saved_count
      : 0
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
      desktopOnboardingComplete: Boolean(onboarding.config?.desktop_onboarding_complete),
    },
    heuristics: {
      missingSwarmName: Boolean(onboarding.heuristics?.missing_swarm_name),
      credentialCount,
      agentCount,
      savedWorkspaceCount,
      vaultConfigured: Boolean(onboarding.heuristics?.vault_configured),
    },
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

export interface DesktopVaultShellProps {
  initialPreferredSessionId?: string | null
}

export function DesktopVaultShell({ initialPreferredSessionId }: DesktopVaultShellProps) {
  const onboardingFlowRequested = false
  const [onboardingStatus, setOnboardingStatus] = useState<DesktopOnboardingStatus | null>(null)
  const [onboardingLoading, setOnboardingLoading] = useState(true)
  const [onboardingError, setOnboardingError] = useState<string | null>(null)
  const directLANDesktopWarning = useMemo(() => getDirectLANDesktopWarning(), [])
  const tailscaleApproval = useTailscaleOriginApproval()

  const onboardingRequest = useRef<AbortController | null>(null)
  const loadOnboardingStatus = useCallback(async () => {
    onboardingRequest.current?.abort()
    const controller = new AbortController()
    onboardingRequest.current = controller
    setOnboardingLoading(true)
    setOnboardingError(null)

    try {
      const next = mapOnboardingBootstrapStatus(
        await requestStartupJson<DesktopOnboardingStatusWire>('/v1/onboarding', { signal: controller.signal }, false),
      )
      if (controller.signal.aborted) return null
      setOnboardingStatus(next)
      return next
    } catch (error) {
      if (controller.signal.aborted) return null
      setOnboardingStatus(null)
      setOnboardingError(error instanceof Error ? error.message : 'Failed to load onboarding')
      return null
    } finally {
      if (!controller.signal.aborted) setOnboardingLoading(false)
      if (onboardingRequest.current === controller) onboardingRequest.current = null
    }
  }, [])

  useEffect(() => {
    if (directLANDesktopWarning || tailscaleApproval.loading || tailscaleApproval.error || tailscaleApproval.status?.required) {
      setOnboardingLoading(false)
      return
    }

    void loadOnboardingStatus()
    return () => { onboardingRequest.current?.abort() }
  }, [directLANDesktopWarning, loadOnboardingStatus, tailscaleApproval.error, tailscaleApproval.loading, tailscaleApproval.status?.required])

  if (directLANDesktopWarning) {
    return <StartupScreen><DirectLANDesktopWarningScreen warning={directLANDesktopWarning} /></StartupScreen>
  }

  if (tailscaleApproval.error) {
    return (
      <StartupScreen><div className="fixed inset-0 z-[9999] flex items-center justify-center bg-black px-6">
        <div className="max-w-xl rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-5 py-4 text-sm text-[var(--app-text)] shadow-2xl shadow-black/40">
          <div className="font-medium">Unable to verify this desktop address.</div>
          <div className="mt-2 text-[var(--app-text)]">{tailscaleApproval.error}</div>
          <button
            type="button"
            onClick={() => { void tailscaleApproval.retry() }}
            className="mt-4 rounded-lg border border-[var(--app-danger-border)] px-3 py-1.5 text-xs font-medium transition hover:bg-[var(--app-danger)]/10 disabled:cursor-not-allowed disabled:opacity-60"
            disabled={tailscaleApproval.loading}
          >
            {tailscaleApproval.loading ? 'Retrying…' : 'Try again'}
          </button>
        </div>
      </div></StartupScreen>
    )
  }

  if (tailscaleApproval.loading) {
    return (
      <div className="fixed inset-0 z-[9999] flex items-center justify-center bg-black text-sm text-[var(--app-text-muted)]">
        Loading Swarm…
      </div>
    )
  }

  if (tailscaleApproval.status?.required) {
    return <StartupScreen><TailscaleOriginApprovalScreen origin={String(tailscaleApproval.status.origin ?? '')} /></StartupScreen>
  }

  if (onboardingError && onboardingStatus === null) {
    return (
      <StartupScreen><div className="fixed inset-0 z-[9999] flex items-center justify-center bg-black px-6">
        <div className="max-w-xl rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-5 py-4 text-sm text-[var(--app-text)] shadow-2xl shadow-black/40">
          <div className="font-medium">Unable to load Swarm onboarding.</div>
          <div className="mt-2 text-[var(--app-text)]">{onboardingError}</div>
          <button
            type="button"
            onClick={() => {
              void loadOnboardingStatus()
            }}
            className="mt-4 rounded-lg border border-[var(--app-danger-border)] px-3 py-1.5 text-xs font-medium transition hover:bg-[var(--app-danger)]/10 disabled:cursor-not-allowed disabled:opacity-60"
            disabled={onboardingLoading}
          >
            {onboardingLoading ? 'Retrying…' : 'Try again'}
          </button>
        </div>
      </div></StartupScreen>
    )
  }

  if (onboardingLoading || onboardingStatus === null) {
    return (
      <div className="fixed inset-0 z-[9999] flex items-center justify-center bg-black text-sm text-[var(--app-text-muted)]">
        Loading Swarm…
      </div>
    )
  }

  if (onboardingStatus !== null && (onboardingStatus.needsOnboarding || onboardingFlowRequested)) {
    return (
      <StartupScreen><DesktopOnboardingGate
        status={onboardingStatus}
        restart={onboardingFlowRequested}
        onReload={async () => mapOnboardingBootstrapStatus(await requestStartupJson<DesktopOnboardingStatusWire>('/v1/onboarding', undefined, false))}
        onComplete={(next) => {
          setOnboardingStatus(next)
        }}
      /></StartupScreen>
    )
  }

  if (onboardingStatus?.vault.enabled && !onboardingStatus.vault.unlocked) {
    return <StartupScreen><DesktopVaultGate /></StartupScreen>
  }

  return (
    <>
      <DesktopModelCatalogSync />
      <DesktopV3RuntimeProvider initialPreferredSessionId={initialPreferredSessionId}>
        <Outlet />
      </DesktopV3RuntimeProvider>
    </>
  )
}
