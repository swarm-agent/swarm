import { useEffect, useRef, useState, type ReactNode } from 'react'

import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import type { DesktopChatRoute } from '../services/chat-routing'
import type { DesktopSlashCommand } from '../services/slash-commands'
import {
  DesktopV3RoutedNewSessionController,
  type DesktopV3RoutedNewSessionState,
  type DesktopV3RoutedStartResult,
} from '../../session-v3/new-session-flow'
import { postDesktopV3RoutedSessionStart } from '../../session-v3/write-api'
import { DesktopV3AgenticComposer } from './desktop-v3-agentic-composer'
import { DesktopV3RoutedPendingShell } from './desktop-v3-routed-pending-shell'

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
  const [controller] = useState(() => new DesktopV3RoutedNewSessionController(postDesktopV3RoutedSessionStart))
  const [routedState, setRoutedState] = useState<DesktopV3RoutedNewSessionState>(() => controller.getState())
  const [draft, setDraft] = useState(() => controller.getState().prompt)
  const resolvedCallbackRef = useRef(onRoutedSessionResolved)
  const handledResolvedOperationRef = useRef('')

  useEffect(() => {
    resolvedCallbackRef.current = onRoutedSessionResolved
  }, [onRoutedSessionResolved])

  useEffect(() => controller.subscribe(setRoutedState), [controller])

  useEffect(() => {
    if (routedState.phase !== 'resolved') return
    if (handledResolvedOperationRef.current === routedState.operation.operationId) return
    handledResolvedOperationRef.current = routedState.operation.operationId
    resolvedCallbackRef.current?.(routedState.result)
  }, [routedState])

  async function handleSubmit(submittedDraft: string) {
    const prompt = submittedDraft.trim()
    if (!prompt || routedState.phase === 'routing' || routedState.phase === 'resolved') return

    if (routedState.phase === 'failed') {
      await controller.retry()
      return
    }

    await controller.submit({
      prompt,
      metadata: { source: 'desktop-v3' },
    })
  }

  function handleRetry() {
    if (controller.getState().phase !== 'failed') return
    void controller.retry()
  }

  const pendingState = routedState.phase === 'resolved' ? 'routing' : routedState.phase
  const showComposer = routedState.phase === 'draft'

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
          canSubmit={Boolean(draft.trim())}
          onSubmit={handleSubmit}
          routedNewSession
          onSlashCommand={onSlashCommand}
        />
      ) : null}
    </div>
  )
}
