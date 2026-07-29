import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import { draftModelQueryOptions, agentStateQueryOptions, modelOptionsQueryOptions, modelProfilesQueryOptions, uiSettingsQueryKey, uiSettingsQueryOptions } from '../../../queries/query-options'
import { normalizeDefaultNewSessionMode, normalizeThinkingTagsEnabled, type DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import { saveThinkingTagsSetting } from '../../settings/swarm/mutations/save-thinking-tags-setting'
import { getDesktopSessionCreateTarget, type DesktopChatRoute } from '../services/chat-routing'
import { formatContextWindow, effectiveContextWindow, modelOptionKey } from '../services/model-options'
import { preferenceFromModelProfile } from '../services/model-profiles'
import { createModelProfile, deleteModelProfile, invalidateModelProfiles, reorderModelProfiles, setDefaultModelProfile, updateModelProfile } from '../queries/model-profile-queries'
import { preferenceFromAgentModelLock, resolveDesktopV3AgentModelLock } from '../services/agent-model-preferences'
import { resolveDesktopV3StartupAgent } from '../services/desktop-startup-agent'
import type { ActiveModelProfileState, AgentStateRecord, ModelProfileChoice, ResolvedSessionPreference, SessionPreferenceRecord } from '../types/chat'
import type { AgentModelControlConfirmInput } from './agent-model-control'
import { DesktopV3AgenticComposer } from './desktop-v3-agentic-composer'
import { DesktopV3ChatHeader } from './desktop-v3-chat-header'
import { listAuthCredentials } from '../../settings/queries/list-auth-credentials'
import { desktopProviderNeedsAuth } from '../services/auth-needs'
import type { DesktopSlashCommand } from '../services/slash-commands'
import {
  clearDesktopV3NewSessionOperation,
  createDesktopV3NewSessionOperation,
  desktopV3NewSessionOperationMatchesRoute,
  loadDesktopV3NewSessionOperation,
  persistDesktopV3NewSessionOperation,
  startNewDesktopV3Session,
  type DesktopV3NewSessionOperation,
  type DesktopV3NewSessionPreference,
} from '../../session-v3/new-session-flow'

const EMPTY_AGENT_STATE: AgentStateRecord = {
  profiles: [],
  activePrimary: '',
  activeSubagent: {},
  version: 0,
  providerDefaultsPreview: null,
  toolInventory: null,
}

function preferenceFromResolved(resolved: ResolvedSessionPreference | undefined): SessionPreferenceRecord {
  return {
    provider: resolved?.preference.provider ?? '',
    model: resolved?.preference.model ?? '',
    thinking: resolved?.preference.thinking ?? '',
    serviceTier: resolved?.preference.serviceTier ?? '',
    contextMode: resolved?.preference.contextMode ?? '',
    updatedAt: resolved?.preference.updatedAt ?? 0,
  }
}

function serviceTierFromPreference(preference: SessionPreferenceRecord): string {
  return preference.serviceTier.trim().toLowerCase() || 'standard'
}

function modelControlDetail(input: { locked: boolean; customized: boolean; modelLabel: string; thinking: string; serviceTier: string }): string {
  return `${input.modelLabel || 'Model'} · thinking ${input.thinking || 'off'} · tier ${input.serviceTier}`
}

function preferenceForRequest(preference: SessionPreferenceRecord): DesktopV3NewSessionPreference {
  const provider = preference.provider.trim()
  const model = preference.model.trim()
  const thinking = preference.thinking.trim()
  if (!provider || !model || !thinking) {
    throw new Error('New Desktop V3 session requires resolved provider, model, and thinking')
  }
  return {
    provider,
    model,
    thinking,
    serviceTier: preference.serviceTier.trim() || undefined,
    contextMode: preference.contextMode.trim() || undefined,
  }
}

export interface DesktopV3NewSessionPaneProps {
  modeCommand?: 'toggle-plan-auto' | null
  onModeCommandHandled?: () => void
  workspace: WorkspaceEntry
  workspaceSlug: string
  routeOptions: DesktopChatRoute[]
  pendingWorktreeBranch?: string | null
  initialMode?: DesktopSessionMode
  onModeChange?: (mode: DesktopSessionMode) => void
  agentName?: string
  preference?: DesktopV3NewSessionPreference
  onOpenChats?: () => void
  mobileSessionQuickMenu?: ReactNode
  onSlashCommand?: (command: DesktopSlashCommand, draft: string) => void | Promise<void>
  agentSettingsOpenSignal?: number
  agentSettingsInitialAgent?: string
}

export function completeDesktopV3NewSessionStarted(input: {
  workspacePath: string
  operation: DesktopV3NewSessionOperation
  mountedRef: { current: boolean }
  setOperation: (operation: DesktopV3NewSessionOperation | null) => void
  setDraft?: (draft: string) => void
  navigateToSession: (sessionId: string) => void
}): void {
  clearDesktopV3NewSessionOperation(input.workspacePath, input.operation.operationId)
  if (!input.mountedRef.current) return
  input.setOperation(null)
  input.setDraft?.('')
  input.navigateToSession(input.operation.sessionId)
}

export function DesktopV3NewSessionPane({
  modeCommand = null,
  onModeCommandHandled,
  workspace,
  workspaceSlug,
  routeOptions,
  pendingWorktreeBranch,
  initialMode: initialModeProp,
  onModeChange,
  agentName: agentNameProp = '',
  preference: preferenceProp,
  onOpenChats,
  mobileSessionQuickMenu,
  onSlashCommand,
  agentSettingsOpenSignal = 0,
  agentSettingsInitialAgent = '',
}: DesktopV3NewSessionPaneProps) {
  const navigate = useNavigate()
  const matchRoute = useMatchRoute()
  const queryClient = useQueryClient()
  const mountedRef = useRef(true)
  const storedOperation = useMemo(
    () => loadDesktopV3NewSessionOperation(workspace.path),
    [workspace.path],
  )
  const operationRef = useRef<DesktopV3NewSessionOperation | null>(storedOperation)
  const agentStateQuery = useQuery(agentStateQueryOptions())
  const modelOptionsQuery = useQuery(modelOptionsQueryOptions())
  const modelProfilesQuery = useQuery(modelProfilesQueryOptions())
  const draftPreferenceQuery = useQuery(draftModelQueryOptions())
  const uiSettingsQuery = useQuery(uiSettingsQueryOptions())
  const authCredentialsQuery = useQuery({
    queryKey: ['auth-credentials', 'desktop-composer'],
    queryFn: () => listAuthCredentials('', '', 200),
    staleTime: 30_000,
  })
  const agentState = agentStateQuery.data ?? EMPTY_AGENT_STATE
  const modelOptions = modelOptionsQuery.data ?? []
  const modelProfileState = modelProfilesQuery.data ?? { profiles: [], defaultProfileId: '' }
  const uiSettings = uiSettingsQuery.data
  const defaultMode = normalizeDefaultNewSessionMode(uiSettings?.chat?.default_new_session_mode)
  const initialMode = initialModeProp ?? defaultMode
  const thinkingTagsEnabled = normalizeThinkingTagsEnabled(uiSettings)

  const writableRoutes = useMemo(
    () => routeOptions.filter((route) => {
      const target = getDesktopSessionCreateTarget(route)
      return target.endpoint === '/v3/sessions'
        && Boolean(target.swarmId?.trim())
        && Boolean(target.workspaceBindingId?.trim())
    }),
    [routeOptions],
  )
  const unsupportedReason = useMemo(() => {
    if (writableRoutes.length > 0) return null
    for (const route of routeOptions) {
      const target = getDesktopSessionCreateTarget(route)
      if (target.endpoint === null) return target.unsupportedReason
    }
    return 'No writable self/host Desktop V3 route is available for this workspace.'
  }, [routeOptions, writableRoutes.length])
  const [selectedRouteId, setSelectedRouteId] = useState(writableRoutes[0]?.id ?? routeOptions[0]?.id ?? '')
  const selectedRoute = useMemo(
    () => writableRoutes.find((route) => route.id === selectedRouteId) ?? writableRoutes[0] ?? routeOptions[0] ?? null,
    [routeOptions, selectedRouteId, writableRoutes],
  )
  const currentOperation = operationRef.current
  const [draft, setDraft] = useState(currentOperation?.firstMessageRequest.content ?? '')
  const [starting, setStarting] = useState(false)
  const [startError, setStartError] = useState<string | null>(null)
  const [thinkingTagsSaving, setThinkingTagsSaving] = useState(false)
  const [agentModelSaving, setAgentModelSaving] = useState(false)
  const [timerNow, setTimerNow] = useState(() => Date.now())
  const [mode, setMode] = useState<DesktopSessionMode>(initialMode)
  const [selectedAgent, setSelectedAgent] = useState(() => resolveDesktopV3StartupAgent(agentState, agentNameProp))
  const modeManuallySelectedRef = useRef(false)
  const agentManuallySelectedRef = useRef(false)
  const preferenceManuallyChangedRef = useRef(false)
  const profileManuallyChangedRef = useRef(false)
  const defaultModelProfile = modelProfileState.profiles.find((candidate) => candidate.profileId === modelProfileState.defaultProfileId) ?? null
  const defaultModelProfilePreference = defaultModelProfile
    ? preferenceFromModelProfile(defaultModelProfile, mode, defaultModelProfile.updatedAt)
    : null
  const [modelProfileChoice, setModelProfileChoice] = useState<ModelProfileChoice | undefined>(() => (
    defaultModelProfilePreference ? { kind: 'account-default' } : undefined
  ))
  const [activeModelProfile, setActiveModelProfile] = useState<ActiveModelProfileState>(() => defaultModelProfile
    ? { source: 'saved', profileId: defaultModelProfile.profileId, name: defaultModelProfile.name, modelMode: defaultModelProfile.modelMode }
    : { source: '', profileId: '', name: '', modelMode: '' })
  const [preference, setPreference] = useState<SessionPreferenceRecord>(() => defaultModelProfilePreference ?? ({
    ...preferenceFromResolved(draftPreferenceQuery.data),
    provider: preferenceProp?.provider ?? draftPreferenceQuery.data?.preference.provider ?? '',
    model: preferenceProp?.model ?? draftPreferenceQuery.data?.preference.model ?? '',
    thinking: preferenceProp?.thinking ?? draftPreferenceQuery.data?.preference.thinking ?? '',
    serviceTier: preferenceProp?.serviceTier ?? draftPreferenceQuery.data?.preference.serviceTier ?? '',
    contextMode: preferenceProp?.contextMode ?? draftPreferenceQuery.data?.preference.contextMode ?? '',
  }))
  const unlockedPreferenceRef = useRef<SessionPreferenceRecord>(preference)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  useEffect(() => {
    if (modeManuallySelectedRef.current) return
    setMode(initialMode)
  }, [initialMode])

  useEffect(() => {
    const active = resolveDesktopV3StartupAgent(agentState, agentNameProp)
    if (!active) return
    setSelectedAgent((current) => {
      if (agentManuallySelectedRef.current) return current
      return current === active ? current : active
    })
  }, [agentNameProp, agentState.activePrimary, agentState.profiles])

  useEffect(() => {
    if (modeManuallySelectedRef.current) return
    const profile = agentState.profiles.find((candidate) => candidate.name === selectedAgent)
    if (!profile) return
    setMode(profile.defaultSessionMode)
  }, [agentState.profiles, selectedAgent])

  useEffect(() => {
    if (!starting) return
    const timer = window.setInterval(() => setTimerNow(Date.now()), 250)
    return () => window.clearInterval(timer)
  }, [starting])

  useEffect(() => {
    if (profileManuallyChangedRef.current || modelProfileChoice || !modelProfileState.defaultProfileId) return
    const profile = modelProfileState.profiles.find((candidate) => candidate.profileId === modelProfileState.defaultProfileId)
    if (!profile) return
    const next = preferenceFromModelProfile(profile, mode, profile.updatedAt)
    if (!next) return
    setModelProfileChoice({ kind: 'account-default' })
    setActiveModelProfile({ source: 'saved', profileId: profile.profileId, name: profile.name, modelMode: profile.modelMode })
    setPreference(next)
  }, [mode, modelProfileChoice, modelProfileState.defaultProfileId, modelProfileState.profiles])

  useEffect(() => {
    if (defaultModelProfilePreference && !profileManuallyChangedRef.current) return
    const resolved = preferenceFromResolved(draftPreferenceQuery.data)
    const nextPreference = {
      ...resolved,
      provider: preferenceProp?.provider ?? resolved.provider,
      model: preferenceProp?.model ?? resolved.model,
      thinking: preferenceProp?.thinking ?? resolved.thinking,
      serviceTier: preferenceProp?.serviceTier ?? resolved.serviceTier,
      contextMode: preferenceProp?.contextMode ?? resolved.contextMode,
    }
    setPreference((current) => {
      if (preferenceManuallyChangedRef.current) return current
      if (
        current.provider === nextPreference.provider
        && current.model === nextPreference.model
        && current.thinking === nextPreference.thinking
        && current.serviceTier === nextPreference.serviceTier
        && current.contextMode === nextPreference.contextMode
      ) {
        return current
      }
      unlockedPreferenceRef.current = nextPreference
      return nextPreference
    })
  }, [defaultModelProfilePreference, draftPreferenceQuery.data, preferenceProp])

  const selectedAgentModelLock = useMemo(
    () => resolveDesktopV3AgentModelLock(agentState.profiles, selectedAgent, mode),
    [agentState.profiles, mode, selectedAgent],
  )

  useEffect(() => {
    if (writableRoutes.length === 0) return
    if (!writableRoutes.some((route) => route.id === selectedRouteId)) {
      setSelectedRouteId(writableRoutes[0]?.id ?? '')
    }
  }, [selectedRouteId, writableRoutes])

  useEffect(() => {
    if (!selectedAgentModelLock.locked) return
    if (modelProfileChoice && modelProfileChoice.kind !== 'agent-default') return
    setPreference((current) => {
      unlockedPreferenceRef.current = current
      return preferenceFromAgentModelLock(selectedAgentModelLock, current, modelOptions)
    })
  }, [modelOptions, modelProfileChoice, selectedAgentModelLock])

  useEffect(() => {
    const operation = loadDesktopV3NewSessionOperation(workspace.path)
    operationRef.current = operation
    if (operation) {
      setDraft(operation.firstMessageRequest.content)
    }
  }, [workspace.path])

  useEffect(() => {
    const operation = operationRef.current
    if (!operation || !selectedRoute) return
    if (desktopV3NewSessionOperationMatchesRoute(operation, selectedRoute)) return
    clearDesktopV3NewSessionOperation(workspace.path, operation.operationId)
    operationRef.current = null
  }, [selectedRoute, workspace.path])

  const accountDefaultProfileActive = !profileManuallyChangedRef.current
    && (!modelProfileChoice || modelProfileChoice.kind === 'account-default')
  const effectiveModelProfileChoice: ModelProfileChoice | undefined = accountDefaultProfileActive && defaultModelProfilePreference
    ? { kind: 'account-default' }
    : modelProfileChoice
  const effectiveActiveModelProfile = accountDefaultProfileActive && defaultModelProfile
    ? { source: 'saved', profileId: defaultModelProfile.profileId, name: defaultModelProfile.name, modelMode: defaultModelProfile.modelMode } satisfies ActiveModelProfileState
    : activeModelProfile
  const modelProfileAuthorityPending = modelProfilesQuery.isLoading && !profileManuallyChangedRef.current && !modelProfileChoice
  const effectivePreference = accountDefaultProfileActive && defaultModelProfilePreference
    ? defaultModelProfilePreference
    : modelProfileAuthorityPending
      ? { provider: '', model: '', thinking: '', serviceTier: '', contextMode: '', updatedAt: 0 }
      : preference
  const selectedModelKey = modelOptionKey(effectivePreference.provider, effectivePreference.model, effectivePreference.contextMode)
  const selectedModelOption = modelOptions.find((option) => option.key === selectedModelKey) ?? null
  const hasResolvedPreference = Boolean(effectivePreference.provider.trim() && effectivePreference.model.trim() && effectivePreference.thinking.trim())
  const selectedModelAvailable = Boolean(selectedModelOption && hasResolvedPreference)
  const needsAuth = desktopProviderNeedsAuth(effectivePreference.provider, authCredentialsQuery.data)
  const selectedContextWindow = selectedModelOption
    ? effectiveContextWindow(selectedModelOption.provider, selectedModelOption.model, selectedModelOption.contextMode, selectedModelOption.contextWindow)
    : draftPreferenceQuery.data?.contextWindow ?? 0
  const contextLabel = selectedContextWindow > 0 ? `${formatContextWindow(selectedContextWindow)} ctx` : 'ctx'
  const selectedAgentName = selectedAgent.trim()
  const workspaceSettingsMatch = matchRoute({ to: '/$workspaceSlug/settings', fuzzy: false })
    ?? matchRoute({ to: '/$workspaceSlug', fuzzy: false })
  const routeWorkspaceSlug = workspaceSettingsMatch && 'workspaceSlug' in workspaceSettingsMatch
    ? String(workspaceSettingsMatch.workspaceSlug ?? '').trim()
    : ''
  const canSubmit = Boolean(
    !starting
      && selectedRoute
      && !unsupportedReason
      && selectedAgentName
      && selectedModelAvailable
      && (operationRef.current || draft.trim()),
  )
  const runStatusModel = starting ? { kind: 'starting' as const, label: 'Starting', active: true } : null

  function handleAgentSelect(nextAgentName: string) {
    const normalizedAgentName = nextAgentName.trim()
    if (!normalizedAgentName) return
    agentManuallySelectedRef.current = true
    setSelectedAgent(normalizedAgentName)
    const nextProfile = agentState.profiles.find((candidate) => candidate.name === normalizedAgentName)
    if (nextProfile) {
      modeManuallySelectedRef.current = false
      setMode(nextProfile.defaultSessionMode)
      onModeChange?.(nextProfile.defaultSessionMode)
    }
    const nextMode = nextProfile?.defaultSessionMode ?? mode
    const nextLock = resolveDesktopV3AgentModelLock(agentState.profiles, normalizedAgentName, nextMode)
    if (nextLock.locked && (modelProfileChoice?.kind === 'agent-default' || !modelProfileChoice)) {
      setPreference((current) => preferenceFromAgentModelLock(nextLock, current, modelOptions))
    }
  }

  function handleModeSelect(nextMode: DesktopSessionMode) {
    modeManuallySelectedRef.current = true
    setMode(nextMode)
    onModeChange?.(nextMode)
    const activeProfile = activeModelProfile.source === 'saved'
      ? modelProfileState.profiles.find((profile) => profile.profileId === activeModelProfile.profileId) ?? null
      : modelProfileChoice?.kind === 'temporary'
        ? modelProfileChoice.profile
        : null
    if (activeProfile) {
      const updatedAt = 'updatedAt' in activeProfile && typeof activeProfile.updatedAt === 'number'
        ? activeProfile.updatedAt
        : Date.now()
      const next = preferenceFromModelProfile(activeProfile, nextMode, updatedAt)
      if (next) setPreference(next)
    }
  }

  async function handleConfirmAgentSettings(input: AgentModelControlConfirmInput) {
    if (agentModelSaving) return
    setAgentModelSaving(true)
    setStartError(null)
    try {
      let appliedProfile = input.modelProfile
      let savedProfileId = ''
      if (input.persistence === 'create' || input.persistence === 'create-copy') {
        const saved = await createModelProfile(input.modelProfile)
        savedProfileId = saved.profileId
        appliedProfile = saved
        if (input.makeDefault) await setDefaultModelProfile(saved.profileId)
        await invalidateModelProfiles(queryClient)
      } else if (input.persistence === 'update') {
        const saved = await updateModelProfile(input.profileId, input.modelProfile)
        savedProfileId = saved.profileId
        appliedProfile = saved
        if (input.makeDefault) await setDefaultModelProfile(saved.profileId)
        await invalidateModelProfiles(queryClient)
      }
      const nextPreference = preferenceFromModelProfile(appliedProfile, mode, Date.now())
      if (!nextPreference) throw new Error('Model profile does not resolve for the selected mode')
      profileManuallyChangedRef.current = true
      preferenceManuallyChangedRef.current = true
      unlockedPreferenceRef.current = nextPreference
      setPreference(nextPreference)
      if (savedProfileId) {
        setModelProfileChoice({ kind: 'saved', profileId: savedProfileId })
        setActiveModelProfile({ source: 'saved', profileId: savedProfileId, name: appliedProfile.name, modelMode: appliedProfile.modelMode })
      } else {
        setModelProfileChoice({ kind: 'temporary', profile: appliedProfile })
        setActiveModelProfile({ source: 'temporary', profileId: '', name: appliedProfile.name, modelMode: appliedProfile.modelMode })
      }
      agentManuallySelectedRef.current = true
      setSelectedAgent(input.agentName)
    } catch (error) {
      if (mountedRef.current) setStartError(error instanceof Error ? error.message : 'Failed to update agent settings')
      throw error
    } finally {
      if (mountedRef.current) setAgentModelSaving(false)
    }
  }

  function handleOpenAuthSettings() {
    if (routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug/settings', params: { workspaceSlug: routeWorkspaceSlug }, search: { tab: 'auth' } })
      return
    }
    void navigate({ to: '/settings', search: { tab: 'auth' } })
  }

  useEffect(() => {
    if (modeCommand !== 'toggle-plan-auto') return
    const nextMode = mode === 'plan' ? 'auto' : 'plan'
    handleModeSelect(nextMode)
    onModeCommandHandled?.()
  }, [mode, modeCommand, onModeCommandHandled])

  async function handleThinkingTagsToggle(enabled: boolean) {
    if (thinkingTagsSaving) return
    setThinkingTagsSaving(true)
    setStartError(null)
    try {
      const updated = await saveThinkingTagsSetting(enabled)
      queryClient.setQueryData(uiSettingsQueryKey(), updated)
    } catch (error) {
      if (mountedRef.current) {
        setStartError(error instanceof Error ? error.message : 'Failed to update thinking tags setting')
      }
    } finally {
      if (mountedRef.current) setThinkingTagsSaving(false)
    }
  }

  async function handleSubmit(submittedDraft = draft) {
    if (starting || !selectedRoute) return

    setStarting(true)
    setStartError(null)
    try {
      const existingOperation = operationRef.current
      if (existingOperation && !desktopV3NewSessionOperationMatchesRoute(existingOperation, selectedRoute)) {
        clearDesktopV3NewSessionOperation(workspace.path, existingOperation.operationId)
        operationRef.current = null
      }
      const reusableOperation = operationRef.current
      const operation = reusableOperation ?? (() => {
        const agentName = selectedAgentName
        if (!agentName) {
          throw new Error('New Desktop V3 session requires agent_name')
        }
        if (!selectedModelAvailable) {
          throw new Error('Select a model before starting the session')
        }
        if (!preference.thinking.trim()) {
          throw new Error('Select a thinking level before starting the session')
        }
        return createDesktopV3NewSessionOperation({
          workspacePath: workspace.path,
          workspaceName: workspace.workspaceName,
          route: selectedRoute,
          prompt: submittedDraft,
          mode,
          agentName,
          preference: preferenceForRequest(effectivePreference),
          modelProfileChoice: effectiveModelProfileChoice,
          sessionMetadata: {
            source: 'desktop-v3',
            workspace_path: workspace.path,
          },
          messageMetadata: {
            source: 'desktop-v3',
          },
          worktree: pendingWorktreeBranch
            ? { mode: 'on', branchName: pendingWorktreeBranch }
            : undefined,
        })
      })()
      operationRef.current = operation
      setDraft('')
      persistDesktopV3NewSessionOperation(operation)

      await startNewDesktopV3Session({
        operation,
        shouldSelectSession: () => mountedRef.current,
        onSessionStarted: () => {
          completeDesktopV3NewSessionStarted({
            workspacePath: workspace.path,
            operation,
            mountedRef,
            setOperation: (nextOperation) => {
              operationRef.current = nextOperation
            },
            setDraft,
            navigateToSession: (sessionId) => {
              void navigate({
                to: '/$workspaceSlug/$sessionId',
                params: { workspaceSlug, sessionId },
                replace: true,
              })
            },
          })
        },
      })
    } catch (error) {
      if (mountedRef.current) {
        setStartError(error instanceof Error ? error.message : String(error))
      }
    } finally {
      if (mountedRef.current) {
        setStarting(false)
      }
    }
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-[var(--app-bg)]" data-testid="desktop-v3-new-session-pane">
      <DesktopV3ChatHeader
        title="New conversation"
        workspaceName={workspace.workspaceName || workspace.path}
        mode={mode}
        runStatus={runStatusModel}
        runStatusNow={timerNow}
        onOpenChats={onOpenChats}
        hideMobileIdentity
      />
      <div className="min-h-0 flex-1 overflow-y-auto py-4 [-webkit-overflow-scrolling:touch] sm:py-6">
        <div className="mx-auto flex min-h-full w-full max-w-[70rem] flex-col justify-start px-3 sm:justify-end sm:px-6">
          {mobileSessionQuickMenu ? <div className="flex min-h-0 w-full flex-1 sm:hidden">{mobileSessionQuickMenu}</div> : null}
        </div>
      </div>
      <DesktopV3AgenticComposer
        draft={draft}
        onDraftChange={setDraft}
        placeholder={`Message ${workspace.workspaceName || 'workspace'}…`}
        inputLabel="Start a new Desktop V3 session"
        disabled={starting || Boolean(unsupportedReason)}
        busy={starting}
        canSubmit={canSubmit}
        error={startError || unsupportedReason}
        onSubmit={handleSubmit}
        mode={mode}
        onModeSelect={handleModeSelect}
        currentAgent={selectedAgentName || agentState.activePrimary || 'Agent'}
        selectedPrimaryAgent={selectedAgentName || agentState.activePrimary || ''}
        agents={agentState.profiles}
        modelProfiles={modelProfileState.profiles}
        activeModelProfile={effectiveActiveModelProfile}
        modelProfilesLoading={modelProfilesQuery.isLoading}
        modelProfilesError={modelProfilesQuery.error instanceof Error ? modelProfilesQuery.error.message : null}
        onModelProfileSetDefault={async (profileId) => {
          await setDefaultModelProfile(profileId)
          await invalidateModelProfiles(queryClient)
        }}
        onModelProfileReorder={async (profileIds) => {
          await reorderModelProfiles(profileIds)
          await invalidateModelProfiles(queryClient)
        }}
        onModelProfileDelete={async (profileId) => {
          await deleteModelProfile(profileId)
          await invalidateModelProfiles(queryClient)
          if (activeModelProfile.profileId === profileId) {
            const remaining = modelProfileState.profiles.filter((profile) => profile.profileId !== profileId)
            const replacement = remaining.find((profile) => profile.isDefault) ?? remaining[0]
            if (replacement) {
              setModelProfileChoice({ kind: 'saved', profileId: replacement.profileId })
              setActiveModelProfile({ source: 'saved', profileId: replacement.profileId, name: replacement.name, modelMode: replacement.modelMode })
              const next = preferenceFromModelProfile(replacement, mode, replacement.updatedAt)
              if (next) setPreference(next)
            } else {
              setModelProfileChoice({ kind: 'agent-default' })
              setActiveModelProfile({ source: 'agent-default', profileId: '', name: '', modelMode: '' })
            }
          }
        }}
        onModelProfileSelect={(profileId) => {
          const profile = modelProfileState.profiles.find((candidate) => candidate.profileId === profileId)
          if (!profile) return
          profileManuallyChangedRef.current = true
          preferenceManuallyChangedRef.current = true
          setModelProfileChoice({ kind: 'saved', profileId })
          setActiveModelProfile({ source: 'saved', profileId, name: profile.name, modelMode: profile.modelMode })
          const next = preferenceFromModelProfile(profile, mode, profile.updatedAt)
          if (next) setPreference(next)
        }}
        onUseAgentModelDefault={() => {
          profileManuallyChangedRef.current = true
          setModelProfileChoice({ kind: 'agent-default' })
          setActiveModelProfile({ source: 'agent-default', profileId: '', name: '', modelMode: '' })
          if (selectedAgentModelLock.locked) {
            setPreference((current) => preferenceFromAgentModelLock(selectedAgentModelLock, current, modelOptions))
          } else {
            setPreference(unlockedPreferenceRef.current)
          }
        }}
        modelOptions={modelOptions}
        selectedModelKey={selectedModelKey}
        selectedServiceTier={effectivePreference.serviceTier}
        agentSettingsOpenSignal={agentSettingsOpenSignal}
        agentSettingsInitialAgent={agentSettingsInitialAgent}
        modelPickerDisabled={selectedAgentModelLock.locked}
        modelPickerDisabledReason={selectedAgentModelLock.disabledReason}
        modelLockNotice={selectedAgentModelLock.locked ? selectedAgentModelLock.disabledReason : ''}
        modelControlDetail={modelControlDetail({ locked: selectedAgentModelLock.locked, customized: selectedAgentModelLock.customized, modelLabel: selectedModelOption?.label || effectivePreference.model, thinking: effectivePreference.thinking, serviceTier: serviceTierFromPreference(effectivePreference) })}
        onAgentSelect={handleAgentSelect}
        needsAuth={needsAuth}
        onOpenAuthSettings={handleOpenAuthSettings}
        onConfirmAgentSettings={handleConfirmAgentSettings}
        agentModelControlBusy={agentModelSaving}
        thinking={effectivePreference.thinking}
        thinkingTagsEnabled={thinkingTagsEnabled}
        onThinkingTagsToggle={(enabled) => { void handleThinkingTagsToggle(enabled) }}
        thinkingTagsBusy={thinkingTagsSaving}
        contextLabel={contextLabel}
        compactDisabled
        onSlashCommand={onSlashCommand}
      />
    </div>
  )
}
