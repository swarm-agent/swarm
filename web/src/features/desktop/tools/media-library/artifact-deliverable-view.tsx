import { useEffect, useState } from 'react'
import { fetchDesktopV3ArtifactTextPreview } from '../../session-v3/artifact-api'
import { DeliverablePreview } from './deliverable-preview'
import type { MediaLibraryItem } from './types'

export function ArtifactDeliverableView({ item }: { item: MediaLibraryItem }) {
  const [content, setContent] = useState<string>()
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  const markdown = item.kind === 'document'
  const supported = markdown || /^(text\/html|image\/svg\+xml)(;|$)/i.test(item.mediaType) || /\.(html?|svg)$/i.test(item.filename) || item.artifact?.kind === 'html'
  useEffect(() => {
    const controller = new AbortController()
    setContent(undefined); setError('')
    if (!supported || !item.artifact) { setError('This deliverable type is not supported for inline preview. Download the original instead.'); return }
    void fetchDesktopV3ArtifactTextPreview(item.artifact, controller.signal).then(value => {
      if (!controller.signal.aborted) setContent(value)
    }, cause => { if (!controller.signal.aborted) setError(String(cause)) })
    return () => controller.abort()
  }, [item, supported, retry])
  return <section className="w-full min-w-0 self-start">
    {error ? <><p role="alert">{error}</p><button onClick={() => setRetry(value => value + 1)}>Retry</button></> : content === undefined ? <p role="status">Loading deliverable…</p> : <DeliverablePreview content={content} markdown={markdown} title={item.title} />}
  </section>
}
