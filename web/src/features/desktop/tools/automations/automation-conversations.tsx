import { useEffect, useRef, useState } from 'react'
import { createAutomationConversation, isAutomationManagementSession, loadAutomationConversations, type AutomationConversationPage } from '../../state/desktop-automation-conversations'
import { AutomationChat } from './automation-chat'
import { automationControl as control } from './automation-editor'

export function AutomationConversations({ workspaceId, workspacePath, selected, onSelect, automationId, prompt }: {
  workspaceId: string; workspacePath: string; selected: string; onSelect: (id: string) => void; automationId?: string; prompt?: { id: number; draft: string }
}) {
  const [page, setPage] = useState<AutomationConversationPage>()
  const [before, setBefore] = useState<AutomationConversationPage['pagination']>()
  const [refresh, setRefresh] = useState(0)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [loading, setLoading] = useState(true)
  const [draft, setDraft] = useState('')
  const [draftOpen, setDraftOpen] = useState(false)
  const [composerRequest, setComposerRequest] = useState<{ sessionId: string; id: number; draft: string; append: boolean }>()
  const sequence = useRef(0)
  const lock = useRef(false)
  const requestId = useRef('')
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  useEffect(() => { if (prompt) { setDraft(prompt.draft); setDraftOpen(true) } }, [prompt])
  useEffect(() => {
    const controller = new AbortController()
    setError(''); setLoading(true)
    void loadAutomationConversations(workspaceId, workspacePath, before, controller.signal).then(result => {
      if (!controller.signal.aborted) setPage(result)
    }).catch(cause => {
      if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'Conversation list unavailable.')
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [workspaceId, workspacePath, before, refresh])
  async function create(useDraft = false) {
    if (lock.current) return
    lock.current = true; setBusy(true); setError('')
    requestId.current ||= crypto.randomUUID()
    try {
      const session = await createAutomationConversation(workspacePath, requestId.current)
      if (!mounted.current) return
      requestId.current = ''; onSelect(session.id); setBefore(undefined); setRefresh(value => value + 1)
      if (useDraft) transferDraft(session.id)
    } catch (cause) { if (mounted.current) setError(cause instanceof Error ? cause.message : 'Conversation could not be created.') }
    finally { lock.current = false; if (mounted.current) setBusy(false) }
  }
  function transferDraft(sessionId: string) {
    setComposerRequest({ sessionId, id: ++sequence.current, draft, append: true })
    setDraft(''); setDraftOpen(false)
  }
  return <section id="automation-ai" tabIndex={-1} aria-label="Automation management conversations" className="min-w-0 space-y-3 border-t border-[var(--app-border)] p-3 focus-visible:outline-2 lg:sticky lg:top-0 lg:max-h-dvh lg:w-[420px] lg:shrink-0 lg:self-start lg:overflow-y-auto lg:border-t-0 lg:border-l">
    <header><h2 className="font-semibold">Automation AI</h2><p className="text-sm text-[var(--app-text-muted)]">Plan, refine and review—with your workspace access and normal permissions. Starting a chat does not save an automation or grant execution.</p></header>
    <div className="flex flex-wrap gap-2"><button className={control} disabled={busy} onClick={() => void create()}>{busy ? 'Starting…' : 'New automation chat'}</button><button className={control} aria-expanded={draftOpen} onClick={() => setDraftOpen(value => !value)}>Draft a request</button></div>
    {(draftOpen || !selected) && <div className="space-y-2"><label className="flex flex-col gap-2">What should this automation do?<textarea className={control} rows={6} value={draft} disabled={busy} onChange={event => setDraft(event.target.value)} placeholder="Describe the task, when it should run, and what you want back." /></label><p className="text-xs">Your draft stays editable in chat. Review it and press Send there; this button does not send a message.</p><button className={control} disabled={busy || !draft.trim()} onClick={() => selected ? transferDraft(selected) : void create(true)}>{selected ? 'Add draft to current chat' : 'Start chat with this draft'}</button></div>}
    <details><summary className="cursor-pointer py-2">Reopen or switch conversations</summary>
      <button className={control} disabled={loading} onClick={() => setRefresh(value => value + 1)}>Refresh conversations</button>
      <nav aria-label="Automation conversations"><ul>{page?.session_order.map(id => page.sessions_by_id[id]).filter(session => isAutomationManagementSession(session, workspaceId)).map(session => <li key={session.id}><button className={`${control} my-1 w-full text-left break-words`} disabled={busy} aria-current={selected === session.id ? 'page' : undefined} onClick={() => onSelect(session.id)}>{session.title || 'Automation planning'}</button></li>)}</ul></nav>
      {before && <button className={control} onClick={() => setBefore(undefined)}>Latest conversations</button>}
      {page?.pagination.has_more && <button className={control} disabled={loading} onClick={() => setBefore(page.pagination)}>Older conversations</button>}
    </details>
    {loading && <p role="status">Loading conversations…</p>}
    {!loading && !error && page?.session_order.length === 0 && <p className="text-sm">No saved management conversations yet. Start here without an existing automation.</p>}
    {error && <p role="alert">{error} <button className={control} onClick={() => setRefresh(value => value + 1)}>Retry list</button></p>}
    {selected && <><p className="break-all text-xs text-[var(--app-text-muted)]">Open conversation: {page?.sessions_by_id[selected]?.title || selected}</p><AutomationChat key={selected} sessionId={selected} automationId={automationId} composerDraftRequest={composerRequest?.sessionId === selected ? composerRequest : undefined} onComposerDraftRequestHandled={() => setComposerRequest(undefined)} /></>}
  </section>
}
