import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import { draftModelQueryOptions, agentStateQueryOptions, modelOptionsQueryOptions, uiSettingsQueryKey, uiSettingsQueryOptions } from '../../../queries/query-options'
import { normalizeDefaultNewSessionMode, normalizeThinkingTagsEnabled } from '../../settings/swarm/types/swarm-settings'
import { saveThinkingTagsSetting } from '../../settings/swarm/mutations/save-thinking-tags-setting'
import { getDesktopSessionCreateTarget, type DesktopChatRoute } from '../services/chat-routing'
import { supportsCodexFastMode, formatContextWindow, effectiveContextWindow } from '../services/model-options'
import type { AgentStateRecord, ModelOptionRecord, ResolvedSessionPreference, SessionPreferenceRecord } from '../types/chat'
import { DesktopV3AgenticComposer } from './desktop-v3-agentic-composer'
import { DesktopV3ChatHeader } from './desktop-v3-chat-header'
import {
  clearDesktopV3NewSessionOperation,
  createDesktopV3NewSessionOperation,
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

function preferenceFromOption(option: ModelOptionRecord | null, current: SessionPreferenceRecord): SessionPreferenceRecord {
  if (!option) return current
  return {
    ...current,
    provider: option.provider,
    model: option.model,
    thinking: current.thinking || option.thinking,
    contextMode: option.contextMode,
  }
}

function fastToggleFromPreference(preference: SessionPreferenceRecord): 'on' | 'off' {
  return preference.serviceTier.trim().toLowerCase() === 'fast' ? 'on' : 'off'
}

function preferenceForRequest(preference: SessionPreferenceRecord): DesktopV3NewSessionPreference {
  return {
    provider: preference.provider,
    model: preference.model,
    thinking: preference.thinking,
    serviceTier: preference.serviceTier,
    contextMode: preference.contextMode,
  }
}

export interface DesktopV3NewSessionPaneProps {
  workspace: WorkspaceEntry
  workspaceSlug: string
  routeOptions: DesktopChatRoute[]
  pendingWorktreeBranch?: string | null
  agentName?: string
  preference?: DesktopV3NewSessionPreference
  onOpenChats?: () => void
}

export function completeDesktopV3NewSessionStarted(input: {
  workspacePath: string
  operation: DesktopV3NewSessionOperation
  mountedRef: { current: boolean }
  setOperation: (operation: DesktopV3NewSessionOperation | null) => void
  navigateToSession: (sessionId: string) => void
}): void {
  clearDesktopV3NewSessionOperation(input.workspacePath, input.operation.operationId)
  if (!input.mountedRef.current) return
  input.setOperation(null)
  input.navigateToSession(input.operation.sessionId)
}

export function DesktopV3NewSessionPane({
  workspace,
  workspaceSlug,
  routeOptions,
  pendingWorktreeBranch,
  agentName: agentNameProp = '',
  preference: preferenceProp,
  onOpenChats,
}: DesktopV3NewSessionPaneProps) {
  const navigate = useNavigate()
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
  const agentState = agentStateQuery.data ?? EMPTY_AGENT_STATE
  const modelOptions = modelOptionsQuery.data ?? []
  const uiSettings = uiSettingsQuery.data
  const defaultMode = normalizeDefaultNewSessionMode(uiSettings?.chat?.default_new_session_mode)
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
  const retainedOperation = operationRef.current
  const [draft, setDraft] = useState(retainedOperation?.firstMessageRequest.content ?? '')
  const [starting, setStarting] = useState(false)
  const [startError, setStartError] = useState<string | null>(null)
  const [thinkingTagsSaving, setThinkingTagsSaving] = useState(false)
  const [mode, setMode] = useState<'auto' | 'plan'>(defaultMode)
  const [selectedAgent, setSelectedAgent] = useState(agentNameProp.trim() || agentState.activePrimary || '')
  const [preference, setPreference] = useState<SessionPreferenceRecord>(() => ({
    ...preferenceFromResolved(draftPreferenceQuery.data),
    provider: preferenceProp?.provider ?? draftPreferenceQuery.data?.preference.provider ?? '',
    model: preferenceProp?.model ?? draftPreferenceQuery.data?.preference.model ?? '',
    thinking: preferenceProp?.thinking ?? draftPreferenceQuery.data?.preference.thinking ?? '',
    serviceTier: preferenceProp?.serviceTier ?? draftPreferenceQuery.data?.preference.serviceTier ?? '',
    contextMode: preferenceProp?.contextMode ?? draftPreferenceQuery.data?.preference.contextMode ?? '',
  }))

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  useEffect(() => {
    setMode(defaultMode)
  }, [defaultMode])

  useEffect(() => {
    const active = agentNameProp.trim() || agentState.activePrimary || ''
    if (active) setSelectedAgent((current) => current || active)
  }, [agentNameProp, agentState.activePrimary])

  useEffect(() => {
    const resolved = preferenceFromResolved(draftPreferenceQuery.data)
    setPreference((current) => {
      if (current.provider || current.model) return current
      return {
        ...resolved,
        provider: preferenceProp?.provider ?? resolved.provider,
        model: preferenceProp?.model ?? resolved.model,
        thinking: preferenceProp?.thinking ?? resolved.thinking,
        serviceTier: preferenceProp?.serviceTier ?? resolved.serviceTier,
        contextMode: preferenceProp?.contextMode ?? resolved.contextMode,
      }
    })
  }, [draftPreferenceQuery.data, preferenceProp])

  useEffect(() => {
    if (writableRoutes.length === 0) return
    if (!writableRoutes.some((route) => route.id === selectedRouteId)) {
      setSelectedRouteId(writableRoutes[0]?.id ?? '')
    }
  }, [selectedRouteId, writableRoutes])

  useEffect(() => {
    const operation = loadDesktopV3NewSessionOperation(workspace.path)
    operationRef.current = operation
    if (operation) {
      setDraft(operation.firstMessageRequest.content)
    }
  }, [workspace.path])

  const selectedModelKey = optionKey(preference.provider, preference.model, preference.contextMode)
  const selectedModelOption = modelOptions.find((option) => option.key === selectedModelKey) ?? null
  const selectedModelAvailable = Boolean(selectedModelOption)
  const fastSupported = selectedModelOption ? supportsCodexFastMode(selectedModelOption.provider, selectedModelOption.model) : false
  const selectedContextWindow = selectedModelOption
    ? effectiveContextWindow(selectedModelOption.provider, selectedModelOption.model, selectedModelOption.contextMode, selectedModelOption.contextWindow)
    : draftPreferenceQuery.data?.contextWindow ?? 0
  const contextLabel = selectedContextWindow > 0 ? `${formatContextWindow(selectedContextWindow)} ctx` : 'ctx'
  const hasRetainedOperation = Boolean(operationRef.current)
  const selectedAgentName = selectedAgent.trim()
  const canSubmit = Boolean(
    !starting
      && selectedRoute
      && !unsupportedReason
      && selectedAgentName
      && selectedModelAvailable
      && (hasRetainedOperation || draft.trim()),
  )

  function handleModelSelect(key: string) {
    const option = modelOptions.find((candidate) => candidate.key === key) ?? null
    setPreference((current) => preferenceFromOption(option, current))
  }

  function handleThinkingChange(value: string) {
    setPreference((current) => ({ ...current, thinking: value.trim() === 'off' ? '' : value.trim() }))
  }

  function handleFastChange(value: 'on' | 'off') {
    setPreference((current) => ({
      ...current,
      serviceTier: value === 'on' && fastSupported ? 'fast' : '',
    }))
  }

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

  async function handleSubmit() {
    if (starting || !selectedRoute) return

    setStarting(true)
    setStartError(null)
    try {
      const existingOperation = operationRef.current
      const operation = existingOperation ?? (() => {
        const agentName = selectedAgentName
        if (!agentName) {
          throw new Error('New Desktop V3 session requires agent_name')
        }
        if (!selectedModelAvailable) {
          throw new Error('Select a model before starting the session')
        }
        return createDesktopV3NewSessionOperation({
          workspacePath: workspace.path,
          workspaceName: workspace.workspaceName,
          route: selectedRoute,
          prompt: draft,
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
      setDraft(operation.firstMessageRequest.content)
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

  function handleAbandonRetainedOperation() {
    const operation = operationRef.current
    if (!operation) return
    const confirmed = window.confirm(
      'Abandon this pending new-session operation? The session or first message may already exist. Retrying is the safest way to avoid duplicates.',
    )
    if (!confirmed) return
    clearDesktopV3NewSessionOperation(workspace.path, operation.operationId)
    operationRef.current = null
    setDraft('')
    setStartError(null)
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-[var(--app-bg)]" data-testid="desktop-v3-new-session-pane">
      <DesktopV3ChatHeader
        title="New conversation"
        workspaceName={workspace.workspaceName || workspace.path}
        mode={mode}
        running={starting}
        onOpenChats={onOpenChats}
      />
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-6 sm:px-8">
        <div className="mx-auto flex h-full w-full max-w-3xl flex-col justify-end" />
      </div>
      <DesktopV3AgenticComposer
        draft={draft}
        onDraftChange={setDraft}
        placeholder={hasRetainedOperation ? 'Retained first prompt' : `Message ${workspace.workspaceName || 'workspace'}…`}
        inputLabel="Start a new Desktop V3 session"
        disabled={starting || Boolean(unsupportedReason)}
        locked={hasRetainedOperation}
        busy={starting}
        canSubmit={canSubmit}
        error={startError || unsupportedReason}
        retainedNotice={hasRetainedOperation ? 'A pending new-session operation is retained for safe retry. Edits are disabled until it succeeds or you abandon it.' : null}
        onAbandonRetained={handleAbandonRetainedOperation}
        onSubmit={handleSubmit}
        mode={mode}
        onModeChange={setMode}
        currentAgent={selectedAgentName || agentState.activePrimary || 'Agent'}
        selectedPrimaryAgent={selectedAgentName || agentState.activePrimary || ''}
        agents={agentState.profiles}
        onAgentSelect={setSelectedAgent}
        modelOptions={modelOptions}
        selectedModelKey={selectedModelKey}
        selectedModelAvailable={selectedModelAvailable}
        onModelSelect={handleModelSelect}
        thinking={preference.thinking}
        onThinkingChange={handleThinkingChange}
        thinkingTagsEnabled={thinkingTagsEnabled}
        onThinkingTagsToggle={(enabled) => { void handleThinkingTagsToggle(enabled) }}
        thinkingTagsBusy={thinkingTagsSaving}
        fast={fastToggleFromPreference(preference)}
        onFastChange={handleFastChange}
        route={selectedRoute}
        routeOptions={routeOptions}
        onRouteSelect={setSelectedRouteId}
        routeTitle="Route this chat through the host or a linked child swarm."
        contextLabel={contextLabel}
        compactDisabled
      />
    </div>
  )
}
