import { useCallback, useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
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
  GitBranch,
  Image as ImageIcon,
  Info,
  Loader2,
  Music,
  RotateCcw,
  Sparkles,
  Star,
  Tag,
  X,
  ZoomIn,
  ZoomOut,
} from 'lucide-react'
import type { MediaLibraryItem } from './types'
import { getMediaSettingsCatalog } from '../../settings/media/queries/get-media-settings'
import { saveImageDefaultModel } from '../../settings/swarm/mutations/save-image-default-model'
import { saveVideoDefaultModel } from '../../settings/swarm/mutations/save-video-models'
import { uiSettingsQueryKey, uiSettingsQueryOptions } from '../../../queries/query-options'

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
  onFineTune?: (item: MediaLibraryItem, editPrompt: string, autoDeploy: boolean, model?: string) => void
  onIterate?: (item: MediaLibraryItem, variantCount: number, stylePrompt: string, autoDeploy: boolean, model?: string) => void
  onGenerateVideo?: (item: MediaLibraryItem, prompt: string, autoDeploy: boolean, model?: string) => void
  onContinueVideo?: (item: MediaLibraryItem, prompt: string, autoDeploy: boolean, model?: string) => void
  initialQuickRouteMode?: QuickRouteMode | null
  isGenerating?: boolean
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
  isGenerating = false,
}: MediaViewerModalProps) {
  const [zoomLevel, setZoomLevel] = useState(1)
  const [showInfo, setShowInfo] = useState(true)
  const [copied, setCopied] = useState(false)
  const [localGenerating, setLocalGenerating] = useState(false)
  const [defaultSaved, setDefaultSaved] = useState(false)

  // Query media catalog and UI settings for model switcher & default upgrade
  const queryClient = useQueryClient()
  const { data: mediaCatalog } = useQuery({
    queryKey: ['media-settings-catalog'],
    queryFn: () => getMediaSettingsCatalog(),
    staleTime: 60_000,
  })
  const { data: uiSettings } = useQuery(uiSettingsQueryOptions())

  // Default mode depends on media kind: images -> fine_tune (edit), videos -> next_scene
  const defaultModeForKind: QuickRouteMode = useMemo(() => {
    if (!item) return 'fine_tune'
    return item.kind === 'video' ? 'next_scene' : 'fine_tune'
  }, [item])

  const [activeQuickRouteMode, setActiveQuickRouteMode] = useState<QuickRouteMode>(
    initialQuickRouteMode ?? defaultModeForKind,
  )
  const [quickRoutePrompt, setQuickRoutePrompt] = useState('')
  const [iterationsLimit, setIterationsLimit] = useState<number>(1)
  const [selectedModel, setSelectedModel] = useState<string>('')

  // Current default model from catalog/settings
  const defaultImageModel = uiSettings?.tools?.image?.default_model || mediaCatalog?.default_image_model || 'imagen-3.0-generate-002'
  const defaultVideoModel = uiSettings?.tools?.video?.default_model || mediaCatalog?.default_video_model || 'veo-3.1-generate-preview'

  const currentDefaultModel = useMemo(() => {
    if (activeQuickRouteMode === 'fine_tune' || activeQuickRouteMode === 'iterate') {
      return defaultImageModel
    }
    return defaultVideoModel
  }, [activeQuickRouteMode, defaultImageModel, defaultVideoModel])

  // Sync selected model when mode or item changes
  useEffect(() => {
    if (activeQuickRouteMode === 'fine_tune' || activeQuickRouteMode === 'iterate') {
      setSelectedModel(defaultImageModel)
    } else {
      setSelectedModel(defaultVideoModel)
    }
  }, [activeQuickRouteMode, defaultImageModel, defaultVideoModel])

  // Reset viewer state when item changes
  useEffect(() => {
    setZoomLevel(1)
    setCopied(false)
    setQuickRoutePrompt('')
    setLocalGenerating(false)
    if (initialQuickRouteMode) {
      setActiveQuickRouteMode(initialQuickRouteMode)
    } else if (item) {
      setActiveQuickRouteMode(item.kind === 'video' ? 'next_scene' : 'fine_tune')
    }
  }, [item?.id, initialQuickRouteMode])

  // Available models based on active mode
  const availableModels = useMemo(() => {
    if (activeQuickRouteMode === 'fine_tune' || activeQuickRouteMode === 'iterate') {
      return mediaCatalog?.image_models ?? []
    }
    return mediaCatalog?.video_generation_models ?? []
  }, [activeQuickRouteMode, mediaCatalog])

  const handleUpgradeDefaultModel = useCallback(async () => {
    if (!selectedModel) return
    try {
      if (activeQuickRouteMode === 'fine_tune' || activeQuickRouteMode === 'iterate') {
        const updated = await saveImageDefaultModel({ current: uiSettings ?? {}, defaultModel: selectedModel })
        queryClient.setQueryData(uiSettingsQueryKey(), updated)
      } else {
        const updated = await saveVideoDefaultModel({ current: uiSettings ?? {}, defaultModel: selectedModel })
        queryClient.setQueryData(uiSettingsQueryKey(), updated)
      }
      void queryClient.invalidateQueries({ queryKey: ['media-settings-catalog'] })
      setDefaultSaved(true)
      setTimeout(() => setDefaultSaved(false), 2000)
    } catch {
      // Fallback
    }
  }, [activeQuickRouteMode, queryClient, selectedModel, uiSettings])

  // Contextual Preset Suggestions
  const presetSuggestions = useMemo(() => {
    if (!item) return []
    if (activeQuickRouteMode === 'fine_tune') {
      return item.kind === 'video'
        ? ['Alternative camera take', 'Darker cinematic mood', 'Neon cyberpunk aesthetic', 'Faster action pace', 'Ambient synthwave soundtrack']
        : ['Change lighting to warm sunset', 'Cyberpunk neon theme', 'Moody dark aesthetic', 'Minimalist vector illustration', 'Add cinematic depth of field', 'Crisp studio photography']
    }
    if (activeQuickRouteMode === 'to_video') {
      return ['Slow cinematic push-in with ambient beats', 'Dramatic camera pan & electronic synth', 'Action sequence with dynamic movement', 'Aerial orbital rotation']
    }
    if (activeQuickRouteMode === 'next_scene') {
      return ['Transition into expansive aerial view', 'Climactic reveal and accelerating tempo', 'Night sequence with glowing skyline', 'Atmospheric wide outro']
    }
    if (activeQuickRouteMode === 'iterate') {
      return ['Diverse creative styles', 'Anime keyframe style', 'Futuristic 3D render', 'Oil painting']
    }
    return []
  }, [activeQuickRouteMode, item])

  // Execute Quick Revision / Video Route
  const handleExecuteQuickRoute = useCallback(
    (autoDeploy: boolean) => {
      if (!item) return
      const prompt = quickRoutePrompt.trim()
      setLocalGenerating(true)

      if (activeQuickRouteMode === 'fine_tune') {
        if (onFineTune) {
          onFineTune(item, prompt, autoDeploy, selectedModel)
        } else if (onIterateSwarm) {
          onIterateSwarm(item)
        }
      } else if (activeQuickRouteMode === 'to_video') {
        if (onGenerateVideo) {
          onGenerateVideo(item, prompt, autoDeploy, selectedModel)
        }
      } else if (activeQuickRouteMode === 'next_scene') {
        if (onContinueVideo) {
          onContinueVideo(item, prompt, autoDeploy, selectedModel)
        }
      } else if (activeQuickRouteMode === 'iterate') {
        if (onIterate) {
          onIterate(item, iterationsLimit, prompt, autoDeploy, selectedModel)
        } else if (onIterateSwarm) {
          onIterateSwarm(item)
        }
      }

      // Keep dock open so user can do further revisions; clear prompt
      setTimeout(() => {
        setLocalGenerating(false)
        setQuickRoutePrompt('')
      }, 1500)
    },
    [activeQuickRouteMode, item, iterationsLimit, onContinueVideo, onFineTune, onGenerateVideo, onIterate, onIterateSwarm, quickRoutePrompt, selectedModel],
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

  // Build Lineage & History Chain
  const lineageChain = useMemo(() => {
    if (!item) return []
    const chain: { role: 'parent' | 'current' | 'child' | 'sibling'; item: MediaLibraryItem; label: string }[] = []

    // 1. Parent / Source keyframe
    const parentId = item.parentId || item.sourceMediaRef
    if (parentId && parentId !== item.id) {
      const parent = items.find((i) => i.id === parentId)
      if (parent) {
        chain.push({
          role: 'parent',
          item: parent,
          label: parent.kind === 'image' ? 'Source Keyframe' : 'Parent Media',
        })
      }
    }

    // 2. Current Asset
    chain.push({
      role: 'current',
      item,
      label: 'Active Asset',
    })

    // 3. Derived Children (items with parentId or sourceMediaRef pointing to this item)
    const children = items.filter((i) => i.id !== item.id && (i.parentId === item.id || i.sourceMediaRef === item.id))
    for (const child of children) {
      chain.push({
        role: 'child',
        item: child,
        label: child.kind === 'video' ? 'Video Continuation' : 'Derived Revision',
      })
    }

    // 4. Siblings in the same iteration group
    if (item.iterationGroupId) {
      const existingIds = new Set(chain.map((c) => c.item.id))
      const siblings = items.filter((i) => !existingIds.has(i.id) && i.iterationGroupId === item.iterationGroupId)
      for (const sib of siblings) {
        chain.push({
          role: 'sibling',
          item: sib,
          label: `Variant ${sib.variantIndex ?? ''}`,
        })
      }
    }

    return chain
  }, [item, items])

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

  const isWorking = isGenerating || localGenerating

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label={`Media viewer: ${item.title}`}
      className="fixed inset-0 z-50 flex flex-col bg-black/92 backdrop-blur-xl text-[var(--app-text)] animate-in fade-in duration-150"
    >
      {/* Sleek, Clean Top Header Bar */}
      <header className="flex h-14 shrink-0 items-center justify-between border-b border-white/10 bg-black/40 px-4 backdrop-blur-md">
        {/* Left: Media Title & Info */}
        <div className="flex items-center gap-3 min-w-0">
          <div className="flex size-8 items-center justify-center rounded-lg bg-white/10 text-white shrink-0 shadow-inner">
            {item.kind === 'image' && <ImageIcon size={16} />}
            {item.kind === 'video' && <Film size={16} />}
            {item.kind === 'audio' && <Music size={16} />}
            {item.kind === 'animation' && <Sparkles size={16} />}
          </div>
          <div className="min-w-0">
            <h2 className="truncate text-sm font-semibold text-white tracking-wide">{item.title}</h2>
            <div className="flex items-center gap-2 truncate text-xs text-white/50">
              <span className="truncate">{item.filename}</span>
              <span>·</span>
              <span>{item.formattedDate} at {item.formattedTime}</span>
              {items.length > 1 && (
                <span className="font-mono text-[10px] text-white/40">({currentIndex + 1} of {items.length})</span>
              )}
              {item.dimensions && (
                <span className="rounded bg-white/10 px-1.5 py-0.2 font-mono text-[10px] text-white/70">
                  {item.dimensions}
                </span>
              )}
            </div>
          </div>
        </div>

        {/* Right: Clean Utility Controls */}
        <div className="flex items-center gap-2">
          {/* Zoom controls for image */}
          {item.kind === 'image' && (
            <div className="flex items-center gap-1 rounded-lg bg-white/10 px-1 py-0.5 text-xs text-white">
              <button
                type="button"
                onClick={() => setZoomLevel((z) => Math.max(0.5, z - 0.25))}
                title="Zoom out"
                aria-label="Zoom out"
                className="p-1.5 hover:bg-white/10 rounded transition"
              >
                <ZoomOut size={14} />
              </button>
              <span className="px-1 font-mono text-[11px]">{Math.round(zoomLevel * 100)}%</span>
              <button
                type="button"
                onClick={() => setZoomLevel((z) => Math.min(3, z + 0.25))}
                title="Zoom in"
                aria-label="Zoom in"
                className="p-1.5 hover:bg-white/10 rounded transition"
              >
                <ZoomIn size={14} />
              </button>
              <button
                type="button"
                onClick={() => setZoomLevel(1)}
                title="Reset zoom"
                aria-label="Reset zoom"
                className="p-1.5 hover:bg-white/10 rounded transition"
              >
                <RotateCcw size={14} />
              </button>
            </div>
          )}

          {/* Copy Link */}
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

          {/* Tag for Task */}
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

          {/* Swarm Iterations Action */}
          {onIterateSwarm && (
            <button
              type="button"
              onClick={() => onIterateSwarm(item)}
              title="Spawn Swarm Iterations for this media"
              aria-label="Swarm Iterations"
              className="flex items-center gap-1.5 rounded-lg bg-emerald-600 hover:bg-emerald-500 px-3 py-1.5 text-xs font-semibold text-white shadow-md transition cursor-pointer"
            >
              <Sparkles size={14} />
              <span className="hidden sm:inline">Swarm Iterations</span>
            </button>
          )}

          {/* Download */}
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

          {/* Open session */}
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

          {/* Details Toggle */}
          <button
            type="button"
            onClick={() => setShowInfo((open) => !open)}
            title={showInfo ? 'Hide details' : 'Show details'}
            aria-label="Toggle details panel"
            className={`p-2 rounded-lg transition ${showInfo ? 'bg-white/20 text-white' : 'bg-white/10 text-white/70 hover:bg-white/20 hover:text-white'}`}
          >
            <Info size={16} />
          </button>

          {/* Close */}
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

        {/* Center Display & Bottom Dock */}
        <div className="flex flex-1 flex-col min-w-0 min-h-0 overflow-hidden">
          {/* Media Canvas */}
          <main className="flex flex-1 items-center justify-center overflow-auto p-4 sm:p-6 min-h-0">
            {item.kind === 'image' && (
              <div className="relative flex items-center justify-center max-h-full max-w-full">
                <img
                  src={item.directUrl}
                  alt={item.title}
                  style={{
                    transform: `scale(${zoomLevel})`,
                    transition: 'transform 0.15s ease-out',
                  }}
                  className="max-h-[68vh] max-w-full rounded-lg object-contain shadow-2xl select-none border border-white/10"
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
                  className="max-h-[65vh] w-full rounded-xl bg-black shadow-2xl border border-white/10"
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
                  className="h-[65vh] w-full rounded-xl border border-white/10 bg-white shadow-2xl"
                />
              </div>
            )}
          </main>

          {/* Persistent Bottom AI Studio Dock */}
          <footer className="shrink-0 border-t border-white/10 bg-slate-950/95 backdrop-blur-2xl px-4 py-3 sm:px-6 shadow-2xl z-20">
            <div className="flex flex-col gap-2.5 max-w-5xl mx-auto">
              {/* Dock Top Sub-Bar: Mode Switcher Tabs + Model Selector + Iterations Limit */}
              <div className="flex flex-wrap items-center justify-between gap-2">
                {/* Action Mode Tabs */}
                <div className="inline-flex rounded-xl bg-white/5 p-1 border border-white/10">
                  {item.kind === 'image' && (
                    <>
                      <button
                        type="button"
                        onClick={() => setActiveQuickRouteMode('fine_tune')}
                        className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition cursor-pointer ${
                          activeQuickRouteMode === 'fine_tune'
                            ? 'bg-amber-600 text-white shadow-md'
                            : 'text-white/60 hover:text-white hover:bg-white/5'
                        }`}
                        title="Fine-Tune / edit image with conversational instructions"
                      >
                        <Edit3 size={13} />
                        <span>Fine-Tune</span>
                        <span className="sr-only">Edit Image</span>
                      </button>
                      <button
                        type="button"
                        onClick={() => setActiveQuickRouteMode('to_video')}
                        className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition cursor-pointer ${
                          activeQuickRouteMode === 'to_video'
                            ? 'bg-purple-600 text-white shadow-md'
                            : 'text-white/60 hover:text-white hover:bg-white/5'
                        }`}
                        title="Transform image keyframe into cinematic motion video"
                      >
                        <Film size={13} />
                        <span>To Video</span>
                        <span className="sr-only">Turn into Video</span>
                      </button>
                      <button
                        type="button"
                        onClick={() => setActiveQuickRouteMode('iterate')}
                        className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition cursor-pointer ${
                          activeQuickRouteMode === 'iterate'
                            ? 'bg-emerald-600 text-white shadow-md'
                            : 'text-white/60 hover:text-white hover:bg-white/5'
                        }`}
                        title="Generate creative variations in parallel"
                      >
                        <Sparkles size={13} />
                        <span>Swarm Iterations</span>
                      </button>
                    </>
                  )}

                  {item.kind === 'video' && (
                    <>
                      <button
                        type="button"
                        onClick={() => setActiveQuickRouteMode('next_scene')}
                        className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition cursor-pointer ${
                          activeQuickRouteMode === 'next_scene'
                            ? 'bg-purple-600 text-white shadow-md'
                            : 'text-white/60 hover:text-white hover:bg-white/5'
                        }`}
                        title="Continue this video story with the next sequence"
                      >
                        <ArrowRight size={13} />
                        <span>Next Scene</span>
                      </button>
                      <button
                        type="button"
                        onClick={() => setActiveQuickRouteMode('fine_tune')}
                        className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition cursor-pointer ${
                          activeQuickRouteMode === 'fine_tune'
                            ? 'bg-amber-600 text-white shadow-md'
                            : 'text-white/60 hover:text-white hover:bg-white/5'
                        }`}
                        title="Fine-tune video atmosphere, pacing, or style"
                      >
                        <Edit3 size={13} />
                        <span>Fine-Tune</span>
                      </button>
                    </>
                  )}

                  {item.kind === 'audio' && (
                    <button
                      type="button"
                      onClick={() => setActiveQuickRouteMode('fine_tune')}
                      className="flex items-center gap-1.5 rounded-lg bg-emerald-600 px-3 py-1.5 text-xs font-semibold text-white shadow-md"
                    >
                      <Music size={13} />
                      <span>Vary Soundtrack</span>
                    </button>
                  )}
                </div>

                {/* Right controls: Model Switcher & Iterations Limit */}
                <div className="flex items-center gap-2">
                  {/* Model Selector Dropdown & Make Default */}
                  <div className="flex items-center gap-1.5 bg-white/5 border border-white/10 rounded-xl px-2.5 py-1 text-xs">
                    <span className="text-[10px] uppercase font-bold text-white/40 tracking-wider">Model:</span>
                    <select
                      value={selectedModel}
                      onChange={(e) => setSelectedModel(e.target.value)}
                      className="bg-transparent text-white font-medium text-xs outline-none cursor-pointer max-w-[170px] truncate"
                      aria-label="Select AI model"
                    >
                      {availableModels.length > 0 ? (
                        availableModels.map((m) => (
                          <option key={m.id} value={m.id} className="bg-slate-900 text-white">
                            {m.display_name || m.model || m.id}
                          </option>
                        ))
                      ) : (
                        <option value={selectedModel || 'default'} className="bg-slate-900 text-white">
                          {selectedModel || 'Default Model'}
                        </option>
                      )}
                    </select>

                    {/* Set as Default button */}
                    {selectedModel && selectedModel !== currentDefaultModel && (
                      <button
                        type="button"
                        onClick={() => void handleUpgradeDefaultModel()}
                        className="flex items-center gap-1 px-2 py-0.5 rounded-md bg-amber-500/20 text-amber-300 hover:bg-amber-500/30 text-[10px] font-bold transition ml-1"
                        title="Set this model as your default model for revisions"
                      >
                        <Star size={10} className="fill-current" />
                        <span>Set Default</span>
                      </button>
                    )}

                    {defaultSaved && (
                      <span className="text-[10px] text-emerald-400 font-semibold flex items-center gap-0.5">
                        <Check size={11} /> Saved
                      </span>
                    )}
                  </div>

                  {/* Iterations Limit Pill */}
                  <div className="flex items-center gap-1 bg-white/5 border border-white/10 rounded-xl p-1 text-[11px] font-mono">
                    <span className="px-1.5 text-white/40 text-[10px] uppercase">Limit:</span>
                    {[1, 2, 4].map((n) => (
                      <button
                        key={n}
                        type="button"
                        onClick={() => setIterationsLimit(n)}
                        className={`px-2 py-0.5 rounded-md transition font-bold ${
                          iterationsLimit === n
                            ? 'bg-blue-600 text-white shadow-xs'
                            : 'text-white/50 hover:text-white hover:bg-white/10'
                        }`}
                        title={`${n} revision iteration${n > 1 ? 's' : ''}`}
                      >
                        {n}
                      </button>
                    ))}
                  </div>
                </div>
              </div>

              {/* Main Prompt Input Bar */}
              <div className="flex items-center gap-2">
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
                    disabled={isWorking}
                    placeholder={
                      activeQuickRouteMode === 'fine_tune'
                        ? item.kind === 'video'
                          ? `Describe changes to video (e.g. "Change color grade to teal and orange, add cinematic pulse")`
                          : `What to modify? (e.g. "Change lighting to sunset golden hour", "Add glowing neon accents")`
                        : activeQuickRouteMode === 'to_video'
                        ? `Video action & camera (e.g. "Slow cinematic push into scene with ambient electronic synth")`
                        : `Next sequence description (e.g. "Transition into wide orbital view with rising crescendo")`
                    }
                    className="w-full h-10 rounded-xl bg-black/70 border border-white/15 px-3.5 text-xs text-white placeholder-white/40 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500 transition shadow-inner"
                  />
                </div>

                {/* 1-Click Fast Revision Button: Route & Run Now */}
                <button
                  type="button"
                  onClick={() => handleExecuteQuickRoute(true)}
                  disabled={isWorking}
                  className="flex items-center gap-1.5 h-10 px-4 rounded-xl bg-gradient-to-r from-blue-600 to-indigo-600 hover:from-blue-500 hover:to-indigo-500 disabled:opacity-50 text-white font-bold text-xs shadow-lg transition shrink-0 cursor-pointer"
                  title="Route & Run Now: generate revision automatically"
                >
                  {isWorking ? (
                    <>
                      <Loader2 size={14} className="animate-spin" />
                      <span>Generating...</span>
                    </>
                  ) : (
                    <>
                      <Sparkles size={14} className="fill-current text-white/90" />
                      <span>Route & Run Now</span>
                      <span className="sr-only">Auto-Revise</span>
                    </>
                  )}
                </button>

                {/* Secondary In Planner button */}
                <button
                  type="button"
                  onClick={() => handleExecuteQuickRoute(false)}
                  disabled={isWorking}
                  className="hidden sm:flex items-center gap-1.5 h-10 px-3 rounded-xl bg-white/10 hover:bg-white/20 text-slate-300 hover:text-white text-xs font-semibold border border-white/15 transition shrink-0 cursor-pointer"
                  title="Open in full task proposal modal"
                >
                  <Edit3 size={13} />
                  <span>In Planner</span>
                </button>
              </div>

              {/* Quick Preset Chips */}
              {presetSuggestions.length > 0 && (
                <div className="flex items-center gap-1.5 flex-wrap pt-0.5">
                  <span className="text-[10px] font-mono text-white/40 uppercase tracking-wider mr-1">Suggestions:</span>
                  {presetSuggestions.map((preset) => (
                    <button
                      key={preset}
                      type="button"
                      onClick={() => {
                        setQuickRoutePrompt((prev) => (prev ? `${prev}, ${preset}` : preset))
                      }}
                      className="px-2.5 py-0.5 rounded-full bg-white/5 border border-white/10 hover:border-blue-500/50 hover:bg-blue-950/40 text-[10px] text-white/70 hover:text-white transition cursor-pointer"
                    >
                      + {preset}
                    </button>
                  ))}
                </div>
              )}
            </div>
          </footer>
        </div>

        {/* Right Details & Lineage Drawer */}
        {showInfo && (
          <aside className="w-84 shrink-0 border-l border-white/10 bg-black/60 p-5 backdrop-blur-xl overflow-y-auto text-xs text-white/80">
            {/* Visual Lineage & History Chain */}
            <div className="mb-6 pb-5 border-b border-white/10">
              <div className="flex items-center gap-2 mb-3">
                <GitBranch size={15} className="text-blue-400" />
                <h3 className="text-sm font-semibold text-white tracking-wide">Lineage & History</h3>
              </div>

              {lineageChain.length > 1 ? (
                <div className="flex flex-col gap-2">
                  <p className="text-[11px] text-white/50 mb-1">
                    Connected chain of {lineageChain.length} versions & continuations:
                  </p>
                  <div className="relative pl-4 space-y-3 before:absolute before:left-2 before:top-2 before:bottom-2 before:w-0.5 before:bg-white/15">
                    {lineageChain.map(({ item: chainItem, label }) => {
                      const isCurrent = chainItem.id === item.id
                      return (
                        <div
                          key={chainItem.id}
                          className={`relative flex items-center gap-2.5 p-2 rounded-xl transition ${
                            isCurrent
                              ? 'bg-blue-600/20 border border-blue-500/50 shadow-md ring-1 ring-blue-500/30'
                              : 'bg-white/5 border border-white/10 hover:bg-white/10 cursor-pointer'
                          }`}
                          onClick={() => {
                            if (!isCurrent) onSelect(chainItem)
                          }}
                        >
                          {/* Dot on vertical line */}
                          <div
                            className={`absolute -left-[17px] size-2 rounded-full border-2 border-black ${
                              isCurrent ? 'bg-blue-400 size-2.5 -left-[18px]' : 'bg-white/50'
                            }`}
                          />

                          {/* Thumbnail */}
                          <div className="size-11 rounded-lg bg-black/50 overflow-hidden shrink-0 border border-white/10 flex items-center justify-center">
                            {chainItem.kind === 'image' && (
                              <img
                                src={chainItem.directUrl}
                                alt={chainItem.title}
                                className="size-full object-cover"
                              />
                            )}
                            {chainItem.kind === 'video' && (
                              <Film size={18} className="text-purple-400" />
                            )}
                            {chainItem.kind === 'audio' && (
                              <Music size={18} className="text-emerald-400" />
                            )}
                            {chainItem.kind === 'animation' && (
                              <Sparkles size={18} className="text-amber-400" />
                            )}
                          </div>

                          {/* Info */}
                          <div className="min-w-0 flex-1">
                            <div className="flex items-center justify-between gap-1">
                              <span
                                className={`text-[10px] font-bold uppercase tracking-wider ${
                                  isCurrent ? 'text-blue-300' : 'text-white/60'
                                }`}
                              >
                                {label}
                              </span>
                              {isCurrent && (
                                <span className="rounded bg-blue-500/30 px-1 py-0.2 text-[9px] font-bold text-blue-200">
                                  Current
                                </span>
                              )}
                            </div>
                            <p className="truncate text-xs font-medium text-white mt-0.5">
                              {chainItem.title}
                            </p>
                            <p className="text-[10px] text-white/40 truncate">
                              {chainItem.formattedDate} · {chainItem.kind}
                            </p>
                          </div>
                        </div>
                      )
                    })}
                  </div>
                </div>
              ) : (
                <div className="rounded-xl border border-white/10 bg-white/5 p-3">
                  <div className="flex items-center justify-between">
                    <span className="text-[10px] uppercase font-bold text-emerald-400 tracking-wider">
                      Initial Keyframe (v1)
                    </span>
                    <span className="rounded bg-white/10 px-1.5 py-0.5 font-mono text-[9px] text-white/60">
                      Root
                    </span>
                  </div>
                  <p className="mt-1.5 text-xs text-white/80 line-clamp-2 italic">
                    {item.artifact?.description || item.title}
                  </p>
                  <p className="mt-2 text-[10px] text-white/40 leading-relaxed">
                    New revisions and video scenes generated with the bottom dock will link automatically into this lineage.
                  </p>
                </div>
              )}
            </div>

            {/* Metadata & Details Section */}
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
