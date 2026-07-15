import { useCallback, useEffect, useMemo, useState } from 'react'
import { Outlet } from '@tanstack/react-router'
import { DesktopV3RuntimeProvider } from '../../runtime/desktop-v3-runtime-provider'
import { requestJson } from '../../../../app/api'
import { DesktopVaultGate } from './desktop-vault-gate'
import { DesktopOnboardingGate } from '../../onboarding/components/desktop-onboarding-gate'
import type { DesktopOnboardingStatus, DesktopOnboardingStatusWire } from '../../onboarding/types'
import { DirectLANDesktopWarningScreen, getDirectLANDesktopWarning } from '../../security/direct-lan-desktop-warning'
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

  const loadOnboardingStatus = useCallback(async () => {
    setOnboardingLoading(true)
    setOnboardingError(null)

    try {
      const next = mapOnboardingBootstrapStatus(
        await requestJson<DesktopOnboardingStatusWire>('/v1/onboarding', undefined, false),
      )
      setOnboardingStatus(next)
      return next
    } catch (error) {
      setOnboardingStatus(null)
      setOnboardingError(error instanceof Error ? error.message : 'Failed to load onboarding')
      return null
    } finally {
      setOnboardingLoading(false)
    }
  }, [])

  useEffect(() => {
    if (directLANDesktopWarning) {
      setOnboardingLoading(false)
      return
    }

    let cancelled = false
    setOnboardingLoading(true)
    setOnboardingError(null)

    void requestJson<DesktopOnboardingStatusWire>('/v1/onboarding', undefined, false)
      .then(mapOnboardingBootstrapStatus)
      .then((next) => {
        if (cancelled) {
          return
        }
        setOnboardingStatus(next)
      })
      .catch((error) => {
        if (cancelled) {
          return
        }
        setOnboardingStatus(null)
        setOnboardingError(error instanceof Error ? error.message : 'Failed to load onboarding')
      })
      .finally(() => {
        if (!cancelled) {
          setOnboardingLoading(false)
        }
      })

    return () => {
      cancelled = true
    }
  }, [directLANDesktopWarning])

  if (directLANDesktopWarning) {
    return <DirectLANDesktopWarningScreen warning={directLANDesktopWarning} />
  }

  if (onboardingError && onboardingStatus === null) {
    return (
      <div className="fixed inset-0 z-[9999] flex items-center justify-center bg-black px-6">
        <div className="max-w-xl rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-5 py-4 text-sm text-[var(--app-danger)] shadow-2xl shadow-black/40">
          <div className="font-medium">Unable to load Swarm onboarding.</div>
          <div className="mt-2 text-[var(--app-danger)]/90">{onboardingError}</div>
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
      </div>
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
      <DesktopOnboardingGate
        status={onboardingStatus}
        restart={onboardingFlowRequested}
        onReload={async () => mapOnboardingBootstrapStatus(await requestJson<DesktopOnboardingStatusWire>('/v1/onboarding', undefined, false))}
        onComplete={(next) => {
          setOnboardingStatus(next)
        }}
      />
    )
  }

  if (onboardingStatus?.vault.enabled && !onboardingStatus.vault.unlocked) {
    return <DesktopVaultGate />
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
