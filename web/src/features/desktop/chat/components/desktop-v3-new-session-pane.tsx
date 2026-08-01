import { useEffect, useRef, useState, type ReactNode } from 'react'

import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import type { DesktopChatRoute } from '../services/chat-routing'
import type { DesktopSlashCommand } from '../services/slash-commands'
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
import {
  createDesktopRoutedWorktreeIntent,
  encodeDesktopRoutedWorktreeIntentMetadata,
  setDesktopRoutedWorktreeIntent,
} from '../services/desktop-routed-worktree-intent'

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
  onRoutedSessionResolved?: (result: DesktopV3RoutedStartResult) => void
  onOpenChats?: () => void
  mobileSessionQuickMenu?: ReactNode
  onSlashCommand?: (command: DesktopSlashCommand, draft: string) => void | Promise<void>
  agentSettingsOpenSignal?: number
  agentSettingsInitialAgent?: string
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
  const stagedAttachmentsRef = useRef<DesktopComposerStagedAttachment[]>([])
  const stagedAttachmentHistoryRef = useRef<DesktopComposerStagedAttachment[]>([])
  const operationAttachmentsRef = useRef<DesktopComposerStagedAttachment[] | null>(null)
  const [controller] = useState(() => {
    const routedController = new DesktopV3RoutedNewSessionController(async (request) => {
      const result = await postDesktopV3RoutedSessionStart(request)
      const submittedAttachments = operationAttachmentsRef.current ?? []
      if (submittedAttachments.length > 0) {
        reconcileDesktopComposerStagedAttachments(submittedAttachments, result.first_message)
      }
      return result
    })
    if (routedController.getState().phase === 'failed') routedController.startDraft()
    return routedController
  })
  const [routedState, setRoutedState] = useState<DesktopV3RoutedNewSessionState>(() => controller.getState())
  const initialControllerState = controller.getState()
  const [draft, setDraft] = useState(() => initialControllerState.snapshot.prompt)
  const [stagedAttachments, setStagedAttachments] = useState<DesktopComposerStagedAttachment[]>([])
  const [worktreeIntent, setWorktreeIntent] = useState(() => createDesktopRoutedWorktreeIntent(initialControllerState.snapshot.worktreePrimed))
  const [restoredSnapshot, setRestoredSnapshot] = useState<DesktopV3RoutedComposerSnapshot | null>(null)
  const [localError, setLocalError] = useState<string | null>(null)
  const resolvedCallbackRef = useRef(onRoutedSessionResolved)
  const handledResolvedOperationRef = useRef('')

  useEffect(() => {
    resolvedCallbackRef.current = onRoutedSessionResolved
  }, [onRoutedSessionResolved])

  useEffect(() => controller.subscribe(setRoutedState), [controller])

  useEffect(() => {
    stagedAttachmentsRef.current = stagedAttachments
  }, [stagedAttachments])

  useEffect(() => {
    if (routedState.phase === 'failed') {
      setLocalError(null)
      const restoredAttachments = operationAttachmentsRef.current ?? []
      stagedAttachmentsRef.current = restoredAttachments
      setStagedAttachments(restoredAttachments)
      setDraft(routedState.snapshot.prompt)
      setWorktreeIntent(createDesktopRoutedWorktreeIntent(routedState.snapshot.worktreePrimed))
      setRestoredSnapshot(routedState.snapshot)
      return
    }
    if (routedState.phase !== 'resolved') return
    if (handledResolvedOperationRef.current === routedState.operation.operationId) return
    handledResolvedOperationRef.current = routedState.operation.operationId
    setLocalError(null)
    operationAttachmentsRef.current = null
    stagedAttachmentHistoryRef.current = []
    stagedAttachmentsRef.current = []
    setStagedAttachments([])
    setRestoredSnapshot(null)
    setWorktreeIntent(createDesktopRoutedWorktreeIntent(false))
    resolvedCallbackRef.current?.(routedState.result)
  }, [routedState])

  function handleSubmit(snapshot: DesktopV3RoutedComposerSnapshot): Promise<DesktopV3RoutedNewSessionState> {
    try {
      const prompt = snapshot.prompt.trim() || (snapshot.attachments.length > 0 ? 'Please review the attached file(s).' : '')
      if (!prompt || routedState.phase === 'routing' || routedState.phase === 'resolved') {
        throw new Error('Routed Desktop start is not editable in its current state')
      }

      const captured = createDesktopV3RoutedComposerSnapshot({
        ...snapshot,
        prompt,
        attachments: snapshot.attachments,
        worktreePrimed: snapshot.worktreePrimed,
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
    stagedAttachmentsRef.current = restoredAttachments
    setStagedAttachments(restoredAttachments)
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
        onRetry={routedState.phase === 'failed' ? handleRetry : undefined}
      />

      {routedState.phase === 'failed' ? (
        <p className="shrink-0 px-4 pb-3 text-center text-sm text-[var(--app-danger)]" role="alert">
          {routedState.error}
        </p>
      ) : null}

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
            const next = current.filter((attachment) => attachment.stagingId !== stagingId)
            stagedAttachmentsRef.current = next
            return next
          })}
          routedComposerSnapshot={restoredSnapshot}
          routedWorktreeRequested={worktreeIntent.requested}
          onRoutedWorktreeRequestedChange={routedState.phase === 'failed' ? undefined : (requested) => {
            setWorktreeIntent((current) => setDesktopRoutedWorktreeIntent(current, requested))
          }}
          error={localError ?? (routedState.phase === 'failed' ? routedState.error : null)}
          routedNewSession
          onSlashCommand={onSlashCommand}
        />
      ) : null}
    </div>
  )
}
