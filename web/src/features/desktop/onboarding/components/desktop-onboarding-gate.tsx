import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { ChevronRight, Plus } from 'lucide-react'
import { queryClient } from '../../../../app/query-client'
import { Button } from '../../../../components/ui/button'
import { Input } from '../../../../components/ui/input'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { acceptOnboardingProviderCredential, patchDesktopOnboarding } from '../api'
import type { DesktopOnboardingStatus } from '../types'
import { startCodexOAuth } from '../../settings/mutations/start-codex-oauth'
import { getCodexOAuthStatus } from '../../settings/queries/get-codex-oauth-status'
import { completeCodexOAuth } from '../../settings/mutations/complete-codex-oauth'
import { upsertAuthCredential } from '../../settings/mutations/upsert-auth-credential'
import { verifyAuthCredential } from '../../settings/mutations/verify-auth-credential'
import { listProviders } from '../../settings/queries/list-providers'
import type { AuthMethod, CodexOAuthSession, ProviderStatus, StartCodexOAuthInput, UpsertAuthCredentialInput } from '../../settings/types/auth'
import { CodexDeviceCode } from '../../settings/auth/components/codex-device-code'
import { codexSetupRecommendation } from '../../settings/auth/codex-setup-recommendation'
import { WorkspaceFolderTree } from '../../../workspaces/launcher/components/workspace-folder-tree'
import { WorkspaceStatus } from '../../../workspaces/launcher/components/workspace-status'
import { applyWorkspaceTheme, workspaceThemeDefaultId } from '../../../workspaces/launcher/services/workspace-theme'
import { useWorkspaceLauncher } from '../../../workspaces/launcher/state/use-workspace-launcher'
import { buildWorkspaceRouteSlugMap, workspaceRouteSlugBase } from '../../../workspaces/launcher/services/workspace-route'
import { formatWorkspacePath } from '../../../workspaces/launcher/services/workspace-format'
import { agentStateQueryOptions, draftModelQueryOptions, modelOptionsQueryOptions, modelProfilesQueryOptions } from '../../../queries/query-options'
import type { WorkspaceDiscoverEntry, WorkspaceResolution } from '../../../workspaces/launcher/types/workspace'

type OnboardingStep = 'identity' | 'provider' | 'workspace'
type CodexOAuthMode = StartCodexOAuthInput['method']
type ProviderSetupMode = 'api' | 'oauth-device' | 'oauth-browser' | 'oauth-manual' | null
type PendingAction = 'identity' | 'provider-save' | 'oauth-device' | 'oauth-browser' | 'oauth-manual' | 'oauth-complete' | 'finalize' | 'workspace' | null

type OnboardingView = OnboardingStep | 'setup'

const SWARM_MARK_SRC = '/favicon.svg'
const STEP_TRANSITION_MS = 180
const ONBOARDING_READY_HOLD_MS = 1_000

const ONBOARDING_STEPS: Record<OnboardingStep, { stepLabel: string; title: string; subtitle: string }> = {
  identity: {
    stepLabel: 'Step 1 of 3 · Identity',
    title: 'Hi, I’m Swarm — your AI command center.',
    subtitle: 'Start with the basics: your username and this device name.',
  },
  provider: {
    stepLabel: 'Step 2 of 3 · Provider',
    title: 'Connect your AI provider.',
    subtitle: 'Connect a provider now or skip ahead. You’ll choose your first workspace next.'
  },
  workspace: {
    stepLabel: 'Step 3 of 3 · Workspace',
    title: 'Choose your first workspace.',
    subtitle: 'Open a saved workspace, add a discovered folder, browse to another folder, or create a new one before entering Swarm.',
  },
}

interface DesktopOnboardingGateProps {
  status: DesktopOnboardingStatus
  restart?: boolean
  onReload: () => Promise<DesktopOnboardingStatus>
  onComplete: (status: DesktopOnboardingStatus) => void
}

function deriveInitialStep(status: DesktopOnboardingStatus): OnboardingStep {
  return status.identity.bootstrapped && status.config.swarmName ? 'provider' : 'identity'
}

function fallbackWorkspaceNameFromPath(path: string): string {
  const parts = path.trim().replace(/[\\/]+$/, '').split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] || path.trim() || 'workspace'
}

function apiCompatibleMethods(provider: ProviderStatus): AuthMethod[] {
  return provider.authMethods.filter((method) => method.credentialType === 'api' || method.credentialType === 'access_token' || method.credentialType === 'token')
}

function supportsCodexOAuth(provider: ProviderStatus | null): boolean {
  if (!provider || provider.id !== 'codex') {
    return false
  }
  return provider.authMethods.some((method) => method.credentialType === 'oauth' || method.id === 'oauth')
}

function credentialLabel(method: AuthMethod | null): string {
  if (!method) {
    return 'Credential'
  }
  if (method.credentialType === 'api') {
    return 'API key'
  }
  return 'Access token'
}

async function refreshAuthDependentQueries(): Promise<void> {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: draftModelQueryOptions().queryKey }),
    queryClient.invalidateQueries({ queryKey: modelOptionsQueryOptions().queryKey }),
    queryClient.invalidateQueries({ queryKey: agentStateQueryOptions().queryKey }),
    queryClient.invalidateQueries({ queryKey: modelProfilesQueryOptions().queryKey }),
    queryClient.invalidateQueries({ queryKey: ['agent-model-settings'] }),
    queryClient.invalidateQueries({ queryKey: ['auth-credentials'] }),
  ])
}

function pendingMessage(action: PendingAction): string | null {
  switch (action) {
    case 'identity':
      return 'Saving identity…'
    case 'provider-save':
      return 'Saving and verifying provider…'
    case 'oauth-device':
      return 'Requesting a device code…'
    case 'oauth-browser':
      return 'Starting browser sign-in…'
    case 'oauth-manual':
      return 'Preparing remote sign-in…'
    case 'oauth-complete':
      return 'Completing sign-in…'
    case 'finalize':
      return 'Setting up your Swarm…'
    case 'workspace':
      return 'Setting up your workspace…'
    default:
      return null
  }
}

function waitForOnboardingReadyHold(): Promise<void> {
  return new Promise((resolve) => {
    const setTimeoutFn = typeof window !== 'undefined' ? window.setTimeout.bind(window) : setTimeout
    setTimeoutFn(resolve, ONBOARDING_READY_HOLD_MS)
  })
}

function OnboardingBrandHeader({ restart, step, visible }: { restart: boolean; step: OnboardingStep; visible: boolean }) {
  const stepCopy = ONBOARDING_STEPS[step]
  const stepIndex = (['identity', 'provider', 'workspace'] as OnboardingStep[]).indexOf(step) + 1

  return (
    <div
      className={[
        'grid min-h-[8.5rem] content-start gap-5 transition-[opacity,transform] duration-200 ease-out motion-reduce:transition-none',
        visible ? 'translate-y-0 opacity-100' : 'translate-y-1 opacity-0',
      ].join(' ')}
    >
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex items-center gap-3">
          <img src={SWARM_MARK_SRC} alt="" className="size-9" aria-hidden="true" />
          <div className="grid gap-0.5">
            <span className="text-sm font-semibold text-[var(--app-text)]">Swarm</span>
            <span className="text-xs text-[var(--app-text-muted)]">{restart ? 'Setup review' : 'First launch'}</span>
          </div>
        </div>
        <div className="grid min-w-32 gap-2 text-right" aria-label={stepCopy.stepLabel}>
          <span className="text-[11px] font-medium uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">{stepIndex} / 3</span>
          <div className="grid grid-cols-3 gap-1">
            {(['identity', 'provider', 'workspace'] as OnboardingStep[]).map((item) => (
              <span
                key={item}
                className={item === step ? 'h-0.5 bg-[var(--app-primary)]' : 'h-0.5 bg-[color-mix(in_oklab,var(--app-border)_62%,transparent)]'}
              />
            ))}
          </div>
        </div>
      </div>
      <div className="grid gap-2">
        <h1 className="text-2xl font-semibold tracking-tight text-[var(--app-text)]">{stepCopy.title}</h1>
        <p className="max-w-2xl text-sm leading-6 text-[var(--app-text-muted)]">{stepCopy.subtitle}</p>
      </div>
    </div>
  )
}

function OnboardingSetupPane() {
  return (
    <div className="grid min-h-[25rem] place-items-center px-6 text-center" role="status" aria-live="polite">
      <div className="grid max-w-md gap-4">
        <div className="mx-auto grid size-14 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] shadow-[0_18px_60px_rgb(0_0_0/0.3)]">
          <img src={SWARM_MARK_SRC} alt="" className="size-8" aria-hidden="true" />
        </div>
        <div className="grid gap-2">
          <h2 className="text-xl font-semibold tracking-tight text-[var(--app-text)]">Setting up your Swarm…</h2>
          <p className="text-sm leading-6 text-[var(--app-text-muted)]">
            Holding the onboarding surface steady while the workspace is confirmed.
          </p>
        </div>
      </div>
    </div>
  )
}

function FeedbackSlot({ error, notice, progress }: { error: string | null; notice: string | null; progress: string | null }) {
  const message = error || progress || notice
  const kind = error ? 'error' : progress ? 'progress' : notice ? 'success' : null

  return (
    <div className="min-h-[3.5rem]" aria-live="polite">
      <div
        role={kind === 'error' ? 'alert' : kind ? 'status' : undefined}
        className={[
          'rounded-xl border px-4 py-3 text-sm transition-[color,background-color,border-color,opacity,transform] duration-200 ease-out motion-reduce:transition-none',
          message ? 'translate-y-0 opacity-100' : 'pointer-events-none translate-y-1 opacity-0',
          kind === 'error'
            ? 'border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] text-[var(--app-danger)]'
            : kind === 'success'
              ? 'border-[var(--app-success-border)] bg-[var(--app-success-bg)] text-[var(--app-success)]'
              : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)]',
        ].join(' ')}
      >
        {message || '\u00a0'}
      </div>
    </div>
  )
}

function OnboardingButtonLabel({ idle, pending, isPending }: { idle: string; pending: string; isPending: boolean }) {
  return (
    <span className="inline-grid place-items-center" aria-hidden="true">
      <span
        className={[
          'col-start-1 row-start-1 transition-[opacity,transform] duration-150 ease-out motion-reduce:transition-none',
          isPending ? '-translate-y-1 opacity-0' : 'translate-y-0 opacity-100',
        ].join(' ')}
      >
        {idle}
      </span>
      <span
        className={[
          'col-start-1 row-start-1 transition-[opacity,transform] duration-150 ease-out motion-reduce:transition-none',
          isPending ? 'translate-y-0 opacity-100' : 'translate-y-1 opacity-0',
        ].join(' ')}
      >
        {pending}
      </span>
    </span>
  )
}

export function DesktopOnboardingGate({ status: initialStatus, restart = false, onReload, onComplete }: DesktopOnboardingGateProps) {
  const navigate = useNavigate()
  const [status, setStatus] = useState(initialStatus)
  const [step, setStep] = useState<OnboardingStep>(() => (restart ? 'identity' : deriveInitialStep(initialStatus)))
  const [view, setView] = useState<OnboardingView>(() => (restart ? 'identity' : deriveInitialStep(initialStatus)))
  const [pendingAction, setPendingAction] = useState<PendingAction>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [panelVisible, setPanelVisible] = useState(true)
  const transitionTimerRef = useRef<number | null>(null)
  const transitionFrameRef = useRef<number | null>(null)

  const [username, setUsername] = useState(initialStatus.identity.username)
  const [swarmName, setSwarmName] = useState(initialStatus.config.swarmName)

  const [providerRecords, setProviderRecords] = useState<ProviderStatus[]>(status.auth.providers)
  const [providerLoading, setProviderLoading] = useState(false)
  const [providerError, setProviderError] = useState<string | null>(null)
  const [providerReloadNonce, setProviderReloadNonce] = useState(0)
  const providerOptions = useMemo(
    () => providerRecords.filter((provider) => provider.id !== '' && !provider.runReason.toLowerCase().includes('search-only provider')),
    [providerRecords],
  )
  const [providerID, setProviderID] = useState(status.auth.activeProviders[0] || providerOptions[0]?.id || '')
  const [providerSetupMode, setProviderSetupMode] = useState<ProviderSetupMode>(null)
  const [credentialValue, setCredentialValue] = useState('')
  const [codexOAuthMode, setCodexOAuthMode] = useState<CodexOAuthMode>('device')
  const [oauthSession, setOAuthSession] = useState<CodexOAuthSession | null>(null)
  const [callbackInput, setCallbackInput] = useState('')
  const [workspaceSearch, setWorkspaceSearch] = useState('')
  const [workspaceError, setWorkspaceError] = useState<string | null>(null)
  const [workspaceExplorerOpen, setWorkspaceExplorerOpen] = useState(false)

  const {
    workspaces,
    discovered,
    loading: workspaceLoading,
    refreshing: workspaceRefreshing,
    selectingPath,
    savingPath,
    browser,
    browserLoading,
    browserError,
    loadError: workspaceLoadError,
    actionError: workspaceActionError,
    openWorkspace,
    saveWorkspace,
    createFolder,
    refresh: refreshWorkspaces,
    browsePath,
  } = useWorkspaceLauncher({
    applyDocumentTheme: false,
    autoRefresh: step === 'workspace',
  })

  const selectedProvider = useMemo(
    () => providerOptions.find((provider) => provider.id === providerID) ?? providerOptions[0] ?? null,
    [providerID, providerOptions],
  )
  const manualMethods = useMemo(() => (selectedProvider ? apiCompatibleMethods(selectedProvider) : []), [selectedProvider])
  const selectedManualMethod = manualMethods[0] ?? null
  const providerAlreadyConnected = Boolean(selectedProvider && status.auth.activeProviders.includes(selectedProvider.id))
  const canStartOAuth = supportsCodexOAuth(selectedProvider)
  const canQuickAuthenticate = Boolean(selectedManualMethod || canStartOAuth)
  const recommendedCodexSetup = codexSetupRecommendation()
  const providerSetupOptionCount = (selectedManualMethod ? 1 : 0) + (canStartOAuth ? 3 : 0)
  const showProviderSetupChoices = providerSetupOptionCount > 1
  const showCredentialSection = (providerSetupMode === 'api' || (!showProviderSetupChoices && Boolean(selectedManualMethod))) && Boolean(selectedManualMethod)
  const showOAuthSection = providerSetupMode === 'oauth-device' || providerSetupMode === 'oauth-browser' || providerSetupMode === 'oauth-manual'
  const submitting = pendingAction !== null
  const progress = pendingMessage(pendingAction)
  const mustUseOnboardingProviderAPI = status.heuristics.credentialCount === 0 && status.heuristics.agentCount === 0
  const workspaceSlugByPath = useMemo(() => buildWorkspaceRouteSlugMap(workspaces), [workspaces])
  const savedWorkspaceByPath = useMemo(() => new Map(workspaces.map((workspace) => [workspace.path, workspace])), [workspaces])
  const visibleSavedWorkspaces = useMemo(() => {
    const query = workspaceSearch.trim().toLowerCase()
    if (!query) {
      return workspaces
    }
    return workspaces.filter((workspace) => [workspace.workspaceName, workspace.path].join(' ').toLowerCase().includes(query))
  }, [workspaceSearch, workspaces])
  const filteredDiscovered = useMemo(() => {
    const query = workspaceSearch.trim().toLowerCase()
    const rows = discovered
      .filter((entry) => !savedWorkspaceByPath.has(entry.path))
      .map((entry) => ({ entry, savedWorkspace: savedWorkspaceByPath.get(entry.path) }))
    if (!query) {
      return rows.slice(0, 8)
    }
    return rows.filter(({ entry, savedWorkspace }) => [entry.name, entry.path, savedWorkspace?.workspaceName ?? ''].join(' ').toLowerCase().includes(query)).slice(0, 12)
  }, [discovered, savedWorkspaceByPath, workspaceSearch])
  const workspaceStatusError = workspaceError || workspaceActionError || workspaceLoadError
  const finishButtonLabel = pendingAction === 'finalize'
    ? 'Finishing…'
    : providerAlreadyConnected
      ? 'Continue'
      : providerOptions.length === 0
        ? 'Continue without provider'
        : 'Skip for now'

  useEffect(() => {
    applyWorkspaceTheme(workspaceThemeDefaultId())
  }, [])

  useEffect(() => {
    return () => {
      if (transitionTimerRef.current !== null) {
        window.clearTimeout(transitionTimerRef.current)
      }
      if (transitionFrameRef.current !== null) {
        window.cancelAnimationFrame(transitionFrameRef.current)
      }
    }
  }, [])

  useEffect(() => {
    if (!status.auth.activeProviders.includes(providerID) && !providerOptions.some((provider) => provider.id === providerID)) {
      setProviderID(status.auth.activeProviders[0] || providerOptions[0]?.id || '')
    }
  }, [providerID, providerOptions, status.auth.activeProviders])

  useEffect(() => {
    setCredentialValue('')
    setCallbackInput('')
    setOAuthSession(null)
    setProviderSetupMode(null)
  }, [providerID])

  useEffect(() => {
    if (!showProviderSetupChoices && selectedManualMethod && providerSetupMode !== 'api') {
      setProviderSetupMode('api')
    }
  }, [providerSetupMode, selectedManualMethod, showProviderSetupChoices])

  useEffect(() => {
    if (step !== 'provider' || !status.identity.bootstrapped) {
      return
    }

    let cancelled = false
    setProviderLoading(true)
    setProviderError(null)
    void listProviders()
      .then((providers) => {
        if (cancelled) {
          return
        }
        setProviderRecords(providers)
      })
      .catch((err) => {
        if (cancelled) {
          return
        }
        setProviderError(err instanceof Error ? err.message : 'Failed to load providers')
      })
      .finally(() => {
        if (!cancelled) {
          setProviderLoading(false)
        }
      })

    return () => {
      cancelled = true
    }
  }, [providerReloadNonce, step, status.identity.bootstrapped])

  useEffect(() => {
    if ((codexOAuthMode !== 'browser' && codexOAuthMode !== 'device') || !oauthSession?.sessionID || oauthSession.status === 'success' || oauthSession.status === 'error') {
      return
    }

    const timer = window.setInterval(() => {
      void getCodexOAuthStatus(oauthSession.sessionID)
        .then((next) => {
          setOAuthSession(next)
          if (next.status === 'error') {
            setError(next.error || 'Codex sign-in failed. Choose a fallback below if device authorization is unavailable.')
          }
          if (next.status === 'success') {
            setError(null)
            void reloadStatus()
              .then(() => refreshAuthDependentQueries())
              .then(() => {
                setNotice('Provider connected. Choose your workspace when you’re ready.')
                transitionToStep('workspace')
              })
              .catch((err) => setError(err instanceof Error ? err.message : 'Provider connected, but failed to refresh status.'))
          }
        })
        .catch((err) => {
          setError(err instanceof Error ? err.message : 'Failed to refresh OAuth status')
        })
    }, 1500)

    return () => {
      window.clearInterval(timer)
    }
  }, [codexOAuthMode, oauthSession])

  const clearStepTransition = () => {
    if (transitionTimerRef.current !== null) {
      window.clearTimeout(transitionTimerRef.current)
      transitionTimerRef.current = null
    }
    if (transitionFrameRef.current !== null) {
      window.cancelAnimationFrame(transitionFrameRef.current)
      transitionFrameRef.current = null
    }
  }

  const revealTransitionPanel = () => {
    transitionFrameRef.current = window.requestAnimationFrame(() => {
      setPanelVisible(true)
      transitionFrameRef.current = null
    })
  }

  const transitionToStep = (nextStep: OnboardingStep) => {
    clearStepTransition()
    if (nextStep === step && view === nextStep) {
      setPanelVisible(true)
      return
    }
    setPanelVisible(false)
    transitionTimerRef.current = window.setTimeout(() => {
      setStep(nextStep)
      setView(nextStep)
      transitionTimerRef.current = null
      revealTransitionPanel()
    }, STEP_TRANSITION_MS)
  }

  const transitionToSetup = () => {
    clearStepTransition()
    if (view === 'setup') {
      setPanelVisible(true)
      return
    }
    setPanelVisible(false)
    transitionTimerRef.current = window.setTimeout(() => {
      setView('setup')
      transitionTimerRef.current = null
      revealTransitionPanel()
    }, STEP_TRANSITION_MS)
  }

  const reloadStatus = async () => {
    const next = await onReload()
    setStatus(next)
    return next
  }

  const retryProviderLoad = useCallback(() => {
    setProviderReloadNonce((value) => value + 1)
  }, [])

  const retryWorkspaceLoad = useCallback(() => {
    setWorkspaceError(null)
    void refreshWorkspaces()
  }, [refreshWorkspaces])

  const retryBrowsePath = useCallback(() => {
    setWorkspaceError(null)
    void browsePath(browser?.resolvedPath || browser?.homePath || '')
  }, [browsePath, browser?.homePath, browser?.resolvedPath])

  const persistIdentity = async () => {
    const normalizedUsername = username.trim()
    const normalizedName = swarmName.trim()
    if (!status.identity.bootstrapped && !normalizedUsername) {
      throw new Error('Username is required for the product owner identity.')
    }
    if (!normalizedName) {
      throw new Error('Swarm name is required.')
    }
    await patchDesktopOnboarding({
      username: status.identity.bootstrapped ? undefined : normalizedUsername,
      swarmName: normalizedName,
      desktopOnboardingComplete: false,
    })
    const refreshed = await onReload()
    setStatus(refreshed)
    setUsername(refreshed.identity.username)
    setSwarmName(refreshed.config.swarmName)
    return refreshed
  }

  const finalizeOnboarding = async () => {
    setPendingAction('finalize')
    transitionToSetup()
    const [, next] = await Promise.all([
      waitForOnboardingReadyHold(),
      patchDesktopOnboarding({ desktopOnboardingComplete: true }).then(() => reloadStatus()),
    ])
    setStatus(next)
    onComplete(next)
    return next
  }

  const navigateToWorkspace = async (resolution: WorkspaceResolution, fallbackPath: string) => {
    const resolvedPath = resolution.resolvedPath.trim() || fallbackPath.trim()
    const workspaceSlug = workspaceSlugByPath.get(resolvedPath) ?? workspaceRouteSlugBase({
      path: resolvedPath,
      workspaceName: resolution.workspaceName || fallbackWorkspaceNameFromPath(resolvedPath),
    })
    if (workspaceSlug) {
      await navigate({
        to: '/$workspaceSlug',
        params: { workspaceSlug },
      })
    } else {
      await navigate({ to: '/' })
    }
  }

  const finishWithWorkspace = async (resolution: WorkspaceResolution, fallbackPath: string) => {
    const next = await finalizeOnboarding()
    await refreshAuthDependentQueries()
    if (next.needsOnboarding) {
      throw new Error('Swarm is still finishing onboarding. Try opening the workspace again in a moment.')
    }
    await navigateToWorkspace(resolution, fallbackPath)
  }

  const handleProviderContinue = () => {
    if (submitting) {
      return
    }
    setError(null)
    setNotice(null)
    transitionToStep('workspace')
  }

  const handleOpenWorkspace = (path: string) => {
    if (submitting) {
      return
    }
    setWorkspaceExplorerOpen(false)
    void (async () => {
      setPendingAction('workspace')
      setError(null)
      setWorkspaceError(null)
      transitionToSetup()
      try {
        const resolution = await openWorkspace(path)
        await finishWithWorkspace(resolution, path)
      } catch (err) {
        transitionToStep('workspace')
        setWorkspaceError(err instanceof Error ? err.message : 'Failed to open workspace')
      } finally {
        setPendingAction(null)
      }
    })()
  }

  const handleSaveAndOpenFolder = (entry: Pick<WorkspaceDiscoverEntry, 'path' | 'name'>) => {
    if (submitting) {
      return
    }
    setWorkspaceExplorerOpen(false)
    void (async () => {
      setPendingAction('workspace')
      setError(null)
      setWorkspaceError(null)
      transitionToSetup()
      try {
        await saveWorkspace({
          path: entry.path,
          name: entry.name || fallbackWorkspaceNameFromPath(entry.path),
          themeId: 'inherit',
          makeCurrent: true,
        })
        await refreshWorkspaces()
        const selectedResolution = await openWorkspace(entry.path)
        await finishWithWorkspace(selectedResolution, entry.path)
      } catch (err) {
        transitionToStep('workspace')
        setWorkspaceError(err instanceof Error ? err.message : 'Failed to save and open workspace')
      } finally {
        setPendingAction(null)
      }
    })()
  }

  const handleUseBrowsedFolder = (path: string) => {
    handleSaveAndOpenFolder({ path, name: fallbackWorkspaceNameFromPath(path) })
  }

  const openWorkspaceExplorer = () => {
    setWorkspaceError(null)
    setWorkspaceExplorerOpen(true)
    if (!browser && !browserLoading) {
      void browsePath('')
    }
  }

  const handleIdentitySubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (submitting) {
      return
    }
    void handleIdentityContinue()
  }

  const handleIdentityContinue = async () => {
    setPendingAction('identity')
    setError(null)
    setNotice(null)
    try {
      const next = await persistIdentity()
      setStatus(next)
      transitionToStep('provider')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save onboarding settings')
    } finally {
      setPendingAction(null)
    }
  }

  const handleProviderSave = async () => {
    if (submitting) {
      return
    }
    setPendingAction('provider-save')
    setError(null)
    setNotice(null)
    try {
      if (!selectedProvider || !selectedManualMethod) {
        throw new Error('Choose a provider with API key support, use browser sign-in, or skip for now.')
      }
      if (!credentialValue.trim()) {
        throw new Error(`${credentialLabel(selectedManualMethod)} is required.`)
      }

      const payload: UpsertAuthCredentialInput = {
        provider: selectedProvider.id,
        type: selectedManualMethod.credentialType,
        active: true,
      }
      if (selectedManualMethod.credentialType === 'api') {
        payload.api_key = credentialValue.trim()
      } else {
        payload.access_token = credentialValue.trim()
      }

      if (mustUseOnboardingProviderAPI) {
        const accepted = await acceptOnboardingProviderCredential(payload)
        if (!accepted.active) {
          throw new Error('Onboarding provider was verified but not activated.')
        }
        if (accepted.connection && !accepted.connection.connected) {
          throw new Error(accepted.connection.message || 'Onboarding provider verification failed.')
        }
        if (!accepted.autoDefaults?.applied) {
          throw new Error(accepted.autoDefaults?.error || 'Onboarding did not hydrate agent defaults.')
        }
      } else {
        const saved = await upsertAuthCredential(payload)
        const verification = await verifyAuthCredential({ provider: saved.provider, id: saved.id })
        if (!verification.connected) {
          throw new Error(verification.message || 'Credential saved, but verification failed.')
        }
      }

      setCredentialValue('')
      await reloadStatus()
      await refreshAuthDependentQueries()
      setNotice('Provider connected. Choose your workspace when you’re ready.')
      transitionToStep('workspace')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save provider credential')
    } finally {
      setPendingAction(null)
    }
  }

  const handleCredentialKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key !== 'Enter' || event.shiftKey || event.nativeEvent.isComposing || submitting) {
      return
    }
    event.preventDefault()
    void handleProviderSave()
  }

  const handleStartOAuth = async (method: CodexOAuthMode) => {
    if (submitting) {
      return
    }
    if (!selectedProvider) {
      setError('Choose a provider first.')
      return
    }
    setPendingAction(method === 'device' ? 'oauth-device' : method === 'browser' ? 'oauth-browser' : 'oauth-manual')
    setError(null)
    setNotice(null)
    setCallbackInput('')
    setCodexOAuthMode(method)
    setOAuthSession(null)
    try {
      const session = await startCodexOAuth({
        provider: selectedProvider.id,
        active: true,
        method,
      })
      setOAuthSession(session)
      if (method === 'browser' && session.authURL && typeof window !== 'undefined') {
        window.open(session.authURL, '_blank', 'noopener,noreferrer')
      }
      setNotice(method === 'device'
        ? 'Open OpenAI’s verification page and enter the one-time code. Swarm will continue automatically after approval.'
        : method === 'browser'
          ? 'Finish local sign-in in your browser. Swarm will continue when it sees the callback.'
          : 'Open the auth URL in a new tab, finish Codex sign-in, then paste the full localhost callback URL here. Click Manual callback again if you need a fresh link.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to start Codex sign-in')
    } finally {
      setPendingAction(null)
    }
  }

  const handleCallbackKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key !== 'Enter' || event.shiftKey || event.nativeEvent.isComposing || submitting) {
      return
    }
    event.preventDefault()
    void handleCompleteOAuth()
  }

  const handleCompleteOAuth = async () => {
    if (submitting) {
      return
    }
    if (!oauthSession?.sessionID) {
      setError('Start remote sign-in first.')
      return
    }
    if (!callbackInput.trim()) {
      setError('Paste the callback URL, query string, or authorization code.')
      return
    }

    setPendingAction('oauth-complete')
    setError(null)
    setNotice(null)
    try {
      const session = await completeCodexOAuth({
        session_id: oauthSession.sessionID,
        callback_input: callbackInput.trim(),
      })
      setOAuthSession(session)
      if (session.status !== 'success') {
        throw new Error(session.error || 'OAuth completion did not succeed.')
      }
      setCredentialValue('')
      setCallbackInput('')
      await reloadStatus()
      await refreshAuthDependentQueries()
      setNotice('Provider connected. Choose your workspace when you’re ready.')
      transitionToStep('workspace')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to complete remote sign-in')
    } finally {
      setPendingAction(null)
    }
  }

  return (
    <div className="fixed inset-0 z-[9999] flex items-center justify-center overflow-y-auto bg-black px-6 py-8 text-[var(--app-text)]">
      <main className="relative w-full max-w-5xl overflow-hidden rounded-[2rem] border border-[color-mix(in_oklab,var(--app-border)_58%,transparent)] bg-[color-mix(in_oklab,var(--app-surface)_88%,black)] shadow-[0_24px_90px_rgb(0_0_0/0.55)] outline outline-1 outline-offset-2 outline-[color-mix(in_oklab,var(--app-border)_34%,transparent)] transition-[box-shadow,transform] duration-300 ease-out">
        <div className="grid min-h-[42rem] grid-rows-[auto_auto_minmax(0,1fr)] gap-5 p-8">
          <OnboardingBrandHeader restart={restart} step={step} visible={panelVisible} />

          <FeedbackSlot error={error} notice={view === 'setup' ? null : notice} progress={progress} />

          <div className="relative min-h-[25rem] overflow-hidden">
            <div
              key={view}
              className={[
                'h-full transition-[opacity,transform] duration-200 ease-out motion-reduce:transition-none',
                panelVisible ? 'translate-y-0 opacity-100' : 'translate-y-2 opacity-0',
              ].join(' ')}
            >
              {view === 'setup' ? <OnboardingSetupPane /> : null}

              {view === 'identity' ? (
                <form className="grid h-full content-start gap-6" onSubmit={handleIdentitySubmit}>
                  <div className="grid gap-2">
                    <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-username">
                      Your username.
                    </label>
                    <Input
                      id="desktop-onboarding-username"
                      autoFocus={!status.identity.bootstrapped}
                      value={username}
                      onChange={(event) => setUsername(event.target.value)}
                      placeholder="Username"
                      autoComplete="username"
                      disabled={status.identity.bootstrapped || submitting}
                    />
                    {status.identity.bootstrapped ? (
                      <p className="text-sm leading-6 text-[var(--app-text-muted)]">
                        This is the product owner identity already configured for this Swarm.
                      </p>
                    ) : null}
                  </div>

                  <div className="grid gap-2">
                    <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-swarm-name">
                      Name this device.
                    </label>
                    <Input
                      id="desktop-onboarding-swarm-name"
                      autoFocus={status.identity.bootstrapped}
                      value={swarmName}
                      onChange={(event) => setSwarmName(event.target.value)}
                      placeholder="Studio MacBook"
                      disabled={submitting}
                    />
                    <p className="text-sm leading-6 text-[var(--app-text-muted)]">
                      This is the device label Swarm shows in launcher screens. It does not change your username.
                    </p>
                  </div>

                  <div className="flex justify-end pt-1">
                    <Button
                      type="submit"
                      disabled={submitting}
                      aria-label={pendingAction === 'identity' ? 'Saving…' : 'Continue to provider'}
                    >
                      <OnboardingButtonLabel idle="Continue to provider" pending="Saving…" isPending={pendingAction === 'identity'} />
                    </Button>
                  </div>
                </form>
              ) : null}

              {view === 'provider' ? (
                <div className="grid h-full content-start gap-6">
                  {providerLoading ? (
                    <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-4 text-sm leading-6 text-[var(--app-text-muted)]">
                      Loading providers…
                    </div>
                  ) : null}

                  {providerError ? (
                    <WorkspaceStatus
                      kind="error"
                      title="Providers unavailable"
                      message={providerError}
                      actionLabel="Try again"
                      onAction={retryProviderLoad}
                    />
                  ) : null}

                  {!providerLoading && providerOptions.length > 0 ? (
                    <>
                      <div className="grid gap-3">
                        <div>
                          <h2 className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">Choose provider</h2>
                          <p className="mt-1 text-sm text-[var(--app-text-muted)]">Pick a provider to reveal setup options.</p>
                        </div>
                        <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
                          {providerOptions.map((provider) => {
                            const active = provider.id === providerID
                            const connected = status.auth.activeProviders.includes(provider.id)
                            return (
                              <button
                                key={provider.id}
                                type="button"
                                onClick={() => {
                                  setProviderID(provider.id)
                                  setError(null)
                                  setNotice(null)
                                }}
                                disabled={submitting}
                                className={[
                                  'grid gap-1 rounded-lg border px-4 py-3 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-60',
                                  active
                                    ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_12%,transparent)] text-[var(--app-text)]'
                                    : 'border-[var(--app-border)] bg-transparent text-[var(--app-text-muted)] hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)]',
                                ].join(' ')}
                              >
                                <span className="text-sm font-medium text-[var(--app-text)]">{provider.id}</span>
                                {connected ? <span className="text-xs text-[var(--app-text-muted)]">Connected</span> : null}
                              </button>
                            )
                          })}
                        </div>
                      </div>

                      <div className="grid gap-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                        <div className="flex flex-wrap items-start justify-between gap-3">
                          <div className="grid gap-1">
                            <span className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">Selected provider</span>
                            <h3 className="text-base font-semibold text-[var(--app-text)]">{selectedProvider?.id || 'Provider setup'}</h3>
                          </div>
                          {providerAlreadyConnected ? (
                            <span className="rounded-full border border-[var(--app-success-border)] px-3 py-1 text-xs font-medium text-[var(--app-success)]">Connected</span>
                          ) : null}
                        </div>

                        {providerAlreadyConnected ? (
                          <p className="text-sm leading-6 text-[var(--app-success)]">
                            {selectedProvider?.id || 'Selected provider'} is connected. Continue when you’re ready.
                          </p>
                        ) : canQuickAuthenticate ? (
                          <div className="grid gap-4">
                            {showProviderSetupChoices ? (
                              <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
                                {selectedManualMethod ? (
                                  <button
                                    type="button"
                                    onClick={() => setProviderSetupMode(providerSetupMode === 'api' ? null : 'api')}
                                    disabled={submitting}
                                    className={[
                                      'rounded-lg border px-4 py-3 text-left text-sm transition-colors disabled:cursor-not-allowed disabled:opacity-60',
                                      providerSetupMode === 'api'
                                        ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_12%,transparent)] text-[var(--app-text)]'
                                        : 'border-[var(--app-border)] bg-transparent text-[var(--app-text-muted)] hover:border-[var(--app-border-accent)] hover:text-[var(--app-text)]',
                                    ].join(' ')}
                                  >
                                    API key
                                  </button>
                                ) : null}
                                {canStartOAuth ? (
                                  <>
                                    <button
                                      type="button"
                                      onClick={() => {
                                        setProviderSetupMode('oauth-device')
                                        void handleStartOAuth('device')
                                      }}
                                      disabled={submitting}
                                      aria-label={pendingAction === 'oauth-device' ? 'Preparing device code…' : 'Device Code'}
                                      className={[
                                        'rounded-lg border px-4 py-3 text-left text-sm transition-colors disabled:cursor-not-allowed disabled:opacity-60',
                                        providerSetupMode === 'oauth-device'
                                          ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_12%,transparent)] text-[var(--app-text)]'
                                          : recommendedCodexSetup === 'device'
                                            ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_6%,transparent)] text-[var(--app-text)] hover:bg-[color-mix(in_oklab,var(--app-primary)_12%,transparent)]'
                                            : 'border-[var(--app-border)] bg-transparent text-[var(--app-text-muted)] hover:border-[var(--app-border-accent)] hover:text-[var(--app-text)]',
                                      ].join(' ')}
                                    >
                                      <span className="block font-semibold">
                                        <OnboardingButtonLabel idle="Device Code" pending="Preparing…" isPending={pendingAction === 'oauth-device'} />
                                      </span>
                                      {recommendedCodexSetup === 'device' ? <span className="mt-1 block text-xs text-[var(--app-text-muted)]">Recommended for remote setup</span> : null}
                                    </button>
                                    <button
                                      type="button"
                                      onClick={() => {
                                        setProviderSetupMode('oauth-browser')
                                        void handleStartOAuth('browser')
                                      }}
                                      disabled={submitting}
                                      aria-label={pendingAction === 'oauth-browser' ? 'Opening local setup…' : 'Local Setup'}
                                      className={[
                                        'rounded-lg border px-4 py-3 text-left text-sm transition-colors disabled:cursor-not-allowed disabled:opacity-60',
                                        providerSetupMode === 'oauth-browser'
                                          ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_12%,transparent)] text-[var(--app-text)]'
                                          : recommendedCodexSetup === 'browser'
                                            ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_6%,transparent)] text-[var(--app-text)] hover:bg-[color-mix(in_oklab,var(--app-primary)_12%,transparent)]'
                                            : 'border-[var(--app-border)] bg-transparent text-[var(--app-text-muted)] hover:border-[var(--app-border-accent)] hover:text-[var(--app-text)]',
                                      ].join(' ')}
                                    >
                                      <span className="block font-semibold">
                                        <OnboardingButtonLabel idle="Local Setup" pending="Opening…" isPending={pendingAction === 'oauth-browser'} />
                                      </span>
                                      {recommendedCodexSetup === 'browser' ? <span className="mt-1 block text-xs text-[var(--app-text-muted)]">Recommended on this device</span> : null}
                                    </button>
                                    <button
                                      type="button"
                                      onClick={() => {
                                        setProviderSetupMode('oauth-manual')
                                        void handleStartOAuth('manual')
                                      }}
                                      disabled={submitting}
                                      aria-label={pendingAction === 'oauth-manual' ? 'Preparing manual callback…' : 'Manual callback fallback'}
                                      className={[
                                        'rounded-lg border px-4 py-3 text-left text-sm transition-colors disabled:cursor-not-allowed disabled:opacity-60',
                                        providerSetupMode === 'oauth-manual'
                                          ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_12%,transparent)] text-[var(--app-text)]'
                                          : 'border-[var(--app-border)] bg-transparent text-[var(--app-text-muted)] hover:border-[var(--app-border-accent)] hover:text-[var(--app-text)]',
                                      ].join(' ')}
                                    >
                                      <OnboardingButtonLabel idle="Manual callback fallback" pending="Preparing…" isPending={pendingAction === 'oauth-manual'} />
                                    </button>
                                  </>
                                ) : null}
                              </div>
                            ) : (
                              <div className="rounded-lg border border-[var(--app-border)] bg-transparent px-4 py-3 text-sm font-medium text-[var(--app-text)]">
                                {selectedManualMethod ? credentialLabel(selectedManualMethod) : 'Provider sign-in'}
                              </div>
                            )}

                            {showCredentialSection && selectedManualMethod ? (
                              <div className="grid gap-3">
                                <label className="grid gap-2">
                                  <span className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">
                                    Enter credential
                                  </span>
                                  <Input
                                    type="password"
                                    autoComplete="off"
                                    value={credentialValue}
                                    onChange={(event) => setCredentialValue(event.target.value)}
                                    onKeyDown={handleCredentialKeyDown}
                                    placeholder={credentialLabel(selectedManualMethod)}
                                    disabled={submitting}
                                  />
                                </label>
                                {selectedManualMethod.description ? (
                                  <p className="text-sm leading-6 text-[var(--app-text-muted)]">{selectedManualMethod.description}</p>
                                ) : null}
                                <div className="flex justify-end">
                                  <Button
                                    type="button"
                                    onClick={() => void handleProviderSave()}
                                    disabled={submitting}
                                    aria-label={pendingAction === 'provider-save' ? 'Verifying…' : 'Save provider'}
                                  >
                                    <OnboardingButtonLabel idle="Save provider" pending="Verifying…" isPending={pendingAction === 'provider-save'} />
                                  </Button>
                                </div>
                              </div>
                            ) : null}

                            {showOAuthSection && oauthSession ? (
                              <div className="grid gap-4 text-sm leading-6 text-[var(--app-text-muted)]">
                                <div>
                                  {codexOAuthMode === 'device' ? 'Device-code sign-in' : codexOAuthMode === 'browser' ? 'Local browser fallback' : 'Manual callback fallback'} status:{' '}
                                  <span className={oauthSession.status === 'success' ? 'font-medium text-[var(--app-success)]' : oauthSession.status === 'error' ? 'font-medium text-[var(--app-danger)]' : 'font-medium text-[var(--app-text)]'}>
                                    {oauthSession.status || 'waiting'}
                                  </span>
                                  {oauthSession.error ? <div className="text-[var(--app-danger)]">{oauthSession.error}</div> : null}
                                </div>
                                {codexOAuthMode === 'device' ? <CodexDeviceCode session={oauthSession} disabled={submitting} /> : null}
                                {codexOAuthMode === 'manual' ? (
                                  <div className="rounded-xl border border-[var(--app-border)] bg-[color-mix(in_oklab,var(--app-surface)_62%,transparent)] px-4 py-3 text-sm leading-6 text-[var(--app-text-muted)]">
                                    Open the auth URL in a new tab and finish Codex / ChatGPT sign-in there. When it lands on <span className="font-mono text-[var(--app-text)]">http://localhost:1455/auth/callback?code=...</span>, copy the full address-bar URL back here; click Manual callback fallback again if you need a fresh link.
                                  </div>
                                ) : null}
                                {codexOAuthMode !== 'device' && oauthSession.authURL ? (
                                  <label className="grid gap-2">
                                    <div className="flex flex-wrap items-center justify-between gap-2">
                                      <span className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">Auth URL</span>
                                      <Button
                                        type="button"
                                        variant="outline"
                                        onClick={() => {
                                          if (typeof window !== 'undefined') {
                                            window.open(oauthSession.authURL, '_blank', 'noopener,noreferrer')
                                          }
                                        }}
                                        disabled={submitting || !oauthSession.authURL}
                                      >
                                        Open in new tab
                                      </Button>
                                    </div>
                                    <textarea readOnly value={oauthSession.authURL} className="min-h-24 rounded-xl border border-[var(--app-border)] bg-transparent px-3 py-2 text-sm text-[var(--app-text)] outline-none" />
                                  </label>
                                ) : null}
                                {codexOAuthMode === 'manual' ? (
                                  <>
                                    <label className="grid gap-2">
                                      <span className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">Callback URL</span>
                                      <textarea
                                        value={callbackInput}
                                        onChange={(event) => setCallbackInput(event.target.value)}
                                        onKeyDown={handleCallbackKeyDown}
                                        placeholder="Paste the full http://localhost:1455/auth/callback?code=...&state=... URL"
                                        disabled={submitting}
                                        className="min-h-24 rounded-xl border border-[var(--app-border)] bg-transparent px-3 py-2 text-sm text-[var(--app-text)] outline-none transition-colors focus:border-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-60"
                                      />
                                    </label>
                                    <div className="flex justify-end">
                                      <Button
                                        type="button"
                                        onClick={() => void handleCompleteOAuth()}
                                        disabled={submitting || !oauthSession.sessionID}
                                        aria-label={pendingAction === 'oauth-complete' ? 'Completing…' : 'Complete remote sign-in'}
                                      >
                                        <OnboardingButtonLabel idle="Complete remote sign-in" pending="Completing…" isPending={pendingAction === 'oauth-complete'} />
                                      </Button>
                                    </div>
                                  </>
                                ) : null}
                              </div>
                            ) : null}
                          </div>
                        ) : (
                          <p className="text-sm leading-6 text-[var(--app-text-muted)]">
                            The selected provider does not expose a quick auth method here. Continue to workspace setup and connect it later from Settings.
                          </p>
                        )}
                      </div>
                    </>
                  ) : !providerLoading ? (
                    <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-4 text-sm leading-6 text-[var(--app-text-muted)]">
                      No providers are available yet. Continue to workspace setup and connect one later from Settings.
                    </div>
                  ) : null}

                  <div className="mt-auto flex items-center justify-between gap-3 pt-1">
                    <Button type="button" variant="outline" onClick={() => transitionToStep('identity')} disabled={submitting}>
                      Back
                    </Button>
                    <Button type="button" variant={providerAlreadyConnected || providerOptions.length === 0 ? 'primary' : 'outline'} onClick={handleProviderContinue} disabled={submitting}>
                      {finishButtonLabel}
                    </Button>
                  </div>
                </div>
              ) : null}

              {view === 'workspace' ? (
                <div className="grid h-full min-h-0 content-start gap-5">
                  <div className="grid min-h-0 content-start gap-5">
                    {workspaceStatusError ? (
                      <WorkspaceStatus
                        kind="error"
                        title="Workspace action needs attention"
                        message={workspaceStatusError}
                        actionLabel={workspaceLoadError ? 'Reload workspaces' : workspaceActionError ? 'Try browsing again' : 'Dismiss'}
                        onAction={workspaceLoadError ? retryWorkspaceLoad : workspaceActionError ? retryBrowsePath : () => setWorkspaceError(null)}
                      />
                    ) : null}

                    <section className="grid gap-3">
                      <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
                        <div>
                          <div className="flex items-center gap-2">
                            <h2 className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-muted)]">All workspaces</h2>
                            <span className="text-[11px] text-[var(--app-text-subtle)]">
                              {workspaces.length} saved · {discovered.length} discovered{workspaceLoading || workspaceRefreshing ? ' · refreshing' : ''}
                            </span>
                          </div>
                          <p className="mt-1 text-sm text-[var(--app-text-muted)]">Folders with AGENTS.md or git repos</p>
                        </div>
                        <Input
                          value={workspaceSearch}
                          onChange={(event) => setWorkspaceSearch(event.target.value)}
                          placeholder="Search workspaces…"
                          disabled={submitting}
                          className="sm:max-w-56"
                        />
                      </div>
                      <div className="grid gap-2 lg:grid-cols-3">
                        {visibleSavedWorkspaces.map((workspace) => (
                          <button
                            key={workspace.path}
                            type="button"
                            onClick={() => handleOpenWorkspace(workspace.path)}
                            disabled={submitting || selectingPath === workspace.path}
                            className="grid gap-1 rounded-lg border border-[var(--app-border)] bg-transparent px-4 py-3 text-left transition-colors hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)] disabled:cursor-wait disabled:opacity-60"
                          >
                            <span className="flex min-w-0 items-center justify-between gap-3">
                              <span className="truncate text-sm font-medium text-[var(--app-text)]">{workspace.workspaceName || fallbackWorkspaceNameFromPath(workspace.path)}</span>
                              <span className="shrink-0 text-xs text-[var(--app-text-muted)]">Open</span>
                            </span>
                            <span className="truncate text-xs text-[var(--app-text-muted)]">{formatWorkspacePath(workspace.path)}</span>
                          </button>
                        ))}
                        {filteredDiscovered.map(({ entry, savedWorkspace }) => (
                          <button
                            key={entry.path}
                            type="button"
                            onClick={() => (savedWorkspace ? handleOpenWorkspace(savedWorkspace.path) : handleSaveAndOpenFolder(entry))}
                            disabled={submitting || selectingPath === entry.path || savingPath === entry.path}
                            className={`group relative grid gap-1 rounded-lg border border-[var(--app-border)] bg-transparent py-3 pl-4 text-left transition-colors hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)] disabled:cursor-wait disabled:opacity-60 ${savedWorkspace ? 'pr-4' : 'pr-12'}`}
                          >
                            <span className="flex min-w-0 items-center justify-between gap-3">
                              <span className="truncate text-sm font-medium text-[var(--app-text)]">{savedWorkspace?.workspaceName || entry.name}</span>
                              {savedWorkspace ? (
                                <span className="shrink-0 text-xs text-[var(--app-text-muted)]">Open</span>
                              ) : (
                                <span
                                  className="absolute right-4 top-1/2 grid size-8 -translate-y-1/2 place-items-center text-[var(--app-text-muted)] opacity-0 transition-[color,opacity] group-hover:text-[var(--app-text)] group-hover:opacity-100 group-focus-visible:text-[var(--app-text)] group-focus-visible:opacity-100"
                                  title="Add workspace"
                                >
                                  <ChevronRight size={18} strokeWidth={2} aria-hidden="true" />
                                  <span className="sr-only">Add workspace</span>
                                </span>
                              )}
                            </span>
                            <span className="truncate text-xs text-[var(--app-text-muted)]">{formatWorkspacePath(entry.path)}</span>
                          </button>
                        ))}
                      </div>
                      {visibleSavedWorkspaces.length === 0 && filteredDiscovered.length === 0 ? (
                        <p className="border-l border-[var(--app-border)] py-2 pl-4 text-sm leading-6 text-[var(--app-text-muted)]">
                          {workspaceSearch.trim() ? 'No matching workspaces. Clear the search or open Explorer.' : 'No saved workspaces yet. Open Explorer to browse, add, or create one.'}
                        </p>
                      ) : null}
                    </section>
                  </div>

                  <div className="flex flex-wrap items-center justify-between gap-3 border-t border-[var(--app-border)] pt-4">
                    <Button type="button" variant="outline" onClick={() => transitionToStep('provider')} disabled={submitting}>
                      Back
                    </Button>
                    <Button
                      type="button"
                      onClick={openWorkspaceExplorer}
                      disabled={submitting}
                      className="min-w-40 border-[var(--app-primary)] bg-[var(--app-primary)] text-[var(--app-primary-text)] shadow-[0_12px_30px_rgb(0_0_0/0.28)] hover:bg-[var(--app-primary-hover)] hover:text-[var(--app-primary-text)] active:bg-[var(--app-primary-active)]"
                    >
                      <Plus size={16} />
                      Add from Explorer
                    </Button>
                  </div>
                </div>
              ) : null}
            </div>
          </div>
        </div>
      </main>

      {workspaceExplorerOpen && view === 'workspace' ? (
        <div className="fixed inset-0 z-[10000] grid place-items-center p-3 sm:p-6" role="dialog" aria-modal="true" aria-label="Add workspace from Explorer">
          <button type="button" className="absolute inset-0 bg-[var(--app-backdrop)]" onClick={() => setWorkspaceExplorerOpen(false)} aria-label="Close Explorer" />
          <div className="relative z-10 flex h-[min(44rem,calc(100dvh-24px))] w-full max-w-xl flex-col overflow-hidden rounded-3xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-[var(--shadow-panel)]">
            <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-5 py-4 sm:px-6">
              <div className="grid gap-1">
                <h2 className="text-xl font-semibold tracking-tight text-[var(--app-text)]">Add workspace</h2>
                <p className="text-sm leading-6 text-[var(--app-text-muted)]">Browse to a folder, create one if needed, then add it to continue.</p>
              </div>
              <ModalCloseButton onClick={() => setWorkspaceExplorerOpen(false)} aria-label="Close Explorer" />
            </div>
            <div className="min-h-0 flex-1">
              <WorkspaceFolderTree
                browser={browser}
                browserLoading={browserLoading}
                browserError={browserError}
                workspaces={workspaces}
                selectingPath={selectingPath}
                savingPath={savingPath}
                showTemporaryAction={false}
                openCreatedFolder
                showPathInWorkspaceAction
                onBrowsePath={(path) => void browsePath(path)}
                onOpenWorkspace={handleOpenWorkspace}
                onUseFolderTemporarily={handleUseBrowsedFolder}
                onCreateWorkspace={handleSaveAndOpenFolder}
                onCreateFolder={createFolder}
              />
            </div>
          </div>
        </div>
      ) : null}
    </div>
  )
}
