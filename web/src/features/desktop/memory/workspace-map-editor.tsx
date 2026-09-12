import { useEffect, useState } from 'react'
import { requestJson } from '../../../app/api'
import { Button } from '../../../components/ui/button'

type MapRecord = { content: string; revision: number; updated_at: number }
export function WorkspaceMapEditor({ onDirtyChange, onBusyChange }: { onDirtyChange: (dirty: boolean) => void; onBusyChange: (busy: boolean) => void }) {
 const [record, setRecord] = useState<MapRecord>()
 const [text, setText] = useState('')
 const [error, setError] = useState('')
 const [busy, setBusy] = useState(false)
 const load = async () => {
  const result = await requestJson<{found: boolean; workspace_map: MapRecord}>('/v1/memory/workspace-map')
  if (!result.found) { setError('No workspace map exists. Ask Swarm to inspect the workspace map in chat.'); return }
  setRecord(result.workspace_map); setText(result.workspace_map.content); onDirtyChange(false)
 }
 useEffect(() => { void load().catch(e => setError(String(e))) }, [])
 const save = async () => {
  if (!record || busy) return
  setBusy(true); onBusyChange(true); setError('')
  try {
   const next = await requestJson<MapRecord>('/v1/memory/workspace-map', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({expected_revision:record.revision,content:text,confirm:true,intent:'User explicitly saved the workspace map'})})
   setRecord(next); setText(next.content); onDirtyChange(false)
  } catch(e) { setError(`${String(e)}. Your draft is preserved. Copy it before reloading to reconcile changes.`) }
  finally { setBusy(false); onBusyChange(false) }
 }
 return <section aria-label="Workspace map editor">
  <p>Private account orientation, not repository instructions or an access grant. Keep portable project rules in AGENTS.md. Never include credential values.</p>
  <p>In chat, ask “Give me an overview of my workspaces” or “Update my workspace map”. Swarm uses authorized workspace inspection and manage_workspace; changes require explicit permission.</p>
  {error && <p role="alert" className="memory-error">{error}</p>}
  {record && <><p>Revision {record.revision} · Updated {new Date(record.updated_at).toLocaleString()}</p>
   <textarea aria-label="Workspace map" value={text} disabled={busy} onChange={e => {setText(e.target.value); onDirtyChange(e.target.value !== record.content)}} />
  </>}
  <div className="memory-modal-actions">
   <Button variant="ghost" disabled={busy} onClick={() => { if (record && text !== record.content && !window.confirm('Discard your unsaved workspace map?')) return; void load().catch(e => setError(String(e))) }}>Reload map</Button>
   <Button disabled={busy || !record || text === record.content || !text.startsWith('# Workspace Map\n')} onClick={() => void save()}>{busy ? 'Saving…' : 'Save workspace map'}</Button>
  </div>
 </section>
}
