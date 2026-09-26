import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  ArrowRight,
  ChevronLeft,
  ChevronRight,
  Copy,
  Check,
  Download,
  Edit3,
  ExternalLink,
  Film,
  Image as ImageIcon,
  Info,
  Music,
  RotateCcw,
  Sparkles,
  Tag,
  X,
  Zap,
  ZoomIn,
  ZoomOut,
} from 'lucide-react'
import type { MediaLibraryItem } from './types'

export type QuickRouteMode = 'fine_tune' | 'iterate' | 'to_video' | 'next_scene'

export interface MediaViewerModalProps {
  item: MediaLibraryItem | null
  items: readonly MediaLibraryItem[]
  onClose: () => void
  onSelect: (item: MediaLibraryItem) => void
  onOpenSession?: (sessionId: string) => void
  isTagged?: boolean
  onToggleTag?: (item: MediaLibraryItem) => void
  onIterateSwarm?: (item: MediaLibraryItem) => void
  onFineTune?: (item: MediaLibraryItem, editPrompt: string, autoDeploy: boolean) => void
  onIterate?: (item: MediaLibraryItem, variantCount: number, stylePrompt: string, autoDeploy: boolean) => void
  onGenerateVideo?: (item: MediaLibraryItem, prompt: string, autoDeploy: boolean) => void
  onContinueVideo?: (item: MediaLibraryItem, prompt: string, autoDeploy: boolean) => void
  initialQuickRouteMode?: QuickRouteMode | null
}

export function MediaViewerModal({
  item,
  items,
  onClose,
  onSelect,
  onOpenSession,
  isTagged,
  onToggleTag,
  onIterateSwarm,
  onFineTune,
  onIterate,
  onGenerateVideo,
  onContinueVideo,
  initialQuickRouteMode,
}: MediaViewerModalProps) {
  const [zoomLevel, setZoomLevel] = useState(1)
  const [showInfo, setShowInfo] = useState(true)
  const [copied, setCopied] = useState(false)

  // Quick Route state & controls
  const [activeQuickRouteMode, setActiveQuickRouteMode] = useState<QuickRouteMode | null>(
    initialQuickRouteMode ?? null,
  )
  const [quickRoutePrompt, setQuickRoutePrompt] = useState('')
  const [quickRouteVariants, setQuickRouteVariants] = useState(5)
  const [quickRouteScenes, setQuickRouteScenes] = useState(2)

  // Reset viewer state when item changes
  useEffect(() => {
    setZoomLevel(1)
    setCopied(false)
    setQuickRoutePrompt('')
    if (initialQuickRouteMode) {
      setActiveQuickRouteMode(initialQuickRouteMode)
    }
  }, [item?.id, initialQuickRouteMode])

  const presetSuggestions = useMemo(() => {
    if (!item) return []
    if (activeQuickRouteMode === 'fine_tune') {
      return item.kind === 'video'
        ? ['Alternative camera take', 'Darker cinematic mood', 'Neon cyberpunk aesthetic', 'Faster action pace', 'Ambient synthwave soundtrack']
        : ['Change lighting to sunset', 'Cyberpunk neon theme', 'Darker moody aesthetic', 'Minimalist flat vector', 'Add glowing cyan accents', 'Photorealistic details']
    }
    if (activeQuickRouteMode === 'iterate') {
      return item.kind === 'video'
        ? ['High-energy take', 'Subtle atmospheric variation', 'Stylized neon sequence']
        : ['Diverse creative styles', 'Subtle variations with consistent subject', 'Anime style', 'Futuristic 3D render', 'Oil painting']
    }
    if (activeQuickRouteMode === 'to_video') {
      return ['Slow cinematic push-in with ambient beats', 'Dramatic camera pan & electronic synth', 'Action sequence with dynamic movement']
    }
    if (activeQuickRouteMode === 'next_scene') {
      return ['Transition into expansive aerial view', 'Climactic reveal and accelerating tempo', 'Night sequence with glowing skyline', 'Atmospheric outro']
    }
    return []
  }, [activeQuickRouteMode, item])

  const handleExecuteQuickRoute = useCallback(
    (autoDeploy: boolean) => {
      if (!item) return
      const prompt = quickRoutePrompt.trim()
      if (activeQuickRouteMode === 'fine_tune') {
        if (onFineTune) {
          onFineTune(item, prompt, autoDeploy)
        } else if (onIterateSwarm) {
          onIterateSwarm(item)
        }
      } else if (activeQuickRouteMode === 'iterate') {
        if (onIterate) {
          onIterate(item, quickRouteVariants, prompt, autoDeploy)
        } else if (onIterateSwarm) {
          onIterateSwarm(item)
        }
      } else if (activeQuickRouteMode === 'to_video') {
        if (onGenerateVideo) {
          onGenerateVideo(item, prompt, autoDeploy)
        }
      } else if (activeQuickRouteMode === 'next_scene') {
        if (onContinueVideo) {
          onContinueVideo(item, prompt, autoDeploy)
        }
      }
      setActiveQuickRouteMode(null)
    },
    [activeQuickRouteMode, item, onContinueVideo, onFineTune, onGenerateVideo, onIterate, onIterateSwarm, quickRoutePrompt, quickRouteVariants],
  )

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

          {/* Quick Route Actions */}
          <button
            type="button"
            onClick={() =>
              setActiveQuickRouteMode((m) => (m === 'fine_tune' ? null : 'fine_tune'))
            }
            title={item.kind === 'video' ? 'Fine-tune / modify video' : 'Fine-tune / modify image ("change this to...")'}
            aria-label="Fine-tune media"
            className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition ${
              activeQuickRouteMode === 'fine_tune'
                ? 'bg-amber-600 text-white shadow-md'
                : 'bg-white/10 text-white hover:bg-white/20'
            }`}
          >
            <Edit3 size={14} />
            <span className="hidden sm:inline">Fine-Tune</span>
          </button>

          <button
            type="button"
            onClick={() =>
              setActiveQuickRouteMode((m) => (m === 'iterate' ? null : 'iterate'))
            }
            title={item.kind === 'video' ? 'Render alternative video takes' : 'Generate creative variations in parallel'}
            aria-label="Swarm Iterations"
            className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition ${
              activeQuickRouteMode === 'iterate'
                ? 'bg-emerald-600 text-white shadow-md'
                : 'bg-white/10 text-white hover:bg-white/20'
            }`}
          >
            <Sparkles size={14} />
            <span className="hidden sm:inline">Swarm Iterations</span>
          </button>

          {item.kind === 'image' && (
            <button
              type="button"
              onClick={() =>
                setActiveQuickRouteMode((m) => (m === 'to_video' ? null : 'to_video'))
              }
              title="Create cinematic video story from this image"
              aria-label="Create video story from image"
              className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition ${
                activeQuickRouteMode === 'to_video'
                  ? 'bg-purple-600 text-white shadow-md'
                  : 'bg-white/10 text-white hover:bg-white/20'
              }`}
            >
              <Film size={14} />
              <span className="hidden sm:inline">To Video</span>
            </button>
          )}

          {item.kind === 'video' && (
            <button
              type="button"
              onClick={() =>
                setActiveQuickRouteMode((m) => (m === 'next_scene' ? null : 'next_scene'))
              }
              title="Continue video story with the next sequence"
              aria-label="Next scene continuation"
              className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition ${
                activeQuickRouteMode === 'next_scene'
                  ? 'bg-purple-600 text-white shadow-md'
                  : 'bg-white/10 text-white hover:bg-white/20'
              }`}
            >
              <ArrowRight size={14} />
              <span className="hidden sm:inline">Next Scene</span>
            </button>
          )}

          {onToggleTag && (
            <button
              type="button"
              onClick={() => onToggleTag(item)}
              title={isTagged ? 'Tagged for Task' : 'Tag for Task'}
              aria-label="Tag media for task"
              className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition ${
                isTagged
                  ? 'bg-blue-600 text-white hover:bg-blue-500 shadow-md'
                  : 'bg-white/10 text-white hover:bg-white/20'
              }`}
            >
              <Tag size={14} className={isTagged ? 'fill-current' : ''} />
              <span className="hidden sm:inline">{isTagged ? 'Tagged for Task' : 'Tag for Task'}</span>
            </button>
          )}

          {onIterateSwarm && (
            <button
              type="button"
              onClick={() => onIterateSwarm(item)}
              title="Spawn Swarm Iterations for this media"
              aria-label="Spawn Swarm Iterations for this media"
              className="flex items-center gap-1.5 rounded-lg bg-emerald-600 hover:bg-emerald-500 px-3 py-1.5 text-xs font-semibold text-white shadow-md transition"
            >
              <Sparkles size={14} />
              <span className="hidden sm:inline">Swarm Iterations</span>
            </button>
          )}

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

      {/* Quick Route Interactive Action Panel */}
      {activeQuickRouteMode && (
        <div className="shrink-0 border-b border-white/15 bg-slate-950/95 backdrop-blur-xl px-4 py-3 sm:px-6 shadow-xl animate-in slide-in-from-top-2 duration-150">
          <div className="flex flex-col gap-2.5 max-w-5xl mx-auto">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                {activeQuickRouteMode === 'fine_tune' && (
                  <>
                    <div className="flex size-6 items-center justify-center rounded-md bg-amber-500/20 text-amber-300">
                      <Edit3 size={13} />
                    </div>
                    <span className="text-xs font-bold text-white tracking-wide">
                      Fine-Tune / Edit {item.kind === 'video' ? 'Video' : 'Image'}:
                    </span>
                    <span className="text-xs text-slate-400 truncate max-w-xs">{item.title}</span>
                  </>
                )}
                {activeQuickRouteMode === 'iterate' && (
                  <>
                    <div className="flex size-6 items-center justify-center rounded-md bg-emerald-500/20 text-emerald-300">
                      <Sparkles size={13} />
                    </div>
                    <span className="text-xs font-bold text-white tracking-wide">
                      Swarm Iterations ({item.kind === 'video' ? `${quickRouteScenes} takes` : `${quickRouteVariants} variants`}):
                    </span>
                    <span className="text-xs text-slate-400 truncate max-w-xs">{item.title}</span>
                  </>
                )}
                {activeQuickRouteMode === 'to_video' && (
                  <>
                    <div className="flex size-6 items-center justify-center rounded-md bg-purple-500/20 text-purple-300">
                      <Film size={13} />
                    </div>
                    <span className="text-xs font-bold text-white tracking-wide">
                      Generate Video Story from Keyframe:
                    </span>
                    <span className="text-xs text-slate-400 truncate max-w-xs">{item.title}</span>
                  </>
                )}
                {activeQuickRouteMode === 'next_scene' && (
                  <>
                    <div className="flex size-6 items-center justify-center rounded-md bg-purple-500/20 text-purple-300">
                      <ArrowRight size={13} />
                    </div>
                    <span className="text-xs font-bold text-white tracking-wide">
                      Continue Video Sequence (Next Scene):
                    </span>
                    <span className="text-xs text-slate-400 truncate max-w-xs">{item.title}</span>
                  </>
                )}
              </div>

              {/* Variant / Scene Selector Controls */}
              <div className="flex items-center gap-2">
                {activeQuickRouteMode === 'iterate' && item.kind !== 'video' && (
                  <div className="flex items-center gap-1 bg-white/5 border border-white/10 rounded-lg p-0.5 text-[11px] font-mono">
                    <span className="px-1.5 text-slate-400 text-[10px]">VARIANTS:</span>
                    {[3, 5, 10, 25].map((v) => (
                      <button
                        key={v}
                        type="button"
                        onClick={() => setQuickRouteVariants(v)}
                        className={`px-2 py-0.5 rounded transition font-bold ${
                          quickRouteVariants === v
                            ? 'bg-emerald-600 text-white'
                            : 'text-slate-400 hover:text-white hover:bg-white/10'
                        }`}
                      >
                        {v}
                      </button>
                    ))}
                  </div>
                )}

                {(activeQuickRouteMode === 'to_video' || activeQuickRouteMode === 'next_scene' || (activeQuickRouteMode === 'iterate' && item.kind === 'video')) && (
                  <div className="flex items-center gap-1 bg-white/5 border border-white/10 rounded-lg p-0.5 text-[11px] font-mono">
                    <span className="px-1.5 text-slate-400 text-[10px]">SCENES:</span>
                    {[2, 3, 4].map((s) => (
                      <button
                        key={s}
                        type="button"
                        onClick={() => setQuickRouteScenes(s)}
                        className={`px-2 py-0.5 rounded transition font-bold ${
                          quickRouteScenes === s
                            ? 'bg-purple-600 text-white'
                            : 'text-slate-400 hover:text-white hover:bg-white/10'
                        }`}
                      >
                        {s}
                      </button>
                    ))}
                  </div>
                )}

                <button
                  type="button"
                  onClick={() => setActiveQuickRouteMode(null)}
                  className="p-1 rounded text-slate-400 hover:text-white hover:bg-white/10 transition"
                  title="Dismiss quick route"
                >
                  <X size={14} />
                </button>
              </div>
            </div>

            {/* Quick Prompt Input & Action Buttons */}
            <div className="flex flex-col sm:flex-row items-stretch sm:items-center gap-2">
              <div className="relative flex-1">
                <input
                  type="text"
                  value={quickRoutePrompt}
                  onChange={(e) => setQuickRoutePrompt(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && !e.shiftKey) {
                      e.preventDefault()
                      handleExecuteQuickRoute(true)
                    }
                  }}
                  placeholder={
                    activeQuickRouteMode === 'fine_tune'
                      ? `What to change? (e.g. "Change lighting to sunset", "Make background neon cyberpunk")`
                      : activeQuickRouteMode === 'iterate'
                      ? `Iteration guidance (e.g. "Diverse styles: anime, 3D render, minimalist vector")`
                      : activeQuickRouteMode === 'to_video'
                      ? `Video story prompt (e.g. "Slow cinematic push into scene with ambient beats")`
                      : `Next scene prompt (e.g. "Transition into wide orbital view with rising crescendo")`
                  }
                  className="w-full h-9 rounded-lg bg-black/60 border border-white/20 px-3 text-xs text-white placeholder-slate-500 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                />
              </div>

              {/* Action Buttons: Route & Run Now vs Open in Planner */}
              <div className="flex items-center gap-2 shrink-0">
                <button
                  type="button"
                  onClick={() => handleExecuteQuickRoute(true)}
                  className="flex items-center gap-1.5 h-9 px-3.5 rounded-lg bg-gradient-to-r from-blue-600 to-emerald-600 hover:from-blue-500 hover:to-emerald-500 text-white font-bold text-xs shadow-md transition"
                  title="Instantly deploy and start autonomous generation"
                >
                  <Zap size={13} className="fill-current" />
                  <span>Route & Run Now</span>
                </button>

                <button
                  type="button"
                  onClick={() => handleExecuteQuickRoute(false)}
                  className="flex items-center gap-1.5 h-9 px-3 rounded-lg bg-white/10 hover:bg-white/20 text-slate-300 hover:text-white text-xs font-semibold border border-white/15 transition"
                  title="Open in full task proposal modal"
                >
                  <Edit3 size={13} />
                  <span>In Planner</span>
                </button>
              </div>
            </div>

            {/* Preset Suggestions Chips */}
            <div className="flex items-center gap-1.5 flex-wrap pt-0.5">
              <span className="text-[10px] font-mono text-slate-400 uppercase tracking-wider mr-1">Quick Presets:</span>
              {presetSuggestions.map((preset) => (
                <button
                  key={preset}
                  type="button"
                  onClick={() => {
                    setQuickRoutePrompt((prev) => (prev ? `${prev}, ${preset}` : preset))
                  }}
                  className="px-2 py-0.5 rounded-full bg-white/5 border border-white/10 hover:border-blue-500/50 hover:bg-blue-950/40 text-[10px] text-slate-300 hover:text-white transition"
                >
                  + {preset}
                </button>
              ))}
            </div>
          </div>
        </div>
      )}

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
