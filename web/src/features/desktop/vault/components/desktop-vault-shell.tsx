import { useEffect, useMemo, useState } from 'react'
import { Outlet } from '@tanstack/react-router'
import { debugLog, createDebugTimer } from '../../../../lib/debug-log'
import { useDesktopStore } from '../../state/use-desktop-store'
import { DesktopRealtimeBootstrap } from '../../realtime/desktop-realtime-bootstrap'
import { DesktopVaultGate } from './desktop-vault-gate'
import { fetchDesktopOnboardingStatus } from '../../onboarding/api'
import { DesktopOnboardingGate } from '../../onboarding/components/desktop-onboarding-gate'
import type { DesktopOnboardingStatus } from '../../onboarding/types'
import { DirectLANDesktopWarningScreen, getDirectLANDesktopWarning } from '../../security/direct-lan-desktop-warning'

export function DesktopVaultShell() {
  debugLog('desktop-vault-shell', 'render', {
    vaultBootstrapped: useDesktopStore.getState().vault.bootstrapped,
  })
  const vault = useDesktopStore((state) => state.vault)
  const bootstrapVault = useDesktopStore((state) => state.bootstrapVault)
  const onboardingFlowRequested = useDesktopStore((state) => state.onboardingFlowRequested)
  const clearOnboardingFlow = useDesktopStore((state) => state.clearOnboardingFlow)
  const [onboardingStatus, setOnboardingStatus] = useState<DesktopOnboardingStatus | null>(null)
  const [onboardingLoading, setOnboardingLoading] = useState(true)
  const [onboardingError, setOnboardingError] = useState<string | null>(null)
  const directLANDesktopWarning = useMemo(() => getDirectLANDesktopWarning(), [])

  useEffect(() => {
    if (directLANDesktopWarning) {
      return
    }
    debugLog('desktop-vault-shell', 'effect:bootstrap-vault-dispatch')
    void bootstrapVault()
  }, [bootstrapVault, directLANDesktopWarning])

  useEffect(() => {
    if (directLANDesktopWarning) {
      setOnboardingLoading(false)
      return
    }
    debugLog('desktop-vault-shell', 'effect:onboarding-check', {
      vaultBootstrapped: vault.bootstrapped,
      vaultEnabled: vault.enabled,
      vaultUnlocked: vault.unlocked,
      onboardingFlowRequested,
    })
    if (!vault.bootstrapped || (vault.enabled && !vault.unlocked)) {
      return
    }

    let cancelled = false
    const finish = createDebugTimer('desktop-vault-shell', 'fetch-onboarding-status', {
      onboardingFlowRequested,
    })
    setOnboardingLoading(true)
    setOnboardingError(null)

    void fetchDesktopOnboardingStatus()
      .then((next) => {
        if (cancelled) {
          finish({ cancelled: true, phase: 'then' })
          return
        }
        debugLog('desktop-vault-shell', 'fetch-onboarding-status:resolved', {
          needsOnboarding: next.needsOnboarding,
          credentialCount: next.auth.credentialCount,
          providerCount: next.auth.providers.length,
          savedWorkspaceCount: next.workspace.savedCount,
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
  }, [directLANDesktopWarning, onboardingFlowRequested, vault.bootstrapped, vault.enabled, vault.unlocked])

  if (directLANDesktopWarning) {
    return <DirectLANDesktopWarningScreen warning={directLANDesktopWarning} />
  }

  if (!vault.bootstrapped) {
    return (
      <div className="absolute inset-0 flex items-center justify-center bg-[var(--app-bg)] text-sm text-[var(--app-text-muted)]">
        Loading vault…
      </div>
    )
  }

  if (vault.enabled && !vault.unlocked) {
    return <DesktopVaultGate />
  }

  if (onboardingLoading || onboardingStatus === null) {
    return (
      <div className="absolute inset-0 flex items-center justify-center bg-[var(--app-bg)] text-sm text-[var(--app-text-muted)]">
        Loading Swarm…
      </div>
    )
  }

  if (onboardingError) {
    return (
      <div className="absolute inset-0 flex items-center justify-center bg-[var(--app-bg)] px-6">
        <div className="max-w-xl rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-5 py-4 text-sm text-[var(--app-danger)]">
          {onboardingError}
        </div>
      </div>
    )
  }

  if (onboardingFlowRequested || onboardingStatus.needsOnboarding) {
    return (
      <DesktopOnboardingGate
        status={onboardingStatus}
        restart={onboardingFlowRequested}
        onComplete={(next) => {
          setOnboardingStatus(next)
          clearOnboardingFlow()
        }}
      />
    )
  }

  return (
    <>
      <DesktopRealtimeBootstrap />
      <Outlet />
    </>
  )
}
