import { useEffect, useState } from 'react'
import { FileText, GalleryHorizontal, Loader2, Maximize2, TriangleAlert } from 'lucide-react'

import { cn } from '../../../../lib/cn'
import {
  buildDesktopV3ArtifactSandboxDocument,
  desktopV3ArtifactCatalogEntryKey,
  fetchDesktopV3Artifact,
  fetchDesktopV3ArtifactPreviewToken,
  type DesktopV3ArtifactCatalogEntry,
} from '../../session-v3/artifact-api'
import type { DesktopSidebarDisplayMode } from './desktop-sidebar-display'

export type DesktopV3SessionSidebarView = 'plan' | 'artifacts'

export function desktopV3ArtifactsForSession(
  artifacts: readonly DesktopV3ArtifactCatalogEntry[],
  sessionId: string,
): DesktopV3ArtifactCatalogEntry[] {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) return []
  return artifacts.filter((artifact) =>
    artifact.sessionId === normalizedSessionId || artifact.lineage?.parentSessionId === normalizedSessionId
  )
}

export function desktopV3NextSessionSidebarView(input: {
  current: DesktopV3SessionSidebarView
  previousArtifactCount: number
  artifactCount: number
  hasPlan: boolean
}): DesktopV3SessionSidebarView {
  if (input.artifactCount === 0) return 'plan'
  if (input.previousArtifactCount === 0 && !input.hasPlan) return 'artifacts'
  return input.current
}

export interface DesktopV3ArtifactSidebarProps {
  artifacts: DesktopV3ArtifactCatalogEntry[]
  displayMode?: DesktopSidebarDisplayMode
  loading?: boolean
  error?: string
  embedded?: boolean
  onOpenArtifact: (artifactKey: string) => void
}

function DesktopV3ArtifactThumbnail({ artifact }: { artifact: DesktopV3ArtifactCatalogEntry }) {
  const [previewURL, setPreviewURL] = useState('')
  const [previewHTML, setPreviewHTML] = useState('')
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    setPreviewURL('')
    setPreviewHTML('')
    setFailed(false)
    if (!artifact.previewable || artifact.status !== 'ready') return undefined

    const controller = new AbortController()
    let objectURL = ''
    void fetchDesktopV3Artifact(artifact.sessionId, artifact.artifactId, controller.signal)
      .then(async (blob) => {
        if (controller.signal.aborted) return
        if (artifact.mediaType === 'text/html') {
          const [source, previewToken] = await Promise.all([
            blob.text(),
            fetchDesktopV3ArtifactPreviewToken(artifact.sessionId, artifact.artifactId, controller.signal),
          ])
          if (!controller.signal.aborted) {
            setPreviewHTML(buildDesktopV3ArtifactSandboxDocument(source, artifact.sessionId, artifact.artifactId, previewToken))
          }
          return
        }
        if (artifact.mediaType.startsWith('image/') || artifact.mediaType === 'application/pdf') {
          objectURL = URL.createObjectURL(blob)
          setPreviewURL(objectURL)
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true)
      })

    return () => {
      controller.abort()
      if (objectURL) URL.revokeObjectURL(objectURL)
    }
  }, [artifact.artifactId, artifact.mediaType, artifact.previewable, artifact.sessionId, artifact.status])

  if (artifact.status === 'staging') return <Loader2 className="size-5 animate-spin text-[var(--app-primary)]" aria-label="Generating artifact" />
  if (artifact.status === 'failed' || artifact.status === 'unavailable' || failed) return <TriangleAlert className="size-5 text-[var(--app-danger)]" aria-label="Artifact unavailable" />
  if (artifact.mediaType.startsWith('image/') && previewURL) {
    return <img src={previewURL} alt="" className="size-full object-cover" />
  }
  if (artifact.mediaType === 'text/html' && previewHTML) {
    return <iframe title={`${artifact.label} thumbnail`} srcDoc={previewHTML} sandbox="allow-scripts" referrerPolicy="no-referrer" tabIndex={-1} className="pointer-events-none absolute left-0 top-0 size-[400%] origin-top-left scale-25 border-0 bg-white" />
  }
  if (artifact.mediaType === 'application/pdf' && previewURL) {
    return <iframe title={`${artifact.label} thumbnail`} src={previewURL} sandbox="" referrerPolicy="no-referrer" tabIndex={-1} className="pointer-events-none size-full border-0 bg-white" />
  }
  return <FileText className="size-5 text-[var(--app-text-muted)]" aria-hidden="true" />
}

export function DesktopV3ArtifactSidebar({
  artifacts,
  displayMode = 'full',
  loading = false,
  error = '',
  embedded = false,
  onOpenArtifact,
}: DesktopV3ArtifactSidebarProps) {
  const thin = displayMode === 'thin'
  const compact = displayMode === 'compact'

  return (
    <aside
      aria-label="Session artifact sidebar"
      data-testid="desktop-session-artifact-sidebar"
      data-display-mode={displayMode}
      className={cn(
        'min-h-0 min-w-0 bg-[var(--app-bg-alt)] text-[var(--app-text)]',
        embedded
          ? 'w-full'
          : 'hidden h-full flex-1 flex-col overflow-hidden border-l border-[var(--app-border)]/60 min-[1300px]:flex',
        !embedded && (thin ? 'w-[56px] max-w-[56px] px-2 py-3' : compact ? 'w-[292px] max-w-[292px] p-3' : 'w-[372px] max-w-[372px] p-4'),
      )}
    >
      <header className={cn('flex shrink-0 items-center', thin ? 'justify-center pb-2' : 'justify-between gap-3 pb-3')}>
        <div className={cn('flex min-w-0 items-center gap-2', thin && 'justify-center')}>
          <GalleryHorizontal className="size-4 shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
          {!thin ? <div className="min-w-0"><h2 className="truncate text-sm font-semibold">Artifacts</h2><p className="text-[10px] text-[var(--app-text-subtle)]">This session · {artifacts.length}</p></div> : null}
        </div>
      </header>

      {loading && artifacts.length === 0 ? <div className="grid flex-1 place-items-center"><Loader2 className="size-5 animate-spin text-[var(--app-primary)]" aria-label="Loading session artifacts" /></div> : null}
      {error && artifacts.length === 0 ? <p className={cn('text-xs text-[var(--app-danger)]', thin ? 'sr-only' : 'rounded-lg border border-[var(--app-danger)]/40 bg-[var(--app-danger-bg)] p-3')}>{error}</p> : null}
      {!loading && !error && artifacts.length === 0 ? <p className={cn('text-xs text-[var(--app-text-muted)]', thin ? 'sr-only' : 'p-3 text-center')}>Artifacts created in this session will appear here.</p> : null}

      {artifacts.length > 0 ? (
        <div
          className={cn(
            'min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]',
            thin ? 'grid auto-rows-max gap-2' : embedded ? 'flex gap-2 overflow-x-auto overflow-y-hidden pb-1' : 'grid auto-rows-max gap-3',
          )}
          aria-label="Session artifact thumbnails"
          data-artifact-thumbnail-rail
        >
          {artifacts.map((artifact) => (
            <button
              key={`${artifact.sessionId}:${artifact.artifactId}`}
              type="button"
              className={cn(
                'group min-w-0 overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] text-left transition hover:border-[var(--app-border-active)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]',
                thin ? 'size-10 rounded-lg' : embedded ? 'w-44 shrink-0' : 'w-full',
              )}
              onClick={() => onOpenArtifact(desktopV3ArtifactCatalogEntryKey(artifact))}
              aria-label={`Open ${artifact.label} in full artifact view`}
            >
              <span className={cn('relative grid overflow-hidden bg-[var(--app-bg)]', thin ? 'size-full place-items-center' : 'aspect-[16/9] place-items-center')}>
                <DesktopV3ArtifactThumbnail artifact={artifact} />
                {!thin ? <span className="absolute right-2 top-2 grid size-7 place-items-center rounded-md bg-black/60 text-white opacity-0 transition group-hover:opacity-100"><Maximize2 size={13} aria-hidden="true" /></span> : null}
              </span>
              {!thin ? <span className="block min-w-0 px-3 py-2"><span className="block truncate text-xs font-semibold">{artifact.label}</span><span className="mt-0.5 block truncate text-[10px] text-[var(--app-text-subtle)]">{artifact.status === 'staging' ? 'Generating' : artifact.kind || artifact.mediaType}</span></span> : null}
            </button>
          ))}
        </div>
      ) : null}
    </aside>
  )
}
