import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Input } from '../../../../components/ui/input'
import { patchDesktopOnboarding } from '../api'
import type { DesktopOnboardingStatus } from '../types'
import { startCodexOAuth } from '../../settings/mutations/start-codex-oauth'
import { getCodexOAuthStatus } from '../../settings/queries/get-codex-oauth-status'
import { completeCodexOAuth } from '../../settings/mutations/complete-codex-oauth'
import { upsertAuthCredential } from '../../settings/mutations/upsert-auth-credential'
import { verifyAuthCredential } from '../../settings/mutations/verify-auth-credential'
import { listProviders } from '../../settings/queries/list-providers'
import type { AuthMethod, CodexOAuthSession, ProviderStatus, StartCodexOAuthInput, UpsertAuthCredentialInput } from '../../settings/types/auth'
import { WorkspaceFolderTree } from '../../../workspaces/launcher/components/workspace-folder-tree'
import { WorkspaceStatus } from '../../../workspaces/launcher/components/workspace-status'
import { useWorkspaceLauncher } from '../../../workspaces/launcher/state/use-workspace-launcher'
import { buildWorkspaceRouteSlugMap, workspaceRouteSlugBase } from '../../../workspaces/launcher/services/workspace-route'
import { formatWorkspacePath } from '../../../workspaces/launcher/services/workspace-format'
import type { WorkspaceDiscoverEntry, WorkspaceResolution } from '../../../workspaces/launcher/types/workspace'

type OnboardingStep = 'identity' | 'provider' | 'workspace'
type CodexOAuthMode = StartCodexOAuthInput['method']
type PendingAction = 'identity' | 'provider-save' | 'oauth-browser' | 'oauth-manual' | 'oauth-complete' | 'finalize' | 'workspace' | null

const SWARM_MARK_SRC = '/favicon.svg'
const STEP_TRANSITION_MS = 140

const ONBOARDING_STEPS: Record<OnboardingStep, { stepLabel: string; title: string; subtitle: string }> = {
  identity: {
    stepLabel: 'Step 1 of 3 · Identity',
    title: 'Hi, I’m Swarm — your AI command center.',
    subtitle: 'Start with the basics: what should Swarm call you, and what should this device be named?',
  },
  provider: {
    stepLabel: 'Step 2 of 3 · Provider',
    title: 'Connect your AI provider.',
    subtitle: 'Add an API key or sign in now so Swarm is ready to work. You’ll choose your first workspace next.',
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

function pendingMessage(action: PendingAction): string | null {
  switch (action) {
    case 'identity':
      return 'Saving identity…'
    case 'provider-save':
      return 'Saving and verifying provider…'
    case 'oauth-browser':
      return 'Starting browser sign-in…'
    case 'oauth-manual':
      return 'Preparing remote sign-in…'
    case 'oauth-complete':
      return 'Completing sign-in…'
    case 'finalize':
    case 'workspace':
      return 'Finishing onboarding…'
    default:
      return null
  }
}

function OnboardingBrandHeader({ restart, step }: { restart: boolean; step: OnboardingStep }) {
  const stepCopy = ONBOARDING_STEPS[step]

  return (
    <div className="grid min-h-[8.5rem] content-start gap-5 transition-[opacity,transform] duration-200 ease-out">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <div className="flex size-10 items-center justify-center rounded-2xl border border-[var(--app-border-strong)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))] p-2 shadow-[0_10px_32px_rgb(0_0_0/0.18)]">
            <img src={SWARM_MARK_SRC} alt="" className="size-6" aria-hidden="true" />
          </div>
          <div className="grid gap-0.5">
            <span className="text-sm font-semibold text-[var(--app-text)]">Swarm</span>
            <span className="text-xs text-[var(--app-text-muted)]">{restart ? 'Setup review' : 'First launch'}</span>
          </div>
        </div>
        <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1 text-xs font-medium text-[var(--app-text-muted)]">
          {stepCopy.stepLabel}
        </span>
      </div>
      <div className="grid gap-2">
        <h1 className="text-2xl font-semibold tracking-tight text-[var(--app-text)]">{stepCopy.title}</h1>
        <p className="max-w-2xl text-sm leading-6 text-[var(--app-text-muted)]">{stepCopy.subtitle}</p>
      </div>
    </div>
  )
}

function FeedbackSlot({ error, notice, progress }: { error: string | null; notice: string | null; progress: string | null }) {
  const message = error || progress || notice
  const kind = error ? 'error' : progress ? 'progress' : notice ? 'success' : null

  return (
    <div className="min-h-[3.5rem]" aria-live="polite">
      {message && kind ? (
        <div
          role={kind === 'error' ? 'alert' : 'status'}
          className={[
            'rounded-xl border px-4 py-3 text-sm transition-[opacity,transform] duration-200 ease-out',
            kind === 'error'
              ? 'border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] text-[var(--app-danger)]'
              : kind === 'success'
                ? 'border-[var(--app-success-border)] bg-[var(--app-success-bg)] text-[var(--app-success)]'
                : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)]',
          ].join(' ')}
        >
          {message}
        </div>
      ) : null}
    </div>
  )
}

export function DesktopOnboardingGate({ status: initialStatus, restart = false, onReload, onComplete }: DesktopOnboardingGateProps) {
  const navigate = useNavigate()
  const [status, setStatus] = useState(initialStatus)
  const [step, setStep] = useState<OnboardingStep>(() => (restart ? 'identity' : deriveInitialStep(initialStatus)))
  const [pendingAction, setPendingAction] = useState<PendingAction>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [panelVisible, setPanelVisible] = useState(true)
  const transitionTimerRef = useRef<number | null>(null)

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
  const [credentialValue, setCredentialValue] = useState('')
  const [codexOAuthMode, setCodexOAuthMode] = useState<CodexOAuthMode>('browser')
  const [oauthSession, setOAuthSession] = useState<CodexOAuthSession | null>(null)
  const [callbackInput, setCallbackInput] = useState('')
  const [workspaceSearch, setWorkspaceSearch] = useState('')
  const [workspaceError, setWorkspaceError] = useState<string | null>(null)
  const [createParentPath, setCreateParentPath] = useState('')
  const [createFolderName, setCreateFolderName] = useState('')

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
  } = useWorkspaceLauncher({ applyDocumentTheme: false })

  const selectedProvider = useMemo(
    () => providerOptions.find((provider) => provider.id === providerID) ?? providerOptions[0] ?? null,
    [providerID, providerOptions],
  )
  const manualMethods = useMemo(() => (selectedProvider ? apiCompatibleMethods(selectedProvider) : []), [selectedProvider])
  const selectedManualMethod = manualMethods[0] ?? null
  const providerAlreadyConnected = Boolean(selectedProvider && status.auth.activeProviders.includes(selectedProvider.id))
  const canStartOAuth = supportsCodexOAuth(selectedProvider)
  const canQuickAuthenticate = Boolean(selectedManualMethod || canStartOAuth)
  const submitting = pendingAction !== null
  const progress = pendingMessage(pendingAction)
  const workspaceSlugByPath = useMemo(() => buildWorkspaceRouteSlugMap(workspaces), [workspaces])
  const pinnedWorkspaces = useMemo(() => workspaces.slice(0, 4), [workspaces])
  const savedWorkspaceByPath = useMemo(() => new Map(workspaces.map((workspace) => [workspace.path, workspace])), [workspaces])
  const filteredDiscovered = useMemo(() => {
    const query = workspaceSearch.trim().toLowerCase()
    const rows = discovered.map((entry) => ({ entry, savedWorkspace: savedWorkspaceByPath.get(entry.path) }))
    if (!query) {
      return rows.slice(0, 6)
    }
    return rows.filter(({ entry, savedWorkspace }) => [entry.name, entry.path, savedWorkspace?.workspaceName ?? ''].join(' ').toLowerCase().includes(query)).slice(0, 8)
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
    return () => {
      if (transitionTimerRef.current !== null) {
        window.clearTimeout(transitionTimerRef.current)
      }
    }
  }, [])

  useEffect(() => {
    if (!status.auth.activeProviders.includes(providerID) && !providerOptions.some((provider) => provider.id === providerID)) {
      setProviderID(status.auth.activeProviders[0] || providerOptions[0]?.id || '')
    }
  }, [providerID, providerOptions, status.auth.activeProviders])

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
    if (codexOAuthMode !== 'browser' || !oauthSession?.sessionID || oauthSession.status === 'success' || oauthSession.status === 'error') {
      return
    }

    const timer = window.setInterval(() => {
      void getCodexOAuthStatus(oauthSession.sessionID)
        .then((next) => {
          setOAuthSession(next)
          if (next.status === 'success') {
            setError(null)
            void reloadStatus()
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

  const transitionToStep = (nextStep: OnboardingStep) => {
    if (nextStep === step) {
      return
    }
    if (transitionTimerRef.current !== null) {
      window.clearTimeout(transitionTimerRef.current)
    }
    setPanelVisible(false)
    transitionTimerRef.current = window.setTimeout(() => {
      setStep(nextStep)
      setPanelVisible(true)
      transitionTimerRef.current = null
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
      child: false,
    })
    const refreshed = await onReload()
    setStatus(refreshed)
    setUsername(refreshed.identity.username)
    setSwarmName(refreshed.config.swarmName)
    return refreshed
  }

  const finalizeOnboarding = async () => {
    await patchDesktopOnboarding({ desktopOnboardingComplete: true })
    const next = await reloadStatus()
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
    await navigateToWorkspace(resolution, fallbackPath)
    await finalizeOnboarding()
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
    void (async () => {
      setPendingAction('workspace')
      setError(null)
      setWorkspaceError(null)
      try {
        const resolution = await openWorkspace(path)
        await finishWithWorkspace(resolution, path)
      } catch (err) {
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
    void (async () => {
      setPendingAction('workspace')
      setError(null)
      setWorkspaceError(null)
      try {
        const savedWorkspace = savedWorkspaceByPath.get(entry.path)
        if (!savedWorkspace) {
          await saveWorkspace({
            path: entry.path,
            name: entry.name || fallbackWorkspaceNameFromPath(entry.path),
            themeId: 'inherit',
            makeCurrent: true,
          })
        }
        const resolution = await openWorkspace(entry.path)
        await finishWithWorkspace(resolution, entry.path)
      } catch (err) {
        setWorkspaceError(err instanceof Error ? err.message : 'Failed to save and open workspace')
      } finally {
        setPendingAction(null)
      }
    })()
  }

  const handleUseBrowsedFolder = (path: string) => {
    handleSaveAndOpenFolder({ path, name: fallbackWorkspaceNameFromPath(path) })
  }

  const handleCreateFolderSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (submitting) {
      return
    }
    void (async () => {
      const parentPath = createParentPath.trim() || browser?.resolvedPath || browser?.homePath || ''
      const folderName = createFolderName.trim()
      if (!parentPath || !folderName) {
        setWorkspaceError('Choose a parent folder and name the new workspace folder.')
        return
      }
      setPendingAction('workspace')
      setError(null)
      setWorkspaceError(null)
      try {
        const createdPath = await createFolder(parentPath, folderName)
        if (!createdPath) {
          throw new Error('Folder was not created.')
        }
        await saveWorkspace({
          path: createdPath,
          name: folderName,
          themeId: 'inherit',
          makeCurrent: true,
        })
        const resolution = await openWorkspace(createdPath)
        await finishWithWorkspace(resolution, createdPath)
      } catch (err) {
        setWorkspaceError(err instanceof Error ? err.message : 'Failed to create workspace')
      } finally {
        setPendingAction(null)
      }
    })()
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

      const saved = await upsertAuthCredential(payload)
      const verification = await verifyAuthCredential({ provider: saved.provider, id: saved.id })
      if (!verification.connected) {
        throw new Error(verification.message || 'Credential saved, but verification failed.')
      }

      setCredentialValue('')
      await reloadStatus()
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
    setPendingAction(method === 'browser' ? 'oauth-browser' : 'oauth-manual')
    setError(null)
    setNotice(null)
    setCallbackInput('')
    try {
      const session = await startCodexOAuth({
        provider: selectedProvider.id,
        active: true,
        method,
      })
      setCodexOAuthMode(method)
      setOAuthSession(session)
      if (method === 'browser' && session.authURL && typeof window !== 'undefined') {
        window.open(session.authURL, '_blank', 'noopener,noreferrer')
      }
      setNotice(method === 'browser'
        ? 'Finish local sign-in in your browser. Swarm will continue when it sees the callback.'
        : 'Open the remote auth URL anywhere, then paste the callback URL or code here.')
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
      setNotice('Provider connected. Choose your workspace when you’re ready.')
      transitionToStep('workspace')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to complete remote sign-in')
    } finally {
      setPendingAction(null)
    }
  }

  return (
    <div className="absolute inset-0 flex items-center justify-center overflow-y-auto bg-[radial-gradient(circle_at_top,color-mix(in_oklab,var(--app-primary)_24%,transparent),transparent_54%),radial-gradient(circle_at_bottom_left,color-mix(in_oklab,var(--app-warning)_12%,transparent),transparent_42%),var(--app-bg)] px-6 py-8">
      <Card className="relative w-full max-w-4xl overflow-hidden border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] transition-[box-shadow,transform] duration-300 ease-out">
        <div className="grid min-h-[42rem] grid-rows-[auto_auto_minmax(0,1fr)] gap-5 p-8">
          <OnboardingBrandHeader restart={restart} step={step} />

          <FeedbackSlot error={error} notice={notice} progress={progress} />

          <div className="relative min-h-[25rem] overflow-hidden">
            <div
              key={step}
              className={[
                'h-full transition-[opacity,transform] duration-200 ease-out',
                panelVisible ? 'translate-y-0 opacity-100' : 'translate-y-2 opacity-0',
              ].join(' ')}
            >
              {step === 'identity' ? (
                <form className="grid h-full content-start gap-6" onSubmit={handleIdentitySubmit}>
                  <div className="grid gap-2">
                    <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-username">
                      What should Swarm call you?
                    </label>
                    <Input
                      id="desktop-onboarding-username"
                      autoFocus={!status.identity.bootstrapped}
                      value={username}
                      onChange={(event) => setUsername(event.target.value)}
                      placeholder="Your name"
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
                      This is the device label Swarm shows in discovery and launcher screens. It does not change your username.
                    </p>
                  </div>

                  <div className="flex justify-end pt-1">
                    <Button type="submit" disabled={submitting}>
                      {pendingAction === 'identity' ? 'Saving…' : 'Continue to provider'}
                    </Button>
                  </div>
                </form>
              ) : null}

              {step === 'provider' ? (
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
                      <div className="grid gap-2">
                        <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-provider">
                          Provider
                        </label>
                        <select
                          id="desktop-onboarding-provider"
                          value={providerID}
                          onChange={(event) => {
                            setProviderID(event.target.value)
                            setCredentialValue('')
                            setCallbackInput('')
                            setOAuthSession(null)
                            setError(null)
                            setNotice(null)
                          }}
                          disabled={submitting}
                          className="h-11 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 text-sm text-[var(--app-text)] outline-none transition-colors focus:border-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-60"
                        >
                          {providerOptions.map((provider) => (
                            <option key={provider.id} value={provider.id}>
                              {provider.id}
                            </option>
                          ))}
                        </select>
                      </div>

                      {providerAlreadyConnected ? (
                        <div className="rounded-2xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-4 py-4 text-sm leading-6 text-[var(--app-success)]">
                          {selectedProvider?.id || 'Selected provider'} is connected. Continue when you’re ready.
                        </div>
                      ) : canQuickAuthenticate ? (
                        <div className="grid gap-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                          {selectedManualMethod ? (
                            <>
                              <label className="grid gap-2">
                                <span className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">
                                  {credentialLabel(selectedManualMethod)}
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
                            </>
                          ) : null}

                          {canStartOAuth && oauthSession ? (
                            <div className="grid gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 text-sm leading-6 text-[var(--app-text-muted)]">
                              <div>
                                {codexOAuthMode === 'browser' ? 'Local browser sign-in' : 'Remote browser sign-in'} status:{' '}
                                <span className={oauthSession.status === 'success' ? 'font-medium text-[var(--app-success)]' : 'font-medium text-[var(--app-text)]'}>
                                  {oauthSession.status || 'waiting'}
                                </span>
                                {oauthSession.error ? <div className="text-[var(--app-danger)]">{oauthSession.error}</div> : null}
                              </div>
                              {oauthSession.authURL ? (
                                <label className="grid gap-2">
                                  <span className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">Auth URL</span>
                                  <textarea readOnly value={oauthSession.authURL} className="min-h-24 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text)] outline-none" />
                                </label>
                              ) : null}
                              {codexOAuthMode === 'manual' ? (
                                <>
                                  <label className="grid gap-2">
                                    <span className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">Callback URL or code</span>
                                    <textarea
                                      value={callbackInput}
                                      onChange={(event) => setCallbackInput(event.target.value)}
                                      onKeyDown={handleCallbackKeyDown}
                                      placeholder="Paste the callback URL, query string, or authorization code"
                                      disabled={submitting}
                                      className="min-h-24 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text)] outline-none transition-colors focus:border-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-60"
                                    />
                                  </label>
                                  <div className="flex justify-end">
                                    <Button type="button" onClick={() => void handleCompleteOAuth()} disabled={submitting || !oauthSession.sessionID}>
                                      {pendingAction === 'oauth-complete' ? 'Completing…' : 'Complete remote sign-in'}
                                    </Button>
                                  </div>
                                </>
                              ) : null}
                            </div>
                          ) : null}

                          <div className="flex flex-wrap justify-end gap-3">
                            {canStartOAuth ? (
                              <>
                                <Button type="button" variant="outline" onClick={() => void handleStartOAuth('browser')} disabled={submitting}>
                                  {pendingAction === 'oauth-browser' ? 'Opening…' : 'Local browser sign-in'}
                                </Button>
                                <Button type="button" variant="outline" onClick={() => void handleStartOAuth('manual')} disabled={submitting}>
                                  {pendingAction === 'oauth-manual' ? 'Preparing…' : 'Remote browser sign-in'}
                                </Button>
                              </>
                            ) : null}
                            {selectedManualMethod ? (
                              <Button type="button" onClick={() => void handleProviderSave()} disabled={submitting}>
                                {pendingAction === 'provider-save' ? 'Verifying…' : 'Save provider'}
                              </Button>
                            ) : null}
                          </div>
                        </div>
                      ) : (
                        <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-4 text-sm leading-6 text-[var(--app-text-muted)]">
                          The selected provider does not expose a quick auth method here. Continue to workspace setup and connect it later from Settings.
                        </div>
                      )}
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

              {step === 'workspace' ? (
                <div className="grid h-full min-h-0 gap-5 lg:grid-cols-[minmax(0,1fr)_18rem]">
                  <div className="grid min-h-0 content-start gap-5">
                    {workspaceLoading ? (
                      <WorkspaceStatus
                        kind="empty"
                        title="Finding workspaces"
                        message="Looking for saved and discovered folders on this computer…"
                      />
                    ) : null}

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
                      <div className="flex items-center justify-between gap-3">
                        <div>
                          <h2 className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-muted)]">Saved workspaces</h2>
                          <p className="mt-1 text-sm text-[var(--app-text-muted)]">Open a pinned workspace to finish setup.</p>
                        </div>
                        <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2.5 py-1 text-xs text-[var(--app-text-muted)]">
                          {workspaces.length}
                        </span>
                      </div>
                      {pinnedWorkspaces.length > 0 ? (
                        <div className="grid gap-2">
                          {pinnedWorkspaces.map((workspace) => (
                            <button
                              key={workspace.path}
                              type="button"
                              onClick={() => handleOpenWorkspace(workspace.path)}
                              disabled={submitting || selectingPath === workspace.path}
                              className="grid gap-1 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-left transition-colors hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)] disabled:cursor-wait disabled:opacity-60"
                            >
                              <span className="truncate text-sm font-medium text-[var(--app-text)]">{workspace.workspaceName || fallbackWorkspaceNameFromPath(workspace.path)}</span>
                              <span className="truncate text-xs text-[var(--app-text-muted)]">{formatWorkspacePath(workspace.path)}</span>
                            </button>
                          ))}
                        </div>
                      ) : (
                        <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-4 text-sm leading-6 text-[var(--app-text-muted)]">
                          No saved workspaces yet. Add a discovered folder, browse to one with Explorer, or create a new folder below.
                        </div>
                      )}
                    </section>

                    <section className="grid gap-3">
                      <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
                        <div>
                          <div className="flex items-center gap-2">
                            <h2 className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-muted)]">All workspaces</h2>
                            <span className="rounded-full bg-[var(--app-surface-subtle)] px-2 py-0.5 text-[11px] text-[var(--app-text-muted)]">{discovered.length}</span>
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
                      {filteredDiscovered.length > 0 ? (
                        <div className="grid gap-2">
                          {filteredDiscovered.map(({ entry, savedWorkspace }) => (
                            <button
                              key={entry.path}
                              type="button"
                              onClick={() => (savedWorkspace ? handleOpenWorkspace(savedWorkspace.path) : handleSaveAndOpenFolder(entry))}
                              disabled={submitting || selectingPath === entry.path || savingPath === entry.path}
                              className="grid gap-1 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-left transition-colors hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)] disabled:cursor-wait disabled:opacity-60"
                            >
                              <span className="flex min-w-0 items-center justify-between gap-3">
                                <span className="truncate text-sm font-medium text-[var(--app-text)]">{savedWorkspace?.workspaceName || entry.name}</span>
                                <span className="shrink-0 text-xs text-[var(--app-text-muted)]">{savedWorkspace ? 'Open' : 'Add'}</span>
                              </span>
                              <span className="truncate text-xs text-[var(--app-text-muted)]">{formatWorkspacePath(entry.path)}</span>
                            </button>
                          ))}
                        </div>
                      ) : (
                        <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-4 text-sm leading-6 text-[var(--app-text-muted)]">
                          {workspaceSearch.trim() ? 'No matching discovered folders. Clear the search or use Explorer to browse to a folder.' : 'No discovered folders yet. Use Explorer to browse to a folder, or create a new one below.'}
                        </div>
                      )}
                    </section>
                  </div>

                  <aside className="min-h-[26rem] overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)]">
                    <WorkspaceFolderTree
                      browser={browser}
                      browserLoading={browserLoading}
                      browserError={browserError}
                      workspaces={workspaces}
                      selectingPath={selectingPath}
                      savingPath={savingPath}
                      useFolderLabel="Use this folder"
                      onBrowsePath={(path) => void browsePath(path)}
                      onOpenWorkspace={handleOpenWorkspace}
                      onUseFolderTemporarily={handleUseBrowsedFolder}
                      onCreateWorkspace={handleSaveAndOpenFolder}
                      onCreateFolder={createFolder}
                    />
                  </aside>

                  <form className="grid gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 lg:col-span-2" onSubmit={handleCreateFolderSubmit}>
                    <div className="flex flex-col gap-3 sm:flex-row">
                      <label className="grid flex-1 gap-2">
                        <span className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">Create in</span>
                        <Input
                          value={createParentPath}
                          onChange={(event) => setCreateParentPath(event.target.value)}
                          placeholder={browser?.resolvedPath || browser?.homePath || 'Parent folder path'}
                          disabled={submitting}
                        />
                      </label>
                      <label className="grid flex-1 gap-2">
                        <span className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">New folder</span>
                        <Input
                          value={createFolderName}
                          onChange={(event) => setCreateFolderName(event.target.value)}
                          placeholder="my-project"
                          disabled={submitting}
                        />
                      </label>
                    </div>
                    <div className="flex flex-wrap items-center justify-between gap-3">
                      <Button type="button" variant="outline" onClick={() => transitionToStep('provider')} disabled={submitting}>
                        Back
                      </Button>
                      <div className="flex flex-wrap justify-end gap-3">
                        <Button type="button" variant="outline" onClick={() => void browsePath(createParentPath.trim() || browser?.resolvedPath || browser?.homePath || '')} disabled={submitting || browserLoading}>
                          {workspaceRefreshing || browserLoading ? 'Browsing…' : 'Browse / retry path'}
                        </Button>
                        <Button type="submit" disabled={submitting}>
                          {pendingAction === 'workspace' ? 'Creating…' : 'Create and open workspace'}
                        </Button>
                      </div>
                    </div>
                  </form>
                </div>
              ) : null}
            </div>
          </div>
        </div>
      </Card>
    </div>
  )
}
