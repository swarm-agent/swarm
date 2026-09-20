import { useCallback, useEffect, useState } from 'react'
import {
  ChevronLeft,
  ChevronRight,
  Copy,
  Check,
  Download,
  ExternalLink,
  Film,
  Image as ImageIcon,
  Info,
  Music,
  RotateCcw,
  Sparkles,
  X,
  ZoomIn,
  ZoomOut,
} from 'lucide-react'
import type { MediaLibraryItem } from './types'

interface MediaViewerModalProps {
  item: MediaLibraryItem | null
  items: readonly MediaLibraryItem[]
  onClose: () => void
  onSelect: (item: MediaLibraryItem) => void
  onOpenSession?: (sessionId: string) => void
}

export function MediaViewerModal({
  item,
  items,
  onClose,
  onSelect,
  onOpenSession,
}: MediaViewerModalProps) {
  const [zoomLevel, setZoomLevel] = useState(1)
  const [showInfo, setShowInfo] = useState(true)
  const [copied, setCopied] = useState(false)

  // Reset viewer state when item changes
  useEffect(() => {
    setZoomLevel(1)
    setCopied(false)
  }, [item?.id])

  // Current index in items
  const currentIndex = item ? items.findIndex((i) => i.id === item.id) : -1
  const hasPrev = currentIndex > 0
  const hasNext = currentIndex >= 0 && currentIndex < items.length - 1

  const handlePrev = useCallback(() => {
    if (hasPrev) {
      onSelect(items[currentIndex - 1])
    }
  }, [currentIndex, hasPrev, items, onSelect])

  const handleNext = useCallback(() => {
    if (hasNext) {
      onSelect(items[currentIndex + 1])
    }
  }, [currentIndex, hasNext, items, onSelect])

  // Keyboard navigation
  useEffect(() => {
    if (!item) return

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        onClose()
      } else if (event.key === 'ArrowLeft') {
        handlePrev()
      } else if (event.key === 'ArrowRight') {
        handleNext()
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [handleNext, handlePrev, item, onClose])

  if (!item) return null

  const handleCopyLink = () => {
    void navigator.clipboard.writeText(window.location.origin + item.directUrl)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  const handleDownload = () => {
    const link = document.createElement('a')
    link.href = item.directUrl
    link.download = item.filename
    document.body.appendChild(link)
    link.click()
    document.body.removeChild(link)
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label={`Media viewer: ${item.title}`}
      className="fixed inset-0 z-50 flex flex-col bg-black/90 backdrop-blur-md text-[var(--app-text)] animate-in fade-in duration-150"
    >
      {/* Top App Bar */}
      <header className="flex h-14 shrink-0 items-center justify-between border-b border-white/10 px-4">
        <div className="flex items-center gap-3 min-w-0">
          <div className="flex size-8 items-center justify-center rounded-lg bg-white/10 text-white shrink-0">
            {item.kind === 'image' && <ImageIcon size={16} />}
            {item.kind === 'video' && <Film size={16} />}
            {item.kind === 'audio' && <Music size={16} />}
            {item.kind === 'animation' && <Sparkles size={16} />}
          </div>
          <div className="min-w-0">
            <h2 className="truncate text-sm font-semibold text-white">{item.title}</h2>
            <p className="truncate text-xs text-white/60">
              {item.filename} · {item.formattedDate} at {item.formattedTime}
              {items.length > 1 ? ` · (${currentIndex + 1} of ${items.length})` : ''}
            </p>
          </div>
        </div>

        {/* Action controls */}
        <div className="flex items-center gap-2">
          {/* Zoom controls for image */}
          {item.kind === 'image' && (
            <div className="hidden sm:flex items-center gap-1 rounded-lg bg-white/10 px-1 py-0.5 text-xs text-white">
              <button
                type="button"
                onClick={() => setZoomLevel((z) => Math.max(0.5, z - 0.25))}
                title="Zoom out"
                aria-label="Zoom out"
                className="p-1.5 hover:bg-white/10 rounded"
              >
                <ZoomOut size={14} />
              </button>
              <span className="px-1 font-mono text-[11px]">{Math.round(zoomLevel * 100)}%</span>
              <button
                type="button"
                onClick={() => setZoomLevel((z) => Math.min(3, z + 0.25))}
                title="Zoom in"
                aria-label="Zoom in"
                className="p-1.5 hover:bg-white/10 rounded"
              >
                <ZoomIn size={14} />
              </button>
              <button
                type="button"
                onClick={() => setZoomLevel(1)}
                title="Reset zoom"
                aria-label="Reset zoom"
                className="p-1.5 hover:bg-white/10 rounded"
              >
                <RotateCcw size={14} />
              </button>
            </div>
          )}

          <button
            type="button"
            onClick={handleCopyLink}
            title={copied ? 'URL Copied!' : 'Copy direct URL'}
            aria-label="Copy direct URL"
            className="flex items-center gap-1.5 rounded-lg bg-white/10 px-3 py-1.5 text-xs font-medium text-white transition hover:bg-white/20"
          >
            {copied ? <Check size={14} className="text-emerald-400" /> : <Copy size={14} />}
            <span className="hidden sm:inline">{copied ? 'Copied' : 'Copy link'}</span>
          </button>

          <button
            type="button"
            onClick={handleDownload}
            title="Download media file"
            aria-label="Download media file"
            className="flex items-center gap-1.5 rounded-lg bg-white/10 px-3 py-1.5 text-xs font-medium text-white transition hover:bg-white/20"
          >
            <Download size={14} />
            <span className="hidden sm:inline">Download</span>
          </button>

          {onOpenSession && item.sessionId && (
            <button
              type="button"
              onClick={() => onOpenSession(item.sessionId)}
              title="Open session in chat"
              aria-label="Open session in chat"
              className="flex items-center gap-1.5 rounded-lg bg-[var(--app-primary)] px-3 py-1.5 text-xs font-medium text-white transition hover:brightness-110"
            >
              <ExternalLink size={14} />
              <span className="hidden sm:inline">Open session</span>
            </button>
          )}

          <button
            type="button"
            onClick={() => setShowInfo((open) => !open)}
            title={showInfo ? 'Hide details' : 'Show details'}
            aria-label="Toggle details panel"
            className={`p-2 rounded-lg transition ${showInfo ? 'bg-white/20 text-white' : 'bg-white/10 text-white/70 hover:bg-white/20 hover:text-white'}`}
          >
            <Info size={16} />
          </button>

          <button
            type="button"
            onClick={onClose}
            title="Close viewer (Esc)"
            aria-label="Close viewer"
            className="p-2 rounded-lg bg-white/10 text-white/80 hover:bg-red-500/80 hover:text-white transition ml-1"
          >
            <X size={18} />
          </button>
        </div>
      </header>

      {/* Main Workspace Area */}
      <div className="relative flex min-h-0 flex-1 overflow-hidden">
        {/* Left Arrow */}
        {hasPrev && (
          <button
            type="button"
            onClick={handlePrev}
            aria-label="Previous item"
            className="absolute left-4 top-1/2 -translate-y-1/2 z-20 flex size-10 items-center justify-center rounded-full bg-black/60 text-white/80 hover:bg-black/90 hover:text-white border border-white/20 transition backdrop-blur-sm"
          >
            <ChevronLeft size={22} />
          </button>
        )}

        {/* Right Arrow */}
        {hasNext && (
          <button
            type="button"
            onClick={handleNext}
            aria-label="Next item"
            className="absolute right-4 top-1/2 -translate-y-1/2 z-20 flex size-10 items-center justify-center rounded-full bg-black/60 text-white/80 hover:bg-black/90 hover:text-white border border-white/20 transition backdrop-blur-sm"
          >
            <ChevronRight size={22} />
          </button>
        )}

        {/* Media Canvas */}
        <main className="flex flex-1 items-center justify-center overflow-auto p-4 sm:p-8">
          {item.kind === 'image' && (
            <div className="relative flex items-center justify-center max-h-full max-w-full">
              <img
                src={item.directUrl}
                alt={item.title}
                style={{
                  transform: `scale(${zoomLevel})`,
                  transition: 'transform 0.15s ease-out',
                }}
                className="max-h-[80vh] max-w-full rounded object-contain shadow-2xl select-none"
              />
            </div>
          )}

          {item.kind === 'video' && (
            <div className="flex w-full max-w-4xl flex-col items-center justify-center">
              <video
                src={item.directUrl}
                controls
                autoPlay
                playsInline
                className="max-h-[75vh] w-full rounded-xl bg-black shadow-2xl"
              >
                Your browser does not support playing this video.
              </video>
            </div>
          )}

          {item.kind === 'audio' && (
            <div className="flex w-full max-w-md flex-col items-center rounded-2xl border border-white/10 bg-white/5 p-8 backdrop-blur-xl shadow-2xl">
              <div className="flex size-20 items-center justify-center rounded-full bg-[var(--app-primary)]/20 text-[var(--app-primary)] mb-6 shadow-inner">
                <Music size={36} />
              </div>
              <h3 className="text-center font-medium text-lg text-white">{item.title}</h3>
              <p className="mt-1 text-xs text-white/50">{item.mediaType}</p>

              {/* Equalizer animation simulation */}
              <div className="flex items-end justify-center gap-1 h-8 my-6 w-32" aria-hidden="true">
                {[40, 70, 90, 60, 80, 50, 95, 65, 30].map((h, i) => (
                  <span
                    key={i}
                    className="w-1.5 rounded-full bg-[var(--app-primary)] transition-all duration-300"
                    style={{
                      height: `${h}%`,
                      opacity: 0.6 + (i % 3) * 0.2,
                    }}
                  />
                ))}
              </div>

              <audio
                src={item.directUrl}
                controls
                autoPlay
                className="w-full"
              >
                Your browser does not support the audio element.
              </audio>
            </div>
          )}

          {item.kind === 'animation' && (
            <div className="flex h-full w-full max-w-5xl flex-col items-center justify-center">
              <iframe
                title={item.title}
                src={item.directUrl}
                sandbox="allow-scripts"
                referrerPolicy="no-referrer"
                className="h-[75vh] w-full rounded-xl border border-white/10 bg-white shadow-2xl"
              />
            </div>
          )}
        </main>

        {/* Right Details Drawer */}
        {showInfo && (
          <aside className="w-80 shrink-0 border-l border-white/10 bg-black/60 p-5 backdrop-blur-md overflow-y-auto text-xs text-white/80">
            <h3 className="text-sm font-semibold text-white mb-4">Metadata & Details</h3>

            <div className="space-y-4">
              <div>
                <span className="text-[10px] uppercase font-bold tracking-wider text-white/40">Title / Label</span>
                <p className="mt-0.5 text-white font-medium text-xs break-words">{item.title}</p>
              </div>

              <div>
                <span className="text-[10px] uppercase font-bold tracking-wider text-white/40">File Name</span>
                <p className="mt-0.5 font-mono text-[11px] text-white/90 break-all">{item.filename}</p>
              </div>

              <div className="grid grid-cols-2 gap-3">
                <div>
                  <span className="text-[10px] uppercase font-bold tracking-wider text-white/40">Type</span>
                  <p className="mt-0.5 capitalize font-medium text-white">{item.kind}</p>
                </div>
                <div>
                  <span className="text-[10px] uppercase font-bold tracking-wider text-white/40">MIME Type</span>
                  <p className="mt-0.5 font-mono text-[11px] text-white/70 truncate">{item.mediaType || 'unknown'}</p>
                </div>
              </div>

              {item.dimensions && (
                <div>
                  <span className="text-[10px] uppercase font-bold tracking-wider text-white/40">Dimensions</span>
                  <p className="mt-0.5 font-mono text-white">{item.dimensions}</p>
                </div>
              )}

              <div>
                <span className="text-[10px] uppercase font-bold tracking-wider text-white/40">Created / Generated</span>
                <p className="mt-0.5 text-white">
                  {item.formattedDate} at {item.formattedTime}
                </p>
              </div>

              {item.iterationGroupTitle && (
                <div>
                  <span className="text-[10px] uppercase font-bold tracking-wider text-white/40">Iteration Group</span>
                  <p className="mt-0.5 text-white font-medium">{item.iterationGroupTitle}</p>
                  {item.totalVariants && item.totalVariants > 1 && (
                    <p className="text-[11px] text-white/60">
                      Variant {item.variantIndex ? item.variantIndex : ''} of {item.totalVariants}
                    </p>
                  )}
                </div>
              )}

              <div>
                <span className="text-[10px] uppercase font-bold tracking-wider text-white/40">Source Session</span>
                <p className="mt-0.5 text-white font-medium break-words">{item.sessionTitle}</p>
                <p className="font-mono text-[10px] text-white/40 truncate">{item.sessionId}</p>
              </div>

              {item.workspaceName && (
                <div>
                  <span className="text-[10px] uppercase font-bold tracking-wider text-white/40">Workspace</span>
                  <p className="mt-0.5 text-white">{item.workspaceName}</p>
                </div>
              )}

              {item.artifact.description && (
                <div>
                  <span className="text-[10px] uppercase font-bold tracking-wider text-white/40">Description / Prompt</span>
                  <p className="mt-0.5 text-white/80 italic break-words leading-relaxed">{item.artifact.description}</p>
                </div>
              )}

              <div className="pt-2 border-t border-white/10">
                <span className="text-[10px] uppercase font-bold tracking-wider text-white/40">Artifact ID</span>
                <p className="mt-0.5 font-mono text-[10px] text-white/60 select-all break-all">{item.id}</p>
              </div>
            </div>
          </aside>
        )}
      </div>
    </div>
  )
}
