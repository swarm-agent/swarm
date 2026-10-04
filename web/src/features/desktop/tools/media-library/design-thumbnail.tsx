import { useEffect, useState } from 'react'
import { VisiblePreview } from '../../../../components/ui/visible-preview'
import { backgroundRead } from '../../../../app/background-read'
import { withRequestDeadline, STARTUP_REQUEST_TIMEOUT_MS } from '../../../../app/request-lifecycle'
import { designRefKey, designSandbox, fetchDesignView, type DesignRevision } from '../../session-v3/design-api'

/** Preview wrappers contain stored PNG only; never render authored design HTML. */
export function DesignThumbnail({ session, revision }: { session: string; revision: DesignRevision }) {
  // fetchDesignView uses only session, ref and kind, not attempt/progress metadata.
  const identity = JSON.stringify([session, designRefKey(revision.ref), revision.kind])
  // A different identity starts empty immediately, never displaying the old preview.
  return <VisiblePreview key={identity} label="Ready design"><DesignThumbnailPreview session={session} revision={revision} /></VisiblePreview>
}

function DesignThumbnailPreview({ session, revision }: { session: string; revision: DesignRevision }) {
  const [content, setContent] = useState<string>()
  useEffect(() => {
    const controller = new AbortController()
    if (revision.kind !== 'plan') {
      void backgroundRead(() => withRequestDeadline(signal => fetchDesignView(session, revision, signal), STARTUP_REQUEST_TIMEOUT_MS, controller.signal), controller.signal).then(
        value => { if (!controller.signal.aborted) setContent(value) },
        () => { /* A missing thumbnail must not disable the exact ready output. */ },
      )
    }
    return () => controller.abort()
  }, [session, revision.ref.artifact_id, revision.ref.revision, revision.ref.sha256, revision.kind])
  return <span className="block w-full h-32 overflow-hidden" aria-hidden="true">
    {content !== undefined ? <iframe title={`Design thumbnail revision ${revision.ref.revision}`} sandbox={designSandbox} referrerPolicy="no-referrer" srcDoc={content} tabIndex={-1} className="pointer-events-none w-full h-full border-0 bg-white" /> : <span className="flex h-full items-center justify-center text-slate-300">{revision.kind === 'plan' ? 'Design plan' : 'Ready design'}</span>}
  </span>
}
