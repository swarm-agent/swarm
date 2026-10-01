import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { createPortal } from 'react-dom'
import { desktopDesigns } from '../../runtime/desktop-design-runtime'
import { DesignViewRequest } from '../../state/design-view-request'
import type { DesignResource } from '../../state/desktop-design-state'
import { designDownloadName, designEditBody, designRefKey, designSandbox, designSelectionBody, fetchDesignView, postDesign, type DesignArtifact, type DesignRevision } from '../../session-v3/design-api'

function useResource<T>(resource: DesignResource<T>) {
  const snapshot = useSyncExternalStore(resource.subscribe, resource.getSnapshot, resource.getSnapshot)
  useEffect(() => { void resource.refresh() }, [resource])
  return snapshot
}
const button = 'rounded border border-white/20 px-3 py-1 text-sm disabled:opacity-40'

/** Independent session gallery, intentionally not an Artifact V3 adapter. */
export function DesktopDesignGallery({ sessionId }: { sessionId: string }) {
  const catalog = useMemo(() => desktopDesigns.catalog(sessionId), [sessionId])
  const { data, loading, error } = useResource(catalog)
  const [open, setOpen] = useState(false)
  const [artifact, setArtifact] = useState('')
  const dialog = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const previous = document.activeElement
    return () => { if (previous instanceof HTMLElement) previous.focus() }
  }, [open])
  return <>
    <button className={`${button} m-2 self-start`} onClick={() => setOpen(true)}>
      Delegated designs ({data?.requests.length ?? 0}){loading ? ' · Refreshing' : ''}{error ? ' · Unavailable' : ''}
    </button>
    {open && createPortal(<div ref={dialog} role="dialog" aria-modal="true" aria-label="Delegated designs" className="fixed inset-4 z-[100] flex min-h-0 flex-col overflow-hidden rounded-xl border border-white/20 bg-neutral-950 p-4 text-white" onKeyDown={event => {
      if (event.key === 'Escape') setOpen(false)
      if (event.key === 'Tab') {
        const items = dialog.current?.querySelectorAll<HTMLElement>('button:not(:disabled), a[href], textarea:not(:disabled), iframe')
        if (!items?.length) return
        const first = items[0]; const last = items[items.length - 1]
        if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
      }
    }}>
      <header className="flex flex-wrap items-center justify-between gap-2"><h2>Delegated designs</h2><div className="flex gap-2"><button className={button} onClick={() => { void catalog.refresh(); desktopDesigns.invalidate(sessionId) }}>Refresh</button><button autoFocus className={button} onClick={() => setOpen(false)}>Close</button></div></header>
      {error && <p role="alert">{error} <button className={button} onClick={() => void catalog.refresh()}>Retry</button></p>}
      <div className="mt-3 grid min-h-0 flex-1 gap-4 overflow-auto md:grid-cols-[minmax(180px,280px)_minmax(0,1fr)]">
        <nav aria-label="Design requests and variants" className="space-y-3 overflow-auto">
          {!data?.requests.length && !loading && !error && <p>No delegated requests yet.</p>}
          {data?.requests.map(request => <section key={request.id} className="rounded border border-white/20 p-2">
            <h3 className="break-all text-sm">Request {request.id}</h3><p className="text-sm">{request.state}</p>
            {request.candidates.map((candidate, index) => <div key={`${candidate.spec.artifact_id}:${index}`} className="mt-2 border-t border-white/10 pt-2">
              <button className={button} aria-pressed={artifact === candidate.spec.artifact_id} onClick={() => setArtifact(candidate.spec.artifact_id)}>Variant {index + 1} · {candidate.spec.kind}</button>
              <p role="status">{candidate.state}</p>
              {candidate.failure_reason && <p role="alert" className="break-words">{candidate.failure_reason}</p>}
              {candidate.router_alert && <p>{candidate.router_alert}</p>}
              {candidate.attempts?.map(attempt => <p key={attempt.number} className="text-xs">Attempt {attempt.number}: {attempt.state}{attempt.reason_code ? ` · ${attempt.reason_code}` : ''}</p>)}
            </div>)}
          </section>)}
          {data?.next_cursor && <button className={button} disabled={loading} onClick={() => void desktopDesigns.moreRequests(sessionId)}>More requests</button>}
        </nav>
        {artifact ? <DesignHistoryPanel key={`${sessionId}:${artifact}`} session={sessionId} artifactId={artifact} /> : <p>Choose a variant to view its immutable history. Preview navigation does not change the selected revision.</p>}
      </div>
    </div>, document.body)}
  </>
}

function DesignHistoryPanel({ session, artifactId }: { session: string; artifactId: string }) {
  const resource = useMemo(() => desktopDesigns.history(session, artifactId), [session, artifactId])
  const { data, error, loading } = useResource(resource)
  const [viewed, setViewed] = useState<DesignRevision>()
  return <section className="min-w-0 space-y-3 overflow-auto">
    <h3 className="break-all">{artifactId}</h3>
    {loading && <p role="status">Refreshing history…</p>}
    {error && <p role="alert">{error} <button className={button} onClick={() => void resource.refresh()}>Retry history</button></p>}
    <p>Selected: {data?.artifact.selected ? `revision ${data.artifact.selected.revision}` : 'none'}. Viewing is separate from selection.</p>
    <div className="flex flex-wrap gap-2" aria-label="Revision history">
      {data?.revisions.map(revision => <button className={button} key={designRefKey(revision.ref)} aria-pressed={viewed && designRefKey(viewed.ref) === designRefKey(revision.ref)} onClick={() => setViewed(revision)}>Revision {revision.ref.revision}{revision.base ? ` ← ${revision.base.revision}` : ' · original'}</button>)}
      {data && data.revisions.length < data.artifact.revision_count && <button className={button} disabled={loading} onClick={() => void desktopDesigns.moreHistory(session, artifactId)}>More history</button>}
    </div>
    {!viewed && <p>Choose an exact revision to preview or edit.</p>}
    {viewed && data && <DesignRevisionPanel key={designRefKey(viewed.ref)} session={session} revision={viewed} artifact={data.artifact} />}
  </section>
}

function DesignRevisionPanel({ session, revision, artifact }: { session: string; revision: DesignRevision; artifact: DesignArtifact }) {
  const [content, setContent] = useState<string>()
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  const [brief, setBrief] = useState('')
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState('')
  const [download, setDownload] = useState('')
  const editIntent = useRef<{ brief: string; key: string } | undefined>(undefined)
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  useEffect(() => {
    const request = new DesignViewRequest()
    setContent(undefined); setError('')
    void request.load(signal => fetchDesignView(session, revision, signal), setContent, cause => setError(String(cause)))
    return () => request.cancel()
  }, [session, revision, retry])
  useEffect(() => () => { if (download) URL.revokeObjectURL(download) }, [download])
  async function act(action: 'edit' | 'select' | 'download') {
    setBusy(true); setError(''); setNotice('')
    try {
      let body: object
      if (action === 'edit') {
        if (!editIntent.current || editIntent.current.brief !== brief) editIntent.current = { brief, key: crypto.randomUUID() }
        body = designEditBody(revision.ref, brief, editIntent.current.key)
      } else if (action === 'select') body = designSelectionBody(revision.ref, artifact, crypto.randomUUID())
      else body = { action, ref: revision.ref }
      const response = await postDesign(session, revision.ref, body)
      if (action === 'download') {
        // Attachment-only blob: never execute authored bytes in this document or an iframe.
        const bytes = await response.arrayBuffer()
        if (mounted.current) setDownload(URL.createObjectURL(new Blob([bytes], { type: 'application/octet-stream' })))
      } else {
        desktopDesigns.invalidate(session)
        if (mounted.current) {
          setNotice(action === 'edit' ? `Edit requested from revision ${revision.ref.revision}; awaiting parent acceptance.` : `Selected revision ${revision.ref.revision}.`)
          if (action === 'edit') { editIntent.current = undefined; setBrief('') }
        }
      }
    } catch (cause) {
      if (mounted.current) setError(String(cause))
      // A CAS conflict is visible; refresh authority, never retry against latest automatically.
      if (action === 'select') desktopDesigns.invalidate(session)
    } finally { if (mounted.current) setBusy(false) }
  }
  return <div className="space-y-3">
    <h4>Viewing revision {revision.ref.revision}</h4>
    <p className="break-all text-xs">SHA256: {revision.ref.sha256}</p>
    <p className="break-all text-sm">{revision.base ? `Parent: ${revision.base.artifact_id}, revision ${revision.base.revision}, SHA256 ${revision.base.sha256}` : 'Original revision'}{revision.plan_source ? ` · Plan: ${revision.plan_source.artifact_id}, revision ${revision.plan_source.revision}` : ''}</p>
    {error && <p role="alert" className="break-words">{error}</p>}
    {content === undefined ? <button className={button} onClick={() => setRetry(value => value + 1)}>Load / retry preview</button> : revision.kind === 'plan' ? <pre className="max-h-[50vh] overflow-auto whitespace-pre-wrap break-words">{content}</pre> : <><p className="text-xs">Stored sandbox capture (not interactive HTML)</p><iframe title={`Design revision ${revision.ref.revision}`} sandbox={designSandbox} referrerPolicy="no-referrer" srcDoc={content} className="h-[55vh] w-full border-0 bg-white" /></>}
    <div className="flex flex-wrap gap-2"><button className={button} disabled={busy} onClick={() => void act('select')}>Select this revision</button><button className={button} disabled={busy} onClick={() => void act('download')}>Prepare {revision.kind === 'plan' ? 'plan' : 'HTML'} download</button>{download && <a className={button} href={download} download={designDownloadName(revision)}>Download exact revision</a>}</div>
    <label className="block">Edit this exact revision ({revision.ref.revision})<textarea className="mt-1 block w-full rounded border border-white/20 bg-neutral-900 p-2" value={brief} disabled={busy} onChange={event => setBrief(event.target.value)} /></label>
    <button className={button} disabled={busy || !brief.trim() || new TextEncoder().encode(brief).length > 65536} onClick={() => void act('edit')}>Request delegated edit</button>
    {notice && <p role="status">{notice}</p>}
  </div>
}
