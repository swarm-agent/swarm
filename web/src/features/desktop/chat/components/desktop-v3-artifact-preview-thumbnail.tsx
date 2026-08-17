import { useEffect, useRef, useState } from 'react'
import { FileText, Loader2 } from 'lucide-react'
import { cn } from '../../../../lib/cn'
import { desktopV3ArtifactLocalRuntimeAssets } from '../../session-v3/artifact-animation-runtime-assets'
import {
  buildDesktopV3ArtifactSandboxDocument,
  fetchDesktopV3Artifact,
  fetchDesktopV3ArtifactDownload,
  fetchDesktopV3ArtifactPreviewToken,
  type DesktopV3ArtifactCatalogEntry,
} from '../../session-v3/artifact-api'

interface DesktopV3ArtifactPreviewThumbnailProps {
  artifact: DesktopV3ArtifactCatalogEntry
  className?: string
  presentation?: 'thumbnail' | 'wide'
}

export function useDesktopV3ArtifactPreviewVisibility<T extends HTMLElement = HTMLDivElement>(enabled = true) {
  const previewRef = useRef<T>(null)
  const [intersecting, setIntersecting] = useState(false)
  const [pageVisible, setPageVisible] = useState(() => typeof document === 'undefined' || document.visibilityState === 'visible')
  const [reducedMotion, setReducedMotion] = useState(() => (
    typeof window !== 'undefined' && typeof window.matchMedia === 'function'
      ? window.matchMedia('(prefers-reduced-motion: reduce)').matches
      : false
  ))

  useEffect(() => {
    const preview = previewRef.current
    if (!enabled || !preview) {
      setIntersecting(false)
      return undefined
    }
    if (typeof IntersectionObserver === 'undefined') {
      setIntersecting(true)
      return undefined
    }
    const observer = new IntersectionObserver((entries) => {
      setIntersecting(entries.some((entry) => entry.isIntersecting))
    }, { threshold: 0.05 })
    observer.observe(preview)
    return () => observer.disconnect()
  }, [enabled])

  useEffect(() => {
    if (typeof document === 'undefined') return undefined
    const updateVisibility = () => setPageVisible(document.visibilityState === 'visible')
    updateVisibility()
    document.addEventListener('visibilitychange', updateVisibility)
    return () => document.removeEventListener('visibilitychange', updateVisibility)
  }, [])

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return undefined
    const media = window.matchMedia('(prefers-reduced-motion: reduce)')
    const updateMotionPreference = () => setReducedMotion(media.matches)
    updateMotionPreference()
    media.addEventListener('change', updateMotionPreference)
    return () => media.removeEventListener('change', updateMotionPreference)
  }, [])

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
  const { previewRef, previewVisible, previewMotionAllowed } = useDesktopV3ArtifactPreviewVisibility()
  const animationExecutionAllowed = !artifact.animationProfile
    || artifact.animationProfile.profileId === 'final_render'
    || previewMotionAllowed
  const animationCanvasStyle = artifact.animationProfile
    ? { maxWidth: Math.sqrt(artifact.animationProfile.budgets.maxCanvasPixels), maxHeight: Math.sqrt(artifact.animationProfile.budgets.maxCanvasPixels) }
    : undefined

  useEffect(() => {
    setPreviewURL('')
    setPreviewText('')
    setFailed(false)
    if (!previewVisible || !animationExecutionAllowed || artifact.status !== 'ready' || !artifact.previewable) {
      setLoading(false)
      return undefined
    }
    if (artifact.content !== undefined) {
      setPreviewText(artifact.content)
      setLoading(false)
      return undefined
    }

    const controller = new AbortController()
    let objectURL = ''
    setLoading(true)
    const fetchPreview = artifact.sourceRef
      ? fetchDesktopV3ArtifactDownload(artifact, controller.signal)
      : fetchDesktopV3Artifact(artifact.sessionId, artifact.artifactId, controller.signal)
    void fetchPreview
      .then(async (blob) => {
        if (controller.signal.aborted) return
        if (artifact.mediaType === 'text/html') {
          const [source, previewToken] = await Promise.all([
            blob.text(),
            fetchDesktopV3ArtifactPreviewToken(artifact.sessionId, artifact.artifactId, controller.signal),
          ])
          if (!controller.signal.aborted) {
            setPreviewText(buildDesktopV3ArtifactSandboxDocument(
              source,
              artifact.sessionId,
              artifact.artifactId,
              previewToken,
              desktopV3ArtifactLocalRuntimeAssets(artifact.animationProfile),
            ))
          }
          return
        }
        if (artifact.mediaType === 'text/markdown' || artifact.mediaType === 'text/plain') {
          const text = await blob.text()
          if (!controller.signal.aborted) setPreviewText(text)
          return
        }
        objectURL = URL.createObjectURL(blob)
        if (!controller.signal.aborted) setPreviewURL(objectURL)
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true)
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })

    return () => {
      controller.abort()
      if (objectURL) URL.revokeObjectURL(objectURL)
    }
  }, [animationExecutionAllowed, artifact.animationProfile, artifact.artifactId, artifact.content, artifact.mediaType, artifact.previewable, artifact.sessionId, artifact.sourceRef, artifact.status, previewVisible])

  const previewActive = previewVisible && animationExecutionAllowed
  const hasHTMLPreview = previewActive && artifact.mediaType === 'text/html' && Boolean(previewText)
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
          srcDoc={previewText}
          sandbox="allow-scripts"
          referrerPolicy="no-referrer"
          tabIndex={-1}
          className="pointer-events-none h-[200%] w-[200%] origin-top-left scale-50 border-0 bg-white"
          data-artifact-html-preview
        />
      ) : null}
      {!loading && hasImagePreview ? (
        <img src={previewURL} alt="" className="size-full object-contain" data-artifact-image-preview />
      ) : null}
      {!loading && hasVideoPreview ? (
        <video
          src={previewURL}
          controls
          playsInline
          preload="metadata"
          className="size-full object-contain bg-black"
          data-artifact-video-preview
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
