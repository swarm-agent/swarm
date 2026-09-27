import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertCircle,
  ArrowRight,
  ChevronLeft,
  ChevronRight,
  Clock,
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
import {
  getMediaSettingsCatalog,
  type MediaCatalogModelOption,
} from '../../settings/media/queries/get-media-settings'
import { saveImageDefaultModel } from '../../settings/swarm/mutations/save-image-default-model'
import { saveVideoDefaultModel } from '../../settings/swarm/mutations/save-video-models'
import { uiSettingsQueryKey, uiSettingsQueryOptions } from '../../../queries/query-options'
import {
  calculateGenerationCost,
  normalizeResKey,
  resolveInitialModel,
  resolveInitialSetting,
  type MediaGenerationAction,
  type MediaGenerationJob,
  type MediaGenerationRequest,
  type MediaGenerationSettings,
} from './media-generation'

export type QuickRouteMode = MediaGenerationAction

export interface MediaViewerModalProps {
  item: MediaLibraryItem | null
  items: readonly MediaLibraryItem[]
  onClose: () => void
  onSelect: (item: MediaLibraryItem) => void
  onOpenSession?: (sessionId: string) => void
  isTagged?: boolean
  onToggleTag?: (item: MediaLibraryItem) => void
  onGenerate?: (request: MediaGenerationRequest) => Promise<void>
  generationJobs?: readonly MediaGenerationJob[]
  initialQuickRouteMode?: QuickRouteMode | null
  isGenerating?: boolean
  [key: string]: unknown
}

export function MediaViewerModal({
  item,
  items,
  onClose,
  onSelect,
  onOpenSession,
  isTagged,
  onToggleTag,
  onGenerate,
  generationJobs = [],
  initialQuickRouteMode,
  isGenerating = false,
}: MediaViewerModalProps) {
  const [zoomLevel, setZoomLevel] = useState(1)
  const [showInfo, setShowInfo] = useState(true)
  const [copied, setCopied] = useState(false)
  const [localSubmitting, setLocalSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [defaultSaved, setDefaultSaved] = useState(false)

  // Stable locks and identity tracking
  const isSubmittingRef = useRef(false)
  const lastInitializedItemIdRef = useRef<string | null>(null)
  const lastModelIdRef = useRef<string>('')

  // Query media catalog and UI settings
  const queryClient = useQueryClient()
  const { data: mediaCatalog } = useQuery({
    queryKey: ['media-settings-catalog'],
    queryFn: () => getMediaSettingsCatalog(),
    staleTime: 60_000,
  })
  const { data: uiSettings } = useQuery(uiSettingsQueryOptions())

  const isVideoSource = item?.kind === 'video'

  const defaultModeForKind: QuickRouteMode = useMemo(() => {
    if (!item) return 'fine_tune'
    return isVideoSource ? 'next_scene' : 'fine_tune'
  }, [item, isVideoSource])

  const [activeQuickRouteMode, setActiveQuickRouteMode] = useState<QuickRouteMode>(
    initialQuickRouteMode ?? defaultModeForKind,
  )
  const [quickRoutePrompt, setQuickRoutePrompt] = useState('')
  const [variantCount, setVariantCount] = useState<number>(1)
  const [selectedModel, setSelectedModel] = useState<string>('')

  // Settings: initialized strictly from supported options metadata or provider default
  const [aspectRatio, setAspectRatio] = useState<string>('')
  const [resolution, setResolution] = useState<string>('')
  const [durationSeconds, setDurationSeconds] = useState<number | undefined>(undefined)

  const isVideoAction =
    activeQuickRouteMode === 'to_video' ||
    activeQuickRouteMode === 'next_scene' ||
    (activeQuickRouteMode === 'fine_tune' && isVideoSource)

  // Available models based on action mode:
  // to_video selects video_generation_models
  // video fine_tune and next_scene select video_iteration_models
  // image actions select image_models
  const availableModels: MediaCatalogModelOption[] = useMemo(() => {
    if (activeQuickRouteMode === 'to_video') {
      return (
        mediaCatalog?.video_generation_models ??
        mediaCatalog?.video_models ??
        []
      )
    }
    if (activeQuickRouteMode === 'next_scene' || (activeQuickRouteMode === 'fine_tune' && isVideoSource)) {
      return (
        mediaCatalog?.video_iteration_models ??
        mediaCatalog?.video_generation_models ??
        mediaCatalog?.video_models ??
        []
      )
    }
    return mediaCatalog?.image_models ?? []
  }, [activeQuickRouteMode, isVideoSource, mediaCatalog])

  const defaultImageModel =
    uiSettings?.tools?.image?.default_model ||
    mediaCatalog?.default_image_model ||
    (mediaCatalog?.image_models?.find((m) => m.ready)?.id ?? '')

  const defaultVideoGenerationModel =
    uiSettings?.tools?.video?.default_model ||
    mediaCatalog?.default_video_model ||
    (mediaCatalog?.video_generation_models?.find((m) => m.ready)?.id ?? '')

  const defaultVideoIterationModel =
    uiSettings?.tools?.video?.iteration_model ||
    (mediaCatalog?.video_iteration_models?.find((m) => m.ready)?.id ?? '') ||
    defaultVideoGenerationModel

  const currentDefaultModel = useMemo(() => {
    if (activeQuickRouteMode === 'to_video') {
      return defaultVideoGenerationModel
    }
    if (activeQuickRouteMode === 'next_scene' || (activeQuickRouteMode === 'fine_tune' && isVideoSource)) {
      return defaultVideoIterationModel
    }
    return defaultImageModel
  }, [
    activeQuickRouteMode,
    defaultImageModel,
    defaultVideoGenerationModel,
    defaultVideoIterationModel,
    isVideoSource,
  ])

  const selectedModelOption = useMemo(() => {
    return availableModels.find((m) => m.id === selectedModel)
  }, [availableModels, selectedModel])

  const modelGenOptions = selectedModelOption?.generation_options

  // Supported options directly from model metadata (no invented fallbacks)
  const supportedRatios = useMemo(() => {
    if (modelGenOptions?.aspect_ratios && modelGenOptions.aspect_ratios.length > 0) {
      return modelGenOptions.aspect_ratios
    }
    return []
  }, [modelGenOptions])

  const supportedResolutions = useMemo(() => {
    if (modelGenOptions?.resolutions && modelGenOptions.resolutions.length > 0) {
      return modelGenOptions.resolutions
    }
    return []
  }, [modelGenOptions])

  const supportedDurations = useMemo(() => {
    if (modelGenOptions?.durations && modelGenOptions.durations.length > 0) {
      return modelGenOptions.durations
    }
    return []
  }, [modelGenOptions])

  // Initialize viewer state when active item ID changes.
  // Must NOT reset user changes on every item object reference identity refresh!
  useEffect(() => {
    if (!item) {
      lastInitializedItemIdRef.current = null
      return
    }
    if (lastInitializedItemIdRef.current === item.id) {
      return
    }
    lastInitializedItemIdRef.current = item.id

    setZoomLevel(1)
    setCopied(false)
    setQuickRoutePrompt('')
    setSubmitError(null)
    setLocalSubmitting(false)
    isSubmittingRef.current = false

    const newMode = initialQuickRouteMode ?? (item.kind === 'video' ? 'next_scene' : 'fine_tune')
    setActiveQuickRouteMode(newMode)
    setVariantCount(newMode === 'iterate' ? 4 : 1)

    // Preselect model: source model if available, else preferred default
    const isVid =
      newMode === 'to_video' ||
      newMode === 'next_scene' ||
      (newMode === 'fine_tune' && item.kind === 'video')

    const models = isVid
      ? newMode === 'to_video'
        ? (mediaCatalog?.video_generation_models ?? mediaCatalog?.video_models ?? [])
        : (mediaCatalog?.video_iteration_models ?? mediaCatalog?.video_generation_models ?? mediaCatalog?.video_models ?? [])
      : (mediaCatalog?.image_models ?? [])

    const defModel = isVid
      ? (newMode === 'to_video' ? defaultVideoGenerationModel : defaultVideoIterationModel)
      : defaultImageModel

    const chosenModelId = resolveInitialModel(item.model, models, defModel)
    setSelectedModel(chosenModelId)
    lastModelIdRef.current = chosenModelId

    const chosenOption = models.find((m) => m.id === chosenModelId)
    const genOpts = chosenOption?.generation_options

    const initRatio = resolveInitialSetting(
      (item as unknown as { aspectRatio?: string }).aspectRatio,
      genOpts?.aspect_ratios,
      genOpts?.default_ratio,
    )
    setAspectRatio(initRatio ?? '')

    const initRes = resolveInitialSetting(
      (item as unknown as { resolution?: string }).resolution,
      genOpts?.resolutions,
      genOpts?.default_resolution,
    )
    setResolution(initRes ?? '')

    const rawDur =
      (item as unknown as { durationSeconds?: number }).durationSeconds ??
      (item.durationMs ? Math.round(item.durationMs / 1000) : undefined)
    const initDur = resolveInitialSetting(rawDur, genOpts?.durations, genOpts?.default_duration)
    setDurationSeconds(initDur)
  }, [
    defaultImageModel,
    defaultVideoGenerationModel,
    defaultVideoIterationModel,
    initialQuickRouteMode,
    item?.id,
    mediaCatalog,
  ])

  // Adjust settings only when selectedModel changes, preserving user choice if still supported
  useEffect(() => {
    if (!selectedModel || selectedModel === lastModelIdRef.current) return
    lastModelIdRef.current = selectedModel

    if (!modelGenOptions) {
      setAspectRatio('')
      setResolution('')
      setDurationSeconds(undefined)
      return
    }

    if (aspectRatio) {
      const stillSupported = modelGenOptions.aspect_ratios?.find(
        (r) => r.toLowerCase().trim() === aspectRatio.toLowerCase().trim(),
      )
      if (stillSupported) {
        setAspectRatio(stillSupported)
      } else {
        setAspectRatio(
          modelGenOptions.default_ratio &&
            modelGenOptions.aspect_ratios?.includes(modelGenOptions.default_ratio)
            ? modelGenOptions.default_ratio
            : (modelGenOptions.aspect_ratios?.[0] ?? ''),
        )
      }
    } else if (modelGenOptions.default_ratio) {
      setAspectRatio(modelGenOptions.default_ratio)
    }

    if (resolution) {
      const normRes = normalizeResKey(resolution)
      const stillSupported = modelGenOptions.resolutions?.find(
        (r) => normalizeResKey(r) === normRes,
      )
      if (stillSupported) {
        setResolution(stillSupported)
      } else {
        setResolution(
          modelGenOptions.default_resolution &&
            modelGenOptions.resolutions?.includes(modelGenOptions.default_resolution)
            ? modelGenOptions.default_resolution
            : (modelGenOptions.resolutions?.[0] ?? ''),
        )
      }
    } else if (modelGenOptions.default_resolution) {
      setResolution(modelGenOptions.default_resolution)
    }

    if (durationSeconds !== undefined) {
      const stillSupported = modelGenOptions.durations?.find((d) => d === durationSeconds)
      if (stillSupported !== undefined) {
        setDurationSeconds(stillSupported)
      } else {
        setDurationSeconds(
          modelGenOptions.default_duration &&
            modelGenOptions.durations?.includes(modelGenOptions.default_duration)
            ? modelGenOptions.default_duration
            : modelGenOptions.durations?.[0],
        )
      }
    } else if (modelGenOptions.default_duration) {
      setDurationSeconds(modelGenOptions.default_duration)
    }
  }, [selectedModel, modelGenOptions, aspectRatio, resolution, durationSeconds])

  const handleModeChange = useCallback(
    (newMode: QuickRouteMode) => {
      setActiveQuickRouteMode(newMode)
      if (newMode === 'iterate') {
        setVariantCount((prev) => (prev > 1 ? prev : 4))
      } else {
        setVariantCount(1)
      }

      const isVid =
        newMode === 'to_video' ||
        newMode === 'next_scene' ||
        (newMode === 'fine_tune' && item?.kind === 'video')

      const models = isVid
        ? newMode === 'to_video'
          ? (mediaCatalog?.video_generation_models ?? mediaCatalog?.video_models ?? [])
          : (mediaCatalog?.video_iteration_models ?? mediaCatalog?.video_generation_models ?? mediaCatalog?.video_models ?? [])
        : (mediaCatalog?.image_models ?? [])

      const defModel = isVid
        ? (newMode === 'to_video' ? defaultVideoGenerationModel : defaultVideoIterationModel)
        : defaultImageModel

      const stillValid = models.find((m) => m.id === selectedModel)
      if (!stillValid) {
        const preferred = resolveInitialModel(item?.model, models, defModel)
        setSelectedModel(preferred)
        lastModelIdRef.current = preferred
      }
    },
    [
      defaultImageModel,
      defaultVideoGenerationModel,
      defaultVideoIterationModel,
      item?.kind,
      item?.model,
      mediaCatalog,
      selectedModel,
    ],
  )

  const handleUpgradeDefaultModel = useCallback(async () => {
    if (!selectedModel) return
    try {
      if (isVideoAction) {
        const updated = await saveVideoDefaultModel({ current: uiSettings ?? {}, defaultModel: selectedModel })
        queryClient.setQueryData(uiSettingsQueryKey(), updated)
      } else {
        const updated = await saveImageDefaultModel({ current: uiSettings ?? {}, defaultModel: selectedModel })
        queryClient.setQueryData(uiSettingsQueryKey(), updated)
      }
      void queryClient.invalidateQueries({ queryKey: ['media-settings-catalog'] })
      setDefaultSaved(true)
      setTimeout(() => setDefaultSaved(false), 2000)
    } catch {
      // Fallback
    }
  }, [isVideoAction, queryClient, selectedModel, uiSettings])

  // Real model metadata cost calculation
  const costEstimate = useMemo(() => {
    const currentSettings: MediaGenerationSettings = {
      aspectRatio: aspectRatio.trim() ? aspectRatio.trim() : undefined,
      resolution: resolution.trim() ? resolution.trim() : undefined,
      durationSeconds: isVideoAction && durationSeconds && durationSeconds > 0 ? durationSeconds : undefined,
    }
    return calculateGenerationCost({
      modelOption: selectedModelOption,
      action: activeQuickRouteMode,
      count: activeQuickRouteMode === 'iterate' ? variantCount : 1,
      settings: currentSettings,
    })
  }, [
    activeQuickRouteMode,
    aspectRatio,
    durationSeconds,
    isVideoAction,
    resolution,
    selectedModelOption,
    variantCount,
  ])

  // Execute Generation / Fine Tune directly with synchronous ref lock and live state
  const handleExecuteGeneration = useCallback(async () => {
    if (!item || isSubmittingRef.current || localSubmitting || isGenerating) return
    const prompt = quickRoutePrompt.trim()
    if (!prompt) return

    isSubmittingRef.current = true
    setLocalSubmitting(true)
    setSubmitError(null)

    const generationSettings: MediaGenerationSettings = {
      aspectRatio: aspectRatio.trim() ? aspectRatio.trim() : undefined,
      resolution: resolution.trim() ? resolution.trim() : undefined,
      durationSeconds: isVideoAction && durationSeconds && durationSeconds > 0 ? durationSeconds : undefined,
    }

    try {
      if (onGenerate) {
        await onGenerate({
          item,
          action: activeQuickRouteMode,
          deltaPrompt: prompt,
          variantCount: activeQuickRouteMode === 'iterate' ? variantCount : 1,
          model: selectedModel,
          settings: generationSettings,
        })
        setQuickRoutePrompt('')
      }
    } catch (err) {
      setSubmitError(err instanceof Error ? err.message : 'Generation request failed.')
    } finally {
      isSubmittingRef.current = false
      setLocalSubmitting(false)
    }
  }, [
    activeQuickRouteMode,
    aspectRatio,
    durationSeconds,
    isGenerating,
    isVideoAction,
    item,
    localSubmitting,
    onGenerate,
    quickRoutePrompt,
    resolution,
    selectedModel,
    variantCount,
  ])

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
        return
      }
      const activeEl = document.activeElement
      const isInput =
        activeEl &&
        (activeEl.tagName === 'INPUT' ||
          activeEl.tagName === 'TEXTAREA' ||
          activeEl.tagName === 'SELECT' ||
          (activeEl as HTMLElement).isContentEditable)
      if (isInput) return

      if (event.key === 'ArrowLeft') {
        handlePrev()
      } else if (event.key === 'ArrowRight') {
        handleNext()
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [handleNext, handlePrev, item, onClose])

  // Related pending / running generation jobs for this active source item
  const relevantJobs = useMemo(() => {
    if (!item) return []
    return generationJobs.filter((job) => job.sourceId === item.id)
  }, [generationJobs, item])

  // Build Lineage & History Chain
  const lineageChain = useMemo(() => {
    if (!item) return []
    const chain: { role: 'parent' | 'current' | 'child' | 'sibling'; item: MediaLibraryItem; label: string }[] = []
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
    chain.push({
      role: 'current',
      item: item,
      label: 'Active Asset',
    })
    const children = items.filter((i) => i.id !== item.id && (i.parentId === item.id || i.sourceMediaRef === item.id))
    for (const child of children) {
      chain.push({
        role: 'child',
        item: child,
        label: child.kind === 'video' ? 'Video Continuation' : 'Derived Revision',
      })
    }
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

  const isWorking = isGenerating || localSubmitting
  const canSubmit =
    !isSubmittingRef.current &&
    !isWorking &&
    Boolean(quickRoutePrompt.trim()) &&
    Boolean(selectedModel)

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label={`Media viewer: ${item.title}`}
      className="fixed inset-0 z-50 flex flex-col bg-black/92 backdrop-blur-xl text-[var(--app-text)] animate-in fade-in duration-150"
    >
      {/* Top Header Bar */}
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

        {/* Right: Utility Controls */}
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

          {/* Swarm Iterations Action Button */}
          <button
            type="button"
            onClick={() => {
              handleModeChange('iterate')
            }}
            title="Configure and run Swarm Iterations for this media"
            aria-label="Swarm Iterations"
            className="flex items-center gap-1.5 rounded-lg bg-emerald-600 hover:bg-emerald-500 px-3 py-1.5 text-xs font-semibold text-white shadow-md transition cursor-pointer"
          >
            <Sparkles size={14} />
            <span className="hidden sm:inline">Swarm Iterations</span>
          </button>

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
                  className="max-h-[56vh] max-w-full rounded-lg object-contain shadow-2xl select-none border border-white/10"
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
                  className="max-h-[54vh] w-full rounded-xl bg-black shadow-2xl border border-white/10"
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

                <audio
                  src={item.directUrl}
                  controls
                  autoPlay
                  className="w-full mt-6"
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
                  className="h-[54vh] w-full rounded-xl border border-white/10 bg-white shadow-2xl"
                />
              </div>
            )}
          </main>

          {/* Persistent Bottom AI Studio Dock */}
          <footer className="shrink-0 border-t border-white/10 bg-slate-950/95 backdrop-blur-2xl px-4 py-3 sm:px-6 shadow-2xl z-20">
            <div className="flex flex-col gap-3 max-w-5xl mx-auto">
              {/* Row 1: Action Mode Switcher + Model Picker + Output Settings */}
              <div className="flex flex-wrap items-center justify-between gap-3">
                {/* Action Mode Tabs */}
                <div className="inline-flex rounded-xl bg-white/5 p-1 border border-white/10">
                  {item.kind === 'image' && (
                    <>
                      <button
                        type="button"
                        onClick={() => handleModeChange('fine_tune')}
                        className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition cursor-pointer ${
                          activeQuickRouteMode === 'fine_tune'
                            ? 'bg-amber-600 text-white shadow-md'
                            : 'text-white/60 hover:text-white hover:bg-white/5'
                        }`}
                        title="Fine-Tune / edit image with instructions"
                      >
                        <Edit3 size={13} />
                        <span>Fine-Tune</span>
                        <span className="sr-only">Edit Image</span>
                      </button>
                      <button
                        type="button"
                        onClick={() => handleModeChange('to_video')}
                        className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition cursor-pointer ${
                          activeQuickRouteMode === 'to_video'
                            ? 'bg-purple-600 text-white shadow-md'
                            : 'text-white/60 hover:text-white hover:bg-white/5'
                        }`}
                        title="Transform image into video"
                      >
                        <Film size={13} />
                        <span>To Video</span>
                        <span className="sr-only">Turn into Video</span>
                      </button>
                      <button
                        type="button"
                        onClick={() => handleModeChange('iterate')}
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
                        onClick={() => handleModeChange('next_scene')}
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
                        onClick={() => handleModeChange('fine_tune')}
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
                      <button
                        type="button"
                        onClick={() => handleModeChange('iterate')}
                        className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition cursor-pointer ${
                          activeQuickRouteMode === 'iterate'
                            ? 'bg-emerald-600 text-white shadow-md'
                            : 'text-white/60 hover:text-white hover:bg-white/5'
                        }`}
                        title="Generate video variations in parallel"
                      >
                        <Sparkles size={13} />
                        <span>Swarm Iterations</span>
                      </button>
                    </>
                  )}

                  {item.kind === 'audio' && (
                    <button
                      type="button"
                      onClick={() => handleModeChange('fine_tune')}
                      className="flex items-center gap-1.5 rounded-lg bg-emerald-600 px-3 py-1.5 text-xs font-semibold text-white shadow-md"
                    >
                      <Music size={13} />
                      <span>Vary Soundtrack</span>
                    </button>
                  )}
                </div>

                {/* Model Selector & Parameters */}
                <div className="flex flex-wrap items-center gap-2">
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
                        <option value={selectedModel || ''} className="bg-slate-900 text-white">
                          {selectedModel || 'Provider default'}
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

                  {/* Aspect Ratio Selector (no invented common options; provider default when metadata absent) */}
                  <div className="flex items-center gap-1.5 bg-white/5 border border-white/10 rounded-xl px-2 py-1 text-xs">
                    <span className="text-[10px] uppercase font-bold text-white/40 tracking-wider">Ratio:</span>
                    <select
                      value={aspectRatio}
                      onChange={(e) => setAspectRatio(e.target.value)}
                      className="bg-transparent text-white font-medium text-xs outline-none cursor-pointer"
                      aria-label="Aspect Ratio"
                    >
                      {supportedRatios.length > 0 ? (
                        supportedRatios.map((ratio) => (
                          <option key={ratio} value={ratio} className="bg-slate-900 text-white">
                            {ratio}
                          </option>
                        ))
                      ) : (
                        <option value="" className="bg-slate-900 text-white">
                          Provider default
                        </option>
                      )}
                    </select>
                  </div>

                  {/* Resolution Selector (no invented common options; provider default when metadata absent) */}
                  <div className="flex items-center gap-1.5 bg-white/5 border border-white/10 rounded-xl px-2 py-1 text-xs">
                    <span className="text-[10px] uppercase font-bold text-white/40 tracking-wider">Res:</span>
                    <select
                      value={resolution}
                      onChange={(e) => setResolution(e.target.value)}
                      className="bg-transparent text-white font-medium text-xs outline-none cursor-pointer uppercase"
                      aria-label="Resolution"
                    >
                      {supportedResolutions.length > 0 ? (
                        supportedResolutions.map((res) => (
                          <option key={res} value={res} className="bg-slate-900 text-white uppercase">
                            {res}
                          </option>
                        ))
                      ) : (
                        <option value="" className="bg-slate-900 text-white">
                          Provider default
                        </option>
                      )}
                    </select>
                  </div>

                  {/* Video Duration Selector (video actions only) */}
                  {isVideoAction && (
                    <div className="flex items-center gap-1.5 bg-white/5 border border-white/10 rounded-xl px-2 py-1 text-xs">
                      <span className="text-[10px] uppercase font-bold text-white/40 tracking-wider">Duration:</span>
                      <select
                        value={durationSeconds ?? ''}
                        onChange={(e) => {
                          const val = e.target.value
                          setDurationSeconds(val ? Number(val) : undefined)
                        }}
                        className="bg-transparent text-white font-medium text-xs outline-none cursor-pointer"
                        aria-label="Video Duration"
                      >
                        {supportedDurations.length > 0 ? (
                          supportedDurations.map((sec) => (
                            <option key={sec} value={sec} className="bg-slate-900 text-white">
                              {sec}s
                            </option>
                          ))
                        ) : (
                          <option value="" className="bg-slate-900 text-white">
                            Provider default
                          </option>
                        )}
                      </select>
                    </div>
                  )}

                  {/* Output Count Pill (Swarm Iterations only; bounded UI contract count <=8 video <=50 image) */}
                  {activeQuickRouteMode === 'iterate' && (
                    <div className="flex items-center gap-1 bg-white/5 border border-white/10 rounded-xl p-1 text-[11px] font-mono">
                      <span className="px-1 text-white/40 text-[10px] uppercase">Outputs:</span>
                      {[1, 2, 4, 8].map((n) => (
                        <button
                          key={n}
                          type="button"
                          onClick={() => setVariantCount(n)}
                          className={`px-2 py-0.5 rounded-md transition font-bold ${
                            variantCount === n
                              ? 'bg-blue-600 text-white shadow-xs'
                              : 'text-white/50 hover:text-white hover:bg-white/10'
                          }`}
                          title={`${n} output${n > 1 ? 's' : ''}`}
                        >
                          {n}
                        </button>
                      ))}
                    </div>
                  )}
                </div>
              </div>

              {/* Row 2: Textarea Prompt Box & Action Submission */}
              <div className="flex flex-col sm:flex-row items-stretch sm:items-end gap-3">
                <div className="relative flex-1">
                  <textarea
                    rows={2}
                    value={quickRoutePrompt}
                    onChange={(e) => setQuickRoutePrompt(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                        e.preventDefault()
                        void handleExecuteGeneration()
                      }
                    }}
                    disabled={isWorking}
                    placeholder={
                      activeQuickRouteMode === 'fine_tune'
                        ? isVideoSource
                          ? 'Describe fine-tune adjustments to camera pacing, lighting, or atmosphere...'
                          : 'Describe edits (e.g. background, lighting, objects)...'
                        : activeQuickRouteMode === 'to_video'
                          ? 'Describe camera movement and cinematic action...'
                          : activeQuickRouteMode === 'next_scene'
                            ? 'Describe next sequence in the story...'
                            : 'Describe variation theme...'
                    }
                    className="w-full rounded-xl bg-black/70 border border-white/15 p-3 text-xs text-white placeholder-white/40 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500 transition shadow-inner resize-none"
                  />
                  <div className="absolute right-2.5 bottom-2.5 flex items-center gap-1.5 text-[10px] text-white/40 font-mono pointer-events-none">
                    <span>Ctrl/⌘+Enter to submit</span>
                  </div>
                </div>

                {/* Right: Submit Button & Dynamic Cost Display */}
                <div className="flex flex-col items-stretch sm:items-end gap-1.5 shrink-0">
                  <div className="flex items-center justify-between sm:justify-end gap-2 text-[11px]">
                    <span className="text-white/50">Model Cost:</span>
                    {costEstimate.isAvailable ? (
                      <span className="font-semibold text-emerald-400 font-mono">
                        {costEstimate.formattedTotal}
                        {costEstimate.quantity > 1 && (
                          <span className="text-white/40 font-normal ml-1">
                            ({costEstimate.formattedPerUnit})
                          </span>
                        )}
                      </span>
                    ) : (
                      <span className="text-white/40 italic">Pricing unavailable</span>
                    )}
                  </div>

                  <button
                    type="button"
                    onClick={() => void handleExecuteGeneration()}
                    disabled={!canSubmit}
                    className="flex items-center justify-center gap-2 h-11 px-5 rounded-xl bg-gradient-to-r from-blue-600 to-indigo-600 hover:from-blue-500 hover:to-indigo-500 disabled:opacity-40 text-white font-bold text-xs shadow-lg transition cursor-pointer"
                    title={
                      !selectedModel
                        ? 'Select an active model'
                        : !quickRoutePrompt.trim()
                          ? 'Enter prompt instructions to submit'
                          : 'Generate revision'
                    }
                  >
                    {isWorking ? (
                      <>
                        <Loader2 size={15} className="animate-spin" />
                        <span>Generating...</span>
                      </>
                    ) : (
                      <>
                        <Sparkles size={15} className="fill-current text-white/90" />
                        <span>Route & Run Now</span>
                        <span className="sr-only">Auto-Revise</span>
                      </>
                    )}
                  </button>
                </div>
              </div>

              {/* Cost Disclosure Note & Submit Error */}
              <div className="flex flex-col gap-1">
                {submitError && (
                  <div className="flex items-center gap-1.5 text-xs text-rose-400 bg-rose-950/40 border border-rose-800/50 rounded-lg px-2.5 py-1.5">
                    <AlertCircle size={14} className="shrink-0" />
                    <span>{submitError}</span>
                  </div>
                )}
                <div className="flex items-center justify-between text-[10px] text-white/40">
                  <span>Estimated model charge from catalog metadata; excludes AI agent input tokens.</span>
                  {!modelGenOptions && (
                    <span className="text-white/30 italic">Using provider defaults.</span>
                  )}
                </div>
                {/* Note: In Planner and presetSuggestions removed per user instruction */}
              </div>

              {/* Live Pending / Queued Generation Jobs for this item */}
              {relevantJobs.length > 0 && (
                <div className="flex items-center gap-2 border-t border-white/10 pt-2 overflow-x-auto">
                  <span className="text-[10px] uppercase font-bold text-white/40 tracking-wider shrink-0 flex items-center gap-1">
                    <Clock size={11} /> Generations:
                  </span>
                  {relevantJobs.map((job) => (
                    <div
                      key={job.id}
                      className="flex items-center gap-1.5 bg-white/5 border border-white/10 rounded-lg px-2 py-1 text-[11px] shrink-0"
                    >
                      {job.status === 'queued' || job.status === 'in_progress' || job.status === 'running' ? (
                        <Loader2 size={12} className="animate-spin text-blue-400 shrink-0" />
                      ) : job.status === 'failed' ? (
                        <AlertCircle size={12} className="text-rose-400 shrink-0" />
                      ) : (
                        <Check size={12} className="text-emerald-400 shrink-0" />
                      )}
                      <span className="text-white/80 max-w-[120px] truncate">{job.title}</span>
                      <span className="font-mono text-[10px] text-white/40">({job.status})</span>
                    </div>
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
