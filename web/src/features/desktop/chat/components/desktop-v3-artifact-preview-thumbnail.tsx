import { useCallback, useEffect, useState } from 'react'
import { FileText, Loader2 } from 'lucide-react'
import { cn } from '../../../../lib/cn'
import {
  fetchDesktopV3ArtifactPreviewAccess,
  preflightDesktopV3ArtifactDirectContent,
  fetchDesktopV3ArtifactTextPreview,
  type DesktopV3ArtifactCatalogEntry,
} from '../../session-v3/artifact-api'

interface DesktopV3ArtifactPreviewThumbnailProps {
  artifact: DesktopV3ArtifactCatalogEntry
  className?: string
  presentation?: 'thumbnail' | 'wide'
}

type ArtifactPreviewIntersectionListener = (intersecting: boolean) => void

const artifactPreviewIntersectionListeners = new Map<Element, ArtifactPreviewIntersectionListener>()
let artifactPreviewIntersectionObserver: IntersectionObserver | undefined

function observeArtifactPreview(element: Element, listener: ArtifactPreviewIntersectionListener): () => void {
  if (typeof IntersectionObserver === 'undefined') {
    listener(true)
    return () => undefined
  }
  artifactPreviewIntersectionObserver ??= new IntersectionObserver((entries) => {
    for (const entry of entries) artifactPreviewIntersectionListeners.get(entry.target)?.(entry.isIntersecting)
  }, { threshold: 0.05 })
  artifactPreviewIntersectionListeners.set(element, listener)
  artifactPreviewIntersectionObserver.observe(element)
  return () => {
    artifactPreviewIntersectionListeners.delete(element)
    artifactPreviewIntersectionObserver?.unobserve(element)
    if (artifactPreviewIntersectionListeners.size === 0) {
      artifactPreviewIntersectionObserver?.disconnect()
      artifactPreviewIntersectionObserver = undefined
    }
  }
}

const artifactPreviewPageVisibilityListeners = new Set<(visible: boolean) => void>()
let artifactPreviewPageVisibilityListening = false
const notifyArtifactPreviewPageVisibility = () => {
  const visible = document.visibilityState === 'visible'
  for (const listener of artifactPreviewPageVisibilityListeners) listener(visible)
}

function subscribeArtifactPreviewPageVisibility(listener: (visible: boolean) => void): () => void {
  if (typeof document === 'undefined') return () => undefined
  artifactPreviewPageVisibilityListeners.add(listener)
  listener(document.visibilityState === 'visible')
  if (!artifactPreviewPageVisibilityListening) {
    document.addEventListener('visibilitychange', notifyArtifactPreviewPageVisibility)
    artifactPreviewPageVisibilityListening = true
  }
  return () => {
    artifactPreviewPageVisibilityListeners.delete(listener)
    if (artifactPreviewPageVisibilityListening && artifactPreviewPageVisibilityListeners.size === 0) {
      document.removeEventListener('visibilitychange', notifyArtifactPreviewPageVisibility)
      artifactPreviewPageVisibilityListening = false
    }
  }
}

const artifactPreviewMotionListeners = new Set<(reduced: boolean) => void>()
let artifactPreviewMotionMedia: MediaQueryList | undefined
const notifyArtifactPreviewMotion = () => {
  const reduced = artifactPreviewMotionMedia?.matches ?? false
  for (const listener of artifactPreviewMotionListeners) listener(reduced)
}

function subscribeArtifactPreviewMotion(listener: (reduced: boolean) => void): () => void {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return () => undefined
  artifactPreviewMotionMedia ??= window.matchMedia('(prefers-reduced-motion: reduce)')
  artifactPreviewMotionListeners.add(listener)
  listener(artifactPreviewMotionMedia.matches)
  if (artifactPreviewMotionListeners.size === 1) artifactPreviewMotionMedia.addEventListener('change', notifyArtifactPreviewMotion)
  return () => {
    artifactPreviewMotionListeners.delete(listener)
    if (artifactPreviewMotionListeners.size === 0) {
      artifactPreviewMotionMedia?.removeEventListener('change', notifyArtifactPreviewMotion)
      artifactPreviewMotionMedia = undefined
    }
  }
}

export function useDesktopV3ArtifactPreviewVisibility<T extends HTMLElement = HTMLDivElement>(enabled = true) {
  const [previewElement, setPreviewElement] = useState<T | null>(null)
  const previewRef = useCallback((element: T | null) => setPreviewElement(element), [])
  const [intersecting, setIntersecting] = useState(false)
  const [pageVisible, setPageVisible] = useState(() => typeof document === 'undefined' || document.visibilityState === 'visible')
  const [reducedMotion, setReducedMotion] = useState(() => (
    typeof window !== 'undefined' && typeof window.matchMedia === 'function'
      ? window.matchMedia('(prefers-reduced-motion: reduce)').matches
      : false
  ))

  useEffect(() => {
    if (!enabled || !previewElement) {
      setIntersecting(false)
      return undefined
    }
    return observeArtifactPreview(previewElement, setIntersecting)
  }, [enabled, previewElement])

  useEffect(() => subscribeArtifactPreviewPageVisibility(setPageVisible), [])
  useEffect(() => subscribeArtifactPreviewMotion(setReducedMotion), [])

  return {
    previewRef,
    previewVisible: enabled && intersecting && pageVisible,
    previewMotionAllowed: !reducedMotion,
  }
}

export function DesktopV3ArtifactPreviewThumbnail({
  artifact,
  className,
  presentation = 'thumbnail',
}: DesktopV3ArtifactPreviewThumbnailProps) {
  const [previewURL, setPreviewURL] = useState('')
  const [previewText, setPreviewText] = useState('')
  const [loading, setLoading] = useState(false)
  const [failed, setFailed] = useState(false)
  const { previewRef, previewVisible } = useDesktopV3ArtifactPreviewVisibility()
  const interactivePreview = Boolean(artifact.animationProfile)
    || artifact.mediaType === 'text/html'
    || artifact.mediaType === 'application/pdf'
    || artifact.mediaType === 'image/gif'
    || artifact.mediaType === 'image/svg+xml'
    || artifact.mediaType.startsWith('video/')
    || artifact.kind === 'video'
  const animationCanvasStyle = artifact.animationProfile
    ? { maxWidth: Math.sqrt(artifact.animationProfile.budgets.maxCanvasPixels), maxHeight: Math.sqrt(artifact.animationProfile.budgets.maxCanvasPixels) }
    : undefined

  useEffect(() => {
    setPreviewURL('')
    setPreviewText('')
    setFailed(false)
    if (!previewVisible || artifact.status !== 'ready' || !artifact.previewable) {
      setLoading(false)
      return undefined
    }
    if (artifact.content !== undefined) {
      setPreviewText(artifact.content)
      setLoading(false)
      return undefined
    }

    const controller = new AbortController()
    const isText = artifact.mediaType === 'text/markdown' || artifact.mediaType === 'text/plain'
    const isHTML = artifact.mediaType === 'text/html'
    setLoading(true)
    const resolvePreview = isHTML
      ? fetchDesktopV3ArtifactPreviewAccess(artifact.sessionId, artifact.artifactId, controller.signal).then((access) => access.url)
      : isText
        ? fetchDesktopV3ArtifactTextPreview(artifact, controller.signal)
        : preflightDesktopV3ArtifactDirectContent(artifact, controller.signal)
    void resolvePreview
      .then((value) => {
        if (controller.signal.aborted) return
        if (isHTML || !isText) setPreviewURL(value)
        else setPreviewText(value)
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true)
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })

    return () => controller.abort()
  }, [artifact.animationProfile, artifact.artifactId, artifact.content, artifact.mediaType, artifact.previewable, artifact.sessionId, artifact.sourceRef, artifact.status, previewVisible])

  const previewActive = previewVisible
  const hasHTMLPreview = previewActive && artifact.mediaType === 'text/html' && Boolean(previewURL)
  const hasImagePreview = previewActive && artifact.mediaType.startsWith('image/') && Boolean(previewURL)
  const videoProfileCompatible = !artifact.animationProfile || artifact.animationProfile.profileId === 'final_render'
  const hasVideoPreview = previewActive && videoProfileCompatible && (artifact.mediaType.startsWith('video/') || artifact.kind === 'video') && Boolean(previewURL)
  const hasPDFPreview = previewActive && artifact.mediaType === 'application/pdf' && Boolean(previewURL)
  const hasTextPreview = previewActive && (artifact.mediaType === 'text/markdown' || artifact.mediaType === 'text/plain') && Boolean(previewText)

  const isWide = presentation === 'wide' && (artifact.mediaType.startsWith('image/') || artifact.mediaType.startsWith('video/') || artifact.kind === 'video')
  const previewAspectRatio = isWide && artifact.outputRequirements
    ? `${artifact.outputRequirements.width} / ${artifact.outputRequirements.height}`
    : undefined

  return (
    <div
      ref={previewRef}
      className={cn(
        'relative aspect-video w-full overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)]',
        isWide ? 'max-w-3xl' : 'max-w-sm',
        className,
      )}
      style={{ ...(previewAspectRatio ? { aspectRatio: previewAspectRatio } : {}), ...animationCanvasStyle }}
      data-artifact-preview-thumbnail
      data-artifact-preview-interactive={interactivePreview || undefined}
      data-artifact-preview-presentation={isWide ? 'wide' : 'thumbnail'}
      data-artifact-preview-media-type={artifact.mediaType}
      data-artifact-preview-visible={previewVisible || undefined}
      data-artifact-animation-profile={artifact.animationProfile?.profileId}
      data-artifact-animation-active={previewActive || undefined}
      data-artifact-animation-max-dpr={artifact.animationProfile?.budgets.maxDevicePixelRatio}
      data-artifact-animation-max-canvas-pixels={artifact.animationProfile?.budgets.maxCanvasPixels}
      data-artifact-animation-max-webgl={artifact.animationProfile?.budgets.maxWebGLContexts}
    >
      {loading ? (
        <div className="grid size-full place-items-center text-[var(--app-text-muted)]">
          <Loader2 className="size-5 motion-safe:animate-spin motion-reduce:animate-none" aria-label="Loading artifact preview" />
        </div>
      ) : null}
      {!loading && hasHTMLPreview ? (
        <iframe
          title={`${artifact.label} preview`}
          src={previewURL}
          sandbox="allow-scripts"
          referrerPolicy="no-referrer"
          tabIndex={-1}
          className="pointer-events-none h-[200%] w-[200%] origin-top-left scale-50 border-0 bg-white"
          data-artifact-html-preview
          onError={() => { setFailed(true); setPreviewURL('') }}
        />
      ) : null}
      {!loading && hasImagePreview ? (
        <img src={previewURL} alt="" className="size-full object-contain" data-artifact-image-preview onError={() => { setFailed(true); setPreviewURL('') }} />
      ) : null}
      {!loading && hasVideoPreview ? (
        <video
          src={previewURL}
          controls
          playsInline
          preload="metadata"
          className="size-full object-contain bg-black"
          data-artifact-video-preview
          onError={() => { setFailed(true); setPreviewURL('') }}
        />
      ) : null}
      {!loading && hasPDFPreview ? (
        <iframe
          title={`${artifact.label} preview`}
          src={previewURL}
          sandbox=""
          referrerPolicy="no-referrer"
          tabIndex={-1}
          className="pointer-events-none size-full border-0 bg-white"
          data-artifact-pdf-preview
          onError={() => { setFailed(true); setPreviewURL('') }}
        />
      ) : null}
      {!loading && hasTextPreview ? (
        <pre className="size-full overflow-hidden whitespace-pre-wrap break-words p-3 text-[10px] leading-4 text-[var(--app-text-muted)]" data-artifact-text-preview>
          {previewText}
        </pre>
      ) : null}
      {!loading && !hasHTMLPreview && !hasImagePreview && !hasVideoPreview && !hasPDFPreview && !hasTextPreview ? (
        <div className="grid size-full place-items-center text-center text-[var(--app-text-muted)]">
          <div>
            <FileText className="mx-auto size-6" aria-hidden="true" />
            <div className="mt-2 text-[10px]">{failed ? 'Preview unavailable' : artifact.mediaType || 'Artifact'}</div>
          </div>
        </div>
      ) : null}
      <div className="pointer-events-none absolute inset-x-0 bottom-0 h-10 bg-gradient-to-t from-black/20 to-transparent" aria-hidden="true" />
    </div>
  )
}
