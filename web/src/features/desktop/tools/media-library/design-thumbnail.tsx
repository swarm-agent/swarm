import { useEffect, useState } from 'react'
import { designSandbox, fetchDesignView, type DesignRevision } from '../../session-v3/design-api'

/** Preview wrappers contain stored PNG only; never render authored design HTML. */
export function DesignThumbnail({ session, revision }: { session: string; revision: DesignRevision }) {
  const [content, setContent] = useState<string>()
  useEffect(() => {
    const controller = new AbortController()
    setContent(undefined)
    if (revision.kind !== 'plan') {
      void fetchDesignView(session, revision, controller.signal).then(
        value => { if (!controller.signal.aborted) setContent(value) },
        () => { /* A missing thumbnail must not disable the exact ready output. */ },
      )
    }
    return () => controller.abort()
  }, [session, revision])
  return <span className="block w-full h-32 overflow-hidden" aria-hidden="true">
    {content !== undefined ? <iframe title={`Design thumbnail revision ${revision.ref.revision}`} sandbox={designSandbox} referrerPolicy="no-referrer" srcDoc={content} tabIndex={-1} className="pointer-events-none w-full h-full border-0 bg-white" /> : <span className="flex h-full items-center justify-center text-slate-300">{revision.kind === 'plan' ? 'Design plan' : 'Ready design'}</span>}
  </span>
}
