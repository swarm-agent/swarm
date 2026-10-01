import { useEffect, useMemo, useRef, useState } from 'react'
import { DesignArchiveButton } from './design-archive-button'
import { desktopDesigns } from '../../runtime/desktop-design-runtime'
import { designDownloadName, designEditBody, designRefKey, designSandbox, designSelectionBody, fetchDesignView, postDesign } from '../../session-v3/design-api'
import { designMediaItem, designNodeId, designStatus } from '../../orchestrate/design-media-task'
import { MediaTaskCard } from '../../orchestrate/media-task-card'
import { useDesignResource, useProjectDesigns } from './design-media'
import type { MediaLibraryItem } from './types'

type DesignItem = Extract<MediaLibraryItem, { source: 'independent-design' }>
/** Source-specific controls inside MediaViewerModal. Authored HTML never enters the Desktop DOM. */
export function DesignRevisionView({ item, onSelect }: { item: DesignItem; onSelect: (item: MediaLibraryItem) => void }) {
  const { revision, projectId } = item.design
  const session = item.sessionId
  const resource = useMemo(() => desktopDesigns.history(session, revision.ref.artifact_id), [session, revision.ref.artifact_id])
  const editsResource = useMemo(() => desktopDesigns.editRequests(session), [session])
  const history = useDesignResource(resource)
  const edits = useDesignResource(editsResource)
  const project = useProjectDesigns(projectId)
  const archivedProject = useProjectDesigns(projectId, 'archived')
  const [content, setContent] = useState<string>()
  const [error, setError] = useState('')
  const [brief, setBrief] = useState('')
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState('')
  const [requestedKey, setRequestedKey] = useState('')
  const [download, setDownload] = useState('')
  const [retry, setRetry] = useState(0)
  const intent = useRef<{ brief: string; key: string } | undefined>(undefined)
  const mutation = useRef<AbortController | undefined>(undefined)
  useEffect(() => () => mutation.current?.abort(), [])
  useEffect(() => {
    const controller = new AbortController()
    setContent(undefined)
    void fetchDesignView(session, revision, controller.signal).then(value => { if (!controller.signal.aborted) setContent(value) }, cause => { if (!controller.signal.aborted) setError(String(cause)) })
    return () => controller.abort()
  }, [session, revision, retry])
  useEffect(() => () => { if (download) URL.revokeObjectURL(download) }, [download])
  const rows = [...new Map([...(project.data?.designs ?? []), ...(archivedProject.data?.designs ?? [])].filter(row => row.request.parent_session_id === session).map(row => [row.request.id, row])).values()]
  const row = rows.find(row => row.request.id === item.design.requestId)
  const acceptedNotice = requestedKey && rows.find(row => row.request.client_request_id === requestedKey && edits.data?.edits.some(edit => edit.messageId === row.request.source_message_id && edit.clientRequestId === requestedKey && row.request.candidates.some(candidate => candidate.spec.base && designRefKey(candidate.spec.base) === designRefKey(edit.base))))
  async function act(action: 'edit' | 'select' | 'download') {
    if (busy) return
    const controller = new AbortController(); mutation.current = controller
    setBusy(true); setError('')
    try {
      if (action === 'edit' && (!intent.current || intent.current.brief !== brief)) intent.current = { brief, key: crypto.randomUUID() }
      if (action === 'select' && !history.data) throw new Error('Refresh revision authority before selecting')
      const body = action === 'edit' ? designEditBody(revision.ref, brief, intent.current!.key) : action === 'select' ? designSelectionBody(revision.ref, history.data!.artifact, crypto.randomUUID()) : { action, ref: revision.ref }
      const response = await postDesign(session, revision.ref, body, controller.signal)
      if (action === 'download') {
        const bytes = await response.arrayBuffer()
        if (!controller.signal.aborted) setDownload(URL.createObjectURL(new Blob([bytes], { type: 'application/octet-stream' })))
      } else if (!controller.signal.aborted) {
        desktopDesigns.invalidate(session); desktopDesigns.invalidateProject(projectId)
        setNotice(action === 'edit' ? `Edit requested from revision ${revision.ref.revision}; awaiting parent acceptance.` : `Selected revision ${revision.ref.revision}.`)
        if (action === 'edit') { setRequestedKey(intent.current!.key); intent.current = undefined; setBrief('') }
      }
    } catch (cause) {
      if (!controller.signal.aborted) setError(String(cause))
      if (action === 'select') desktopDesigns.invalidate(session)
    } finally { if (!controller.signal.aborted) setBusy(false) }
  }
  return <section className="w-full min-w-0 self-start space-y-3 text-white" aria-label="Design revision turns">
    {history.data && <DesignArchiveButton key={`${projectId}:${session}:${revision.ref.artifact_id}`} session={session} reference={revision.ref} version={history.data.artifact.archive_version ?? 0} archived={history.data.artifact.archived} />}
    <h3>Viewing revision {revision.ref.revision}</h3>
    <p className="break-all text-xs">SHA256: {revision.ref.sha256}</p>
    <p className="break-all text-xs">{revision.base ? `Base: ${revision.base.artifact_id} revision ${revision.base.revision} SHA256 ${revision.base.sha256}` : 'Original request'}</p>
    <nav aria-label="Revision history" className="flex flex-wrap gap-2">
      {history.data?.revisions.map(next => <button key={designNodeId(session, next.ref)} aria-pressed={designRefKey(next.ref) === designRefKey(revision.ref)} onClick={() => {
        const owner = rows.find(candidate => candidate.request.id === next.request_id) ?? row
        if (owner) onSelect(designMediaItem(owner, next.candidate ?? item.design.candidate, next))
        else onSelect({ ...item, id: designNodeId(session, next.ref), design: { ...item.design, revision: next }, parentId: next.base ? designNodeId(session, next.base) : undefined })
      }}>Revision {next.ref.revision}{next.base ? ` ← ${next.base.revision}` : ' · original'}</button>)}
      {history.data && history.data.revisions.length < history.data.artifact.revision_count && <button disabled={history.loading} onClick={() => void desktopDesigns.moreHistory(session, revision.ref.artifact_id)}>More history</button>}
    </nav>
    {[error, history.error, edits.error, project.error, archivedProject.error].filter(Boolean).map((value, index) => <p role="alert" key={index}>{value}</p>)}
    {content === undefined ? <button onClick={() => setRetry(value => value + 1)}>Load / retry preview</button> : revision.kind === 'plan' ? <pre className="whitespace-pre-wrap break-words">{content}</pre> : <iframe title={`Design revision ${revision.ref.revision}`} sandbox={designSandbox} referrerPolicy="no-referrer" srcDoc={content} className="h-[50vh] w-full bg-white border-0" />}
    <p>Selected: {history.data?.artifact.selected?.revision ?? 'none'}. Browsing does not select.</p>
    <div className="flex flex-wrap gap-3"><button disabled={busy || !history.data} onClick={() => void act('select')}>Select this revision</button><button disabled={busy} onClick={() => void act('download')}>Prepare {revision.kind === 'plan' ? 'plan' : 'HTML'} download</button>{download && <a href={download} download={designDownloadName(revision)}>Download exact revision</a>}</div>
    <label className="block">Edit this exact revision ({revision.ref.revision})<textarea className="block w-full bg-slate-900 border border-white/20 p-2" value={brief} onChange={event => setBrief(event.target.value)} /></label>
    <button disabled={busy || !brief.trim() || new TextEncoder().encode(brief).length > 65536} onClick={() => void act('edit')}>Request delegated edit</button>
    {notice && <p role="status">{acceptedNotice ? `Edit accepted · ${designStatus(acceptedNotice.request.state)}` : notice}</p>}
    {!!edits.data?.nextBefore && <button disabled={edits.loading} onClick={() => void desktopDesigns.moreEdits(session)}>Older edit requests</button>}
    {edits.data?.edits.filter(edit => edit.base.artifact_id === revision.ref.artifact_id).map(edit => {
      const accepted = rows.find(row => row.request.source_message_id === edit.messageId && row.request.client_request_id === edit.clientRequestId && row.request.candidates.some(candidate => candidate.spec.base && designRefKey(candidate.spec.base) === designRefKey(edit.base)))
      return <section key={edit.messageId}><p role="status">Edit from revision {edit.base.revision}: {accepted ? `accepted · ${designStatus(accepted.request.state)}` : 'edit requested · awaiting parent acceptance'}</p></section>
    })}
    {rows.filter(row => row.request.candidates.some(candidate => candidate.spec.artifact_id === revision.ref.artifact_id || candidate.spec.base?.artifact_id === revision.ref.artifact_id)).map(row => <MediaTaskCard key={row.request.id} source="independent-design" archived={history.data?.artifact.archived} design={row} onDesignPreview={onSelect} />)}
    {archivedProject.data?.next_cursor && <button disabled={archivedProject.loading} onClick={() => void desktopDesigns.moreProject(projectId, 'archived')}>More archived design requests</button>}
    {project.data?.next_cursor && <button disabled={project.loading} onClick={() => void desktopDesigns.moreProject(projectId)}>More design requests</button>}
  </section>
}
