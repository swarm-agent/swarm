import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import type { DesktopSlashCommand } from '../services/slash-commands'
import { agentStateQueryOptions, modelOptionsQueryOptions, modelProfilesQueryOptions, uiSettingsQueryKey, uiSettingsQueryOptions } from '../../../queries/query-options'
import { createModelProfile, invalidateModelProfiles, setDefaultModelProfile, updateModelProfile } from '../queries/model-profile-queries'
import { agentModelSettingsQueryOptions } from '../../settings/swarm/queries/get-agent-model-settings'
import { saveShowTipsSetting } from '../../settings/swarm/mutations/save-show-tips-setting'
import { normalizeShowTipsEnabled } from '../../settings/swarm/types/swarm-settings'
import type { AgentModelControlConfirmInput } from './agent-model-control'
import { modelOptionKey } from '../services/model-options'
import {
  DesktopV3RoutedNewSessionController,
  createDesktopV3RoutedComposerSnapshot,
  type DesktopV3RoutedComposerSnapshot,
  type DesktopV3RoutedNewSessionState,
  type DesktopV3RoutedWorkspaceAuthority,
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
import type { DesktopV3ArtifactMessageSelection } from '../../session-v3/artifact-api'
import {
  createDesktopRoutedWorktreeIntent,
  encodeDesktopRoutedWorktreeIntentMetadata,
  setDesktopRoutedWorktreeIntent,
} from '../services/desktop-routed-worktree-intent'

export interface DesktopV3NewSessionPaneProps {
  workspace: WorkspaceEntry
  workspaceAuthority: DesktopV3RoutedWorkspaceAuthority
  onRoutedSessionResolved: (result: DesktopV3RoutedStartResult, authority: DesktopV3RoutedWorkspaceAuthority) => void | Promise<void>
  mobileSessionQuickMenu?: ReactNode
  onSlashCommand?: (command: DesktopSlashCommand, draft: string) => void | Promise<void>
  workspaces?: WorkspaceEntry[]
  onSelectWorkspace?: (workspace: WorkspaceEntry) => void
  onSetWorkspaceIcon?: (path: string, iconPNGDataURL: string) => Promise<void>
  onOpenActionSettings?: () => void
  composerFocusSignal?: number
  initialPrompt?: string
  initialWorktreeRequested?: boolean
  initialPlanModeRequested?: boolean
  agentSettingsOpenSignal?: number
  agentSettingsInitialAgent?: string
  artifactSelectionRequest?: DesktopV3ArtifactMessageSelection | null
  onArtifactSelectionRequestHandled?: () => void
}

/**
 * Owns only the local draft/routing shell. The app-level owner activates the
 * validated routed result through onRoutedSessionResolved; this component does
 * not write pending state into the V3 cache, realtime controller, sidebar, or URL.
 */
export function DesktopV3NewSessionPane({
  workspace,
  workspaceAuthority,
  onRoutedSessionResolved,
  mobileSessionQuickMenu,
  onSlashCommand,
  workspaces = [workspace],
  onSelectWorkspace,
  onSetWorkspaceIcon,
  onOpenActionSettings,
  composerFocusSignal = 0,
  initialPrompt = '',
  initialWorktreeRequested = false,
  initialPlanModeRequested = false,
  agentSettingsOpenSignal = 0,
  agentSettingsInitialAgent = '',
  artifactSelectionRequest = null,
  onArtifactSelectionRequestHandled,
}: DesktopV3NewSessionPaneProps) {
  const queryClient = useQueryClient()
  const agentStateQuery = useQuery(agentStateQueryOptions())
  const modelOptionsQuery = useQuery(modelOptionsQueryOptions())
  const modelProfilesQuery = useQuery(modelProfilesQueryOptions())
  const agentModelSettingsQuery = useQuery(agentModelSettingsQueryOptions())
  const uiSettingsQuery = useQuery(uiSettingsQueryOptions())
  const showTips = normalizeShowTipsEnabled(uiSettingsQuery.data)
  const [agentModelSaving, setAgentModelSaving] = useState(false)
  const modelProfiles = modelProfilesQuery.data?.profiles ?? []
  const actionModel = agentModelSettingsQuery.data?.swarm.action ?? null
  const activeModelProfile = { source: 'agent-default' as const, profileId: '', name: 'Swarm action model' }
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
    const commandPrompt = initialPrompt.trim()
    if (routedController.getState().phase === 'draft' && commandPrompt) {
      const snapshot = createDesktopV3RoutedComposerSnapshot({
        prompt: commandPrompt,
        worktreePrimed: initialWorktreeRequested,
        planModeRequested: initialPlanModeRequested,
      })
      if (initialWorktreeRequested) routedController.primeWorktree(commandPrompt, snapshot)
      else routedController.startDraft(commandPrompt, snapshot)
    }
    return routedController
  })
  const [routedState, setRoutedState] = useState<DesktopV3RoutedNewSessionState>(() => controller.getState())
  const initialControllerState = controller.getState()
  const initialCommandPrompt = initialControllerState.phase === 'failed' ? '' : initialPrompt.trim()
  const [draft, setDraft] = useState(() => initialControllerState.phase === 'failed' ? initialControllerState.snapshot.prompt : initialCommandPrompt)
  const [mode, setMode] = useState<'auto' | 'plan'>(() => initialControllerState.phase === 'failed'
    ? (initialControllerState.snapshot.planModeRequested ? 'plan' : 'auto')
    : (initialPlanModeRequested ? 'plan' : 'auto'))
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
  const [worktreeIntent, setWorktreeIntent] = useState(() => createDesktopRoutedWorktreeIntent(initialControllerState.phase === 'failed'
    ? initialControllerState.snapshot.worktreePrimed
    : initialWorktreeRequested))
  const [restoredSnapshot, setRestoredSnapshot] = useState<DesktopV3RoutedComposerSnapshot | null>(() => initialControllerState.phase === 'failed' ? initialControllerState.snapshot : null)
  const [localError, setLocalError] = useState<string | null>(null)
  const [tipsSaving, setTipsSaving] = useState(false)
  const resolvedCallbackRef = useRef(onRoutedSessionResolved)
  const activatingOperationRef = useRef('')
  const initialPromptSubmittedRef = useRef(false)
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
      setMode(routedState.snapshot.planModeRequested ? 'plan' : 'auto')
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
      .then(() => resolvedCallbackRef.current(routedState.result, routedState.operation.request))
      .then(() => {
        controller.acknowledgeResolved(operationId)
        operationAttachmentsRef.current = null
        stagedAttachmentHistoryRef.current = []
        removedStagedAttachmentIdsRef.current.clear()
        stagedAttachmentsRef.current = []
        if (cancelled) return
        setStagedAttachments([])
        setRestoredSnapshot(null)
        setMode('auto')
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

  async function handleDisableTips() {
    if (tipsSaving) return
    setTipsSaving(true)
    setLocalError(null)
    try {
      const saved = await saveShowTipsSetting(false)
      queryClient.setQueryData(uiSettingsQueryKey(), saved)
    } catch (cause) {
      setLocalError(cause instanceof Error ? cause.message : 'Failed to disable home tips.')
    } finally {
      setTipsSaving(false)
    }
  }

  async function handleConfirmAgentSettings(input: AgentModelControlConfirmInput) {
    if (agentModelSaving) return
    setAgentModelSaving(true)
    setLocalError(null)
    try {
      if (input.agentName.trim().toLowerCase() === 'swarm') {
        throw new Error('Configure Swarm Action and Plan models directly in agent setup.')
      }
      let profileId = input.profileId.trim()
      if (input.persistence === 'create' || input.persistence === 'create-copy') {
        const profile = await createModelProfile(input.modelProfile)
        profileId = profile.profileId
      } else if (input.persistence === 'update') {
        const profile = await updateModelProfile(profileId, input.modelProfile)
        profileId = profile.profileId
      } else {
        throw new Error('Save this favorite before assigning it as Swarm’s action model.')
      }
      if (input.makeDefault) await setDefaultModelProfile(profileId)
      await invalidateModelProfiles(queryClient)
    } catch (cause) {
      setLocalError(cause instanceof Error ? cause.message : 'Failed to update Swarm agent setup.')
      throw cause
    } finally {
      setAgentModelSaving(false)
    }
  }

  function handleSubmit(snapshot: DesktopV3RoutedComposerSnapshot): Promise<DesktopV3RoutedNewSessionState> {
    try {
      const prompt = snapshot.prompt.trim()
        || (snapshot.attachments.length > 0 ? 'Please review the attached file(s).' : '')
        || (snapshot.videoAttachments.length > 0 ? 'Please review the attached video(s).' : '')
        || (snapshot.artifactSelections.length > 0 ? 'Please review the selected artifact(s).' : '')
      if (!prompt || routedState.phase === 'routing' || routedState.phase === 'resolved') {
        throw new Error('Routed Desktop start is not editable in its current state')
      }

      const captured = createDesktopV3RoutedComposerSnapshot({
        ...snapshot,
        prompt,
        attachments: snapshot.attachments,
      })
      if (routedState.phase !== 'failed' && captured.attachments.length !== stagedAttachmentsRef.current.length) {
        throw new Error('Routed composer staged attachment state changed before submit')
      }
      if (routedState.phase === 'failed') return controller.retry()
      setLocalError(null)
      operationAttachmentsRef.current = [...stagedAttachmentsRef.current]
      return controller.submit({
        workspace: workspaceAuthority,
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

  useEffect(() => {
    if (!initialCommandPrompt || initialPromptSubmittedRef.current) return
    initialPromptSubmittedRef.current = true
    void handleSubmit(createDesktopV3RoutedComposerSnapshot({
      prompt: initialCommandPrompt,
      worktreePrimed: initialWorktreeRequested,
      planModeRequested: initialPlanModeRequested,
    }))
  }, [initialCommandPrompt, initialPlanModeRequested, initialWorktreeRequested])

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
    setMode(current.snapshot.planModeRequested ? 'plan' : 'auto')
    setWorktreeIntent(createDesktopRoutedWorktreeIntent(current.snapshot.worktreePrimed))
    setRestoredSnapshot(current.snapshot)
    void controller.retry()
  }

  const initialCommandStarting = Boolean(initialCommandPrompt)
    && !initialPromptSubmittedRef.current
    && (routedState.phase === 'draft' || routedState.phase === 'worktree-primed')
  const activationPending = initialCommandStarting
    || routedState.phase === 'routing'
    || routedState.phase === 'resolved'
  // Keep the current new-chat surface mounted until the durable destination is
  // activated. Swapping it for the routed pending shell causes a visible page
  // flash on fast starts and is unnecessary because the composer can own the
  // transient busy state without publishing another session authority.
  const pendingState = routedState.phase === 'failed' || routedState.phase === 'worktree-primed'
    ? routedState.phase
    : 'draft'

  return (
    <div
      className="relative flex min-h-0 flex-1 flex-col bg-[var(--app-bg)]"
      data-desktop-chat-drop-zone
      data-testid="desktop-v3-new-session-pane"
      data-routed-phase={routedState.phase}
    >
      {mobileSessionQuickMenu ? (
        <section
          className="flex min-h-0 flex-1 flex-col overflow-hidden pt-[var(--app-safe-area-top)] sm:hidden"
          data-testid="mobile-workspace-session-list"
          aria-label="Workspace sessions"
        >
          {mobileSessionQuickMenu}
        </section>
      ) : null}

      <DesktopV3RoutedPendingShell
        state={pendingState}
        startPath={routedState.snapshot.worktreePrimed ? 'router' : 'session'}
        pendingPrompt={routedState.prompt}
        error={routedState.phase === 'failed' ? routedState.error : undefined}
        onRetry={routedState.phase === 'failed' ? handleRetry : undefined}
        showTips={showTips}
        onDisableTips={tipsSaving ? undefined : () => { void handleDisableTips() }}
        workspace={workspace}
        workspaces={workspaces}
        onSelectWorkspace={onSelectWorkspace}
        onSetWorkspaceIcon={onSetWorkspaceIcon}
        className={mobileSessionQuickMenu ? 'hidden sm:flex' : undefined}
      />

      <DesktopV3AgenticComposer
          workspacePath={workspace.path}
          draft={draft}
          focusSignal={composerFocusSignal}
          onDraftChange={setDraft}
          placeholder="What would you like to work on?"
          inputLabel="Start a routed Desktop V3 session"
          disabled={activationPending}
          busy={activationPending}
          canSubmit={Boolean(draft.trim()) || stagedAttachments.length > 0 || Boolean(artifactSelectionRequest)}
          onSubmit={() => undefined}
          artifactSelectionRequest={artifactSelectionRequest}
          onArtifactSelectionRequestHandled={onArtifactSelectionRequestHandled}
          onRoutedSubmit={handleSubmit}
          routedStagedAttachments={stagedAttachments}
          onRoutedStageAttachments={routedState.phase === 'failed' || activationPending ? undefined : handleStageAttachments}
          onRoutedRemoveStagedAttachment={routedState.phase === 'failed' || activationPending ? undefined : (stagingId) => setStagedAttachments((current) => {
            removedStagedAttachmentIdsRef.current.add(stagingId)
            const next = current.filter((attachment) => attachment.stagingId !== stagingId)
            stagedAttachmentsRef.current = next
            return next
          })}
          routedComposerSnapshot={restoredSnapshot}
          routedWorktreeRequested={worktreeIntent.requested}
          mode={mode}
          onModeSelect={routedState.phase === 'failed' || activationPending ? undefined : setMode}
          onRoutedWorktreeRequestedChange={routedState.phase === 'failed' || activationPending ? undefined : (requested) => {
            setWorktreeIntent((current) => setDesktopRoutedWorktreeIntent(current, requested))
          }}
          currentAgent="swarm"
          selectedPrimaryAgent="swarm"
          agents={agentStateQuery.data?.profiles ?? []}
          modelProfiles={modelProfiles}
          activeModelProfile={activeModelProfile}
          modelOptions={modelOptionsQuery.data ?? []}
          selectedModelKey={actionModel ? modelOptionKey(actionModel.provider, actionModel.model, actionModel.contextMode) : ''}
          selectedServiceTier={actionModel?.serviceTier ?? ''}
          thinking={actionModel?.thinking ?? ''}
          modelControlDetail={actionModel ? `${actionModel.provider}/${actionModel.model}` : 'Swarm action model'}
          modelStatusLabel="Ready"
          agentSettingsOpenSignal={agentSettingsOpenSignal}
          agentSettingsInitialAgent={agentSettingsInitialAgent}
          onConfirmAgentSettings={handleConfirmAgentSettings}
          agentModelControlBusy={agentModelSaving}
          error={localError ?? (routedState.phase === 'failed' ? routedState.error : null)}
          routedNewSession
          onSlashCommand={onSlashCommand}
          onOpenActionSettings={onOpenActionSettings}
          slashCommandContext="new-session"
        />
    </div>
  )
}
