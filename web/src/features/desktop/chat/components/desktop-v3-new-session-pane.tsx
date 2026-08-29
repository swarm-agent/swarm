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
import type { ModelProfileRecord } from '../types/chat'
import {
  appendFirstDesktopV3Message,
  createDesktopV3NewSessionOperation,
  startDesktopV3CreateOnlySession,
  startNewDesktopV3Session,
  type DesktopV3RoutedComposerSnapshot,
  type DesktopV3RoutedWorkspaceAuthority,
} from '../../session-v3/new-session-flow'
import { DesktopV3AgenticComposer } from './desktop-v3-agentic-composer'
import { DesktopV3ChatHeader } from './desktop-v3-chat-header'
import type { DesktopV3RunStatusModel } from './desktop-v3-run-status'
import { DesktopV3RoutedPendingShell } from './desktop-v3-routed-pending-shell'
import {
  stageDesktopComposerAttachments,
  type DesktopComposerStagedAttachment,
} from '../services/composer-attachments'
import type { DesktopV3ArtifactMessageSelection } from '../../session-v3/artifact-api'
import { getDesktopV3MediaCapability, uploadDesktopV3MediaAsset } from '../../session-v3/write-api'

export interface DesktopV3NewSessionPaneProps {
  workspace: WorkspaceEntry
  workspaceAuthority: DesktopV3RoutedWorkspaceAuthority
  onSessionStarted: (sessionId: string) => void | Promise<void>
  mobileSessionQuickMenu?: ReactNode
  onSlashCommand?: (command: DesktopSlashCommand, draft: string) => void | Promise<void>
  developerMode?: boolean
  workspaces?: WorkspaceEntry[]
  onSelectWorkspace?: (workspace: WorkspaceEntry) => void
  onSetWorkspaceIcon?: (path: string, iconPNGDataURL: string) => Promise<void>
  onOpenActionSettings?: () => void
  composerFocusSignal?: number
  initialPrompt?: string
  initialPlanModeRequested?: boolean
  agentSettingsOpenSignal?: number
  agentSettingsInitialAgent?: string
  artifactSelectionRequest?: DesktopV3ArtifactMessageSelection | null
  onArtifactSelectionRequestHandled?: () => void
}

/**
 * Creates one ordinary durable Swarm session and appends its first message.
 * Worktree allocation is intentionally absent here: the running primary Swarm
 * agent owns same-session worktree adoption through manage_workspace.
 */
export function DesktopV3NewSessionPane({
  workspace,
  workspaceAuthority,
  onSessionStarted,
  mobileSessionQuickMenu,
  onSlashCommand,
  developerMode = false,
  workspaces = [workspace],
  onSelectWorkspace,
  onSetWorkspaceIcon,
  onOpenActionSettings,
  composerFocusSignal = 0,
  initialPrompt = '',
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
  const [chatOnlyModelProfile, setChatOnlyModelProfile] = useState<ModelProfileRecord | null>(null)
  const activeModelProfile = chatOnlyModelProfile
    ? { source: 'temporary' as const, profileId: '', name: chatOnlyModelProfile.name }
    : { source: 'agent-default' as const, profileId: '', name: 'Swarm action model' }
  const selectedActionModel = chatOnlyModelProfile ?? actionModel
  const stagedAttachmentsRef = useRef<DesktopComposerStagedAttachment[]>([])
  const stagedAttachmentHistoryRef = useRef<DesktopComposerStagedAttachment[]>([])
  const removedStagedAttachmentIdsRef = useRef(new Set<string>())
  const operationIdRef = useRef(crypto.randomUUID())
  const [draft, setDraft] = useState(() => initialPrompt.trim())
  const [mode, setMode] = useState<'auto' | 'plan'>(() => initialPlanModeRequested ? 'plan' : 'auto')
  const [stagedAttachments, setStagedAttachments] = useState<DesktopComposerStagedAttachment[]>([])
  const [restoredSnapshot, setRestoredSnapshot] = useState<DesktopV3RoutedComposerSnapshot | null>(null)
  const [localError, setLocalError] = useState<string | null>(null)
  const [tipsSaving, setTipsSaving] = useState(false)
  const startedCallbackRef = useRef(onSessionStarted)
  const [starting, setStarting] = useState(false)
  const initialPromptSubmittedRef = useRef(false)
  useEffect(() => {
    startedCallbackRef.current = onSessionStarted
  }, [onSessionStarted])

  useEffect(() => {
    stagedAttachmentsRef.current = stagedAttachments
  }, [stagedAttachments])

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

  async function handleSubmit(snapshot: DesktopV3RoutedComposerSnapshot): Promise<void> {
    if (starting) return
    const prompt = snapshot.prompt.trim()
      || (snapshot.attachments.length > 0 ? 'Please review the attached file(s).' : '')
      || (snapshot.videoAttachments.length > 0 ? 'Please review the attached video(s).' : '')
      || (snapshot.artifactSelections.length > 0 ? 'Please review the selected artifact(s).' : '')
    if (!prompt) {
      setLocalError('A first prompt or attachment is required.')
      return
    }
    const selectedModel = chatOnlyModelProfile ?? actionModel
    if (!selectedModel?.provider?.trim() || !selectedModel.model?.trim() || !selectedModel.thinking?.trim()) {
      setLocalError('The Swarm action model is unavailable.')
      return
    }
    const operation = createDesktopV3NewSessionOperation({
      workspacePath: workspace.path,
      workspaceName: workspace.workspaceName,
      route: {
        id: 'primary',
        label: 'Primary',
        swarmId: workspaceAuthority.swarm_id,
        workspaceBindingId: workspaceAuthority.workspace_binding_id,
        hostSwarmId: workspaceAuthority.swarm_id,
        hostSwarmName: 'Primary',
        hostWorkspacePath: workspaceAuthority.host_workspace_path,
        hostWorkspaceName: workspace.workspaceName,
        runtimeWorkspacePath: workspaceAuthority.runtime_workspace_path,
        workspaceName: workspace.workspaceName,
        targetKind: workspaceAuthority.target_kind,
        targetRelationship: workspaceAuthority.target_relationship,
      },
      prompt,
      mode,
      agentName: 'swarm',
      preference: {
        provider: selectedModel.provider,
        model: selectedModel.model,
        thinking: selectedModel.thinking,
        serviceTier: selectedModel.serviceTier,
        contextMode: selectedModel.contextMode,
      },
      modelProfileChoice: chatOnlyModelProfile ? {
        kind: 'temporary',
        profile: chatOnlyModelProfile,
      } : { kind: 'agent-default' },
      sessionMetadata: { source: 'desktop-v3' },
      messageMetadata: { source: 'desktop-v3' },
      worktree: { mode: 'off' },
    })
    if (snapshot.attachments.length > 0) {
      const createResult = await startDesktopV3CreateOnlySession({ operation })
      const capability = await getDesktopV3MediaCapability(createResult.sessionId)
      const contractToken = capability.contract_token?.trim() ?? ''
      if (!contractToken) throw new Error('The new session did not expose a media upload contract')
      operation.firstMessageRequest.media = await Promise.all(snapshot.attachments.map(async (attachment, index) => {
        const staged = stagedAttachmentsRef.current[index]
        if (!staged?.file || staged.stagingId !== attachment.staging_id) {
          throw new Error('Staged attachment state changed before session start')
        }
        return uploadDesktopV3MediaAsset({
          sessionId: createResult.sessionId,
          file: staged.file,
          mimeType: staged.mimeType,
          modality: staged.modality,
          fileType: staged.fileType,
          contractToken,
        })
      }))
      operation.firstMessageRequest.video_attachments = snapshot.videoAttachments
      operation.firstMessageRequest.artifact_selections = snapshot.artifactSelections
      const messageResponse = await appendFirstDesktopV3Message({ operation })
      await startedCallbackRef.current(messageResponse.session_id)
      stagedAttachmentHistoryRef.current = []
      removedStagedAttachmentIdsRef.current.clear()
      stagedAttachmentsRef.current = []
      setStagedAttachments([])
      setRestoredSnapshot(null)
      return
    }
    operation.firstMessageRequest.video_attachments = snapshot.videoAttachments
    operation.firstMessageRequest.artifact_selections = snapshot.artifactSelections
    setStarting(true)
    setLocalError(null)
    setRestoredSnapshot(snapshot)
    try {
      const result = await startNewDesktopV3Session({ operation })
      await startedCallbackRef.current(result.sessionId)
      stagedAttachmentHistoryRef.current = []
      removedStagedAttachmentIdsRef.current.clear()
      stagedAttachmentsRef.current = []
      setStagedAttachments([])
      setRestoredSnapshot(null)
    } catch (cause) {
      setLocalError(cause instanceof Error ? cause.message : 'Session start failed.')
    } finally {
      setStarting(false)
    }
  }

  useEffect(() => {
    const initialCommandPrompt = initialPrompt.trim()
    if (!initialCommandPrompt || initialPromptSubmittedRef.current) return
    initialPromptSubmittedRef.current = true
    void handleSubmit({
      prompt: initialCommandPrompt,
      attachments: [],
      artifactSelections: [],
      videoAttachments: [],
      selectedAction: null,
      selectedSkill: null,
      planModeRequested: initialPlanModeRequested,
    })
  }, [initialPrompt, initialPlanModeRequested])

  async function handleStageAttachments(files: File[], signal: AbortSignal) {
    if (starting) throw new Error('Wait for the current session start to finish.')
    const history = stagedAttachmentHistoryRef.current
    const stagedHistory = await stageDesktopComposerAttachments({
      files,
      routedClientRequestId: operationIdRef.current,
      existing: history,
      signal,
    })
    const staged = [...stagedAttachmentsRef.current, ...stagedHistory.slice(history.length)]
    stagedAttachmentHistoryRef.current = stagedHistory
    stagedAttachmentsRef.current = staged
    setStagedAttachments(staged)
  }

  const activationPending = starting
  const headerStatus: DesktopV3RunStatusModel | null = starting
    ? { kind: 'starting', label: 'Starting…', active: false }
    : localError
      ? { kind: 'failed', label: 'Start failed', active: false }
      : null

  return (
    <div
      className="relative flex min-h-0 flex-1 flex-col bg-[var(--app-bg)]"
      data-desktop-chat-drop-zone
      data-testid="desktop-v3-new-session-pane"
      data-start-phase={starting ? 'starting' : localError ? 'failed' : 'draft'}
    >
      <DesktopV3ChatHeader
        title="New chat"
        workspaceName={workspace.workspaceName}
        runStatus={headerStatus}
      />

      {mobileSessionQuickMenu ? (
        <section
          className="flex min-h-0 flex-1 flex-col overflow-hidden sm:hidden"
          data-testid="mobile-workspace-session-list"
          aria-label="Workspace sessions"
        >
          {mobileSessionQuickMenu}
        </section>
      ) : null}

      <DesktopV3RoutedPendingShell
        state="draft"
        startPath="direct"
        pendingPrompt={draft}
        error={localError ?? undefined}
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
          inputLabel="Start a Desktop V3 session"
          disabled={activationPending}
          busy={activationPending}
          canSubmit={Boolean(draft.trim()) || stagedAttachments.length > 0 || Boolean(artifactSelectionRequest)}
          onSubmit={() => undefined}
          artifactSelectionRequest={artifactSelectionRequest}
          onArtifactSelectionRequestHandled={onArtifactSelectionRequestHandled}
          onRoutedSubmit={handleSubmit}
          routedStagedAttachments={stagedAttachments}
          onRoutedStageAttachments={activationPending ? undefined : handleStageAttachments}
          onRoutedRemoveStagedAttachment={activationPending ? undefined : (stagingId) => setStagedAttachments((current) => {
            removedStagedAttachmentIdsRef.current.add(stagingId)
            const next = current.filter((attachment) => attachment.stagingId !== stagingId)
            stagedAttachmentsRef.current = next
            return next
          })}
          routedComposerSnapshot={restoredSnapshot}
          mode={mode}
          onModeSelect={activationPending ? undefined : setMode}
          currentAgent="swarm"
          selectedPrimaryAgent="swarm"
          agents={agentStateQuery.data?.profiles ?? []}
          modelProfiles={modelProfiles}
          activeModelProfile={activeModelProfile}
          modelOptions={modelOptionsQuery.data ?? []}
          selectedModelKey={selectedActionModel ? modelOptionKey(selectedActionModel.provider, selectedActionModel.model, selectedActionModel.contextMode) : ''}
          selectedServiceTier={selectedActionModel?.serviceTier ?? ''}
          thinking={selectedActionModel?.thinking ?? ''}
          modelControlDetail={selectedActionModel ? `${selectedActionModel.provider}/${selectedActionModel.model}` : 'Swarm action model'}
          modelStatusLabel="Ready"
          agentSettingsOpenSignal={agentSettingsOpenSignal}
          agentSettingsInitialAgent={agentSettingsInitialAgent}
          onConfirmAgentSettings={handleConfirmAgentSettings}
          onApplyModelFavoriteChatOnly={(profile) => setChatOnlyModelProfile(profile)}
          agentModelControlBusy={agentModelSaving}
          error={localError}
          routedNewSession
          onSlashCommand={onSlashCommand}
          developerMode={developerMode}
          onOpenActionSettings={onOpenActionSettings}
          slashCommandContext="new-session"
        />
    </div>
  )
}
