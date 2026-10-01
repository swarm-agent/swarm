import { useMemo } from 'react'
import { MarkdownRenderer } from '../../chat/markdown/render'

// The script-free outer document owns the inner browsing context's navigation
// policy. A sandbox alone does NOT prevent a document navigating its own frame.
// about:srcdoc inherits CSP; frame-src 'none' rejects subsequent URL navigations.
export const deliverableCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; media-src data:; connect-src 'none'; frame-src 'none'; child-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"
const attribute = (value: string) => value.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
export function liveDeliverableDocument(source: string): string {
  const policy = `<meta http-equiv="Content-Security-Policy" content="${attribute(deliverableCSP)}">`
  const inner = `<!doctype html><html><head>${policy}<meta name="referrer" content="no-referrer"><style>@media(prefers-reduced-motion:reduce){*,*::before,*::after{animation-duration:0.001ms!important;animation-iteration-count:1!important;transition-duration:0.001ms!important}}</style></head><body>${source}</body></html>`
  return `<!doctype html><html><head>${policy}<meta name="referrer" content="no-referrer"><style>html,body,iframe{margin:0;width:100%;height:100%;border:0;display:block;overflow:hidden}</style></head><body><iframe title="Live deliverable" sandbox="allow-scripts" referrerpolicy="no-referrer" srcdoc="${attribute(inner)}"></iframe></body></html>`
}

export function DeliverablePreview({ content, markdown = false, title }: { content: string; markdown?: boolean; title: string }) {
  const document = useMemo(() => markdown ? '' : liveDeliverableDocument(content), [content, markdown])
  return <div className="w-full min-w-0">
    {markdown ? <article className="break-words p-4"><MarkdownRenderer content={content} /></article> : <>
      <iframe title={title} sandbox="allow-scripts" referrerPolicy="no-referrer" srcDoc={document} className="h-[65vh] min-h-64 w-full border-0 bg-white" />
      <p className="text-xs text-white/60">Live isolated preview · Network, external dependencies and navigation are blocked. Self-contained HTML/SVG only. Motion follows the document and your reduced-motion preference.</p>
    </>}
    <details className="mt-2"><summary>Source</summary><pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words text-xs">{content}</pre></details>
  </div>
}
