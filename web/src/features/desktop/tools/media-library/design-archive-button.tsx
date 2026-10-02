import { useRef, useState } from 'react'
import { desktopDesigns } from '../../runtime/desktop-design-runtime'
import type { DesignRef } from '../../session-v3/design-api'

export function DesignArchiveButton({ session, reference, version, archived = false }: { session: string; reference: DesignRef; version: number; archived?: boolean }) {
  const busy = useRef(false)
  const intent = useRef<{ identity: string; key: string } | undefined>(undefined)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const run = async () => {
    if (busy.current) return
    busy.current = true; setPending(true); setError('')
    const identity = JSON.stringify([session, reference, version, archived])
    if (intent.current?.identity !== identity) intent.current = { identity, key: crypto.randomUUID() }
    try { await desktopDesigns.archive(session, reference, version, !archived, intent.current.key); intent.current = undefined }
    catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)) }
    finally { busy.current = false; setPending(false) }
  }
  return <span className="text-xs"><button type="button" disabled={pending} aria-busy={pending} onClick={event => { event.stopPropagation(); void run() }}>{pending ? (archived ? 'Restoring…' : 'Archiving…') : error ? 'Retry' : archived ? 'Restore' : 'Archive'}</button>{error && <span role="alert">{error} <button onClick={() => { desktopDesigns.invalidate(session); desktopDesigns.invalidateProject() }}>Refresh version</button></span>}</span>
}
