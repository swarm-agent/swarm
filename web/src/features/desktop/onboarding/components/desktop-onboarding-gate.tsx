import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { queryClient } from '../../../../app/query-client'
import { Button } from '../../../../components/ui/button'
import { Input } from '../../../../components/ui/input'
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
import { WorkspaceStatus } from '../../../workspaces/launcher/components/workspace-status'
import { applyWorkspaceTheme, workspaceThemeDefaultId } from '../../../workspaces/launcher/services/workspace-theme'
import { agentStateQueryOptions, draftModelQueryOptions, modelOptionsQueryOptions, modelProfilesQueryOptions } from '../../../queries/query-options'

import { completeAccountOnboarding } from '../../orchestrate/project-entry-policy'
import { requestJson } from '../../../../app/api'
import { registerProjectFolder } from '../../runtime/project-creation'
import { createProjectConversation, projectConversationLink } from '../../orchestrate/project-conversations'
import { projectRouteSegment } from '../../orchestrate/project-route'
import { PersonalizingCard } from '../../orchestrate/project-creation-flow'
import type { CreationProject, CreationWorkspace } from '../../state/project-creation'

type OnboardingStep = 'identity' | 'provider' | 'project' | 'workspaces'
type CodexOAuthMode = StartCodexOAuthInput['method']
type ProviderSetupMode = 'api' | 'oauth-device' | 'oauth-browser' | 'oauth-manual' | null
type PendingAction = 'identity' | 'provider-save' | 'oauth-device' | 'oauth-browser' | 'oauth-manual' | 'oauth-complete' | 'finalize' | 'project-create' | 'folder-add' | null

type OnboardingView = OnboardingStep | 'setup' | 'personalizing'

const SWARM_MARK_SRC = '/favicon.svg'
const STEP_TRANSITION_MS = 220
const ONBOARDING_READY_HOLD_MS = 1_000
const ONBOARDING_STEPS: Record<OnboardingStep, { stepLabel: string; title: string; subtitle: string }> = {
  identity: {
    stepLabel: 'Step 1 of 4 · Identity',
    title: 'Hi, I’m Swarm — your AI command center.',
    subtitle: 'Start with the basics: your username and this device name.',
  },
  provider: {
    stepLabel: 'Step 2 of 4 · Provider',
    title: 'Connect your AI provider.',
    subtitle: 'Connect a provider now or skip ahead. No workspace or credit card required.',
  },
  project: {
    stepLabel: 'Step 3 of 4 · Project',
    title: 'Name your first project.',
    subtitle: 'Projects organize your conversations, workspaces, and AI context.',
  },
  workspaces: {
    stepLabel: 'Step 4 of 4 · Workspaces',
    title: 'Add workspaces to your project.',
    subtitle: 'Select code folders to personalize AI context, or skip straight to chatting with Swarm.',
  },
}

interface DesktopOnboardingGateProps {
  status: DesktopOnboardingStatus
  restart?: boolean
  onReload: () => Promise<DesktopOnboardingStatus>
  onComplete: (status: DesktopOnboardingStatus) => void
}

function deriveInitialStep(status: DesktopOnboardingStatus): OnboardingStep {
  return status.identity.bootstrapped ? 'provider' : 'identity'
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
    case 'project-create':
      return 'Creating your project…'
    case 'folder-add':
      return 'Registering workspace folder…'
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

function OnboardingBrandHeader({ restart, step, visible, projectName }: { restart: boolean; step: OnboardingStep; visible: boolean; projectName?: string }) {
  const stepCopy = ONBOARDING_STEPS[step]
  const visibleSteps: OnboardingStep[] = restart ? ['identity', 'provider'] : ['identity', 'provider', 'project', 'workspaces']
  const stepIndex = visibleSteps.indexOf(step) + 1
  const stepLabel = restart ? `Step ${stepIndex} of 2 · ${step === 'identity' ? 'Identity' : 'Provider'}` : stepCopy.stepLabel
  const title = step === 'workspaces' && projectName?.trim()
    ? `Add workspaces into ${projectName.trim()}?`
    : stepCopy.title

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
        <div className="grid min-w-32 gap-2 text-right" aria-label={stepLabel}>
          <span className="text-[11px] font-medium uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">{stepIndex} / {visibleSteps.length}</span>
          <div className="grid grid-flow-col auto-cols-fr gap-1">
            {visibleSteps.map((item) => (
              <span
                key={item}
                className={item === step ? 'h-0.5 bg-[var(--app-primary)]' : 'h-0.5 bg-[color-mix(in_oklab,var(--app-border)_62%,transparent)]'}
              />
            ))}
          </div>
        </div>
      </div>
      <div className="grid gap-2">
        <h1 className="text-2xl font-semibold tracking-tight text-[var(--app-text)]">{title}</h1>
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
            Finishing account setup before opening your projects.
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
  const [pendingAction, updatePendingAction] = useState<PendingAction>(null)
  const pendingActionRef = useRef<PendingAction>(null)
  const setPendingAction = (action: PendingAction) => {
    pendingActionRef.current = action
    updatePendingAction(action)
  }
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [panelVisible, setPanelVisible] = useState(true)
  const [closing, setClosing] = useState(false)
  const transitionTimerRef = useRef<number | null>(null)
  const transitionFrameRef = useRef<number | null>(null)
  const targetViewRef = useRef<OnboardingView>(view)

  const [username, setUsername] = useState(initialStatus.identity.username)
  const [swarmName, setSwarmName] = useState(initialStatus.config.swarmName)
  const [projectName, setProjectName] = useState('')
  const [projectDescription, setProjectDescription] = useState('')
  const [workspaceCatalog, setWorkspaceCatalog] = useState<Array<{ id: string; path: string; label: string }>>([])
  const [selectedWorkspaceIds, setSelectedWorkspaceIds] = useState<string[]>([])
  const [customFolderPath, setCustomFolderPath] = useState('')
  const [createdProject, setCreatedProject] = useState<CreationProject | null>(null)
  const [personalizingBusy, setPersonalizingBusy] = useState(false)
  const autoOpenedRef = useRef(false)

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
  const submitting = pendingAction !== null || !panelVisible
  const progress = pendingMessage(pendingAction)
  const mustUseOnboardingProviderAPI = status.heuristics.credentialCount === 0 && status.heuristics.agentCount === 0
  const finishButtonLabel = pendingAction === 'finalize'
    ? 'Finishing…'
    : restart
      ? 'Finish setup review'
      : providerAlreadyConnected
        ? 'Continue to project'
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
    if (step !== 'provider' || (codexOAuthMode !== 'browser' && codexOAuthMode !== 'device') || !oauthSession?.sessionID || oauthSession.status === 'success' || oauthSession.status === 'error') {
      return
    }

    let cancelled = false
    let inFlight = false
    const timer = window.setInterval(() => {
      if (inFlight || pendingActionRef.current !== null) return
      inFlight = true
      void getCodexOAuthStatus(oauthSession.sessionID)
        .then(async (next) => {
          if (cancelled) return
          if (next.status !== 'success') setOAuthSession(next)
          if (next.status === 'error') {
            setError(next.error || 'Codex sign-in failed. Choose a fallback below if device authorization is unavailable.')
          }
          if (next.status === 'success') {
            setError(null)
            await reloadStatus()
            if (cancelled) return
            await refreshAuthDependentQueries()
            if (cancelled) return
            setOAuthSession(next)
            if (restart) {
              setNotice('Provider connected. Review complete.')
              transitionToStep('provider')
            } else {
              setNotice('Provider connected. Advancing to project setup…')
              transitionToStep('project')
            }
          }
        })
        .catch((err) => {
          if (!cancelled) setError(err instanceof Error ? err.message : 'Failed to refresh OAuth status')
        })
        .finally(() => { inFlight = false })
    }, 1500)

    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [codexOAuthMode, oauthSession, step])

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
    if (targetViewRef.current === nextStep && view === nextStep) {
      setPanelVisible(true)
      return
    }
    targetViewRef.current = nextStep
    clearStepTransition()
    setPanelVisible(false)
    transitionTimerRef.current = window.setTimeout(() => {
      setStep(nextStep)
      setView(nextStep)
      transitionTimerRef.current = null
      revealTransitionPanel()
    }, STEP_TRANSITION_MS)
  }

  const transitionToSetup = () => {
    if (targetViewRef.current === 'setup') {
      if (view === 'setup') {
        setPanelVisible(true)
      }
      return
    }
    targetViewRef.current = 'setup'
    clearStepTransition()
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

  const persistIdentity = async () => {
    const normalizedUsername = username.trim()
    const normalizedName = swarmName.trim() || 'default'
    if (!status.identity.bootstrapped && !normalizedUsername) {
      throw new Error('Username is required for the product owner identity.')
    }
    // A previous request can succeed even if its response was lost.
    const current = await onReload()
    setStatus(current)
    await patchDesktopOnboarding({
      username: current.identity.bootstrapped ? undefined : normalizedUsername,
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
    setPendingAction('finalize'); transitionToSetup()
    const [, next] = await Promise.all([
      waitForOnboardingReadyHold(),
      patchDesktopOnboarding({ desktopOnboardingComplete: true }).then(() => reloadStatus()),
    ])
    setStatus(next)
    return next
  }

  const handleProviderContinue = () => {
    if (submitting || pendingActionRef.current !== null) {
      return
    }
    setError(null)
    setNotice(null)
    setCredentialValue('')
    setCallbackInput('')
    setOAuthSession(null)
    void completeAccountOnboarding({
      finalize: () => finalizeOnboarding(),
      refreshAuth: refreshAuthDependentQueries,
      openProjects: () => navigate({ to: '/projects' }),
      complete: next => { setClosing(true); onComplete(next) },
    }).catch(err => {
      setError(err instanceof Error ? err.message : 'Unable to finish onboarding')
      transitionToStep('provider')
    }).finally(() => setPendingAction(null))
  }

  const handleIdentitySubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (submitting || pendingActionRef.current !== null) {
      return
    }
    void handleIdentityContinue()
  }

  const handleIdentityContinue = async () => {
    if (pendingActionRef.current !== null) return
    const invalid = !status.identity.bootstrapped && !username.trim() ? 'desktop-onboarding-username' : null
    if (invalid) {
      setError('Your name is required.')
      document.getElementById(invalid)?.focus()
      return
    }
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

  useEffect(() => {
    if (step !== 'workspaces') return
    let active = true
    void requestJson<{ workspaces?: Array<{ id?: string; workspace_id?: string; path: string; name?: string }> }>('/v1/workspace/list?limit=200')
      .then(({ workspaces = [] }) => {
        if (!active) return
        const rows = workspaces.flatMap(w => (w.id || w.workspace_id) ? [{ id: (w.id || w.workspace_id)!, path: w.path, label: w.name || w.path }] : [])
        setWorkspaceCatalog(rows)
      })
      .catch(() => { /* non-fatal */ })
    return () => { active = false }
  }, [step])

  const handleAddCustomFolder = async () => {
    const raw = customFolderPath.trim()
    if (!raw || submitting) return
    setPendingAction('folder-add')
    setError(null)
    try {
      const reg = await registerProjectFolder(raw)
      setWorkspaceCatalog(prev => {
        const next = [...prev.filter(w => w.id !== reg.workspace_id), { id: reg.workspace_id, path: reg.path, label: reg.label }]
        return next
      })
      setSelectedWorkspaceIds(ids => [...new Set([...ids, reg.workspace_id])])
      setCustomFolderPath('')
      setNotice(`Folder registered: ${reg.path}`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unable to register folder')
    } finally {
      setPendingAction(null)
    }
  }

  const finishAndOpenProjectChat = async (project: CreationProject) => {
    setPendingAction('finalize')
    try {
      await patchDesktopOnboarding({ desktopOnboardingComplete: true })
      const next = await reloadStatus()
      await refreshAuthDependentQueries()
      const sessionId = await createProjectConversation(project.id, `desktop-project-first:${project.id}`)
      setClosing(true)
      onComplete(next)
      void navigate(projectConversationLink(projectRouteSegment(project, [project]), sessionId))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to complete project setup')
    } finally {
      setPendingAction(null)
    }
  }

  const handleCreateAndPersonalize = async () => {
    if (submitting || !projectName.trim()) return
    const chosenWorkspaces: CreationWorkspace[] = workspaceCatalog
      .filter(w => selectedWorkspaceIds.includes(w.id))
      .map(w => ({ workspace_id: w.id, path: w.path, label: w.label, role: 'auxiliary' }))
    setPendingAction('project-create')
    setError(null)
    setNotice(null)
    try {
      const clientRequestId = `desktop-project:${crypto.randomUUID()}`
      const { project } = await requestJson<{ project: CreationProject }>('/v3/projects', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          client_request_id: clientRequestId,
          name: projectName.trim(),
          description: projectDescription.trim(),
          workspaces: chosenWorkspaces,
        }),
      })
      if (!project?.id) throw new Error('Project creation failed: no project ID returned.')
      setCreatedProject(project)
      setView('personalizing')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create project')
    } finally {
      setPendingAction(null)
    }
  }

  const handleCreateWithoutWorkspaces = async () => {
    if (submitting || !projectName.trim()) return
    setPendingAction('project-create')
    setError(null)
    setNotice(null)
    try {
      const clientRequestId = `desktop-project:${crypto.randomUUID()}`
      const { project } = await requestJson<{ project: CreationProject }>('/v3/projects', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          client_request_id: clientRequestId,
          name: projectName.trim(),
          description: projectDescription.trim(),
          workspaces: [],
        }),
      })
      if (!project?.id) throw new Error('Project creation failed: no project ID returned.')
      await finishAndOpenProjectChat(project)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create project')
    } finally {
      setPendingAction(null)
    }
  }

  const handlePersonalizeRefresh = async () => {
    if (!createdProject?.id || personalizingBusy) return
    setPersonalizingBusy(true)
    try {
      const { project } = await requestJson<{ project: CreationProject }>(`/v3/projects/${encodeURIComponent(createdProject.id)}`)
      if (project) setCreatedProject(project)
    } catch { /* ignore */ }
    finally { setPersonalizingBusy(false) }
  }

  const handlePersonalizeRetry = async () => {
    if (!createdProject?.id || personalizingBusy) return
    setPersonalizingBusy(true)
    try {
      const { project } = await requestJson<{ project: CreationProject }>(`/v3/projects/${encodeURIComponent(createdProject.id)}/context:retry`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ expected_attempt: createdProject.context_generation?.attempt ?? 0 }),
      })
      if (project) setCreatedProject(project)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to retry context generation')
    } finally { setPersonalizingBusy(false) }
  }

  useEffect(() => {
    if (view !== 'personalizing' || !createdProject?.id) return
    if (createdProject.context_generation?.status === 'ready' || createdProject.context_generation?.status === 'failed') return
    const timer = setInterval(() => {
      void handlePersonalizeRefresh()
    }, 1500)
    return () => clearInterval(timer)
  }, [view, createdProject?.id, createdProject?.context_generation?.status])

  useEffect(() => {
    if (view !== 'personalizing' || !createdProject || autoOpenedRef.current) return
    if (createdProject.context_generation?.status === 'ready') {
      autoOpenedRef.current = true
      const timer = setTimeout(() => {
        void finishAndOpenProjectChat(createdProject)
      }, 700)
      return () => clearTimeout(timer)
    }
  }, [view, createdProject])

  const handleProviderSave = async () => {
    if (submitting || pendingActionRef.current !== null) {
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
      const next = await reloadStatus()
      await refreshAuthDependentQueries()
      setStatus(next)
      if (restart) {
        setNotice('Provider connected. Review complete.')
        transitionToStep('provider')
      } else {
        setNotice('Provider connected. Advancing to project setup…')
        transitionToStep('project')
      }
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
    if (submitting || pendingActionRef.current !== null) {
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
    if (submitting || pendingActionRef.current !== null) {
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
      if (restart) {
        setNotice('Provider connected. Review complete.')
        transitionToStep('provider')
      } else {
        setNotice('Provider connected. Advancing to project setup…')
        transitionToStep('project')
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to complete remote sign-in')
    } finally {
      setPendingAction(null)
    }
  }

  return (
    <div className={`fixed inset-0 z-[9999] flex items-center justify-center overflow-y-auto bg-black px-6 py-8 text-[var(--app-text)] transition-opacity duration-200 ease-out ${closing ? 'pointer-events-none opacity-0' : 'opacity-100'}`}>
      <main className="relative w-full max-w-5xl overflow-hidden rounded-[2rem] border border-[color-mix(in_oklab,var(--app-border)_58%,transparent)] bg-[color-mix(in_oklab,var(--app-surface)_88%,black)] shadow-[0_24px_90px_rgb(0_0_0/0.55)] outline outline-1 outline-offset-2 outline-[color-mix(in_oklab,var(--app-border)_34%,transparent)] transition-[box-shadow,transform] duration-300 ease-out">
        <div className={view === 'setup' || view === 'personalizing' ? 'grid min-h-[42rem] place-items-center p-8' : 'grid min-h-[42rem] grid-rows-[auto_auto_minmax(0,1fr)] gap-5 p-8'}>
          {view !== 'setup' && view !== 'personalizing' ? (
            <>
              <OnboardingBrandHeader restart={restart} step={step} visible={panelVisible} projectName={projectName} />
              <FeedbackSlot error={error} notice={notice} progress={progress} />
            </>
          ) : null}

          <div className="relative min-h-[25rem] w-full overflow-hidden">
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
                      Your name.
                    </label>
                    <Input
                      id="desktop-onboarding-username"
                      autoFocus={!status.identity.username}
                      value={username}
                      onChange={(event) => setUsername(event.target.value)}
                      onKeyDown={(event) => {
                        if (event.key !== 'Enter' || event.nativeEvent.isComposing) return
                        event.preventDefault()
                        if (username.trim()) document.getElementById('desktop-onboarding-swarm-name')?.focus()
                        else setError('Your name is required.')
                      }}
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
                      Name your Swarm (optional).
                    </label>
                    <Input
                      id="desktop-onboarding-swarm-name"
                      autoFocus={Boolean(status.identity.username)}
                      value={swarmName}
                      onChange={(event) => setSwarmName(event.target.value)}
                      placeholder="default"
                      disabled={submitting}
                    />
                    <p className="text-sm leading-6 text-[var(--app-text-muted)]">
                      This is the device label Swarm shows in launcher screens. It defaults to &apos;default&apos; if omitted.
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
                          <p className="mt-1 text-sm text-[var(--app-text-muted)]">Pick a provider to connect, or use a connected provider and continue to projects.</p>
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
                                  if (pendingActionRef.current !== null) return
                                  setProviderID(provider.id)
                                  if (connected) transitionToStep('provider')
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
                            The selected provider does not expose a quick auth method here. Continue to projects and connect it later from Settings.
                          </p>
                        )}
                      </div>
                    </>
                  ) : !providerLoading ? (
                    <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-4 text-sm leading-6 text-[var(--app-text-muted)]">
                      No providers are available yet. Continue to projects and connect one later from Settings.
                    </div>
                  ) : null}

                  <div className="mt-auto flex items-center justify-between gap-3 pt-1">
                    <Button type="button" variant="outline" onClick={() => transitionToStep('identity')} disabled={submitting}>
                      Back
                    </Button>
                    <Button
                      type="button"
                      variant={providerAlreadyConnected || providerOptions.length === 0 ? 'primary' : 'outline'}
                      onClick={restart ? handleProviderContinue : () => transitionToStep('project')}
                      disabled={submitting}
                      aria-label={finishButtonLabel}
                    >
                      {finishButtonLabel}
                    </Button>
                  </div>
                </div>
              ) : null}

              {view === 'project' ? (
                <div className="grid h-full content-start gap-6">
                  <div className="grid gap-2">
                    <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-project-name">
                      Project name.
                    </label>
                    <Input
                      id="desktop-onboarding-project-name"
                      autoFocus
                      value={projectName}
                      onChange={(event) => setProjectName(event.target.value)}
                      onKeyDown={(event) => {
                        if (event.key !== 'Enter' || event.nativeEvent.isComposing) return
                        event.preventDefault()
                        if (projectName.trim()) {
                          setError(null)
                          transitionToStep('workspaces')
                        } else {
                          setError('Project name is required.')
                        }
                      }}
                      placeholder="e.g. My Application"
                      disabled={submitting}
                    />
                    <p className="text-sm leading-6 text-[var(--app-text-muted)]">
                      Name your first project. Git is not required; you can attach code folders in the next step or chat with Swarm directly.
                    </p>
                  </div>

                  <div className="grid gap-2">
                    <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-project-description">
                      Description (optional).
                    </label>
                    <Input
                      id="desktop-onboarding-project-description"
                      value={projectDescription}
                      onChange={(event) => setProjectDescription(event.target.value)}
                      placeholder="e.g. Main web application and services"
                      disabled={submitting}
                    />
                  </div>

                  <div className="mt-auto flex items-center justify-between gap-3 pt-1">
                    <Button type="button" variant="outline" onClick={() => transitionToStep('provider')} disabled={submitting}>
                      Back
                    </Button>
                    <div className="flex items-center gap-3">
                      <Button
                        type="button"
                        variant="outline"
                        onClick={handleProviderContinue}
                        disabled={submitting}
                        aria-label="Continue to projects"
                      >
                        Continue to projects
                      </Button>
                      <Button
                        type="button"
                        variant="primary"
                        disabled={!projectName.trim() || submitting}
                        onClick={() => {
                          if (projectName.trim()) {
                            setError(null)
                            transitionToStep('workspaces')
                          } else {
                            setError('Project name is required.')
                          }
                        }}
                      >
                        Continue to workspaces
                      </Button>
                    </div>
                  </div>
                </div>
              ) : null}

              {view === 'workspaces' ? (
                <div className="grid h-full content-start gap-5">
                  <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 space-y-4">
                    <div className="flex items-center justify-between">
                      <h2 className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">
                        Project folders
                      </h2>
                      <span className="text-xs text-[var(--app-text-muted)]">
                        {selectedWorkspaceIds.length} selected
                      </span>
                    </div>

                    {workspaceCatalog.length > 0 ? (
                      <div className="grid gap-2 max-h-48 overflow-y-auto pr-1">
                        {workspaceCatalog.map((w) => {
                          const isChecked = selectedWorkspaceIds.includes(w.id)
                          return (
                            <label
                              key={w.id}
                              className={[
                                'flex items-start gap-3 rounded-lg border p-3 cursor-pointer transition-colors',
                                isChecked
                                  ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,transparent)]'
                                  : 'border-[var(--app-border)] bg-transparent hover:border-[var(--app-border-accent)]',
                              ].join(' ')}
                            >
                              <input
                                type="checkbox"
                                checked={isChecked}
                                onChange={(e) => {
                                  setSelectedWorkspaceIds(ids =>
                                    e.target.checked ? [...ids, w.id] : ids.filter(id => id !== w.id)
                                  )
                                }}
                                className="mt-0.5"
                              />
                              <div className="grid gap-0.5 overflow-hidden">
                                <span className="text-sm font-medium text-[var(--app-text)] truncate">{w.label}</span>
                                <span className="text-xs font-mono text-[var(--app-text-muted)] truncate">{w.path}</span>
                              </div>
                            </label>
                          )
                        })}
                      </div>
                    ) : (
                      <p className="text-sm text-[var(--app-text-muted)]">
                        No registered folders found. Add a folder below or skip straight to chatting with Swarm.
                      </p>
                    )}

                    <div className="grid gap-2 pt-2 border-t border-[var(--app-border)]">
                      <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-add-folder">
                        Add folder path
                      </label>
                      <div className="flex gap-2">
                        <Input
                          id="desktop-onboarding-add-folder"
                          value={customFolderPath}
                          onChange={(e) => setCustomFolderPath(e.target.value)}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter') {
                              e.preventDefault()
                              void handleAddCustomFolder()
                            }
                          }}
                          placeholder="/path/to/my/project"
                          disabled={submitting}
                        />
                        <Button
                          type="button"
                          variant="outline"
                          disabled={!customFolderPath.trim() || submitting}
                          onClick={() => void handleAddCustomFolder()}
                        >
                          <OnboardingButtonLabel idle="Add folder" pending="Adding…" isPending={pendingAction === 'folder-add'} />
                        </Button>
                      </div>
                    </div>
                  </div>

                  <p className="text-xs text-[var(--app-text-muted)]">
                    Swarm AI Router will synthesize project context directly into Swarm’s durable Pebble database. Zero files will be written to disk.
                  </p>

                  <div className="mt-auto flex items-center justify-between gap-3 pt-1">
                    <Button type="button" variant="outline" onClick={() => transitionToStep('project')} disabled={submitting}>
                      Back
                    </Button>
                    <div className="flex items-center gap-3">
                      {selectedWorkspaceIds.length > 0 ? (
                        <>
                          <Button
                            type="button"
                            variant="outline"
                            onClick={() => void handleCreateWithoutWorkspaces()}
                            disabled={submitting}
                          >
                            Skip workspaces &amp; Talk to Swarm
                          </Button>
                          <Button
                            type="button"
                            variant="primary"
                            onClick={() => void handleCreateAndPersonalize()}
                            disabled={submitting}
                          >
                            <OnboardingButtonLabel idle="Personalize &amp; Talk to Swarm" pending="Creating…" isPending={pendingAction === 'project-create'} />
                          </Button>
                        </>
                      ) : (
                        <Button
                          type="button"
                          variant="primary"
                          onClick={() => void handleCreateWithoutWorkspaces()}
                          disabled={submitting}
                        >
                          <OnboardingButtonLabel idle="Skip to Talk to Swarm" pending="Creating…" isPending={pendingAction === 'project-create'} />
                        </Button>
                      )}
                    </div>
                  </div>
                </div>
              ) : null}

              {view === 'personalizing' && createdProject ? (
                <div className="grid h-full content-center">
                  <PersonalizingCard
                    project={createdProject}
                    busy={personalizingBusy}
                    onRetry={() => void handlePersonalizeRetry()}
                    onRefresh={() => void handlePersonalizeRefresh()}
                    onOpen={() => void finishAndOpenProjectChat(createdProject)}
                  />
                </div>
              ) : null}

            </div>
          </div>
        </div>
      </main>

    </div>
  )
}
