import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import type { DesktopSlashCommand } from '../services/slash-commands'
import { agentStateQueryOptions, modelOptionsQueryOptions, modelProfilesQueryOptions } from '../../../queries/query-options'
import { createModelProfile, invalidateModelProfiles } from '../queries/model-profile-queries'
import { getSwarmModelSettings } from '../../settings/swarm/queries/get-model-settings'
import { saveSwarmModelSettings } from '../../settings/swarm/mutations/save-model-settings'
import type { SwarmModelSettings } from '../../settings/swarm/types/model-settings'
import { swarmModelSettingsQueryKey } from '../../settings/models/components/models-settings-page'
import { modelOptionKey } from '../services/model-options'
import type { ModelProfileInput } from '../types/chat'
import {
  DesktopV3RoutedNewSessionController,
  createDesktopV3RoutedComposerSnapshot,
  type DesktopV3RoutedComposerSnapshot,
  type DesktopV3RoutedNewSessionState,
  type DesktopV3RoutedStartResult,
} from '../../session-v3/new-session-flow'
import { postDesktopV3RoutedSessionStart } from '../../session-v3/write-api'
import { DesktopV3AgenticComposer } from './desktop-v3-agentic-composer'
import { DesktopV3RoutedPendingShell } from './desktop-v3-routed-pending-shell'
import {
  reconcileDesktopComposerStagedAttachments,
  stageDesktopComposerAttachments,
  type DesktopComposerStagedAttachment,
} from '../services/composer-attachments'
import { DESKTOP_V3_MEDIA_STAGING_MAX_TTL_SECONDS } from '../../session-v3/media-staging-api'
import {
  createDesktopRoutedWorktreeIntent,
  encodeDesktopRoutedWorktreeIntentMetadata,
  resolveDesktopRoutedWorktreeIntent,
  setDesktopRoutedWorktreeIntent,
  stripDesktopRoutedWorktreeDirective,
} from '../services/desktop-routed-worktree-intent'

export interface DesktopV3NewSessionPaneProps {
  workspace: WorkspaceEntry
  onRoutedSessionResolved: (result: DesktopV3RoutedStartResult) => void | Promise<void>
  mobileSessionQuickMenu?: ReactNode
  onSlashCommand?: (command: DesktopSlashCommand, draft: string) => void | Promise<void>
  composerFocusSignal?: number
}

/**
 * Owns only the local draft/routing shell. The app-level owner activates the
 * validated routed result through onRoutedSessionResolved; this component does
 * not write pending state into the V3 cache, realtime controller, sidebar, or URL.
 */
export function DesktopV3NewSessionPane({
  workspace,
  onRoutedSessionResolved,
  mobileSessionQuickMenu,
  onSlashCommand,
  composerFocusSignal = 0,
}: DesktopV3NewSessionPaneProps) {
  const queryClient = useQueryClient()
  const agentStateQuery = useQuery(agentStateQueryOptions())
  const modelOptionsQuery = useQuery(modelOptionsQueryOptions())
  const modelProfilesQuery = useQuery(modelProfilesQueryOptions())
  const swarmModelSettingsQuery = useQuery({
    queryKey: swarmModelSettingsQueryKey,
    queryFn: ({ signal }: { signal?: AbortSignal }) => getSwarmModelSettings(signal),
    staleTime: 30_000,
  })
  const [agentModelSaving, setAgentModelSaving] = useState(false)
  const modelProfiles = modelProfilesQuery.data?.profiles ?? []
  const actionFavoriteId = swarmModelSettingsQuery.data?.actionFavoriteId ?? modelProfilesQuery.data?.defaultProfileId ?? ''
  const actionFavorite = useMemo(() => modelProfiles.find((profile) => profile.profileId === actionFavoriteId) ?? null, [actionFavoriteId, modelProfiles])
  const activeModelProfile = actionFavorite
    ? { source: 'saved' as const, profileId: actionFavorite.profileId, name: actionFavorite.name }
    : { source: 'agent-default' as const, profileId: '', name: 'Action favorite' }
  const stagedAttachmentsRef = useRef<DesktopComposerStagedAttachment[]>([])
  const stagedAttachmentHistoryRef = useRef<DesktopComposerStagedAttachment[]>([])
  const removedStagedAttachmentIdsRef = useRef(new Set<string>())
  const operationAttachmentsRef = useRef<DesktopComposerStagedAttachment[] | null>(null)
  const [controller] = useState(() => {
    const routedController = new DesktopV3RoutedNewSessionController(async (request) => {
      const result = await postDesktopV3RoutedSessionStart(request)
      const submittedAttachments = operationAttachmentsRef.current ?? []
      if (submittedAttachments.length > 0 && submittedAttachments.every((attachment) => attachment.size > 0)) {
        reconcileDesktopComposerStagedAttachments(submittedAttachments, result.first_message)
      } else if ((result.first_message.media?.length ?? 0) !== submittedAttachments.length) {
        throw new Error('Routed session returned a different attachment count')
      }
      return result
    })
    return routedController
  })
  const [routedState, setRoutedState] = useState<DesktopV3RoutedNewSessionState>(() => controller.getState())
  const initialControllerState = controller.getState()
  const [draft, setDraft] = useState(() => initialControllerState.snapshot.prompt)
  const initialStagedAttachments = initialControllerState.phase === 'failed'
    ? initialControllerState.snapshot.attachments.map((attachment, index) => ({
        id: `restored:${index}:${attachment.staging_id}`,
        stagingId: attachment.staging_id,
        idempotencyKey: `${initialControllerState.operation.request.client_request_id}:media:${index}`,
        name: `Staged attachment ${index + 1}`,
        mimeType: 'application/octet-stream',
        fileType: attachment.file_type,
        modality: attachment.modality ?? 'document',
        size: 0,
        createdAt: initialControllerState.operation.createdAt,
        expiresAt: initialControllerState.operation.createdAt + (DESKTOP_V3_MEDIA_STAGING_MAX_TTL_SECONDS * 1000),
      }))
    : []
  const [stagedAttachments, setStagedAttachments] = useState<DesktopComposerStagedAttachment[]>(initialStagedAttachments)
  const [worktreeIntent, setWorktreeIntent] = useState(() => createDesktopRoutedWorktreeIntent(initialControllerState.snapshot.worktreePrimed))
  const [restoredSnapshot, setRestoredSnapshot] = useState<DesktopV3RoutedComposerSnapshot | null>(() => initialControllerState.phase === 'failed' ? initialControllerState.snapshot : null)
  const [localError, setLocalError] = useState<string | null>(null)
  const resolvedCallbackRef = useRef(onRoutedSessionResolved)
  const activatingOperationRef = useRef('')
  if (operationAttachmentsRef.current === null && initialControllerState.phase === 'failed') {
    operationAttachmentsRef.current = initialStagedAttachments
    stagedAttachmentsRef.current = initialStagedAttachments
    stagedAttachmentHistoryRef.current = initialStagedAttachments
  }

  useEffect(() => {
    resolvedCallbackRef.current = onRoutedSessionResolved
  }, [onRoutedSessionResolved])

  useEffect(() => controller.subscribe(setRoutedState), [controller])

  useEffect(() => {
    stagedAttachmentsRef.current = stagedAttachments
  }, [stagedAttachments])

  useEffect(() => {
    if (routedState.phase === 'failed') {
      activatingOperationRef.current = ''
      setLocalError(null)
      const restoredAttachments = operationAttachmentsRef.current ?? []
      const visibleAttachments = restoredAttachments.filter((attachment) => !removedStagedAttachmentIdsRef.current.has(attachment.stagingId))
      stagedAttachmentsRef.current = visibleAttachments
      setStagedAttachments(visibleAttachments)
      setDraft(routedState.snapshot.prompt)
      setWorktreeIntent(createDesktopRoutedWorktreeIntent(routedState.snapshot.worktreePrimed))
      setRestoredSnapshot(routedState.snapshot)
      return
    }
    if (routedState.phase !== 'resolved') return
    if (activatingOperationRef.current === routedState.operation.operationId) return
    activatingOperationRef.current = routedState.operation.operationId
    setLocalError(null)
    let cancelled = false
    const operationId = routedState.operation.operationId
    void Promise.resolve()
      .then(() => resolvedCallbackRef.current(routedState.result))
      .then(() => {
        controller.acknowledgeResolved(operationId)
        operationAttachmentsRef.current = null
        stagedAttachmentHistoryRef.current = []
        removedStagedAttachmentIdsRef.current.clear()
        stagedAttachmentsRef.current = []
        if (cancelled) return
        setStagedAttachments([])
        setRestoredSnapshot(null)
        setWorktreeIntent(createDesktopRoutedWorktreeIntent())
      })
      .catch((error) => {
        activatingOperationRef.current = ''
        if (controller.getState().phase === 'resolved') {
          controller.rejectResolved(operationId, error)
        }
      })
    return () => { cancelled = true }
  }, [controller, routedState])

  async function setSwarmActionFavorite(profileId: string) {
    const normalized = profileId.trim()
    if (!normalized || agentModelSaving) return
    setAgentModelSaving(true)
    setLocalError(null)
    try {
      const current = swarmModelSettingsQuery.data ?? await getSwarmModelSettings()
      const saved = await saveSwarmModelSettings({
        actionFavoriteId: normalized,
        planEnabled: current.planEnabled,
        ...(current.planFavoriteId ? { planFavoriteId: current.planFavoriteId } : {}),
      })
      queryClient.setQueryData<SwarmModelSettings>(swarmModelSettingsQueryKey, saved)
    } catch (cause) {
      setLocalError(cause instanceof Error ? cause.message : 'Failed to save the Action favorite.')
      throw cause
    } finally {
      setAgentModelSaving(false)
    }
  }

  async function createActionFavorite(input: ModelProfileInput): Promise<string> {
    const created = await createModelProfile(input)
    await invalidateModelProfiles(queryClient)
    return created.profileId
  }

  function handleSubmit(snapshot: DesktopV3RoutedComposerSnapshot): Promise<DesktopV3RoutedNewSessionState> {
    try {
      const prompt = snapshot.prompt.trim() || (snapshot.attachments.length > 0 ? 'Please review the attached file(s).' : '')
      if (!prompt || routedState.phase === 'routing' || routedState.phase === 'resolved') {
        throw new Error('Routed Desktop start is not editable in its current state')
      }

      const resolvedWorktreeIntent = resolveDesktopRoutedWorktreeIntent(
        createDesktopRoutedWorktreeIntent(snapshot.worktreePrimed),
        prompt,
      )
      const routedPrompt = stripDesktopRoutedWorktreeDirective(prompt)
      if (!routedPrompt) {
        throw new Error('Enter a prompt after /worktree.')
      }
      const captured = createDesktopV3RoutedComposerSnapshot({
        ...snapshot,
        prompt: routedPrompt,
        attachments: snapshot.attachments,
        worktreePrimed: resolvedWorktreeIntent.requested,
      })
      if (routedState.phase !== 'failed' && captured.attachments.length !== stagedAttachmentsRef.current.length) {
        throw new Error('Routed composer staged attachment state changed before submit')
      }
      if (routedState.phase === 'failed') return controller.retry()
      setLocalError(null)
      operationAttachmentsRef.current = [...stagedAttachmentsRef.current]
      return controller.submit({
        snapshot: captured,
        metadata: encodeDesktopRoutedWorktreeIntentMetadata(
          createDesktopRoutedWorktreeIntent(captured.worktreePrimed),
          { source: 'desktop-v3' },
        ),
      })
    } catch (cause) {
      setLocalError(cause instanceof Error ? cause.message : 'Routed session start failed.')
      return Promise.resolve(controller.getState())
    }
  }

  async function handleStageAttachments(files: File[], signal: AbortSignal) {
    if (controller.getState().phase === 'failed') {
      throw new Error('Retry or start a new routed session before changing staged attachments')
    }
    const identity = controller.prepareOperationIdentity()
    const history = stagedAttachmentHistoryRef.current
    const stagedHistory = await stageDesktopComposerAttachments({
      files,
      routedClientRequestId: identity.clientRequestId,
      existing: history,
      signal,
    })
    const staged = [...stagedAttachmentsRef.current, ...stagedHistory.slice(history.length)]
    stagedAttachmentHistoryRef.current = stagedHistory
    stagedAttachmentsRef.current = staged
    setStagedAttachments(staged)
  }

  function handleRetry() {
    const current = controller.getState()
    if (current.phase !== 'failed') return
    const restoredAttachments = operationAttachmentsRef.current ?? []
    const visibleAttachments = restoredAttachments.filter((attachment) => !removedStagedAttachmentIdsRef.current.has(attachment.stagingId))
    if (visibleAttachments.length !== current.snapshot.attachments.length) {
      setLocalError('Restore every staged attachment before retrying this routed session.')
      return
    }
    stagedAttachmentsRef.current = visibleAttachments
    setStagedAttachments(visibleAttachments)
    setDraft(current.snapshot.prompt)
    setWorktreeIntent(createDesktopRoutedWorktreeIntent(current.snapshot.worktreePrimed))
    setRestoredSnapshot(current.snapshot)
    void controller.retry()
  }

  const pendingState = routedState.phase === 'resolved' ? 'routing' : routedState.phase
  const showComposer = routedState.phase === 'draft' || routedState.phase === 'worktree-primed' || routedState.phase === 'failed'

  return (
    <div
      className="relative flex min-h-0 flex-1 flex-col bg-[var(--app-bg)]"
      data-desktop-chat-drop-zone
      data-testid="desktop-v3-new-session-pane"
      data-routed-phase={routedState.phase}
    >
      {mobileSessionQuickMenu && showComposer ? (
        <div className="absolute inset-x-0 top-0 z-10 flex min-h-0 sm:hidden">{mobileSessionQuickMenu}</div>
      ) : null}

      <DesktopV3RoutedPendingShell
        state={pendingState}
        pendingPrompt={routedState.prompt}
        error={routedState.phase === 'failed' ? routedState.error : undefined}
        onRetry={routedState.phase === 'failed' ? handleRetry : undefined}
      />

      {showComposer ? (
        <DesktopV3AgenticComposer
          workspacePath={workspace.path}
          draft={draft}
          focusSignal={composerFocusSignal}
          onDraftChange={setDraft}
          placeholder="What would you like to work on?"
          inputLabel="Start a routed Desktop V3 session"
          canSubmit={Boolean(draft.trim()) || stagedAttachments.length > 0}
          onSubmit={() => undefined}
          onRoutedSubmit={handleSubmit}
          routedStagedAttachments={stagedAttachments}
          onRoutedStageAttachments={routedState.phase === 'failed' ? undefined : handleStageAttachments}
          onRoutedRemoveStagedAttachment={routedState.phase === 'failed' ? undefined : (stagingId) => setStagedAttachments((current) => {
            removedStagedAttachmentIdsRef.current.add(stagingId)
            const next = current.filter((attachment) => attachment.stagingId !== stagingId)
            stagedAttachmentsRef.current = next
            return next
          })}
          routedComposerSnapshot={restoredSnapshot}
          routedWorktreeRequested={worktreeIntent.requested}
          onRoutedWorktreeRequestedChange={routedState.phase === 'failed' ? undefined : (requested) => {
            setWorktreeIntent((current) => setDesktopRoutedWorktreeIntent(current, requested))
          }}
          currentAgent="swarm"
          selectedPrimaryAgent="swarm"
          agents={agentStateQuery.data?.profiles ?? []}
          modelProfiles={modelProfiles}
          activeModelProfile={activeModelProfile}
          onModelProfileSelect={setSwarmActionFavorite}
          onModelFavoriteCreate={createActionFavorite}
          modelProfilesLoading={modelProfilesQuery.isPending || swarmModelSettingsQuery.isPending}
          modelProfilesError={modelProfilesQuery.error instanceof Error ? modelProfilesQuery.error.message : swarmModelSettingsQuery.error instanceof Error ? swarmModelSettingsQuery.error.message : null}
          modelOptions={modelOptionsQuery.data ?? []}
          selectedModelKey={actionFavorite ? modelOptionKey(actionFavorite.provider, actionFavorite.model, actionFavorite.contextMode) : ''}
          selectedServiceTier={actionFavorite?.serviceTier ?? ''}
          thinking={actionFavorite?.thinking ?? ''}
          modelControlDetail={actionFavorite ? `${actionFavorite.provider}/${actionFavorite.model}` : 'Action favorite'}
          agentModelControlBusy={agentModelSaving}
          error={localError ?? (routedState.phase === 'failed' ? routedState.error : null)}
          routedNewSession
          onSlashCommand={onSlashCommand}
        />
      ) : null}
    </div>
  )
}
