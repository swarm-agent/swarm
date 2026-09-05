import { useCallback, useEffect, useState } from 'react'
import { ArrowLeft, CheckCircle2, RefreshCw } from 'lucide-react'

import { Button } from '../../../../components/ui/button'
import { DesktopV3ExistingConversationPane } from '../../chat/components/desktop-v3-existing-conversation-pane'
import { DesktopV3RuntimeProvider } from '../../runtime/desktop-v3-runtime-provider'
import { isDesktopV3SessionTailReady, selectRenderedSessionMessages, type RenderedSessionMessages } from '../../state/desktop-v3-cache-selectors'
import { useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import { selectAndHydrateDesktopV3Session } from '../../state/desktop-v3-session-hydrator'

function renderedMessagesEqual(left: RenderedSessionMessages, right: RenderedSessionMessages): boolean {
  return left.committed === right.committed
    && left.pendingUser === right.pendingUser
    && left.liveRuns === right.liveRuns
    && left.runIntents === right.runIntents
    && left.currentRunIntent === right.currentRunIntent
    && left.latestRunIntent === right.latestRunIntent
}

export interface WorkspaceOnboardingAssistantProps {
  sessionId: string
  path: string
  onCheckRepository: () => void
  checkingRepository: boolean
  checkError: string | null
}

export function WorkspaceOnboardingAssistant({
  sessionId,
  path,
  onCheckRepository,
  checkingRepository,
  checkError,
}: WorkspaceOnboardingAssistantProps) {
  const [hydrateError, setHydrateError] = useState<string | null>(null)
  const renderedMessages = useDesktopV3CacheSelector(
    useCallback((state) => selectRenderedSessionMessages(state, sessionId), [sessionId]),
    renderedMessagesEqual,
  )
  const messagesLoaded = useDesktopV3CacheSelector(
    useCallback((state) => isDesktopV3SessionTailReady(state, sessionId), [sessionId]),
  )
  const loadedMessageCount = useDesktopV3CacheSelector(
    useCallback((state) => state.messagesBySession[sessionId]?.items.length ?? 0, [sessionId]),
  )
  const hydrating = useDesktopV3CacheSelector(
    useCallback((state) => (state.hydrateInFlightBySession[sessionId] ?? 0) > 0, [sessionId]),
  )
  const sessionMetadata = useDesktopV3CacheSelector(
    useCallback((state) => {
      const record = state.sessionsById[sessionId]
      return record?.kind === 'full' ? record.session.metadata : undefined
    }, [sessionId]),
  )
  useEffect(() => {
    setHydrateError(null)
    const frameId = window.requestAnimationFrame(() => {
      void selectAndHydrateDesktopV3Session(sessionId).catch((error) => {
        setHydrateError(error instanceof Error ? error.message : 'Could not load Onboarding Swarm.')
      })
    })
    return () => window.cancelAnimationFrame(frameId)
  }, [sessionId])

  return (
    <DesktopV3RuntimeProvider initialPreferredSessionId={sessionId}>
      <div className="fixed inset-0 z-[10001] flex min-h-0 flex-col bg-[var(--app-bg)] text-[var(--app-text)]">
        <header className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 sm:px-6">
          <div className="flex min-w-0 items-center gap-3">
            <Button type="button" variant="ghost" onClick={onCheckRepository} disabled={checkingRepository} aria-label="Check repository and return to workspace onboarding">
              <ArrowLeft size={16} />
              Check and return
            </Button>
            <div className="min-w-0">
              <h1 className="text-sm font-semibold text-[var(--app-text)]">Onboarding Swarm</h1>
              <p className="truncate text-xs text-[var(--app-text-muted)]" title={path}>Preparing {path}</p>
            </div>
          </div>
          <Button type="button" onClick={onCheckRepository} disabled={checkingRepository}>
            {checkingRepository ? <RefreshCw size={15} className="animate-spin motion-reduce:animate-none" /> : <CheckCircle2 size={15} />}
            {checkingRepository ? 'Checking repository…' : 'Check repository and finish onboarding'}
          </Button>
        </header>
        {checkError ? (
          <div role="alert" className="border-b border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)] sm:px-6">
            {checkError}
          </div>
        ) : null}
        {hydrateError ? (
          <div role="alert" className="border-b border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)] sm:px-6">
            {hydrateError} Use Check and return, then reopen Onboarding Swarm to retry.
          </div>
        ) : null}
        <div className="min-h-0 flex-1">
          <DesktopV3ExistingConversationPane
            sessionId={sessionId}
            initialHydrateStatus={hydrateError ? 'error' : hydrating ? 'loading' : messagesLoaded ? 'ready' : 'cached'}
            renderedMessages={renderedMessages}
            messagesLoaded={messagesLoaded}
            loadedMessageCount={loadedMessageCount}
            routeOptions={[]}
            metadata={sessionMetadata}
          />
        </div>
      </div>
    </DesktopV3RuntimeProvider>
  )
}
