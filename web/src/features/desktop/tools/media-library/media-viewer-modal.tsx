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
import { subscribeDesktopSessionReset } from '../../../../app/api'
import { DesignRevisionView } from './design-revision-view'
import { ArtifactDeliverableView } from './artifact-deliverable-view'
import { useDesignResource, useProjectDesigns } from './design-media'
import { desktopDesigns } from '../../runtime/desktop-design-runtime'
import { designMediaItem, designNodeId, designReadyItems, designRequestId } from '../../orchestrate/design-media-task'
import { designRefKey } from '../../session-v3/design-api'
import {
  getMediaSettingsCatalog,
  type MediaCatalogModelOption,
} from '../../settings/media/queries/get-media-settings'
import { saveImageDefaultModel } from '../../settings/swarm/mutations/save-image-default-model'
import { saveVideoDefaultModel } from '../../settings/swarm/mutations/save-video-models'
import { uiSettingsQueryKey, uiSettingsQueryOptions } from '../../../queries/query-options'
import {
  calculateGenerationCost,
  evaluateVideoActionSupport,
  getSupportedDurationsForResolution,
  extractGenerationDurationSeconds,
  resolveInitialModel,
  resolveInitialSetting,
  resolveVideoContinuationModel,
  validateMediaGenerationRequest,
  type VideoActionSupportResult,
  type MediaGenerationAction,
  type MediaGenerationJob,
  type MediaGenerationRequest,
  type MediaGenerationSettings,
} from './media-generation'

import { videoSections } from './video-sections'
import { getMediaIterationJobs, getMediaIterationOutputs, isMediaGenerationPending } from './media-iteration-thread'

export type QuickRouteMode = MediaGenerationAction

export interface MediaViewerModalProps {
  item: MediaLibraryItem | null
  items: readonly MediaLibraryItem[]
  threadItems?: readonly MediaLibraryItem[]
  onClose: () => void
  onSelect: (item: MediaLibraryItem) => void
  onOpenSession?: (sessionId: string) => void
  isTagged?: boolean
  onToggleTag?: (item: MediaLibraryItem) => void
  onGenerate?: (request: MediaGenerationRequest) => Promise<void>
  generationJobs?: readonly MediaGenerationJob[]
  initialQuickRouteMode?: QuickRouteMode | null
  isGenerating?: boolean
}

function modalTabStops(dialog: HTMLElement): HTMLElement[] {
  return Array.from(dialog.querySelectorAll<HTMLElement>('button, input, select, textarea, a[href], video[controls], audio[controls], iframe, [tabindex], [contenteditable="true"]'))
    .filter(node => node.tabIndex >= 0 && !node.matches(':disabled') && !node.closest('[inert]') && node.getClientRects().length > 0 && getComputedStyle(node).visibility !== 'hidden')
    .sort((a, b) => (a.tabIndex || Number.MAX_SAFE_INTEGER) - (b.tabIndex || Number.MAX_SAFE_INTEGER))
}

export function MediaViewerModal(props: MediaViewerModalProps) {
  if (props.item?.source === 'independent-design') return <DesignMediaViewer {...props} item={props.item} />
  return <MediaViewer {...props} />
}
function DesignMediaViewer(props: MediaViewerModalProps & { item: Extract<MediaLibraryItem, { source: 'independent-design' }> }) {
  const { item } = props
  const project = useProjectDesigns(item.design.projectId)
  const historyResource = useMemo(() => desktopDesigns.history(item.sessionId, item.design.revision.ref.artifact_id), [item.sessionId, item.design.revision.ref.artifact_id])
  const history = useDesignResource(historyResource)
  const editsResource = useMemo(() => desktopDesigns.editRequests(item.sessionId), [item.sessionId])
  const edits = useDesignResource(editsResource)
  const rows = project.data?.designs.filter(row => row.request.parent_session_id === item.sessionId) ?? []
  const ready = designReadyItems(rows)
  const revisions = (history.data?.revisions ?? []).map(revision => {
    const row = rows.find(row => row.request.id === revision.request_id)
    if (row) return designMediaItem(row, revision.candidate ?? item.design.candidate, revision)
    return { ...item, id: designNodeId(item.sessionId, revision.ref), design: { ...item.design, requestId: revision.request_id ?? item.design.requestId, candidate: revision.candidate ?? item.design.candidate, revision }, parentId: revision.base ? designNodeId(item.sessionId, revision.base) : undefined }
  })
  const threadItems = [...new Map([...(props.threadItems ?? props.items), ...ready, ...revisions, item].map(row => [row.id, row])).values()]
  const jobs: MediaGenerationJob[] = rows.map(row => {
    const outputs = [...new Map([...designReadyItems([row]), ...revisions.filter(output => output.design?.revision.request_id === row.request.id)].map(output => [output.id, output])).values()]
    const bases = [...new Map(row.request.candidates.flatMap(candidate => candidate.spec.base ? [[designRefKey(candidate.spec.base), candidate.spec.base] as const] : [])).values()]
    const base = bases.length === 1 ? bases[0] : undefined
    return { id: designRequestId(row), sourceId: base ? designNodeId(item.sessionId, base) : outputs[0]?.id ?? '', outputIds: outputs.map(output => output.id), title: row.title, count: row.request.candidates.length, status: row.request.state, sessionId: item.sessionId, error: row.request.candidates.flatMap(candidate => [candidate.failure_reason, candidate.router_alert].filter(Boolean)).join('; ') }
  })
  // Retained revisions can outlive the currently loaded request catalog page.
  const historicalRequests = new Set(revisions.map(output => output.design?.requestId).filter((id): id is string => Boolean(id)))
  for (const requestId of historicalRequests) {
    if (rows.some(row => row.request.id === requestId)) continue
    const outputs = revisions.filter(output => output.design?.requestId === requestId)
    const base = outputs.find(output => output.parentId)?.parentId
    jobs.push({ id: JSON.stringify(['design-request', item.sessionId, requestId]), sourceId: base ?? outputs[0]?.id ?? '', outputIds: outputs.map(output => output.id), title: outputs[0]?.title ?? 'Retained design', count: outputs.length, status: 'succeeded', sessionId: item.sessionId })
  }
  for (const edit of edits.data?.edits ?? []) {
    if (rows.some(row => row.request.source_message_id === edit.messageId && row.request.client_request_id === edit.clientRequestId && row.request.candidates.some(candidate => candidate.spec.base && designRefKey(candidate.spec.base) === designRefKey(edit.base)))) continue
    jobs.push({ id: `design-edit:${item.sessionId}:${edit.messageId}`, sourceId: designNodeId(item.sessionId, edit.base), title: 'Edit requested · awaiting parent acceptance', status: 'requested', count: 1 })
  }
  return <MediaViewer {...props} threadItems={threadItems} generationJobs={jobs} onGenerate={undefined} onToggleTag={undefined} />
}

function MediaViewer({
  item,
  items,
  threadItems = items,
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
  useEffect(() => subscribeDesktopSessionReset(onClose), [onClose])
  const dialogRef = useRef<HTMLDivElement>(null)
  const isOpen = Boolean(item)
  useEffect(() => {
    if (!isOpen) return
    const dialog = dialogRef.current
    if (!dialog) return
    const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const background = new Map<HTMLElement, boolean>()
    const isolate = () => {
      // The viewer is nested in the library, so inert siblings at every ancestor,
      // never an ancestor containing the dialog itself.
      for (let branch: HTMLElement = dialog; branch.parentElement; branch = branch.parentElement) {
        for (const sibling of Array.from(branch.parentElement.children)) {
          if (sibling instanceof HTMLElement && sibling !== branch && !background.has(sibling)) {
            background.set(sibling, sibling.inert)
            sibling.inert = true
          }
        }
      }
    }
    isolate()
    const observer = new MutationObserver(isolate)
    for (let ancestor = dialog.parentElement; ancestor; ancestor = ancestor.parentElement) {
      observer.observe(ancestor, { childList: true })
    }
    const focusInside = () => (dialog.querySelector<HTMLElement>('[aria-label="Close viewer"]') || dialog).focus()
    const containFocus = (event: FocusEvent) => {
      if (event.target instanceof Node && !dialog.contains(event.target)) focusInside()
    }
    document.addEventListener('focusin', containFocus)
    focusInside()
    return () => {
      observer.disconnect()
      document.removeEventListener('focusin', containFocus)
      for (const [node, wasInert] of background) node.inert = wasInert
      if (trigger?.isConnected) trigger.focus()
    }
  }, [isOpen])

  const videoRef = useRef<HTMLVideoElement>(null)
  const [sectionIndex, setSectionIndex] = useState(0)
  const sections = useMemo(() => item ? videoSections(item, threadItems) : [], [item, threadItems])
  useEffect(() => { setSectionIndex(0) }, [item?.id])
  const [zoomLevel, setZoomLevel] = useState(1)
  const [showInfo, setShowInfo] = useState(true)
  const [copied, setCopied] = useState(false)
  const [localSubmitting, setLocalSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [defaultSaved, setDefaultSaved] = useState(false)
  const [selectedTurn, setSelectedTurn] = useState<MediaGenerationJob | null>(null)
  const selectedTurnRef = useRef<HTMLButtonElement>(null)
  const activeJob = selectedTurn && (generationJobs.find((job) => job.id === selectedTurn.id || (selectedTurn.taskId && job.taskId === selectedTurn.taskId)) || selectedTurn)
  const turnOutputs = useMemo(() => getMediaIterationOutputs(activeJob || undefined, threadItems), [activeJob, threadItems])
  const showingTurn = Boolean(activeJob && item && (activeJob.sourceId === item.id || activeJob.outputIds?.includes(item.id)))
  const showingRequest = showingTurn && !turnOutputs.some((output) => output.id === item?.id)

  const selectAsset = useCallback((nextItem: MediaLibraryItem) => {
    setSelectedTurn(null)
    onSelect(nextItem)
  }, [onSelect])

  useEffect(() => {
    if (!item) setSelectedTurn(null)
  }, [item?.id])

  // Follow only the selected request. Older jobs completing must not steal focus.
  useEffect(() => {
    if (showingTurn && turnOutputs.length > 0 && item?.id === activeJob?.sourceId) {
      if (turnOutputs[0].id !== item?.id) onSelect(turnOutputs[0])
    }
  }, [showingTurn, turnOutputs, item?.id, activeJob?.sourceId, onSelect])

  useEffect(() => {
    selectedTurnRef.current?.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth' })
  }, [selectedTurn?.id])

  // Stable locks and identity tracking
  const isSubmittingRef = useRef(false)
  const lastInitializedItemIdRef = useRef<string | null>(null)
  const lastModelIdRef = useRef<string>('')
  const hasInitializedSettingsRef = useRef(false)

  // Query media catalog and UI settings
  const queryClient = useQueryClient()
  const { data: mediaCatalog, isLoading: catalogLoading, error: catalogError } = useQuery({
    queryKey: ['media-settings-catalog'],
    queryFn: () => getMediaSettingsCatalog(),
    staleTime: 60_000,
  })
  const { data: uiSettings } = useQuery(uiSettingsQueryOptions())

  const isVideoSource = item?.kind === 'video'
  const isImageSource = item?.kind === 'image'
  const sourceModel = item?.model?.trim() || ''
  const sourceAspectRatio = item?.aspectRatio?.trim() || ''
  const sourceResolution = item?.resolution?.trim() || ''
  const sourceDurationSeconds = item?.durationSeconds
  const sourceProvenance = item?.videoProvenance ?? item?.artifact?.videoProvenance ?? null

  const allVideoModels = useMemo(() => {
    return mediaCatalog?.video_models ?? mediaCatalog?.video_generation_models ?? []
  }, [mediaCatalog])

  const allImageModels = useMemo(() => {
    return mediaCatalog?.image_models ?? []
  }, [mediaCatalog])

  const sourceModelOption = useMemo(() => {
    if (!sourceModel) return undefined
    return resolveVideoContinuationModel(sourceModel, isVideoSource ? allVideoModels : allImageModels, sourceProvenance?.provider).modelOption
  }, [allImageModels, allVideoModels, isVideoSource, sourceModel, sourceProvenance])

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
    ((activeQuickRouteMode === 'fine_tune' || activeQuickRouteMode === 'iterate') && isVideoSource)

  const nextSceneSupport = useMemo<VideoActionSupportResult>(() => {
    if (!item || item.kind !== 'video') return { supported: false, reason: 'Next Scene is only supported for videos.' }
    return evaluateVideoActionSupport('next_scene', item, sourceModelOption)
  }, [item, sourceModelOption])

  const fineTuneSupport = useMemo<VideoActionSupportResult>(() => {
    if (!item) return { supported: false }
    if (item.kind !== 'video') return { supported: true }
    return evaluateVideoActionSupport('fine_tune', item, sourceModelOption)
  }, [item, sourceModelOption])

  const iterateSupport = useMemo<VideoActionSupportResult>(() => {
    if (!item) return { supported: false }
    if (item.kind !== 'video') return { supported: true }
    return evaluateVideoActionSupport('iterate', item, sourceModelOption)
  }, [item, sourceModelOption])

  const toVideoSupport = useMemo<VideoActionSupportResult>(() => {
    if (!item || item.kind !== 'image') return { supported: false }
    return { supported: true }
  }, [item])

  const currentActionSupport = useMemo<VideoActionSupportResult>(() => {
    if (activeQuickRouteMode === 'next_scene') return nextSceneSupport
    if (activeQuickRouteMode === 'fine_tune') return fineTuneSupport
    if (activeQuickRouteMode === 'iterate') return iterateSupport
    if (activeQuickRouteMode === 'to_video') return toVideoSupport
    return { supported: true }
  }, [activeQuickRouteMode, fineTuneSupport, iterateSupport, nextSceneSupport, toVideoSupport])

  const activeLockedOptions = currentActionSupport.lockedOptions

  // Available models based on action mode:
  // to_video selects video_generation_models with initial_image support
  // video fine_tune and next_scene lock to generating model (no cross-family switch)
  // image actions select image_models
  const availableModels: MediaCatalogModelOption[] = useMemo(() => {
    if (activeQuickRouteMode === 'to_video') {
      const allVid = mediaCatalog?.video_generation_models ?? mediaCatalog?.video_models ?? []
      return allVid.filter((m) => {
        const initImg = m.generation_options?.initial_image
        const initConstraint = m.constraints?.create?.initial_image_supported
        return m.constraints?.create?.supported === true && (initConstraint === true || initImg?.supported === true)
      })
    }
    if (isVideoSource && (activeQuickRouteMode === 'next_scene' || activeQuickRouteMode === 'fine_tune' || activeQuickRouteMode === 'iterate')) {
      if (sourceModelOption) return [sourceModelOption]
      if (sourceModel) {
        return [{
          id: sourceModel,
          provider: sourceProvenance?.provider || 'unknown',
          model: sourceModel,
          display_name: sourceModel,
          kind: 'video_generation',
          ready: false,
          reason: 'Source model is unavailable in the catalog.',
        }]
      }
      return []
    }
    if (isImageSource && (activeQuickRouteMode === 'fine_tune' || activeQuickRouteMode === 'iterate')) {
      const imgs = mediaCatalog?.image_models ?? []
      if (sourceModel && !imgs.some((m) => m.id === sourceModel || m.model === sourceModel)) {
        return [
          {
            id: sourceModel,
            provider: 'unknown',
            model: sourceModel,
            display_name: `${sourceModel} (unavailable)`,
            kind: 'image_generation',
            ready: false,
            reason: 'Model is unavailable or not configured in catalog',
          },
          ...imgs,
        ]
      }
      return imgs
    }
    return mediaCatalog?.image_models ?? []
  }, [activeQuickRouteMode, isImageSource, isVideoSource, mediaCatalog, sourceModel, sourceModelOption, sourceProvenance])

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
    if (activeQuickRouteMode === 'next_scene' || ((activeQuickRouteMode === 'fine_tune' || activeQuickRouteMode === 'iterate') && isVideoSource)) {
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
    if (isVideoSource && (activeQuickRouteMode === 'next_scene' || activeQuickRouteMode === 'fine_tune' || activeQuickRouteMode === 'iterate')) {
      return sourceModelOption
    }
    return availableModels.find((m) => m.id === selectedModel)
  }, [activeQuickRouteMode, availableModels, isVideoSource, selectedModel, sourceModelOption])

  const modelGenOptions = selectedModelOption?.generation_options

  // Supported options directly from model metadata and active locks
  const supportedRatios = useMemo(() => {
    if (activeLockedOptions?.aspectRatio) {
      return [activeLockedOptions.aspectRatio]
    }
    if (modelGenOptions?.aspect_ratios && modelGenOptions.aspect_ratios.length > 0) {
      return modelGenOptions.aspect_ratios
    }
    return []
  }, [activeLockedOptions, modelGenOptions])

  const supportedResolutions = useMemo(() => {
    if (activeLockedOptions?.resolution) {
      return [activeLockedOptions.resolution]
    }
    if (modelGenOptions?.resolutions && modelGenOptions.resolutions.length > 0) {
      return modelGenOptions.resolutions
    }
    return []
  }, [activeLockedOptions, modelGenOptions])

  const supportedDurations = useMemo(() => {
    if (activeLockedOptions?.durationSeconds !== undefined) {
      return [activeLockedOptions.durationSeconds]
    }
    if (activeLockedOptions?.supportsDuration === false) {
      return []
    }
    if (modelGenOptions?.durations && modelGenOptions.durations.length > 0) {
      if (modelGenOptions.resolution_durations && resolution) {
        return getSupportedDurationsForResolution(selectedModelOption, resolution)
      }
      return modelGenOptions.durations
    }
    return []
  }, [activeLockedOptions, modelGenOptions, resolution, selectedModelOption])

  // Initialize viewer state when active item ID changes.
  // Must NOT reset user changes on every item object reference identity refresh!
  useEffect(() => {
    if (!item) {
      lastInitializedItemIdRef.current = null
      hasInitializedSettingsRef.current = false
      return
    }
    if (!mediaCatalog) return
    if (lastInitializedItemIdRef.current === item.id) {
      return
    }
    lastInitializedItemIdRef.current = item.id
    hasInitializedSettingsRef.current = false

    setZoomLevel(1)
    setCopied(false)
    setQuickRoutePrompt('')
    setSubmitError(null)
    setLocalSubmitting(false)
    isSubmittingRef.current = false

    const isVid = item.kind === 'video'
    const newMode: QuickRouteMode = initialQuickRouteMode ?? (isVid ? 'next_scene' : 'fine_tune')
    setActiveQuickRouteMode(newMode)
    setVariantCount(newMode === 'iterate' ? 4 : 1)

    let chosenModelId = ''
    if (isVid) {
      // For video continuation / fine-tune: keep exact generating model!
      // Unknown source stays explicitly unknown; don't silently replace unavailable known model with default.
      if (item.model && item.model.trim()) {
        const cont = resolveVideoContinuationModel(item.model, allVideoModels, sourceProvenance?.provider)
        chosenModelId = cont.modelId
      } else {
        chosenModelId = ''
      }
    } else {
      if (newMode === 'to_video') {
        const toVidModels = allVideoModels.filter((m) => {
          const initImg = m.generation_options?.initial_image
          const initConstraint = m.constraints?.create?.initial_image_supported
          return m.constraints?.create?.supported === true && (initConstraint === true || initImg?.supported === true)
        })
        chosenModelId = resolveInitialModel(undefined, toVidModels, defaultVideoGenerationModel)
      } else {
        // Image editing: opening image with saved model unavailable must not fallback silently to default!
        chosenModelId = item.model ? resolveVideoContinuationModel(item.model, mediaCatalog.image_models ?? []).modelId : ''
      }
    }

    setSelectedModel(chosenModelId)
    lastModelIdRef.current = chosenModelId

    const chosenOption = isVid
      ? allVideoModels.find((m) => m.id === chosenModelId || m.model === chosenModelId)
      : (mediaCatalog.image_models ?? []).find((m) => m.id === chosenModelId) || allVideoModels.find((m) => m.id === chosenModelId)
    const genOpts = chosenOption?.generation_options
    const actionSupport: VideoActionSupportResult = isVid ? evaluateVideoActionSupport(newMode, item, chosenOption) : { supported: true }
    const locks = actionSupport.lockedOptions

    const initRatio = locks?.aspectRatio ?? resolveInitialSetting(
      item.aspectRatio,
      genOpts?.aspect_ratios,
      genOpts?.default_ratio,
      { preserveUnsupported: true },
    ) ?? (item.aspectRatio || '')
    setAspectRatio(initRatio)

    const initRes = locks?.resolution ?? resolveInitialSetting(
      item.resolution,
      genOpts?.resolutions,
      genOpts?.default_resolution,
      { preserveUnsupported: true },
    ) ?? (item.resolution || '')
    setResolution(initRes)

    const rawDur = extractGenerationDurationSeconds(item)
    const initDur = locks?.durationSeconds !== undefined
      ? locks.durationSeconds
      : locks?.supportsDuration === false
      ? undefined
      : resolveInitialSetting(rawDur, genOpts?.durations, genOpts?.default_duration)
    setDurationSeconds(initDur)

    hasInitializedSettingsRef.current = true
  }, [
    allVideoModels,
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
      if (activeLockedOptions?.aspectRatio) setAspectRatio(activeLockedOptions.aspectRatio)
      else setAspectRatio('')
      if (activeLockedOptions?.resolution) setResolution(activeLockedOptions.resolution)
      else setResolution('')
      if (activeLockedOptions?.durationSeconds !== undefined) setDurationSeconds(activeLockedOptions.durationSeconds)
      else setDurationSeconds(undefined)
      return
    }

    setAspectRatio(activeLockedOptions?.aspectRatio ?? resolveInitialSetting(aspectRatio, modelGenOptions.aspect_ratios, modelGenOptions.default_ratio) ?? '')
    setResolution(activeLockedOptions?.resolution ?? resolveInitialSetting(resolution, modelGenOptions.resolutions, modelGenOptions.default_resolution) ?? '')
    setDurationSeconds(activeLockedOptions?.durationSeconds ?? (activeLockedOptions?.supportsDuration === false ? undefined : resolveInitialSetting(durationSeconds, modelGenOptions.durations, modelGenOptions.default_duration)))
  }, [selectedModel, modelGenOptions, aspectRatio, resolution, durationSeconds, activeLockedOptions])

  // Adjust duration when resolution changes if resolution_durations is defined
  useEffect(() => {
    if (supportedDurations.length > 0 && durationSeconds !== undefined) {
      if (!supportedDurations.includes(durationSeconds)) {
        setDurationSeconds(supportedDurations[0])
      }
    }
  }, [supportedDurations, durationSeconds])

  const handleModeChange = useCallback(
    (newMode: QuickRouteMode) => {
      if (isVideoSource && item && !evaluateVideoActionSupport(newMode, item, sourceModelOption).supported) return
      setActiveQuickRouteMode(newMode)
      if (newMode === 'iterate') {
        setVariantCount((prev) => (prev > 1 ? prev : 4))
      } else {
        setVariantCount(1)
      }

      if (isVideoSource) {
        if (sourceModel) {
          setSelectedModel(sourceModelOption?.id || sourceModel)
        } else {
          setSelectedModel('')
        }
        const support = evaluateVideoActionSupport(newMode, item!, sourceModelOption)
        if (support.lockedOptions) {
          if (support.lockedOptions.aspectRatio) setAspectRatio(support.lockedOptions.aspectRatio)
          if (support.lockedOptions.resolution) setResolution(support.lockedOptions.resolution)
          if (support.lockedOptions.durationSeconds !== undefined) setDurationSeconds(support.lockedOptions.durationSeconds)
          else if (support.lockedOptions.supportsDuration === false) setDurationSeconds(undefined)
        }
      } else {
        if (newMode === 'to_video') {
          const toVidModels = allVideoModels.filter((m) => {
            const initImg = m.generation_options?.initial_image
            const initConstraint = m.constraints?.create?.initial_image_supported
            return m.constraints?.create?.supported === true && (initConstraint === true || initImg?.supported === true)
          })
          const chosen = resolveInitialModel(undefined, toVidModels, defaultVideoGenerationModel)
          setSelectedModel(chosen)
        } else {
          const chosen = item?.model ? resolveVideoContinuationModel(item.model, mediaCatalog?.image_models ?? []).modelId : ''
          setSelectedModel(chosen)
        }
      }
    },
    [allVideoModels, defaultImageModel, defaultVideoGenerationModel, isVideoSource, item, mediaCatalog, sourceModel, sourceModelOption],
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
    } catch (error) {
      setSubmitError(error instanceof Error ? error.message : 'Could not save default model.')
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
    if (!item || !onGenerate || !selectedModelOption?.ready || !hasInitializedSettingsRef.current || lastInitializedItemIdRef.current !== item.id || isSubmittingRef.current || localSubmitting || isGenerating || showingRequest) return
    const prompt = quickRoutePrompt.trim()
    if (!prompt) return

    const generationSettings: MediaGenerationSettings = {
      aspectRatio: aspectRatio.trim() ? aspectRatio.trim() : undefined,
      resolution: resolution.trim() ? resolution.trim() : undefined,
      durationSeconds: isVideoAction && durationSeconds && durationSeconds > 0 ? durationSeconds : undefined,
    }

    // Preflight validation gate
    const validation = validateMediaGenerationRequest({
      action: activeQuickRouteMode,
      item,
      model: selectedModel,
      modelOption: selectedModelOption,
      prompt,
      settings: generationSettings,
      variantCount: activeQuickRouteMode === 'iterate' ? variantCount : 1,
    })

    if (!validation.valid) {
      setSubmitError(validation.error || 'Generation request cannot be submitted.')
      return
    }

    isSubmittingRef.current = true
    setLocalSubmitting(true)
    setSubmitError(null)

    const requestId = crypto.randomUUID()
    const pendingTurn: MediaGenerationJob = {
      id: requestId,
      sourceId: item.id,
      title: 'New iteration',
      prompt,
      count: activeQuickRouteMode === 'iterate' ? variantCount : 1,
      createdAt: Date.now(),
      status: 'submitting',
    }
    setSelectedTurn(pendingTurn)

    try {
      if (onGenerate) {
        await onGenerate({
          requestId,
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
      const error = err instanceof Error ? err.message : 'Generation request failed.'
      setSubmitError(error)
      setSelectedTurn((turn) => turn?.id === requestId ? { ...turn, status: 'failed', error } : turn)
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
    showingRequest,
    quickRoutePrompt,
    resolution,
    selectedModel,
    selectedModelOption,
    variantCount,
  ])

  // Current index in items
  const currentIndex = item ? items.findIndex((i) => i.id === item.id) : -1
  const hasPrev = currentIndex > 0
  const hasNext = currentIndex >= 0 && currentIndex < items.length - 1

  const handlePrev = useCallback(() => {
    if (hasPrev) {
      selectAsset(items[currentIndex - 1])
    }
  }, [currentIndex, hasPrev, items, selectAsset])

  const handleNext = useCallback(() => {
    if (hasNext) {
      selectAsset(items[currentIndex + 1])
    }
  }, [currentIndex, hasNext, items, selectAsset])

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

  const relevantJobs = useMemo(() => {
    const jobs = selectedTurn && !generationJobs.some((job) => job.id === selectedTurn.id || (selectedTurn.taskId && job.taskId === selectedTurn.taskId))
      ? [...generationJobs, selectedTurn] : generationJobs
    return getMediaIterationJobs(item?.id, threadItems, jobs)
  }, [generationJobs, item?.id, threadItems, selectedTurn])

  const turnIndex = relevantJobs.findIndex(job => showingTurn ? job.id === activeJob?.id : job.outputIds?.includes(item?.id ?? ''))
  const openTurn = (job: MediaGenerationJob) => {
    const outputs = getMediaIterationOutputs(job, threadItems)
    const target = outputs[0] ?? threadItems.find(source => source.id === job.sourceId)
    if (!target) return
    setSelectedTurn(job)
    onSelect(target)
  }

  // Build Lineage & History Chain
  const lineageChain = useMemo(() => {
    if (!item) return []
    const chain: { role: 'parent' | 'current' | 'child' | 'sibling'; item: MediaLibraryItem; label: string }[] = []
    const parentId = item.parentId || item.sourceMediaRef
    if (parentId && parentId !== item.id) {
      const parent = threadItems.find((i) => i.id === parentId)
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
    const children = threadItems.filter((i) => i.id !== item.id && (i.parentId === item.id || i.sourceMediaRef === item.id))
    for (const child of children) {
      chain.push({
        role: 'child',
        item: child,
        label: child.kind === 'video' ? 'Video Continuation' : 'Derived Revision',
      })
    }
    if (item.iterationGroupId) {
      const existingIds = new Set(chain.map((c) => c.item.id))
      const siblings = threadItems.filter((i) => !existingIds.has(i.id) && i.iterationGroupId === item.iterationGroupId)
      for (const sib of siblings) {
        chain.push({
          role: 'sibling',
          item: sib,
          label: `Variant ${sib.variantIndex ?? ''}`,
        })
      }
    }
    return chain
  }, [item, threadItems])

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

  const activeValidation = (() => {
    if (!item || !selectedModelOption) return { valid: false, error: 'Model selection required.' }
    return validateMediaGenerationRequest({
      action: activeQuickRouteMode,
      item,
      model: selectedModel,
      modelOption: selectedModelOption,
      prompt: quickRoutePrompt.trim() || 'preview',
      settings: {
        aspectRatio: aspectRatio.trim() ? aspectRatio.trim() : undefined,
        resolution: resolution.trim() ? resolution.trim() : undefined,
        durationSeconds: isVideoAction && durationSeconds && durationSeconds > 0 ? durationSeconds : undefined,
      },
      variantCount: activeQuickRouteMode === 'iterate' ? variantCount : 1,
    })
  })()

  const isWorking = isGenerating || localSubmitting
  const canSubmit =
    !isSubmittingRef.current &&
    !isWorking &&
    !showingRequest &&
    Boolean(quickRoutePrompt.trim()) &&
    Boolean(onGenerate && selectedModelOption?.ready && hasInitializedSettingsRef.current && lastInitializedItemIdRef.current === item.id && currentActionSupport.supported && activeValidation.valid)

  return (
    <div
      ref={dialogRef}
      role="dialog"
      aria-modal="true"
      tabIndex={-1}
      onClick={(event) => { if (event.target === event.currentTarget) onClose() }}
      onKeyDown={(event) => {
        if (event.key !== 'Tab') return
        const controls = modalTabStops(event.currentTarget)
        const index = controls.indexOf(document.activeElement as HTMLElement)
        event.preventDefault()
        if (controls.length === 0) { event.currentTarget.focus(); return }
        const next = index < 0 ? (event.shiftKey ? controls.length - 1 : 0) : (index + (event.shiftKey ? -1 : 1) + controls.length) % controls.length
        controls[next].focus()
      }}
      aria-label={`Media viewer: ${item.title}`}
      style={{ containerType: 'inline-size' }}
      className="media-viewer fixed inset-0 z-50 flex flex-col bg-black/92 backdrop-blur-xl text-[var(--app-text)] animate-in fade-in duration-150"
    >
      <style>{`
        .media-viewer { height: 100dvh; padding: env(safe-area-inset-top) env(safe-area-inset-right) env(safe-area-inset-bottom) env(safe-area-inset-left); }
        .media-viewer button, .media-viewer select { min-height: 44px; min-width: 44px; }
        .media-viewer :is(button, input, select, textarea, [tabindex]):focus-visible { outline: 2px solid #60a5fa; outline-offset: -2px; }
        .media-viewer header { height: auto; min-height: 56px; max-height: 35%; overflow-y: auto; flex-wrap: wrap; gap: 8px; padding-block: 8px; }
        .media-viewer header > div:first-child { flex: 1 1 240px; }
        .media-viewer header > div:last-child { flex-wrap: wrap; max-width: 100%; }
        .media-viewer header button[aria-label="Close viewer"] { order: -1; }
        .media-viewer footer, .media-viewer aside { overflow-wrap: anywhere; }
        .media-viewer footer div { min-width: 0; max-width: 100%; }
        .media-viewer footer select { min-width: 0; max-width: 100%; }
        .media-viewer footer div:has(> select) { flex-wrap: wrap; }
        .media-viewer main > div { min-width: 0; max-height: 100%; }
        .media-viewer main img { max-height: 100%; object-fit: contain; }
        .media-viewer main video, .media-viewer main iframe { max-height: 100%; }
        .media-viewer aside :is(p, span) { overflow-wrap: anywhere; }
        .media-viewer footer .inline-flex { flex-wrap: wrap; }
        .media-viewer footer textarea { padding-bottom: 32px; }
        .media-viewer aside { display: block; }
        @container (max-width: 1000px) {
          .media-viewer-workspace { display: block; overflow-y: auto; }
          .media-viewer-center { overflow: visible; }
          .media-viewer main { flex: none; min-height: 220px; height: 35dvh; padding: 16px; }
          .media-viewer footer { max-height: none; overflow: visible; padding: 16px; }
          .media-viewer footer > div > div { flex-wrap: wrap; }
          .media-viewer-prompt { flex-direction: column; align-items: stretch; }
          .media-viewer aside { width: 100%; border-left: 0; border-top: 1px solid #ffffff1a; }
          .media-viewer header .hidden { display: none; }
          .media-viewer-nav { position: absolute; top: 8px; left: 8px; right: auto; transform: none; margin: 0; }
          .media-viewer-nav[aria-label="Next item"] { left: auto; right: 8px; }
          .media-viewer main { padding-inline: 60px; }
        }
      `}</style>
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
            <h2 className="truncate text-sm font-semibold text-white tracking-wide">{showingRequest && activeJob ? activeJob.title : item.title}</h2>
            <div className="flex items-center gap-2 truncate text-xs text-white/50">
              <span className="truncate">{item.filename}</span>
              <span>·</span>
              <span>{item.formattedDate} at {item.formattedTime}</span>
              {items.length > 1 && currentIndex >= 0 && (
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
          {item.source !== 'independent-design' && <button
            type="button"
            onClick={handleCopyLink}
            title={copied ? 'URL Copied!' : 'Copy direct URL'}
            aria-label="Copy direct URL"
            className="flex items-center gap-1.5 rounded-lg bg-white/10 px-3 py-1.5 text-xs font-medium text-white transition hover:bg-white/20"
          >
            {copied ? <Check size={14} className="text-emerald-400" /> : <Copy size={14} />}
            <span className="hidden sm:inline">{copied ? 'Copied' : 'Copy link'}</span>
          </button>

          }
          {/* Tag for Task */}
          {item.source !== 'independent-design' && onToggleTag && (
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
          {item.kind === 'image' && (
            <button
              type="button"
              onClick={() => {
                if (isVideoSource && !iterateSupport.supported) return
                handleModeChange('iterate')
              }}
              disabled={isVideoSource && !iterateSupport.supported}
              title={
                isVideoSource && !iterateSupport.supported
                  ? (iterateSupport.reason || 'Iterations not supported for this video')
                  : 'Configure and run Swarm Iterations for this media'
              }
              aria-label="Swarm Iterations"
              className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold shadow-md transition ${
                isVideoSource && !iterateSupport.supported
                  ? 'bg-white/10 text-white/40 cursor-not-allowed opacity-50'
                  : 'bg-emerald-600 hover:bg-emerald-500 text-white cursor-pointer'
              }`}
            >
              <Sparkles size={14} />
              <span className="hidden sm:inline">Swarm Iterations</span>
            </button>
          )}

          {/* Download */}
          {item.source !== 'independent-design' && <button
            type="button"
            onClick={handleDownload}
            title="Download media file"
            aria-label="Download media file"
            className="flex items-center gap-1.5 rounded-lg bg-white/10 px-3 py-1.5 text-xs font-medium text-white transition hover:bg-white/20"
          >
            <Download size={14} />
            <span className="hidden sm:inline">Download</span>
          </button>

          }
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
            aria-expanded={showInfo}
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
      <div className="media-viewer-workspace relative flex min-h-0 flex-1 overflow-hidden">
        {/* Left Arrow */}
        {hasPrev && (
          <button
            type="button"
            onClick={handlePrev}
            aria-label="Previous item"
            className="media-viewer-nav absolute left-4 top-1/2 -translate-y-1/2 z-20 flex size-10 items-center justify-center rounded-full bg-black/60 text-white/80 hover:bg-black/90 hover:text-white border border-white/20 transition backdrop-blur-sm"
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
            className="media-viewer-nav absolute right-4 top-1/2 -translate-y-1/2 z-20 flex size-10 items-center justify-center rounded-full bg-black/60 text-white/80 hover:bg-black/90 hover:text-white border border-white/20 transition backdrop-blur-sm"
          >
            <ChevronRight size={22} />
          </button>
        )}

        {/* Center Display & Bottom Dock */}
        <div className="media-viewer-center flex flex-1 flex-col min-w-0 min-h-0 overflow-hidden">
          {relevantJobs.length > 0 && <nav aria-label="Viewer turn navigation" className="flex shrink-0 flex-wrap items-center justify-center gap-3 border-b border-white/10 px-4 py-2 text-xs text-white/70">
            <button type="button" disabled={turnIndex <= 0} onClick={() => openTurn(relevantJobs[turnIndex - 1])}>← Prev Turn</button>
            <span>{turnIndex >= 0 ? `Turn ${turnIndex + 1} of ${relevantJobs.length}` : 'Source output'}</span>
            <button type="button" disabled={turnIndex >= relevantJobs.length - 1} onClick={() => openTurn(relevantJobs[turnIndex + 1])}>Next Turn →</button>
          </nav>}
          {/* Media Canvas */}
          <main onClick={(event) => { if (event.target === event.currentTarget) onClose() }} className="flex flex-1 items-center justify-center overflow-auto p-4 sm:p-6 min-h-0">
            {showingRequest && item.kind !== 'video' && activeJob && (
              <section role="status" aria-live="polite" className="w-full max-w-xl rounded-2xl border border-white/15 bg-white/5 p-6 text-center">
                {isMediaGenerationPending(activeJob.status)
                  ? <Loader2 size={32} className="mx-auto mb-4 animate-spin text-blue-400" />
                  : <AlertCircle size={32} className="mx-auto mb-4 text-amber-400" />}
                <h3 className="text-lg font-semibold text-white">{isMediaGenerationPending(activeJob.status) ? 'Iteration pending' : activeJob.status === 'completed' ? 'Loading iteration outputs' : activeJob.status.replace(/_/g, ' ')}</h3>
                <p className="mt-3 max-h-32 overflow-y-auto whitespace-pre-wrap break-words text-sm text-white/80">{activeJob.prompt || activeJob.title}</p>
                <p className="mt-3 text-xs text-white/50">{activeJob.count} outputs · {activeJob.status.replace(/_/g, ' ')}</p>
                {activeJob.error && <p role="alert" className="mt-3 text-sm text-rose-300">{activeJob.error}</p>}
                <button type="button" onClick={() => setSelectedTurn(null)} className="mt-4 rounded-lg bg-white/10 px-3 py-2 text-xs text-white">Back to source</button>
              </section>
            )}
            {!showingRequest && item.kind === 'image' && (
              <div className="relative flex h-full items-center justify-center max-h-full max-w-full">
                <img
                  src={item.directUrl}
                  alt={item.title}
                  style={{
                    transform: `scale(${zoomLevel})`,
                    transition: 'transform 0.15s ease-out',
                  }}
                  className="max-h-full max-w-full rounded-lg object-contain shadow-2xl select-none border border-white/10"
                />
              </div>
            )}

            {item.kind === 'video' && (
              <div className="flex w-full max-w-4xl flex-col items-center justify-center">
                <video
                  ref={videoRef}
                  aria-label="Full selected video"
                  onTimeUpdate={(event) => {
                    const time = event.currentTarget.currentTime
                    const index = sections.findIndex(section => time >= section.start && time < section.end)
                    if (index >= 0) setSectionIndex(index)
                  }}
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

            {!showingRequest && item.kind === 'audio' && (
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

            {!showingRequest && item.source === 'independent-design' && <DesignRevisionView key={item.id} item={item} onSelect={onSelect} />}
            {!showingRequest && item.source !== 'independent-design' && (item.kind === 'animation' || item.kind === 'document') && <ArtifactDeliverableView key={item.id} item={item} />}
          </main>

          {item.kind === 'video' && <section aria-label="Video sections" className="shrink-0 border-t border-white/10 px-4 py-3 text-xs text-white/80">
            <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
              <span>Full video · {sections[sections.length - 1]?.end.toFixed(2) ?? 'Unknown duration'}{sections.length ? 's' : ''}</span>
              {sections[sectionIndex] && sections[sectionIndex].source.id !== item.id && <button type="button" className="rounded bg-white/10 px-3" onClick={() => selectAsset(sections[sectionIndex].source)}>Use this retained version as source</button>}
            </div>
            <div className="flex gap-2 overflow-x-auto">
              {sections.map((section, index) => <button type="button" key={section.source.id} aria-pressed={index === sectionIndex} aria-label={`Seek section ${index + 1}`} onClick={() => { if (videoRef.current) videoRef.current.currentTime = section.start; setSectionIndex(index) }} className={`min-w-32 shrink-0 rounded-lg border p-2 text-left ${index === sectionIndex ? 'border-blue-400 bg-blue-500/15' : 'border-white/20'}`}>
                <Film size={16} /><span className="block">Section {index + 1}</span><span>{section.start.toFixed(2)}–{section.end.toFixed(2)}s</span>
              </button>)}
            </div>
            <p className="mt-2 text-white/50">{sections.length ? 'Seeking only changes playback. Continue uses the source version named below, not the playhead.' : 'Clip boundaries are not verified. Choose a retained version below; arbitrary playhead continuation is unavailable.'}</p>
            {showingRequest && activeJob && <div role="status" className="mt-2 flex flex-wrap items-center gap-2">
              {isMediaGenerationPending(activeJob.status) && <Loader2 size={14} className="animate-spin" />}
              <span>{activeJob.status === 'completed' ? 'Loading ready output…' : activeJob.status.replace(/_/g, ' ')}</span>
              {activeJob.error && <span role="alert">{activeJob.error}</span>}
              <button type="button" onClick={() => { setQuickRoutePrompt(activeJob.prompt ?? ''); setSelectedTurn(null) }}>{activeJob.status === 'failed' || activeJob.status === 'cancelled' ? 'Retry with this prompt' : 'Back to source'}</button>
            </div>}
          </section>}

          {relevantJobs.length > 0 && (
            <nav aria-label="Iteration timeline" className="max-h-[28vh] shrink-0 overflow-y-auto border-t border-white/10 bg-black/40 px-4 py-3">
              <div className="mb-2 flex items-center gap-2 text-xs font-semibold text-white/70"><Clock size={13} /> {item.kind === 'video' ? 'Versions & alternatives' : 'Iteration thread'}</div>
              <div className="flex gap-3 overflow-x-auto pb-1">
                {relevantJobs.map((job, index) => {
                  const selected = index === turnIndex
                  const outputs = getMediaIterationOutputs(job, threadItems)
                  return (
                    <div key={job.id} className={`w-64 shrink-0 rounded-xl border p-3 ${selected ? 'border-blue-400 bg-blue-500/15' : 'border-white/15 bg-white/5'}`}>
                      <button
                        type="button"
                        ref={selected ? selectedTurnRef : undefined}
                        aria-current={selected ? 'step' : undefined}
                        onClick={() => openTurn(job)}
                        className="w-full text-left"
                      >
                        <span className="flex items-center gap-2 text-xs font-semibold text-white">
                          {isMediaGenerationPending(job.status) ? <Loader2 size={13} className="animate-spin text-blue-400" /> : job.error || job.status === 'failed' || job.status === 'cancelled' ? <AlertCircle size={13} className="text-rose-400" /> : <Check size={13} className="text-emerald-400" />}
                          Turn {index + 1} · {job.status.replace(/_/g, ' ')}
                        </span>
                        <span className="mt-2 block line-clamp-2 break-words text-xs text-white/80" title={job.prompt || job.title}>{job.prompt || job.title}</span>
                        <span className="mt-1 block text-[10px] text-white/50">{outputs.length} / {job.count} outputs</span>
                      </button>
                      {job.error && <p role="alert" className="mt-2 max-h-16 overflow-y-auto break-words text-xs text-rose-300">{job.error}</p>}
                      {outputs.length > 0 && <div className="mt-2 flex gap-1 overflow-x-auto">
                        {outputs.map((output, outputIndex) => <button
                          key={output.id}
                          type="button"
                          aria-label={`Turn ${index + 1}, output ${outputIndex + 1}`}
                          aria-pressed={!showingRequest && item.id === output.id}
                          onClick={() => { setSelectedTurn(job); onSelect(output) }}
                          className={`flex size-10 shrink-0 items-center justify-center overflow-hidden rounded border ${item.id === output.id && !showingRequest ? 'border-blue-400' : 'border-white/20'}`}
                        >{output.kind === 'image' ? <img src={output.directUrl} alt={output.title} className="size-full object-cover" /> : <Film size={16} />}</button>)}
                      </div>}
                    </div>
                  )
                })}
              </div>
            </nav>
          )}

          {/* Persistent Bottom AI Studio Dock */}
          {(item.kind === 'image' || item.kind === 'video') && <footer className="shrink-0 max-h-[50vh] overflow-y-auto border-t border-white/10 bg-slate-950/95 backdrop-blur-2xl px-4 py-3 sm:px-6 shadow-2xl z-20">
            <div className="flex flex-col gap-3 max-w-5xl mx-auto">
              {item.kind === 'video' && <p aria-label="Generation source" className="text-sm text-white">{activeQuickRouteMode === 'next_scene' ? 'Continue from end of' : 'Edit'}: <strong>{item.title}</strong> · retained version <span className="break-all">{item.id}</span>. Newer versions are kept.</p>}
              {/* Row 0: Immutable "Generated With" Provenance Banner */}
              <div className="flex flex-wrap items-center justify-between gap-2 px-3 py-1.5 rounded-xl bg-white/5 border border-white/10 text-xs">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-[10px] uppercase font-bold tracking-wider text-white/50">Generated with:</span>
                  <span className="font-semibold text-white">
                    {sourceModelOption?.display_name || sourceModel || <span className="text-amber-400 italic">Unknown source model</span>}
                  </span>
                  {(sourceAspectRatio || sourceResolution || sourceDurationSeconds !== undefined) && (
                    <span className="text-white/60 font-mono text-[11px]">
                      · {[sourceAspectRatio, sourceResolution, sourceDurationSeconds !== undefined ? `${sourceDurationSeconds}s` : undefined].filter(Boolean).join(' · ')}
                    </span>
                  )}
                </div>
                {activeLockedOptions?.explanation && currentActionSupport.supported && (
                  <div className="text-[10px] text-blue-300 font-medium bg-blue-950/60 border border-blue-800/40 rounded-md px-2 py-0.5">
                    {activeLockedOptions.explanation}
                  </div>
                )}
              </div>

              {item.legacyRequestedSettings && (!sourceModel || !sourceAspectRatio || !sourceResolution) && (
                <p className="text-xs text-white/60">Legacy task request (not verified output metadata): {[item.legacyRequestedSettings.model, item.legacyRequestedSettings.aspectRatio, item.legacyRequestedSettings.resolution, item.legacyRequestedSettings.durationSeconds ? `${item.legacyRequestedSettings.durationSeconds}s` : undefined].filter(Boolean).join(' · ')}</p>
              )}
              {currentActionSupport.supported && !activeValidation.valid && (
                <p role="status" className="text-xs text-amber-300">{activeValidation.error}</p>
              )}
              {/* Action Disabled Alert Banner */}
              {!currentActionSupport.supported && (
                <div className="flex items-center gap-2 rounded-xl bg-amber-950/40 border border-amber-800/50 p-2.5 text-xs text-amber-300">
                  <AlertCircle size={15} className="shrink-0 text-amber-400" />
                  <span>{currentActionSupport.reason || 'This action is not available for this media.'}</span>
                </div>
              )}
              <details open={item.kind !== 'video' ? true : undefined}>
                <summary className="cursor-pointer py-2 text-xs text-white/60">Action & advanced settings</summary>
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
                        disabled={!nextSceneSupport.supported}
                        onClick={() => handleModeChange('next_scene')}
                        className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition ${
                          !nextSceneSupport.supported
                            ? 'opacity-40 cursor-not-allowed text-white/40'
                            : activeQuickRouteMode === 'next_scene'
                            ? 'bg-purple-600 text-white shadow-md cursor-pointer'
                            : 'text-white/60 hover:text-white hover:bg-white/5 cursor-pointer'
                        }`}
                        title={!nextSceneSupport.supported ? (nextSceneSupport.reason || 'Next scene continuation not supported') : 'Continue this video story with the next sequence'}
                      >
                        <ArrowRight size={13} />
                        <span>Extend video</span>
                      </button>
                      <button
                        type="button"
                        disabled={!fineTuneSupport.supported}
                        onClick={() => handleModeChange('fine_tune')}
                        className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition ${
                          !fineTuneSupport.supported
                            ? 'opacity-40 cursor-not-allowed text-white/40'
                            : activeQuickRouteMode === 'fine_tune'
                            ? 'bg-amber-600 text-white shadow-md cursor-pointer'
                            : 'text-white/60 hover:text-white hover:bg-white/5 cursor-pointer'
                        }`}
                        title={!fineTuneSupport.supported ? (fineTuneSupport.reason || 'Fine-tune not supported') : 'Fine-tune video atmosphere, pacing, or style'}
                      >
                        <Edit3 size={13} />
                        <span>Fine-Tune</span>
                      </button>
                      <button
                        type="button"
                        disabled={!iterateSupport.supported}
                        onClick={() => handleModeChange('iterate')}
                        className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition ${
                          !iterateSupport.supported
                            ? 'opacity-40 cursor-not-allowed text-white/40'
                            : activeQuickRouteMode === 'iterate'
                            ? 'bg-emerald-600 text-white shadow-md cursor-pointer'
                            : 'text-white/60 hover:text-white hover:bg-white/5 cursor-pointer'
                        }`}
                        title={!iterateSupport.supported ? (iterateSupport.reason || 'Iterations not supported') : 'Generate video variations in parallel'}
                      >
                        <Sparkles size={13} />
                        <span>Swarm Iterations</span>
                      </button>
                    </>
                  )}


                </div>

                {/* Model Selector & Parameters */}
                <div className="flex flex-wrap items-center gap-2">
                  {/* Model Selector Dropdown & Make Default */}
                  <div className="flex items-center gap-1.5 bg-white/5 border border-white/10 rounded-xl px-2.5 py-1 text-xs">
                    <span className="text-[10px] uppercase font-bold text-white/40 tracking-wider">Model:</span>
                    <select
                      value={selectedModel}
                      disabled={isVideoSource}
                      onChange={(e) => setSelectedModel(e.target.value)}
                      className="bg-transparent text-white font-medium text-xs outline-none cursor-pointer max-w-[170px] truncate disabled:opacity-80 disabled:cursor-not-allowed"
                      aria-label="Select AI model"
                      title={isVideoSource ? 'Model is locked to the generating model for video operations' : 'Select AI model'}
                    >
                      {!selectedModel && <option value="">Unknown source model — choose explicitly</option>}
                      {availableModels.length > 0 ? (
                        availableModels.map((m) => (
                          <option key={m.id} value={m.id} disabled={!m.ready} className="bg-slate-900 text-white">
                            {m.display_name || m.model || m.id}{!m.ready ? ' (unavailable)' : ''}
                          </option>
                        ))
                      ) : (
                        <option value={selectedModel || ''} className="bg-slate-900 text-white">
                          {selectedModel || 'Unknown source model'}
                        </option>
                      )}
                    </select>

                    {/* Set as Default button */}
                    {selectedModel && !isVideoSource && selectedModel !== currentDefaultModel && (
                      <button
                        type="button"
                        onClick={() => void handleUpgradeDefaultModel()}
                        className="flex items-center gap-1 px-2 py-0.5 rounded-md bg-amber-500/20 text-amber-300 hover:bg-amber-500/30 text-[10px] font-bold transition ml-1 cursor-pointer"
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

                  {/* Aspect Ratio Selector */}
                  <div className="flex items-center gap-1.5 bg-white/5 border border-white/10 rounded-xl px-2 py-1 text-xs">
                    <span className="text-[10px] uppercase font-bold text-white/40 tracking-wider">Ratio:</span>
                    <select
                      value={aspectRatio}
                      disabled={Boolean(activeLockedOptions?.aspectRatio)}
                      onChange={(e) => setAspectRatio(e.target.value)}
                      className="bg-transparent text-white font-medium text-xs outline-none cursor-pointer disabled:opacity-80 disabled:cursor-not-allowed"
                      aria-label="Aspect Ratio"
                      title={activeLockedOptions?.aspectRatio ? `Locked: ${activeLockedOptions.explanation || activeLockedOptions.aspectRatio}` : 'Aspect Ratio'}
                    >
                      {!aspectRatio && <option value="">Provider default</option>}
                      {supportedRatios.length > 0 ? (
                        supportedRatios.map((ratio) => (
                          <option key={ratio} value={ratio} className="bg-slate-900 text-white">
                            {ratio}
                          </option>
                        ))
                      ) : (
                        <option value={aspectRatio || ''} className="bg-slate-900 text-white">
                          {aspectRatio || 'Provider default'}
                        </option>
                      )}
                    </select>
                  </div>

                  {/* Resolution Selector */}
                  <div className="flex items-center gap-1.5 bg-white/5 border border-white/10 rounded-xl px-2 py-1 text-xs">
                    <span className="text-[10px] uppercase font-bold text-white/40 tracking-wider">Res:</span>
                    <select
                      value={resolution}
                      disabled={Boolean(activeLockedOptions?.resolution)}
                      onChange={(e) => setResolution(e.target.value)}
                      className="bg-transparent text-white font-medium text-xs outline-none cursor-pointer uppercase disabled:opacity-80 disabled:cursor-not-allowed"
                      aria-label="Resolution"
                      title={activeLockedOptions?.resolution ? `Locked: ${activeLockedOptions.explanation || activeLockedOptions.resolution}` : 'Resolution'}
                    >
                      {!resolution && <option value="">Provider default</option>}
                      {supportedResolutions.length > 0 ? (
                        supportedResolutions.map((res) => (
                          <option key={res} value={res} className="bg-slate-900 text-white uppercase">
                            {res}
                          </option>
                        ))
                      ) : (
                        <option value={resolution || ''} className="bg-slate-900 text-white uppercase">
                          {resolution || 'Provider default'}
                        </option>
                      )}
                    </select>
                  </div>

                  {/* Video Duration Selector (video actions only) */}
                  {isVideoAction && (
                    <div className="flex items-center gap-1.5 bg-white/5 border border-white/10 rounded-xl px-2 py-1 text-xs">
                      <span className="text-[10px] uppercase font-bold text-white/40 tracking-wider">Duration:</span>
                      <select
                        value={activeLockedOptions?.durationSeconds ?? (activeLockedOptions?.supportsDuration === false ? '' : (durationSeconds ?? ''))}
                        disabled={activeLockedOptions?.supportsDuration === false || activeLockedOptions?.durationSeconds !== undefined}
                        onChange={(e) => {
                          const val = e.target.value
                          setDurationSeconds(val ? Number(val) : undefined)
                        }}
                        className="bg-transparent text-white font-medium text-xs outline-none cursor-pointer disabled:opacity-80 disabled:cursor-not-allowed"
                        aria-label="Video Duration"
                        title={
                          activeLockedOptions?.durationSeconds !== undefined
                            ? `Locked: ${activeLockedOptions.explanation || `${activeLockedOptions.durationSeconds}s`}`
                            : activeLockedOptions?.supportsDuration === false
                            ? 'Duration managed automatically'
                            : 'Video Duration'
                        }
                      >
                        {activeLockedOptions?.supportsDuration === false && activeLockedOptions?.durationSeconds === undefined ? (
                          <option value="" className="bg-slate-900 text-white">Auto (Managed)</option>
                        ) : (
                          <>
                            {durationSeconds === undefined && <option value="">Provider default</option>}
                            {supportedDurations.length > 0 ? (
                              supportedDurations.map((sec) => (
                                <option key={sec} value={sec} className="bg-slate-900 text-white">
                                  {sec}s
                                </option>
                              ))
                            ) : (
                              <option value={durationSeconds ?? ''} className="bg-slate-900 text-white">
                                {durationSeconds ? `${durationSeconds}s` : 'Provider default'}
                              </option>
                            )}
                          </>
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

              </details>
              {/* Row 2: Textarea Prompt Box & Action Submission */}
              <div className="media-viewer-prompt flex flex-col sm:flex-row items-stretch sm:items-end gap-3">
                <div className="relative flex-1">
                  <textarea
                    aria-label="Generation instructions"
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
                    <span className="text-white/50">Estimated total:</span>
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
                        : !selectedModelOption?.ready
                          ? 'Selected model is unavailable'
                          : !currentActionSupport.supported
                            ? (currentActionSupport.reason || 'Action is not supported for this media')
                            : !activeValidation.valid
                              ? (activeValidation.error || 'Invalid configuration')
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
                        <span>{activeQuickRouteMode === 'next_scene' ? 'Continue from here' : activeQuickRouteMode === 'iterate' ? `Generate ${variantCount} variations` : 'Generate revision'}</span>
                        <span className="sr-only">Auto-Revise</span>
                      </>
                    )}
                  </button>
                </div>
              </div>

              {catalogLoading && <p role="status" className="text-xs text-white/60">Loading model settings and pricing…</p>}
              {catalogError && <p role="alert" className="text-xs text-rose-300">Unable to load model settings. Reopen the viewer to retry.</p>}
              {/* Cost Disclosure Note & Submit Error */}
              <div className="flex flex-col gap-1">
                {submitError && (
                  <div className="flex items-center gap-1.5 text-xs text-rose-400 bg-rose-950/40 border border-rose-800/50 rounded-lg px-2.5 py-1.5">
                    <AlertCircle size={14} className="shrink-0" />
                    <span>{submitError}</span>
                  </div>
                )}
                <div className="flex items-center justify-between text-[10px] text-white/40">
                  <span>USD estimate from model catalog for the selected outputs. Input and text-token charges may be additional.</span>
                  {!modelGenOptions && (
                    <span className="text-white/30 italic">Using provider defaults.</span>
                  )}
                </div>
                {/* Note: In Planner and presetSuggestions removed per user instruction */}
              </div>


            </div>
          </footer>}
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
                      const isCurrent = !showingRequest && chainItem.id === item.id
                      return (
                        <div
                          key={chainItem.id}
                          role="button"
                          tabIndex={0}
                          onKeyDown={(event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); selectAsset(chainItem) } }}
                          className={`relative flex items-center gap-2.5 p-2 rounded-xl transition ${
                            isCurrent
                              ? 'bg-blue-600/20 border border-blue-500/50 shadow-md ring-1 ring-blue-500/30'
                              : 'bg-white/5 border border-white/10 hover:bg-white/10 cursor-pointer'
                          }`}
                          onClick={() => {
                            selectAsset(chainItem)
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
              {/* Generation Provenance Card */}
              <div className="rounded-xl border border-white/10 bg-white/5 p-3 space-y-2">
                <span className="text-[10px] uppercase font-bold tracking-wider text-blue-300">Generation Provenance</span>
                <div>
                  <span className="text-[10px] text-white/50">Model</span>
                  <p className="font-semibold text-white text-xs">{sourceModelOption?.display_name || sourceModel || <span className="text-amber-400 italic">Unknown</span>}</p>
                </div>
                <div className="grid grid-cols-3 gap-2 text-[11px]">
                  <div>
                    <span className="text-[10px] text-white/50">Ratio</span>
                    <p className="font-mono text-white/90">{sourceAspectRatio || '—'}</p>
                  </div>
                  <div>
                    <span className="text-[10px] text-white/50">Resolution</span>
                    <p className="font-mono text-white/90">{sourceResolution || '—'}</p>
                  </div>
                  <div>
                    <span className="text-[10px] text-white/50">Duration</span>
                    <p className="font-mono text-white/90">{sourceDurationSeconds !== undefined ? `${sourceDurationSeconds}s` : '—'}</p>
                  </div>
                </div>
                {sourceProvenance && (
                  <div className="pt-2 border-t border-white/10 space-y-1 text-[10px] text-white/60">
                    <div className="flex justify-between">
                      <span>Provider:</span>
                      <span className="font-mono text-white/80">{sourceProvenance.provider}</span>
                    </div>
                    {sourceProvenance.operation && (
                      <div className="flex justify-between">
                        <span>Operation:</span>
                        <span className="font-mono uppercase text-white/80">{sourceProvenance.operation}</span>
                      </div>
                    )}
                    {sourceProvenance.extension_count !== undefined && (
                      <div className="flex justify-between">
                        <span>Extensions:</span>
                        <span className="font-mono text-white/80">{sourceProvenance.extension_count} / 20</span>
                      </div>
                    )}
                    {sourceProvenance.expires_at && (
                      <div className="flex justify-between">
                        <span>Expires:</span>
                        <span className="font-mono text-white/80">{new Date(sourceProvenance.expires_at).toLocaleDateString()}</span>
                      </div>
                    )}
                  </div>
                )}
              </div>
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

              {item.artifact?.description && (
                <div>
                  <span className="text-[10px] uppercase font-bold tracking-wider text-white/40">Description / Prompt</span>
                  <p className="mt-0.5 text-white/80 italic break-words leading-relaxed">{item.artifact?.description}</p>
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
