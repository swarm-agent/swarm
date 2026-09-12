import { useEffect, useRef, useState } from 'react'
import { Brain, Plus, X } from 'lucide-react'
import { Button } from '../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { requestJson } from '../../../app/api'
import { WorkspaceMapEditor } from './workspace-map-editor'
import './memory-page.css'

type Entry = { id: string; kind: string; content: string; workspace_id?: string; session_id?: string; pinned: boolean; purpose?: string; origin?: string; subject?: string; created_at?: number; updated_at?: number }
type MemoryDocument = { revision: number; entries: Entry[] | null }
type Draft = { entry: Entry; revision: number; isNew: boolean; original: string }
const purposes = { '': 'Unknown / legacy', preference: 'Preference', project_context: 'Project context', operational_context: 'Operational context', recovery: 'Recovery guidance', orientation: 'Orientation' }
const label = (value?: string) => value ? value.replace(/_/g, ' ') : 'Unknown / legacy'
const date = (value?: number) => value ? new Date(value).toLocaleString() : 'Unknown / legacy'
const guidance = (entry: Entry) => ['operational_context','recovery'].includes(entry.purpose ?? '') || entry.origin === 'learned'

export function MemoryModal({ onClose, recoveryText }: { onClose: () => void; recoveryText?: string }) {
 const [doc, setDoc] = useState<MemoryDocument>()
 const [draft, setDraft] = useState<Draft>()
 const [forget, setForget] = useState<Entry>()
 const [view, setView] = useState('requested')
 const [mapDirty, setMapDirty] = useState(false)
 const [mapBusy, setMapBusy] = useState(false)
 const [consent, setConsent] = useState(false)
 const [error, setError] = useState('')
 const [busy, setBusy] = useState(false)
 const panel = useRef<HTMLElement>(null)
 const writing = useRef<HTMLTextAreaElement>(null)
 const load = async () => {
  const next = await requestJson<MemoryDocument>('/v1/memory'); setDoc(next); return next
 }
 useEffect(() => {
  const previous = document.activeElement as HTMLElement | null
  panel.current?.querySelector<HTMLButtonElement>('button')?.focus()
  void load().then(next => {
   if (recoveryText !== undefined) {
    const entry = {id:crypto.randomUUID(),kind:'rule',content:recoveryText,purpose:'recovery',pinned:false}
    setDraft({entry,revision:next.revision,isNew:true,original:''}); setView('guidance')
   }
  }).catch(cause => setError(String(cause)))
  return () => previous?.focus()
 }, [])
 useEffect(() => { if(draft) writing.current?.focus() }, [draft?.entry.id])
 const close = () => {
  if (busy || mapBusy) return
  if ((mapDirty || draft && JSON.stringify(draft.entry) !== draft.original) && !window.confirm('Discard your unsaved memory?')) return
  onClose()
 }
 const mutate = async (body: Record<string,unknown>, revision: number) => {
  if(busy) return
  setBusy(true); setError('')
  try {
   await requestJson('/v1/memory',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...body,expected_revision:revision})},true,150000)
   setDraft(undefined); setForget(undefined); setDoc(undefined)
   try { await load() } catch(cause) { setError(`Saved, but memories could not be refreshed. ${String(cause)}`) }
  } catch(cause) { setError(`${String(cause)}. Your draft is preserved. Copy it before cancelling and refreshing to reconcile changes.`) }
  finally { setBusy(false) }
 }
 const entries = doc?.entries?.filter(entry => entry.id !== 'workspace-map' && (view === 'guidance' ? guidance(entry) : !guidance(entry))) ?? []
 return <Dialog role="dialog" aria-modal="true" aria-labelledby="memory-title" className="z-[90]" onKeyDown={event => {
  if(event.key === 'Escape') {event.stopPropagation(); close()}
  if(event.key !== 'Tab') return
  const controls = panel.current?.querySelectorAll<HTMLElement>('button:not(:disabled), textarea:not(:disabled), input:not(:disabled), select:not(:disabled), [tabindex="0"]')
  if(!controls?.length) {event.preventDefault(); return}
  const first=controls[0], last=controls[controls.length-1]
  if(event.shiftKey && document.activeElement === first) {event.preventDefault();last.focus()}
  else if(!event.shiftKey && document.activeElement === last) {event.preventDefault();first.focus()}
 }}>
  <DialogBackdrop onClick={close} />
  <DialogPanel className="memory-modal"><section ref={panel} className="memory-modal-inner">
   <header className="memory-modal-header"><div><h2 id="memory-title"><Brain size={20} />{draft ? (draft.isNew ? 'Add memory' : 'Edit memory') : 'Memory'}</h2><p>Private account context. Keep portable repository instructions in AGENTS.md.</p></div><Button variant="ghost" size="sm" aria-label="Close memory" disabled={busy || mapBusy} onClick={close}><X size={18} /></Button></header>
   <div className="memory-modal-body">
    {error && <p role="alert" className="memory-error">{error}</p>}
    {draft ? <>
     <p>Never enter passwords, tokens or credential values. Scope labels do not grant access. Origin and timestamps are server-owned.</p>
     <div className="memory-fields">
      <label>Purpose<select aria-label="Purpose" disabled={busy} value={draft.entry.purpose ?? ''} onChange={e => {setConsent(false);setDraft({...draft,entry:{...draft.entry,purpose:e.target.value}})}}>{Object.entries(purposes).map(([value,text]) => <option key={value} value={value}>{text}</option>)}</select></label>
      {(['subject','workspace_id','session_id'] as const).map(field => <label key={field}>{field === 'subject' ? 'Related subject' : field === 'workspace_id' ? 'Workspace scope ID' : 'Session scope ID'}<input disabled={busy} maxLength={256} value={draft.entry[field] ?? ''} onChange={e => setDraft({...draft,entry:{...draft.entry,[field]:e.target.value}})} /></label>)}
     </div>
     <p className="memory-metadata">Origin: {draft.isNew ? 'User (on save)' : label(draft.entry.origin)} · Created: {date(draft.entry.created_at)} · Updated: {date(draft.entry.updated_at)}</p>
     <textarea ref={writing} aria-label="Memory" rows={9} disabled={busy} value={draft.entry.content} onChange={e => {setConsent(false);setDraft({...draft,entry:{...draft.entry,content:e.target.value}})}} />
     {draft.entry.purpose === 'recovery' && <label className="memory-consent"><input type="checkbox" disabled={busy} checked={consent} onChange={e => setConsent(e.target.checked)} />I reviewed this recovery guidance and want it saved. This does not resume any session.</label>}
     <div className="memory-modal-actions"><Button variant="ghost" disabled={busy} onClick={() => {setDraft(undefined);setError('')}}>Cancel</Button><Button disabled={busy || !draft.entry.content.trim() || draft.entry.purpose === 'recovery' && !consent} onClick={() => void mutate({action:draft.isNew ? 'remember' : 'edit',entry:draft.entry,reason:draft.entry.purpose === 'recovery' ? 'User reviewed and explicitly saved recovery guidance' : 'User explicitly saved a memory'},draft.revision)}>{busy ? 'Saving…' : 'Save memory'}</Button></div>
    </> : forget ? <>
     <h3>Forget this memory?</h3><p className="memory-content">{forget.content}</p><p>This permanently removes the memory and redacts its history. It cannot be undone.</p>
     <div className="memory-modal-actions"><Button variant="ghost" disabled={busy} onClick={() => {setForget(undefined);setError('')}}>Cancel</Button><Button disabled={busy || !doc} onClick={() => doc && void mutate({action:'forget',entry_id:forget.id,reason:'User confirmed permanently forgetting this memory'},doc.revision)}>Forget permanently</Button></div>
    </> : <>
     <nav className="memory-views" aria-label="Memory views">{[['requested','Your requested memories'],['guidance','AI operating guidance'],['map','Workspace map']].map(([id,text]) => <Button key={id} variant="ghost" disabled={mapBusy} aria-pressed={view === id} onClick={() => {if(view === id) return;if(mapDirty && !window.confirm('Discard your unsaved workspace map?')) return;setMapDirty(false);setView(id)}}>{text}</Button>)}</nav>
     <p className="memory-explanation">Purpose describes what a memory is for; origin describes where it came from. Legacy entries have unknown origin, not assumed recovery approval.</p>
     {view === 'map' ? <WorkspaceMapEditor onDirtyChange={setMapDirty} onBusyChange={setMapBusy} /> : <>
      <div className="memory-modal-toolbar"><span>{entries.length} saved {entries.length === 1 ? 'memory' : 'memories'}</span><div><Button variant="ghost" size="sm" disabled={busy} onClick={() => {setError('');void load().catch(e => setError(String(e)))}}>Refresh</Button><Button size="sm" disabled={busy || !doc} onClick={() => {if(!doc)return;setConsent(false);const entry={id:crypto.randomUUID(),kind:'rule',content:'',pinned:false,purpose:view === 'guidance' ? 'operational_context' : ''};setDraft({entry,revision:doc.revision,isNew:true,original:JSON.stringify(entry)})}}><Plus size={16} />Add memory</Button></div></div>
      {!doc ? <p role="status">{error ? 'Use Refresh to load your memories.' : 'Loading memories…'}</p> : !entries.length ? <div className="memory-empty"><Brain size={28} /><h3>No memories yet</h3><p>Add something you’d like Swarm to remember for next time.</p></div> : <div className="memory-grid" aria-label="Saved memories">{entries.map(entry => <article key={entry.id} className="memory-card">
       <button className="memory-card-content" aria-label={`Edit memory: ${entry.content.slice(0,80)}`} onClick={() => {setError('');setConsent(false);setDraft({entry:{...entry},revision:doc.revision,isNew:false,original:JSON.stringify(entry)})}}><span className="memory-content">{entry.content}</span></button>
       <p className="memory-metadata">Purpose: {label(entry.purpose)} · Origin: {label(entry.origin)}<br />Subject: {entry.subject || 'Not specified'} · Workspace: {entry.workspace_id || 'Account-wide'} · Session: {entry.session_id || 'Not scoped'}<br />Created: {date(entry.created_at)} · Updated: {date(entry.updated_at)}</p>
       <div className="memory-card-actions"><span className="memory-card-hint">Click text to edit</span><Button variant="ghost" size="sm" onClick={() => {setError('');setForget(entry)}}>Forget</Button></div>
      </article>)}</div>}
     </>}
    </>}
   </div>
  </section></DialogPanel>
 </Dialog>
}
