import { useCallback, useEffect, useState } from 'react'
import { DesktopV3ExistingConversationPane } from '../../chat/components/desktop-v3-existing-conversation-pane'
import { isDesktopV3SessionTailReady, selectRenderedSessionMessages } from '../../state/desktop-v3-cache-selectors'
import { useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import { selectAndHydrateDesktopV3Session } from '../../state/desktop-v3-session-hydrator'

export function AutomationChat({ sessionId, automationId, composerDraftRequest, onComposerDraftRequestHandled }: { sessionId: string; automationId?: string; composerDraftRequest?: { id: number; draft: string; append?: boolean }; onComposerDraftRequestHandled?: (id: number) => void }) {
  const [error, setError] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const messages = useDesktopV3CacheSelector(useCallback(state => selectRenderedSessionMessages(state, sessionId), [sessionId]), (left, right) => left.committed === right.committed && left.pendingUser === right.pendingUser && left.liveRuns === right.liveRuns && left.runIntents === right.runIntents && left.currentRunIntent === right.currentRunIntent && left.latestRunIntent === right.latestRunIntent)
  const ready = useDesktopV3CacheSelector(useCallback(state => isDesktopV3SessionTailReady(state, sessionId), [sessionId]))
  const count = useDesktopV3CacheSelector(useCallback(state => state.messagesBySession[sessionId]?.items.length ?? 0, [sessionId]))
  useEffect(() => {
    let active = true
    setError(false)
    void selectAndHydrateDesktopV3Session(sessionId).catch(() => { if (active) setError(true) })
    return () => { active = false }
  }, [sessionId, attempt])
  return <aside aria-label="Automation AI chat" className="flex min-h-[65dvh] min-w-0 flex-col border-t border-[var(--app-border)] w-full">
    <h2 className="p-3 font-semibold">Automation conversation and artifacts</h2>
    {error && <div role="alert">Conversation could not load. <button onClick={() => setAttempt(value => value + 1)}>Retry chat</button></div>}
    <DesktopV3ExistingConversationPane presentation="sidebar" composerDraftRequest={composerDraftRequest} onComposerDraftRequestHandled={onComposerDraftRequestHandled} contextChip={automationId ? { id: automationId, label: 'Automation', kind: 'automation', description: `Inspect automation ${automationId} with manage_automation. Management changes require explicit user review; this context does not authorize execution or replace pinned plans.` } : null} sessionId={sessionId} initialHydrateStatus={error ? 'error' : ready ? 'ready' : 'loading'} renderedMessages={messages} messagesLoaded={ready} loadedMessageCount={count} />
  </aside>
}
