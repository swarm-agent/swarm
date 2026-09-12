import { useEffect, useRef, useState } from 'react'
import { Brain, Plus, X } from 'lucide-react'
import { Button } from '../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { requestJson } from '../../../app/api'
import './memory-page.css'

type Entry = { id: string; kind: string; content: string; workspace_id?: string; session_id?: string; pinned: boolean; revision?: number }
type MemoryDocument = { revision: number; entries: Entry[] | null }
type Draft = { entry: Entry; revision: number; isNew: boolean }

export function MemoryModal({ onClose }: { onClose: () => void }) {
  const [doc, setDoc] = useState<MemoryDocument>()
  const [draft, setDraft] = useState<Draft>()
  const [forget, setForget] = useState<Entry>()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const panel = useRef<HTMLElement>(null)
  const writing = useRef<HTMLTextAreaElement>(null)

  const load = async () => {
    const next = await requestJson<MemoryDocument>('/v1/memory')
    setDoc(next)
  }
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    panel.current?.querySelector<HTMLButtonElement>('button')?.focus()
    void load().catch(cause => setError(String(cause)))
    return () => previous?.focus()
  }, [])
  useEffect(() => { if (draft) writing.current?.focus() }, [draft?.entry.id])

  const close = () => {
    if (busy) return
    if (draft && draft.entry.content !== (doc?.entries?.find(entry => entry.id === draft.entry.id)?.content ?? '') &&
      !window.confirm('Discard your unsaved memory?')) return
    onClose()
  }
  const mutate = async (body: Record<string, unknown>, revision: number) => {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await requestJson('/v1/memory', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...body, expected_revision: revision }),
      }, true, 150000)
      setDraft(undefined)
      setForget(undefined)
      // A successful write must not remain a retryable draft if the refresh fails.
      setDoc(undefined)
      try { await load() } catch (cause) { setError(`Saved, but memories could not be refreshed. ${String(cause)}`) }
    } catch (cause) {
      setError(`${String(cause)}. Your text is preserved. If memory changed elsewhere, cancel and reopen it before saving again.`)
    } finally { setBusy(false) }
  }

  return <Dialog role="dialog" aria-modal="true" aria-labelledby="memory-title" className="z-[90]" onKeyDown={event => {
    if (event.key === 'Escape') { event.stopPropagation(); close() }
    if (event.key !== 'Tab') return
    const controls = panel.current?.querySelectorAll<HTMLElement>('button:not(:disabled), textarea:not(:disabled), [tabindex="0"]')
    if (!controls?.length) { event.preventDefault(); return }
    const first = controls[0], last = controls[controls.length - 1]
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
  }}>
    <DialogBackdrop onClick={close} />
    <DialogPanel className="memory-modal">
      <section ref={panel} className="memory-modal-inner">
        <header className="memory-modal-header">
          <div><h2 id="memory-title"><Brain size={20} />{draft ? (draft.isNew ? 'Add memory' : 'Edit memory') : 'Memory'}</h2>
            <p>{draft ? 'Write it the way you would tell Swarm.' : 'The things you want Swarm to remember.'}</p></div>
          <Button variant="ghost" size="sm" aria-label="Close memory" disabled={busy} onClick={close}><X size={18} /></Button>
        </header>
        <div className="memory-modal-body">
          {error && <p role="alert" className="memory-error">{error}</p>}
          {draft ? <>
            <textarea ref={writing} aria-label="Memory" placeholder="What would you like Swarm to remember?" rows={9} disabled={busy}
              value={draft.entry.content} onChange={event => setDraft({ ...draft, entry: { ...draft.entry, content: event.target.value } })} />
            <div className="memory-modal-actions">
              <Button variant="ghost" disabled={busy} onClick={() => { setDraft(undefined); setError('') }}>Cancel</Button>
              <Button disabled={busy || !draft.entry.content.trim()} onClick={() => void mutate({ action: 'remember', entry: draft.entry, reason: draft.isNew ? 'User added a memory' : 'User edited this memory' }, draft.revision)}>{busy ? 'Saving…' : 'Save memory'}</Button>
            </div>
          </> : forget ? <>
            <h3>Forget this memory?</h3>
            <p className="memory-content">{forget.content}</p>
            <p>This permanently removes the memory and redacts its history. It cannot be undone.</p>
            <div className="memory-modal-actions">
              <Button variant="ghost" disabled={busy} onClick={() => { setForget(undefined); setError('') }}>Cancel</Button>
              <Button disabled={busy || !doc} onClick={() => doc && void mutate({ action: 'forget', entry_id: forget.id, reason: 'User confirmed permanently forgetting this memory' }, doc.revision)}>{busy ? 'Forgetting…' : 'Forget permanently'}</Button>
            </div>
          </> : <>
            <div className="memory-modal-toolbar">
              <span>{doc ? `${doc.entries?.length ?? 0} saved ${(doc.entries?.length ?? 0) === 1 ? 'memory' : 'memories'}` : 'Your memories'}</span>
              <div><Button variant="ghost" size="sm" disabled={busy} onClick={() => { setError(''); void load().catch(cause => setError(String(cause))) }}>Refresh</Button>
                <Button size="sm" disabled={busy || !doc} onClick={() => doc && setDraft({ entry: { id: crypto.randomUUID(), kind: 'rule', content: '', pinned: false }, revision: doc.revision, isNew: true })}><Plus size={16} />Add memory</Button></div>
            </div>
            {!doc ? <p role="status">{error ? 'Use Refresh to load your memories.' : 'Loading memories…'}</p> : !doc.entries?.length ? <div className="memory-empty"><Brain size={28} /><h3>No memories yet</h3><p>Add something you’d like Swarm to remember for next time.</p></div> :
              <div className="memory-grid" aria-label="Saved memories">{doc.entries.map(entry => <article key={entry.id} className="memory-card">
                <button className="memory-card-content" disabled={busy} aria-label={`Edit memory: ${entry.content.slice(0, 80)}`} onClick={() => { setError(''); setDraft({ entry: { id: entry.id, kind: entry.kind, content: entry.content, pinned: entry.pinned, workspace_id: entry.workspace_id, session_id: entry.session_id }, revision: doc.revision, isNew: false }) }}>
                  <span className="memory-card-kind">{entry.kind === 'rule' ? 'Instruction' : entry.kind === 'orientation' ? 'Context' : 'Learned'}{entry.pinned ? ' · Pinned' : ''}{entry.workspace_id ? ' · Workspace' : entry.session_id ? ' · Session' : ''}</span>
                  <span className="memory-content">{entry.content}</span>
                  <span className="memory-card-hint">Click to edit</span>
                </button>
                <Button variant="ghost" size="sm" disabled={busy} onClick={() => { setError(''); setForget(entry) }}>Forget</Button>
              </article>)}</div>}
          </>}
        </div>
      </section>
    </DialogPanel>
  </Dialog>
}
