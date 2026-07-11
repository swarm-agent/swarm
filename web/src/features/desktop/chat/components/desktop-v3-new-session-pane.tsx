import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import { draftModelQueryOptions, agentStateQueryOptions, modelOptionsQueryOptions, uiSettingsQueryKey, uiSettingsQueryOptions } from '../../../queries/query-options'
import { normalizeDefaultNewSessionMode, normalizeThinkingTagsEnabled, type DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import { saveThinkingTagsSetting } from '../../settings/swarm/mutations/save-thinking-tags-setting'
import { getDesktopSessionCreateTarget, type DesktopChatRoute } from '../services/chat-routing'
import { formatContextWindow, effectiveContextWindow } from '../services/model-options'
import { preferenceFromAgentModelLock, resolveDesktopV3AgentModelLock } from '../services/agent-model-preferences'
import type { AgentStateRecord, ResolvedSessionPreference, SessionPreferenceRecord } from '../types/chat'
import { refreshAgentModelMutationCaches, updateAgentProfile } from '../queries/agent-preference-mutations'
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

function optionKey(provider: string, model: string, contextMode = ''): string {
  return `${provider}:${model}:${contextMode.trim().toLowerCase()}`
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
  const draftPreferenceQuery = useQuery(draftModelQueryOptions())
  const uiSettingsQuery = useQuery(uiSettingsQueryOptions())
  const authCredentialsQuery = useQuery({
    queryKey: ['auth-credentials', 'desktop-composer'],
    queryFn: () => listAuthCredentials('', '', 200),
    staleTime: 30_000,
  })
  const agentState = agentStateQuery.data ?? EMPTY_AGENT_STATE
  const modelOptions = modelOptionsQuery.data ?? []
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
  const [selectedAgent, setSelectedAgent] = useState(agentNameProp.trim() || agentState.activePrimary || '')
  const modeManuallySelectedRef = useRef(false)
  const agentManuallySelectedRef = useRef(false)
  const preferenceManuallyChangedRef = useRef(false)
  const [preference, setPreference] = useState<SessionPreferenceRecord>(() => ({
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
    const active = agentNameProp.trim() || agentState.activePrimary || ''
    if (!active) return
    setSelectedAgent((current) => {
      if (agentManuallySelectedRef.current) return current
      return current === active ? current : active
    })
  }, [agentNameProp, agentState.activePrimary])

  useEffect(() => {
    if (!starting) return
    const timer = window.setInterval(() => setTimerNow(Date.now()), 250)
    return () => window.clearInterval(timer)
  }, [starting])

  useEffect(() => {
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
  }, [draftPreferenceQuery.data, preferenceProp])

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
    setPreference((current) => {
      unlockedPreferenceRef.current = current
      return preferenceFromAgentModelLock(selectedAgentModelLock, current, modelOptions)
    })
  }, [modelOptions, selectedAgentModelLock])

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

  const selectedModelKey = optionKey(preference.provider, preference.model, preference.contextMode)
  const selectedModelOption = modelOptions.find((option) => option.key === selectedModelKey) ?? null
  const hasResolvedPreference = Boolean(preference.provider.trim() && preference.model.trim() && preference.thinking.trim())
  const selectedModelAvailable = Boolean(selectedModelOption && hasResolvedPreference)
  const needsAuth = desktopProviderNeedsAuth(preference.provider, authCredentialsQuery.data)
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

  function handleModeSelect(nextMode: DesktopSessionMode) {
    modeManuallySelectedRef.current = true
    setMode(nextMode)
    onModeChange?.(nextMode)
  }

  async function handleConfirmAgentSettings(input: AgentModelControlConfirmInput) {
    if (agentModelSaving) return
    setAgentModelSaving(true)
    setStartError(null)
    try {
      const action = input.action
      const basePreference = preference
      await updateAgentProfile(input.profile, action.agentPatch)
      const agentStateResult = await refreshAgentModelMutationCaches(queryClient)
      const refreshedLock = resolveDesktopV3AgentModelLock(agentStateResult.profiles, input.agentName, mode)
      const nextPreference = refreshedLock.locked
        ? preferenceFromAgentModelLock(refreshedLock, basePreference, modelOptions)
        : basePreference
      unlockedPreferenceRef.current = refreshedLock.locked ? basePreference : nextPreference
      setPreference(nextPreference)
      agentManuallySelectedRef.current = true
      preferenceManuallyChangedRef.current = false
      setSelectedAgent(input.agentName)
    } catch (error) {
      if (mountedRef.current) setStartError(error instanceof Error ? error.message : 'Failed to update agent settings')
      throw error
    } finally {
      if (mountedRef.current) setAgentModelSaving(false)
    }
  }

  function handleOpenAgentSettings() {
    if (routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug/settings', params: { workspaceSlug: routeWorkspaceSlug }, search: { tab: 'agents' } })
      return
    }
    void navigate({ to: '/settings', search: { tab: 'agents' } })
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
    modeManuallySelectedRef.current = true
    const nextMode = mode === 'plan' ? 'auto' : 'plan'
    setMode(nextMode)
    onModeChange?.(nextMode)
    onModeCommandHandled?.()
  }, [mode, modeCommand, onModeChange, onModeCommandHandled])

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
          preference: preferenceForRequest(preference),
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
      />
      <div className="min-h-0 flex-1 overflow-y-auto py-6 [-webkit-overflow-scrolling:touch]">
        <div className="mx-auto flex min-h-full w-full max-w-[70rem] flex-col justify-start px-4 sm:justify-end sm:px-6">
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
        modelOptions={modelOptions}
        selectedModelKey={selectedModelKey}
        selectedServiceTier={preference.serviceTier}
        modelPickerDisabled={selectedAgentModelLock.locked}
        modelPickerDisabledReason={selectedAgentModelLock.disabledReason}
        modelLockNotice={selectedAgentModelLock.locked ? selectedAgentModelLock.disabledReason : ''}
        modelControlDetail={modelControlDetail({ locked: selectedAgentModelLock.locked, customized: selectedAgentModelLock.customized, modelLabel: selectedModelOption?.label || preference.model, thinking: preference.thinking, serviceTier: serviceTierFromPreference(preference) })}
        onOpenAgentSettings={handleOpenAgentSettings}
        needsAuth={needsAuth}
        onOpenAuthSettings={handleOpenAuthSettings}
        onConfirmAgentSettings={handleConfirmAgentSettings}
        agentModelControlBusy={agentModelSaving}
        thinking={preference.thinking}
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
