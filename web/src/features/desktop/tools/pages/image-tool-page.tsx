import { type CSSProperties, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, ChevronLeft, ChevronRight, Clipboard, Download, ExternalLink, FolderOpen, Image, Link2, Moon, Sparkles, TriangleAlert, X } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Select } from '../../../../components/ui/select'
import { Textarea } from '../../../../components/ui/textarea'
import { apiFetch, requestJson } from '../../../../app/api'
import { useDesktopStore } from '../../state/use-desktop-store'
import { listWorkspaces } from '../../../workspaces/launcher/queries/list-workspaces'
import { uiSettingsQueryOptions } from '../../../queries/query-options'
import { normalizeGlobalThemeSettings } from '../../settings/swarm/types/swarm-settings'
import { resolveWorkspaceBySlug } from '../../../workspaces/launcher/services/workspace-route'
import { applyWorkspaceTheme, createWorkspaceThemeStyle } from '../../../workspaces/launcher/services/workspace-theme'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import { SwarmToolSidebar } from '../components/swarm-tool-sidebar'

type ImageAsset = {
  id: string
  name: string
  path: string
  extension: string
  sizeBytes: number
  modifiedAt: number
  url?: string
}

type ImageAssetWire = {
  id?: string
  name?: string
  path?: string
  extension?: string
  size_bytes?: number
  sizeBytes?: number
  modified_at?: number
  modifiedAt?: number
}

type ImageThreadRecord = {
  id: string
  title: string
  workspacePath: string
  workspaceName: string
  imageFolders: string[]
  imageAssets: ImageAsset[]
  imageAssetOrder: string[]
  metadata?: Record<string, unknown>
  createdAt: number
  updatedAt: number
}

type ImageThreadWire = {
  id?: string
  title?: string
  workspace_path?: string
  workspace_name?: string
  image_folders?: string[]
  image_assets?: ImageAssetWire[]
  image_asset_order?: string[]
  metadata?: Record<string, unknown>
  created_at?: number
  updated_at?: number
}

type ImageProviderStatus = {
  id: string
  label: string
  ready: boolean
  reason?: string
  default_model: string
  models?: string[]
}

type ImageGenerationPartial = {
  item_id?: string
  output_index?: number
  sequence_number?: number
  partial_image_index?: number
  base64_image?: string
  data_url?: string
  output_format?: string
  size?: string
  quality?: string
  background?: string
}

type ImageGenerationResponse = {
  result?: {
    assets?: Array<ImageAssetWire & { url?: string }>
    partials?: ImageGenerationPartial[]
    provider_response?: Record<string, unknown>
    target?: {
      thread?: ImageThreadWire
    }
  }
}

type LiveGenerationEvent = {
  type?: string
  item_id?: string
  output_index?: number
  partial_image_b64?: string
  partial_image_index?: number
  sequence_number?: number
  output_format?: string
  size?: string
  quality?: string
  background?: string
  text?: string
  thinking?: string
}

type LiveGenerationPreview = {
  id: string
  index: number
  dataUrl: string
  label: string
}

type GeminiChargeInfo = {
  label: string
  detail: string
  hasCharge: boolean
}

type ImageThumbnailItem =
  | { id: string; kind: 'asset'; asset: ImageAsset; preview: null; label: string; meta: string }
  | { id: string; kind: 'live'; asset: null; preview: LiveGenerationPreview; label: string; meta: string }
  | { id: string; kind: 'pending'; asset: null; preview: null; label: string; meta: string }

type GenerationStage = 'idle' | 'queued' | 'generating' | 'partial' | 'final' | 'error'
type GenerationControlMode = 'manual' | 'ai'

const IMAGE_TOOL_BLACK_MODE_STORAGE_KEY = 'swarm.imageTool.blackMode'
const DEFAULT_IMAGE_SESSION_TITLE = 'Swarm image session'

const IMAGE_MODEL_OPTIONS = [
  { id: 'codex-image-gen', provider: 'codex_openai', model: 'gpt-5.5', label: 'Codex Image Gen', helper: 'OAuth only. Uses Codex/ChatGPT OAuth image generation.', kind: 'codex-image-gen' },
  { id: 'gemini-nano-banana-2', provider: 'google_gemini', model: 'gemini-3.1-flash-image-preview', label: 'Nano Banana 2', helper: 'Google API key. Fast Gemini image generation with real streaming.', kind: 'google-gemini' },
  { id: 'gemini-nano-banana-pro', provider: 'google_gemini', model: 'gemini-3-pro-image-preview', label: 'Nano Banana Pro', helper: 'Google API key. Pro Gemini image generation.', kind: 'google-gemini' },
  { id: 'gemini-nano-banana', provider: 'google_gemini', model: 'gemini-2.5-flash-image', label: 'Nano Banana', helper: 'Google API key. Supports 512, 1K, 2K, and 4K output sizes.', kind: 'google-gemini' },
] as const

const OPENAI_IMAGE_SIZE_OPTIONS = [
  { id: 'auto', label: 'Auto', helper: 'Best fit for prompt', aspectRatio: '1:1', size: 'Model default' },
  { id: '1024x1024', label: 'Square', helper: '1024 × 1024', aspectRatio: '1:1', size: '1.0 MP' },
  { id: '1536x1024', label: 'Landscape', helper: '1536 × 1024', aspectRatio: '3:2', size: '1.5 MP' },
  { id: '1024x1536', label: 'Portrait', helper: '1024 × 1536', aspectRatio: '2:3', size: '1.5 MP' },
]

const FINAL_IMAGE_COUNT_OPTIONS = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10] as const

const GOOGLE_GEMINI_ASPECT_RATIO_OPTIONS = [
  { id: '1:1', label: 'Square', helper: '1:1' },
  { id: '16:9', label: 'Wide', helper: '16:9' },
  { id: '9:16', label: 'Story', helper: '9:16' },
  { id: '3:2', label: 'Landscape', helper: '3:2' },
  { id: '2:3', label: 'Portrait', helper: '2:3' },
  { id: '4:3', label: 'Classic', helper: '4:3' },
  { id: '3:4', label: 'Poster', helper: '3:4' },
  { id: '21:9', label: 'Cinema', helper: '21:9' },
] as const

const GOOGLE_GEMINI_IMAGE_SIZE_OPTIONS = [
  { id: '512', label: '512', helper: 'Flash 2.5 only' },
  { id: '1K', label: '1K', helper: 'Default' },
  { id: '2K', label: '2K', helper: 'Higher resolution' },
  { id: '4K', label: '4K', helper: 'Highest resolution' },
] as const

function livePreviewSlotKey(value: { item_id?: string; output_index?: number }): string {
  const itemId = String(value.item_id ?? '').trim()
  if (itemId) return `item:${itemId}`
  if (typeof value.output_index === 'number' && value.output_index >= 0) return `output:${value.output_index}`
  return 'output:0'
}

function livePreviewFromEvent(event: LiveGenerationEvent): LiveGenerationPreview | null {
  const base64 = String(event.partial_image_b64 ?? '').trim()
  if (!base64) return null
  const slotIndex = typeof event.output_index === 'number' && event.output_index >= 0 ? event.output_index : 0
  const partialIndex = typeof event.partial_image_index === 'number' ? event.partial_image_index : 0
  const format = String(event.output_format ?? 'png').trim() || 'png'
  return {
    id: livePreviewSlotKey(event),
    index: slotIndex,
    dataUrl: `data:image/${format};base64,${base64}`,
    label: `Image ${slotIndex + 1} preview ${partialIndex + 1}`,
  }
}

function livePreviewFromPartial(partial: ImageGenerationPartial): LiveGenerationPreview | null {
  const dataUrl = String(partial.data_url ?? '').trim()
  const base64 = String(partial.base64_image ?? '').trim()
  if (!dataUrl && !base64) return null
  const slotIndex = typeof partial.output_index === 'number' && partial.output_index >= 0 ? partial.output_index : 0
  const partialIndex = typeof partial.partial_image_index === 'number' ? partial.partial_image_index : 0
  const format = String(partial.output_format ?? 'png').trim() || 'png'
  return {
    id: livePreviewSlotKey(partial),
    index: slotIndex,
    dataUrl: dataUrl || `data:image/${format};base64,${base64}`,
    label: `Image ${slotIndex + 1} preview ${partialIndex + 1}`,
  }
}

function mergeLivePreview(current: LiveGenerationPreview[], next: LiveGenerationPreview): LiveGenerationPreview[] {
  const withoutSameSlot = current.filter((preview) => preview.id !== next.id)
  return [...withoutSameSlot, next].sort((a, b) => a.index - b.index)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object'
}

function metadataStringArray(value: unknown): string[] {
  return Array.isArray(value)
    ? value.map((entry) => String(entry ?? '').trim()).filter(Boolean)
    : []
}

function imageAssetURL(threadId: string, assetId: string): string {
  const search = new URLSearchParams({ thread_id: threadId, asset_id: assetId })
  return `/v1/image/assets?${search.toString()}`
}

function metadataAssets(value: unknown): ImageAsset[] {
  if (!Array.isArray(value)) {
    return []
  }
  return value
    .map((entry): ImageAsset | null => {
      if (!isRecord(entry)) return null
      const id = String(entry.id ?? '').trim()
      const name = String(entry.name ?? '').trim()
      const path = String(entry.path ?? '').trim()
      if (!id || !name || !path) return null
      return {
        id,
        name,
        path,
        extension: String(entry.extension ?? '').trim(),
        sizeBytes: typeof entry.size_bytes === 'number'
          ? entry.size_bytes
          : typeof entry.sizeBytes === 'number'
            ? entry.sizeBytes
            : 0,
        modifiedAt: typeof entry.modified_at === 'number'
          ? entry.modified_at
          : typeof entry.modifiedAt === 'number'
            ? entry.modifiedAt
            : 0,
      }
    })
    .filter((entry): entry is ImageAsset => Boolean(entry))
}

function mapImageThread(wire: ImageThreadWire): ImageThreadRecord | null {
  const id = String(wire.id ?? '').trim()
  const workspacePath = String(wire.workspace_path ?? '').trim()
  if (!id || !workspacePath) {
    return null
  }
  return {
    id,
    title: String(wire.title ?? '').trim(),
    workspacePath,
    workspaceName: String(wire.workspace_name ?? '').trim(),
    imageFolders: metadataStringArray(wire.image_folders),
    imageAssets: metadataAssets(wire.image_assets).map((asset) => ({ ...asset, url: imageAssetURL(id, asset.id) })),
    imageAssetOrder: metadataStringArray(wire.image_asset_order),
    metadata: isRecord(wire.metadata) ? wire.metadata : undefined,
    createdAt: typeof wire.created_at === 'number' ? wire.created_at : 0,
    updatedAt: typeof wire.updated_at === 'number' ? wire.updated_at : 0,
  }
}

function formatStartedAt(value: number): string {
  if (!value) {
    return 'Date unavailable'
  }
  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  }).format(new Date(value))
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return 'Size unavailable'
  const units = ['B', 'KB', 'MB', 'GB']
  let size = value
  let unitIndex = 0
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024
    unitIndex += 1
  }
  return `${size.toFixed(unitIndex === 0 ? 0 : 1)} ${units[unitIndex]}`
}

function imageSessionPath(thread: ImageThreadRecord | null): string {
  return thread?.imageFolders[0] ?? ''
}

function imageDownloadName(asset: ImageAsset): string {
  const name = asset.name.trim()
  if (name) return name
  const ext = asset.extension.trim()
  return ext ? `generated-image.${ext.replace(/^\./, '')}` : 'generated-image'
}

async function copyTextToClipboard(value: string): Promise<void> {
  const text = value.trim()
  if (!text) throw new Error('Nothing to copy')
  if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText) {
    throw new Error('Clipboard unavailable')
  }
  await navigator.clipboard.writeText(text)
}

function extractGeminiChargeInfo(providerResponse: unknown): GeminiChargeInfo | null {
  if (!isRecord(providerResponse)) return null
  const cost = providerResponse.cost
  if (!isRecord(cost)) return null
  if (cost.available === true) {
    const amount = typeof cost.amount_usd === 'number' ? `$${cost.amount_usd.toFixed(4)}` : 'Google charge returned'
    return { label: amount, detail: 'Google returned charge metadata for this generation.', hasCharge: true }
  }
  const usage = providerResponse.usage_metadata
  const usageCount = Array.isArray(usage) ? usage.length : isRecord(usage) && usage.available ? 1 : 0
  if (usageCount > 0) {
    return { label: 'Usage recorded', detail: String(cost.reason ?? 'Google returned usage metadata but no exact dollar charge.'), hasCharge: false }
  }
  return null
}

function latestSessionChargeInfo(thread: ImageThreadRecord | null): GeminiChargeInfo | null {
  if (!thread?.metadata) return null
  const latest = thread.metadata.last_image_generation
  if (!isRecord(latest)) return null
  return extractGeminiChargeInfo(latest.provider_response)
}

async function fetchImageThreads(workspacePath: string): Promise<ImageThreadRecord[]> {
  const search = new URLSearchParams({ workspace_path: workspacePath })
  const response = await requestJson<{ threads?: ImageThreadWire[] }>(`/v1/workspace/image/threads?${search.toString()}`)
  return (response.threads ?? [])
    .map(mapImageThread)
    .filter((thread): thread is ImageThreadRecord => Boolean(thread))
}

async function fetchImageProviders(): Promise<ImageProviderStatus[]> {
  const response = await requestJson<{ providers?: ImageProviderStatus[] }>('/v1/image/providers')
  return response.providers ?? []
}

async function createImageThread(input: {
  title: string
  workspacePath: string
  workspaceName: string
}): Promise<ImageThreadRecord> {
  const response = await requestJson<{ thread?: ImageThreadWire }>('/v1/workspace/image/threads', {
    method: 'POST',
    body: JSON.stringify({
      title: input.title,
      workspace_path: input.workspacePath,
      workspace_name: input.workspaceName,
      image_folders: [],
      image_assets: [],
      image_asset_order: [],
      metadata: {
        tool_kind: 'image',
        session_schema_version: 1,
        storage_area: 'app_managed_workspace_bucket/tools/image/sessions',
      },
    }),
  })
  const thread = mapImageThread(response.thread ?? {})
  if (!thread) {
    throw new Error('Image thread create returned no thread')
  }
  return thread
}

export function ImageToolPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const matchRoute = useMatchRoute()
  const workspaceImageToolSessionMatch = matchRoute({ to: '/$workspaceSlug/tools/image/$imageSessionId', fuzzy: false })
  const workspaceImageToolMatch = matchRoute({ to: '/$workspaceSlug/tools/image', fuzzy: false })
  const rootImageToolSessionMatch = matchRoute({ to: '/tools/image/$imageSessionId', fuzzy: false })
  const routeWorkspaceSlug = workspaceImageToolSessionMatch ? workspaceImageToolSessionMatch.workspaceSlug.trim() : workspaceImageToolMatch ? workspaceImageToolMatch.workspaceSlug.trim() : ''
  const routeImageSessionId = workspaceImageToolSessionMatch ? workspaceImageToolSessionMatch.imageSessionId.trim() : rootImageToolSessionMatch ? rootImageToolSessionMatch.imageSessionId.trim() : ''
  const activeSessionId = useDesktopStore((state) => state.activeSessionId)
  const activeWorkspacePath = useDesktopStore((state) => state.activeWorkspacePath)

  const [createError, setCreateError] = useState<string | null>(null)
  const [generationError, setGenerationError] = useState<string | null>(null)
  const [revealingStorage, setRevealingStorage] = useState(false)
  const [lastStoragePath, setLastStoragePath] = useState('')
  const [pathCopyStatus, setPathCopyStatus] = useState('')
  const [sessionLinkCopyStatus, setSessionLinkCopyStatus] = useState('')
  const [imageActionStatus, setImageActionStatus] = useState('')
  const [newSessionTitle, setNewSessionTitle] = useState('')
  const [creatingSession, setCreatingSession] = useState(false)
  const [generatingImage, setGeneratingImage] = useState(false)
  const [generationStage, setGenerationStage] = useState<GenerationStage>('idle')
  const [livePreviews, setLivePreviews] = useState<LiveGenerationPreview[]>([])
  const [selectedLivePreviewId, setSelectedLivePreviewId] = useState<string | null>(null)
  const [selectedFinalImageCount, setSelectedFinalImageCount] = useState<(typeof FINAL_IMAGE_COUNT_OPTIONS)[number]>(1)
  const [activeGenerationCount, setActiveGenerationCount] = useState(1)
  const [selectedThreadId, setSelectedThreadId] = useState<string | null>(null)
  const [selectedImageAssetId, setSelectedImageAssetId] = useState<string | null>(null)
  const [imageLightboxOpen, setImageLightboxOpen] = useState(false)
  const [lightboxNaturalSize, setLightboxNaturalSize] = useState<{ width: number; height: number } | null>(null)
  const followLivePreviewRef = useRef(false)
  const [selectedImageModel, setSelectedImageModel] = useState<string>(IMAGE_MODEL_OPTIONS[0]?.id ?? '')
  const [selectedOpenAIImageSize, setSelectedOpenAIImageSize] = useState('auto')
  const [selectedGoogleAspectRatio, setSelectedGoogleAspectRatio] = useState('1:1')
  const [selectedGoogleImageSize, setSelectedGoogleImageSize] = useState('1K')
  const [promptText, setPromptText] = useState('')
  const [generationControlMode, setGenerationControlMode] = useState<GenerationControlMode>('manual')
  const [liveGenerationText, setLiveGenerationText] = useState('')
  const [liveGenerationThinking, setLiveGenerationThinking] = useState('')
  const [lastGeminiChargeInfo, setLastGeminiChargeInfo] = useState<GeminiChargeInfo | null>(null)
  const [blackModeEnabled, setBlackModeEnabled] = useState(() => {
    if (typeof window === 'undefined') {
      return false
    }
    return window.localStorage.getItem(IMAGE_TOOL_BLACK_MODE_STORAGE_KEY) === 'true'
  })

  const workspacesQuery = useQuery({
    queryKey: ['image-tool-workspaces'],
    queryFn: () => listWorkspaces(200),
    staleTime: 30_000,
  })
  const uiSettingsQuery = useQuery(uiSettingsQueryOptions())
  const imageProvidersQuery = useQuery({
    queryKey: ['image-generation-providers'],
    queryFn: fetchImageProviders,
    staleTime: 15_000,
  })
  const workspaces = workspacesQuery.data ?? []

  const selectedWorkspace = useMemo<WorkspaceEntry | null>(() => {
    if (routeWorkspaceSlug) {
      return resolveWorkspaceBySlug(workspaces, routeWorkspaceSlug)
    }
    if (activeWorkspacePath) {
      return workspaces.find((workspace) => workspace.path === activeWorkspacePath) ?? null
    }
    return workspaces[0] ?? null
  }, [activeWorkspacePath, routeWorkspaceSlug, workspaces])

  const selectedWorkspacePath = selectedWorkspace?.path ?? ''
  const selectedWorkspaceName = selectedWorkspace?.workspaceName ?? ''
  const userThemeId = selectedWorkspace?.themeId?.trim() || normalizeGlobalThemeSettings(uiSettingsQuery.data).activeId
  const darkOverrideButtonStyle = useMemo(() => createWorkspaceThemeStyle(userThemeId, '--image-tool-user-theme') as CSSProperties, [userThemeId])

  const imageThreadsQuery = useQuery({
    queryKey: ['image-tool-threads', selectedWorkspacePath],
    queryFn: () => fetchImageThreads(selectedWorkspacePath),
    enabled: selectedWorkspacePath.trim() !== '',
    staleTime: 15_000,
  })
  const imageThreads = imageThreadsQuery.data ?? []

  useEffect(() => {
    if (!routeImageSessionId) return
    if (imageThreads.some((thread) => thread.id === routeImageSessionId) && selectedThreadId !== routeImageSessionId) {
      setSelectedThreadId(routeImageSessionId)
    }
  }, [imageThreads, routeImageSessionId, selectedThreadId])

  useEffect(() => {
    if (!selectedThreadId) return
    if (!imageThreads.some((thread) => thread.id === selectedThreadId)) {
      setSelectedThreadId(null)
    }
  }, [imageThreads, selectedThreadId])

  const selectedThread = useMemo(() => {
    if (!selectedThreadId) return null
    return imageThreads.find((thread) => thread.id === selectedThreadId) ?? null
  }, [imageThreads, selectedThreadId])

  const orderedImageAssets = useMemo(() => {
    if (!selectedThread) return []
    const assetsById = new Map(selectedThread.imageAssets.map((asset) => [asset.id, asset]))
    const orderedAssets = selectedThread.imageAssetOrder
      .map((assetId) => assetsById.get(assetId))
      .filter((asset): asset is ImageAsset => Boolean(asset))
    const orderedIds = new Set(orderedAssets.map((asset) => asset.id))
    return [...orderedAssets, ...selectedThread.imageAssets.filter((asset) => !orderedIds.has(asset.id))]
  }, [selectedThread])

  const selectedImageAsset = useMemo(() => {
    if (orderedImageAssets.length === 0 || !selectedImageAssetId) return null
    return orderedImageAssets.find((asset) => asset.id === selectedImageAssetId) ?? null
  }, [orderedImageAssets, selectedImageAssetId])

  const selectedImageAssetIndex = selectedImageAsset
    ? orderedImageAssets.findIndex((asset) => asset.id === selectedImageAsset.id)
    : -1
  const activePreviewNumber = selectedImageAssetIndex >= 0 ? selectedImageAssetIndex + 1 : 1
  const selectedImageSource = selectedImageAsset && selectedThread
    ? selectedImageAsset.url ?? imageAssetURL(selectedThread.id, selectedImageAsset.id)
    : ''
  const selectedSessionStoragePath = imageSessionPath(selectedThread)
  const selectedAssetFilePath = selectedImageAsset?.path ?? ''
  const selectedCopyFilePath = selectedAssetFilePath || selectedSessionStoragePath
  const selectedSessionURL = selectedThread
    ? `${typeof window !== 'undefined' ? window.location.origin : ''}${routeWorkspaceSlug ? `/${routeWorkspaceSlug}/tools/image/${selectedThread.id}` : `/tools/image/${selectedThread.id}`}`
    : ''
  const selectedModelOption = IMAGE_MODEL_OPTIONS.find((option) => option.id === selectedImageModel) ?? IMAGE_MODEL_OPTIONS[0]
  const selectedProviderStatus = (imageProvidersQuery.data ?? []).find((provider) => provider.id === selectedModelOption.provider)
  const selectedProviderReady = selectedProviderStatus?.ready === true
  const isGoogleGeminiModel = selectedModelOption.provider === 'google_gemini'
  const selectedProviderWarning = selectedProviderStatus
    ? selectedProviderReady
      ? ''
      : selectedProviderStatus.reason || (isGoogleGeminiModel ? 'Connect a Google API key before using Gemini image generation.' : 'Codex Image Gen requires Codex/ChatGPT OAuth before it can generate images.')
    : imageProvidersQuery.isLoading
      ? (isGoogleGeminiModel ? 'Checking Google API-key status…' : 'Checking Codex OAuth status…')
      : (isGoogleGeminiModel ? 'Connect a Google API key before using Gemini image generation.' : 'Codex Image Gen requires Codex/ChatGPT OAuth before it can generate images.')
  const selectedProviderUnavailableReason = selectedProviderWarning || 'Image provider is unavailable'
  const selectedOpenAISizeOption = OPENAI_IMAGE_SIZE_OPTIONS.find((option) => option.id === selectedOpenAIImageSize) ?? OPENAI_IMAGE_SIZE_OPTIONS[0]
  const selectedShapeLabel = isGoogleGeminiModel ? selectedGoogleAspectRatio : selectedOpenAISizeOption.aspectRatio
  const selectedSizeLabel = isGoogleGeminiModel ? selectedGoogleImageSize : selectedOpenAIImageSize
  const selectedCountOptions = isGoogleGeminiModel ? FINAL_IMAGE_COUNT_OPTIONS : FINAL_IMAGE_COUNT_OPTIONS.filter((count) => count <= 3)
  const selectedSessionChargeInfo = latestSessionChargeInfo(selectedThread)
  const displayedChargeInfo = lastGeminiChargeInfo ?? selectedSessionChargeInfo
  const selectedModelLabel = selectedModelOption.label
  const canGenerateImage = Boolean(selectedThread && promptText.trim() && selectedProviderReady && !generatingImage)
  const generationSlotCount = generatingImage ? activeGenerationCount : selectedFinalImageCount
  const activeLivePreview = selectedLivePreviewId
    ? livePreviews.find((preview) => preview.id === selectedLivePreviewId) ?? null
    : null
  const thumbnailItems: ImageThumbnailItem[] = [
    ...orderedImageAssets.map((asset, index) => ({
      id: `asset:${asset.id}`,
      kind: 'asset' as const,
      asset,
      preview: null,
      label: asset.name || `Image ${index + 1}`,
      meta: `Saved ${index + 1}`,
    })),
    ...(generatingImage
      ? Array.from({ length: generationSlotCount }, (_, index) => index)
        .filter((index) => !livePreviews.some((preview) => preview.index === index))
        .map((index) => ({ id: `live:pending:${index}`, kind: 'pending' as const, asset: null, preview: null, label: `Image ${index + 1}`, meta: 'Waiting for preview' }))
      : []),
    ...livePreviews.map((preview) => ({
      id: `live:${preview.id}`,
      kind: 'live' as const,
      asset: null,
      preview,
      label: preview.label,
      meta: generatingImage ? 'Generating' : 'Preview',
    })),
  ]
  const selectedThumbnailId = activeLivePreview
    ? `live:${activeLivePreview.id}`
    : selectedImageAsset
      ? `asset:${selectedImageAsset.id}`
      : generatingImage
        ? 'live:pending:0'
        : null
  const previewCountLabel = activeLivePreview
    ? `Generating image ${activeLivePreview.index + 1} of ${generationSlotCount} · ${orderedImageAssets.length} saved`
    : selectedImageAsset
      ? `Image ${activePreviewNumber} of ${orderedImageAssets.length}`
      : generatingImage
        ? `Generating ${generationSlotCount} image${generationSlotCount === 1 ? '' : 's'} · ${orderedImageAssets.length} saved`
        : `Image 1 of ${Math.max(orderedImageAssets.length, 1)}`

  useEffect(() => {
    if (!isGoogleGeminiModel && selectedFinalImageCount > 3) {
      setSelectedFinalImageCount(3)
    }
  }, [isGoogleGeminiModel, selectedFinalImageCount])

  useEffect(() => {
    if (selectedGoogleImageSize === '512' && selectedModelOption.model !== 'gemini-2.5-flash-image') {
      setSelectedGoogleImageSize('1K')
    }
  }, [selectedGoogleImageSize, selectedModelOption.model])

  useEffect(() => {
    if (orderedImageAssets.length === 0) {
      if (selectedImageAssetId !== null) {
        setSelectedImageAssetId(null)
      }
      return
    }
    if (generatingImage && selectedImageAssetId === null) {
      return
    }
    if (!selectedImageAssetId || !orderedImageAssets.some((asset) => asset.id === selectedImageAssetId)) {
      setSelectedImageAssetId(orderedImageAssets[0].id)
    }
  }, [generatingImage, orderedImageAssets, selectedImageAssetId])

  useEffect(() => {
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(IMAGE_TOOL_BLACK_MODE_STORAGE_KEY, blackModeEnabled ? 'true' : 'false')
    }
    applyWorkspaceTheme(blackModeEnabled ? 'black' : userThemeId)
  }, [blackModeEnabled, userThemeId])

  useEffect(() => {
    if (!selectedImageAsset) {
      setImageLightboxOpen(false)
    }
    setLightboxNaturalSize(null)
  }, [selectedImageAsset])

  const handleBackToWorkspace = useMemo(() => {
    if (routeWorkspaceSlug) {
      if (activeSessionId) {
        return () => {
          void navigate({ to: '/$workspaceSlug/$sessionId', params: { workspaceSlug: routeWorkspaceSlug, sessionId: activeSessionId } })
        }
      }
      return () => {
        void navigate({ to: '/$workspaceSlug', params: { workspaceSlug: routeWorkspaceSlug } })
      }
    }
    return () => {
      void navigate({ to: '/' })
    }
  }, [activeSessionId, navigate, routeWorkspaceSlug])

  const handleCreateSession = useCallback(async () => {
    if (!selectedWorkspacePath || !selectedWorkspaceName) {
      setCreateError('Select a workspace before starting an image session.')
      return
    }
    const title = newSessionTitle.trim() || DEFAULT_IMAGE_SESSION_TITLE
    setCreatingSession(true)
    setCreateError(null)
    try {
      const createdThread = await createImageThread({
        title,
        workspacePath: selectedWorkspacePath,
        workspaceName: selectedWorkspaceName,
      })
      queryClient.setQueryData<ImageThreadRecord[]>(['image-tool-threads', selectedWorkspacePath], (current = []) => {
        const withoutCreated = current.filter((thread) => thread.id !== createdThread.id)
        return [createdThread, ...withoutCreated]
      })
      setSelectedThreadId(createdThread.id)
      if (routeWorkspaceSlug) {
        void navigate({ to: '/$workspaceSlug/tools/image/$imageSessionId', params: { workspaceSlug: routeWorkspaceSlug, imageSessionId: createdThread.id } })
      } else {
        void navigate({ to: '/tools/image/$imageSessionId', params: { imageSessionId: createdThread.id } })
      }
      setNewSessionTitle('')
      await queryClient.invalidateQueries({ queryKey: ['image-tool-threads', selectedWorkspacePath] })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setCreatingSession(false)
    }
  }, [navigate, newSessionTitle, queryClient, routeWorkspaceSlug, selectedWorkspaceName, selectedWorkspacePath])

  const handlePreviousPreview = useCallback(() => {
    if (orderedImageAssets.length === 0) return
    const currentIndex = selectedImageAssetIndex >= 0 ? selectedImageAssetIndex : 0
    const nextIndex = (currentIndex - 1 + orderedImageAssets.length) % orderedImageAssets.length
    followLivePreviewRef.current = false
    setSelectedLivePreviewId(null)
    setSelectedImageAssetId(orderedImageAssets[nextIndex].id)
  }, [orderedImageAssets, selectedImageAssetIndex])

  const handleNextPreview = useCallback(() => {
    if (orderedImageAssets.length === 0) return
    const currentIndex = selectedImageAssetIndex >= 0 ? selectedImageAssetIndex : 0
    const nextIndex = (currentIndex + 1) % orderedImageAssets.length
    followLivePreviewRef.current = false
    setSelectedLivePreviewId(null)
    setSelectedImageAssetId(orderedImageAssets[nextIndex].id)
  }, [orderedImageAssets, selectedImageAssetIndex])

  useEffect(() => {
    if (!imageLightboxOpen) return
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setImageLightboxOpen(false)
      } else if (event.key === 'ArrowLeft') {
        handlePreviousPreview()
      } else if (event.key === 'ArrowRight') {
        handleNextPreview()
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [handleNextPreview, handlePreviousPreview, imageLightboxOpen])

  const openImageLightbox = useCallback(() => {
    if (!selectedImageAsset) return
    setImageLightboxOpen(true)
  }, [selectedImageAsset])

  const handleRevealImageStorage = useCallback(async (assetId?: string) => {
    if (!selectedThread) return
    setRevealingStorage(true)
    setGenerationError(null)
    setImageActionStatus('')
    try {
      const search = new URLSearchParams({ thread_id: selectedThread.id })
      if (assetId) search.set('asset_id', assetId)
      const result = await requestJson<{ ok?: boolean; path?: string; method?: string }>(`/v1/image/storage/reveal?${search.toString()}`, { method: 'POST' })
      const resolvedPath = String(result.path ?? '').trim()
      if (resolvedPath) {
        setLastStoragePath(resolvedPath)
        setPathCopyStatus(assetId ? 'File path resolved.' : 'Folder path resolved.')
      }
    } catch (error) {
      setGenerationError(error instanceof Error ? error.message : String(error))
    } finally {
      setRevealingStorage(false)
    }
  }, [selectedThread])

  const handleCopyFilePath = useCallback(async (path: string) => {
    setGenerationError(null)
    setPathCopyStatus('')
    try {
      await copyTextToClipboard(path)
      setPathCopyStatus('Copied filepath.')
      setImageActionStatus('')
    } catch (error) {
      setGenerationError(error instanceof Error ? error.message : String(error))
    }
  }, [])

  const handleCopySessionLink = useCallback(async () => {
    setGenerationError(null)
    setSessionLinkCopyStatus('')
    try {
      await copyTextToClipboard(selectedSessionURL)
      setSessionLinkCopyStatus('Copied session URL.')
    } catch (error) {
      setGenerationError(error instanceof Error ? error.message : String(error))
    }
  }, [selectedSessionURL])

  const handleOpenSelectedImage = useCallback(() => {
    if (!selectedImageSource) return
    window.open(selectedImageSource, '_blank', 'noopener,noreferrer')
  }, [selectedImageSource])

  const handleDownloadSelectedImage = useCallback(async () => {
    if (!selectedImageAsset || !selectedImageSource) return
    setGenerationError(null)
    setImageActionStatus('')
    try {
      const response = await fetch(selectedImageSource)
      if (!response.ok) throw new Error(`Download failed with status ${response.status}`)
      const blob = await response.blob()
      const url = window.URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = imageDownloadName(selectedImageAsset)
      document.body.appendChild(anchor)
      anchor.click()
      anchor.remove()
      window.URL.revokeObjectURL(url)
      setImageActionStatus('Download started.')
      setPathCopyStatus('')
    } catch (error) {
      setGenerationError(error instanceof Error ? error.message : String(error))
    }
  }, [selectedImageAsset, selectedImageSource])

  const handleGenerateImage = useCallback(async () => {
    if (!selectedThread) {
      setGenerationError('Select an image session before generating.')
      return
    }
    if (!promptText.trim()) {
      setGenerationError('Enter a prompt before generating.')
      return
    }
    if (!selectedProviderReady) {
      setGenerationError(selectedProviderUnavailableReason)
      return
    }
    const requestedPrompt = promptText.trim()
    const requestedImageCount = selectedFinalImageCount
    setActiveGenerationCount(requestedImageCount)
    setGeneratingImage(true)
    setGenerationStage('queued')
    setLivePreviews([])
    setLiveGenerationText('')
    setLiveGenerationThinking('')
    setLastGeminiChargeInfo(null)
    followLivePreviewRef.current = true
    setSelectedImageAssetId(null)
    setSelectedLivePreviewId(null)
    setGenerationError(null)
    try {
      const response = await apiFetch('/v1/image/generations?stream=true', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
        body: JSON.stringify({
          provider: selectedModelOption.provider,
          model: selectedModelOption.model,
          prompt: requestedPrompt,
          count: requestedImageCount,
          // partial_images are Codex progression frames per final output; Gemini ignores this and streams real SDK chunks.
          partial_images: isGoogleGeminiModel ? 0 : 3,
          size: isGoogleGeminiModel ? selectedGoogleAspectRatio : selectedOpenAIImageSize,
          settings: isGoogleGeminiModel
            ? { aspect_ratio: selectedGoogleAspectRatio, image_size: selectedGoogleImageSize }
            : { size: selectedOpenAIImageSize },
          target: {
            kind: 'workspace_image_session',
            thread_id: selectedThread.id,
          },
        }),
      })
      if (!response.ok) {
        const errorText = await response.text()
        throw new Error(errorText || `Image generation failed with status ${response.status}`)
      }
      if (!response.body) {
        throw new Error('Image generation stream returned no body')
      }
      const reader = response.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''
      let finalResponse: ImageGenerationResponse | null = null
      const handleSSEBlock = (block: string) => {
        const lines = block.split('\n')
        let eventName = 'message'
        const dataLines: string[] = []
        for (const line of lines) {
          if (line.startsWith('event:')) eventName = line.slice('event:'.length).trim()
          if (line.startsWith('data:')) dataLines.push(line.slice('data:'.length).trimStart())
        }
        const dataText = dataLines.join('\n').trim()
        if (!dataText) return
        const payload = JSON.parse(dataText) as { ok?: boolean; error?: string; http_status?: number; event?: LiveGenerationEvent; result?: ImageGenerationResponse['result'] }
        if (eventName === 'error' || payload.ok === false) {
          const status = typeof payload.http_status === 'number' ? payload.http_status : null
          throw new Error(`${payload.error || 'Image generation failed'}${status ? ` (status ${status})` : ''}`)
        }
        if (eventName === 'started' || payload.event?.type === 'started') {
          setGenerationStage('generating')
        }
        if (eventName === 'generating' || payload.event?.type === 'generating') {
          setGenerationStage((stage) => (stage === 'partial' ? stage : 'generating'))
        }
        if (eventName === 'partial_image' || payload.event?.type === 'partial_image') {
          const preview = livePreviewFromEvent(payload.event ?? {})
          if (preview) {
            setGenerationStage('partial')
            setLivePreviews((current) => mergeLivePreview(current, preview))
            if (followLivePreviewRef.current) {
              setSelectedLivePreviewId(preview.id)
            }
          }
        }
        if (eventName === 'text' && payload.event?.text) {
          setGenerationStage((stage) => (stage === 'partial' ? stage : 'generating'))
          setLiveGenerationText((current) => current + payload.event?.text)
        }
        if (eventName === 'thinking' && payload.event?.thinking) {
          setGenerationStage((stage) => (stage === 'partial' ? stage : 'generating'))
          setLiveGenerationThinking((current) => current + payload.event?.thinking)
        }
        if (eventName === 'image' || payload.event?.type === 'image') {
          setGenerationStage('generating')
        }
        if (eventName === 'completed' && payload.result) {
          finalResponse = { result: payload.result }
          const finalPartials = (payload.result.partials ?? [])
            .map(livePreviewFromPartial)
            .filter((preview): preview is LiveGenerationPreview => Boolean(preview))
          if (finalPartials.length > 0) {
            const collapsedPartials = finalPartials.reduce((current, preview) => mergeLivePreview(current, preview), [] as LiveGenerationPreview[])
            setLivePreviews(collapsedPartials)
            if (followLivePreviewRef.current) {
              setSelectedLivePreviewId(collapsedPartials[collapsedPartials.length - 1]?.id ?? null)
            }
          }
          setGenerationStage('final')
        }
      }
      while (true) {
        const { done, value } = await reader.read()
        if (value) {
          buffer += decoder.decode(value, { stream: !done })
          const blocks = buffer.split(/\n\n/)
          buffer = blocks.pop() ?? ''
          for (const block of blocks) {
            handleSSEBlock(block)
          }
        }
        if (done) break
      }
      if (buffer.trim()) {
        handleSSEBlock(buffer)
      }
      const completedResponse = finalResponse as ImageGenerationResponse | null
      if (!completedResponse) {
        throw new Error('Image generation stream ended before final result')
      }
      const updatedThread = mapImageThread(completedResponse.result?.target?.thread ?? {})
      if (!updatedThread) {
        throw new Error('Image generation returned no updated thread')
      }
      queryClient.setQueryData<ImageThreadRecord[]>(['image-tool-threads', selectedWorkspacePath], (current = []) => {
        const withoutUpdated = current.filter((thread) => thread.id !== updatedThread.id)
        return [updatedThread, ...withoutUpdated]
      })
      const chargeInfo = extractGeminiChargeInfo(completedResponse.result?.provider_response)
      setLastGeminiChargeInfo(chargeInfo)
      const generatedAssets = completedResponse.result?.assets ?? []
      const generatedAssetId = generatedAssets[generatedAssets.length - 1]?.id ?? updatedThread.imageAssetOrder[updatedThread.imageAssetOrder.length - 1]
      followLivePreviewRef.current = false
      setLivePreviews([])
      setSelectedLivePreviewId(null)
      if (generatedAssetId) {
        setSelectedImageAssetId(generatedAssetId)
      }
      setSelectedThreadId(updatedThread.id)
      if (!routeImageSessionId) {
        if (routeWorkspaceSlug) {
          void navigate({ to: '/$workspaceSlug/tools/image/$imageSessionId', params: { workspaceSlug: routeWorkspaceSlug, imageSessionId: updatedThread.id } })
        } else {
          void navigate({ to: '/tools/image/$imageSessionId', params: { imageSessionId: updatedThread.id } })
        }
      }
      setPromptText('')
      await queryClient.invalidateQueries({ queryKey: ['image-tool-threads', selectedWorkspacePath] })
    } catch (error) {
      setGenerationStage('error')
      setGenerationError(error instanceof Error ? error.message : String(error))
    } finally {
      setGeneratingImage(false)
    }
  }, [isGoogleGeminiModel, navigate, promptText, queryClient, routeImageSessionId, routeWorkspaceSlug, selectedFinalImageCount, selectedGoogleAspectRatio, selectedGoogleImageSize, selectedOpenAIImageSize, selectedModelOption.model, selectedModelOption.provider, selectedProviderReady, selectedProviderUnavailableReason, selectedThread, selectedWorkspacePath])

  return (
    <div className="absolute inset-0 overflow-hidden bg-[var(--app-bg)] text-[var(--app-text)]">
      <div className="mx-auto flex h-full w-full max-w-none flex-col px-4 py-4 sm:px-5 sm:py-5">
        {createError || generationError ? (
          <div className="mb-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 text-sm text-[var(--app-text)]">
            {createError || generationError}
          </div>
        ) : null}

        <main className="flex min-h-0 flex-1 overflow-hidden py-5">
          <SwarmToolSidebar
            backLabel={routeWorkspaceSlug ? (activeSessionId ? 'Back to chat' : 'Workspace') : 'Launcher'}
            onBack={handleBackToWorkspace}
            darkModeEnabled={blackModeEnabled}
            onToggleDarkMode={() => setBlackModeEnabled((enabled) => !enabled)}
            darkModeStyle={darkOverrideButtonStyle}
            darkModeActiveClassName="border-[var(--image-tool-user-theme-accent)] bg-[var(--image-tool-user-theme-surface)] text-[var(--image-tool-user-theme-text)] hover:bg-[var(--image-tool-user-theme-surface-hover)]"
            toolIcon={<Image size={16} strokeWidth={1.8} />}
            toolTitle="Image"
            toolDescription="Swarm image sessions store generated assets in Swarm’s private app-managed workspace bucket, outside the repository."
            createLabel="Start new image session"
            createTitle={newSessionTitle}
            onCreateTitleChange={setNewSessionTitle}
            createPlaceholder={DEFAULT_IMAGE_SESSION_TITLE}
            onCreate={() => void handleCreateSession()}
            creating={creatingSession}
            createDisabled={!selectedWorkspacePath}
            sessionsLabel="Image sessions"
            sessionsLoading={imageThreadsQuery.isLoading || workspacesQuery.isLoading}
            sessions={imageThreads.map((thread) => ({
              id: thread.id,
              title: thread.title || 'Image Thread',
              subtitle: String(thread.imageAssets.length) + ' asset' + (thread.imageAssets.length === 1 ? '' : 's') + ' · ' + formatStartedAt(thread.createdAt),
            }))}
            selectedSessionId={selectedThread?.id ?? null}
            onSelectSession={(threadId) => {
              setSelectedThreadId(threadId)
              if (routeWorkspaceSlug) {
                void navigate({ to: '/$workspaceSlug/tools/image/$imageSessionId', params: { workspaceSlug: routeWorkspaceSlug, imageSessionId: threadId } })
              } else {
                void navigate({ to: '/tools/image/$imageSessionId', params: { imageSessionId: threadId } })
              }
            }}
            emptySessionsMessage="No image sessions yet. Start session to get started."
            defaultSessionTitle="Image Thread"
          >
            {selectedThread ? (
              <div className="min-h-0 overflow-y-auto py-3">
                <p className="mb-2 px-2 text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Current image session</p>
                <div className="pt-3">
                  <div className="border border-[var(--app-border)] bg-[var(--app-bg)] p-3">
                    <h2 className="truncate text-sm font-semibold text-[var(--app-text)]">{selectedThread.title || 'Image thread'}</h2>
                    <p className="mt-2 break-all text-[11px] leading-5 text-[var(--app-text-subtle)]">{selectedThread.workspacePath}</p>
                    {selectedSessionURL ? <p className="mt-2 break-all text-[10px] leading-4 text-[var(--app-text-subtle)]">URL: {selectedSessionURL}</p> : null}
                    <div className="mt-4 grid grid-cols-2 gap-2 text-[11px]">
                      <div className="border border-[var(--app-border)] bg-[var(--app-surface)] p-2"><div className="text-[10px] uppercase text-[var(--app-text-subtle)]">Folders</div><div className="mt-1 text-[var(--app-text)]">{selectedThread.imageFolders.length}</div></div>
                      <div className="border border-[var(--app-border)] bg-[var(--app-surface)] p-2"><div className="text-[10px] uppercase text-[var(--app-text-subtle)]">Assets</div><div className="mt-1 text-[var(--app-text)]">{selectedThread.imageAssets.length}</div></div>
                    </div>
                    <div className="mt-3 grid grid-cols-2 gap-2">
                      <Button variant="outline" className="h-8 rounded-xl px-3 text-xs" onClick={() => void handleCopyFilePath(selectedCopyFilePath)} disabled={!selectedCopyFilePath}>
                        <Clipboard size={13} />Copy filepath
                      </Button>
                      <Button variant="outline" className="h-8 rounded-xl px-3 text-xs" onClick={() => void handleCopySessionLink()} disabled={!selectedSessionURL}>
                        <Link2 size={13} />Copy URL
                      </Button>
                    </div>
                    <Button variant="outline" className="mt-2 h-8 w-full rounded-xl px-3 text-xs" onClick={() => void handleRevealImageStorage()} disabled={revealingStorage}>
                      <FolderOpen size={13} />{revealingStorage ? 'Opening…' : 'Reveal local folder'}
                    </Button>
                    {(pathCopyStatus || sessionLinkCopyStatus) ? <p className="mt-2 text-[10px] text-[var(--app-text-muted)]">{pathCopyStatus || sessionLinkCopyStatus}</p> : null}
                    {(lastStoragePath || selectedSessionStoragePath) ? <p className="mt-2 break-all text-[10px] leading-4 text-[var(--app-text-subtle)]">{lastStoragePath || selectedSessionStoragePath}</p> : null}
                  </div>
                </div>
              </div>
            ) : null}
          </SwarmToolSidebar>

          <section className="flex min-w-0 flex-1 flex-col overflow-y-auto">
            <div className="mb-4 flex items-center justify-between gap-3 lg:hidden">
              <Button variant="ghost" className="h-9 rounded-xl px-3 text-[var(--app-text-muted)]" onClick={handleBackToWorkspace}><ArrowLeft size={15} />{routeWorkspaceSlug ? (activeSessionId ? 'Back to chat' : 'Workspace') : 'Launcher'}</Button>
              <Button variant="outline" style={darkOverrideButtonStyle} className={`h-8 w-8 rounded-xl px-0 ${blackModeEnabled ? 'border-[var(--image-tool-user-theme-accent)] bg-[var(--image-tool-user-theme-surface)] text-[var(--image-tool-user-theme-text)] hover:bg-[var(--image-tool-user-theme-surface-hover)]' : ''}`} onClick={() => setBlackModeEnabled((enabled) => !enabled)} aria-label="Toggle dark mode override for this page" aria-pressed={blackModeEnabled} title="Toggle dark mode override for this page"><Moon size={14} aria-hidden="true" /></Button>
            </div>

            {!selectedThread ? (
              <div className="grid min-h-full place-items-center border border-dashed border-[var(--app-border)] bg-[var(--app-surface)] px-6 py-16 text-center">
                <div className="max-w-sm">
                  <Image className="mx-auto text-[var(--app-primary)]" size={42} strokeWidth={1.5} />
                  <h2 className="mt-5 text-2xl font-semibold tracking-[-0.05em] text-[var(--app-text)]">Start session to get started</h2>
                  <p className="mt-3 text-sm leading-6 text-[var(--app-text-muted)]">
                    Name an image session in the reusable tool sidebar. The post-start image interface will be added next.
                  </p>
                </div>
              </div>
            ) : (
              <div className="grid min-h-full gap-3 xl:h-full xl:min-h-0 xl:grid-cols-[minmax(0,1fr)_360px] xl:overflow-hidden 2xl:grid-cols-[minmax(0,1fr)_400px]">
                <div className="flex min-h-[520px] flex-col overflow-hidden border border-[var(--app-border)] bg-[var(--app-surface)] xl:min-h-0">
                  <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)] px-3 py-2">
                    <div className="min-w-0">
                      <p className="text-[10px] font-medium uppercase tracking-[0.22em] text-[var(--app-text-subtle)]">Generation area</p>
                      <h2 className="mt-1 truncate text-xl font-semibold tracking-[-0.045em] text-[var(--app-text)]">{selectedThread.title || 'Image thread'}</h2>
                    </div>
                    <div className="flex flex-wrap items-center justify-end gap-2 text-xs text-[var(--app-text-muted)]">
                      {selectedImageAsset ? (
                        <>
                          <Button variant="outline" className="h-8 rounded-xl px-3 text-xs" onClick={() => void handleDownloadSelectedImage()}>
                            <Download size={13} />Download
                          </Button>
                          <Button variant="outline" className="h-8 rounded-xl px-3 text-xs" onClick={() => void handleCopyFilePath(selectedCopyFilePath)} disabled={!selectedCopyFilePath}>
                            <Clipboard size={13} />Copy filepath
                          </Button>
                          <Button variant="outline" className="h-8 rounded-xl px-3 text-xs" onClick={handleOpenSelectedImage}>
                            <ExternalLink size={13} />Open full-res
                          </Button>
                        </>
                      ) : null}
                      <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-bg)] px-2.5 py-1">{selectedModelLabel}</span>
                      <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-bg)] px-2.5 py-1">{selectedShapeLabel}</span>
                      <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-bg)] px-2.5 py-1">{selectedSizeLabel}</span>
                    </div>
                  </div>

                  <div className="grid min-h-0 flex-1 grid-rows-[minmax(0,1fr)_auto]">
                    <div className="relative grid min-h-0 place-items-center overflow-hidden bg-[radial-gradient(circle_at_top,var(--app-surface-hover),transparent_34%),var(--app-bg)] px-2 py-2 sm:px-4 sm:py-4">
                      <div className="absolute left-4 top-4 rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1 text-xs text-[var(--app-text-muted)]">
                        {previewCountLabel}
                      </div>
                      <div className="absolute right-4 top-4 rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1 text-xs text-[var(--app-text-muted)]">
                        {generatingImage ? generationStage === 'partial' ? 'Streaming partial image' : 'Generating…' : 'Carousel preview'}
                      </div>
                      <Button variant="outline" className="absolute left-4 top-1/2 h-10 w-10 -translate-y-1/2 rounded-full px-0" onClick={handlePreviousPreview} disabled={orderedImageAssets.length <= 1} aria-label="Previous image">
                        <ChevronLeft size={18} />
                      </Button>
                      <div className="grid h-full min-h-0 w-full place-items-center overflow-hidden border border-[var(--app-border)] bg-[linear-gradient(135deg,var(--app-surface)_0%,var(--app-bg)_52%,var(--app-surface-hover)_100%)] shadow-2xl shadow-black/10">
                        {activeLivePreview ? (
                          <div className="relative flex h-full min-h-0 w-full items-center justify-center p-2 text-center">
                            <img src={activeLivePreview.dataUrl} alt={activeLivePreview.label} className="h-full w-full object-contain" />
                            <div className="absolute bottom-3 left-3 rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1 text-xs text-[var(--app-text-muted)]">
                              {activeLivePreview.label} · generating
                            </div>
                          </div>
                        ) : selectedImageAsset ? (
                          <div className="grid h-full min-h-0 w-full grid-rows-[minmax(0,1fr)_auto] items-center gap-2 p-2 text-center">
                            <button type="button" className="group relative grid h-full min-h-0 w-full cursor-zoom-in place-items-center overflow-hidden" onClick={openImageLightbox} aria-label="Open image fullscreen">
                              <img src={selectedImageSource} alt={selectedImageAsset.name} className="h-full min-h-0 w-full object-contain transition duration-150 group-hover:scale-[1.01]" />
                              <span className="pointer-events-none absolute bottom-3 right-3 rounded-full border border-white/20 bg-black/60 px-3 py-1 text-xs font-medium text-white opacity-0 shadow-lg transition group-hover:opacity-100">Click for fullscreen</span>
                            </button>
                            <div>
                              <p className="text-sm font-semibold tracking-[-0.04em] text-[var(--app-text)]">{selectedImageAsset.name}</p>
                              <p className="mt-1 break-all text-[10px] leading-4 text-[var(--app-text-muted)]">{selectedImageAsset.path}</p>
                            </div>
                          </div>
                        ) : generatingImage ? (
                          <div className="flex h-full w-full flex-col items-center justify-center gap-4 p-8 text-center">
                            <Sparkles className="animate-pulse text-[var(--app-primary)]" size={56} strokeWidth={1.35} />
                            <div className="max-w-md">
                              <p className="text-2xl font-semibold tracking-[-0.055em] text-[var(--app-text)]">Generating…</p>
                              <p className="mt-3 text-sm leading-6 text-[var(--app-text-muted)]">{isGoogleGeminiModel ? 'Waiting for Gemini streamed chunks and final image data.' : 'Waiting for the first streamed partial image from OpenAI.'}</p>
                            </div>
                          </div>
                        ) : (
                          <div className="flex h-full w-full flex-col items-center justify-center gap-4 p-8 text-center">
                            <Sparkles className="text-[var(--app-primary)]" size={56} strokeWidth={1.35} />
                            <div className="max-w-md">
                              <p className="text-2xl font-semibold tracking-[-0.055em] text-[var(--app-text)]">Your generated image will appear here</p>
                              <p className="mt-3 text-sm leading-6 text-[var(--app-text-muted)]">Choose a prompt, model, and model-specific output shape below, then generate one image into this managed session.</p>
                            </div>
                          </div>
                        )}
                      </div>
                      <Button variant="outline" className="absolute right-4 top-1/2 h-10 w-10 -translate-y-1/2 rounded-full px-0" onClick={handleNextPreview} disabled={orderedImageAssets.length <= 1} aria-label="Next image">
                        <ChevronRight size={18} />
                      </Button>
                    </div>

                    <div className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2">
                      <div className="mb-1.5 flex items-center justify-between gap-3 text-[10px] font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">
                        <span>Session images</span>
                        <span>{orderedImageAssets.length} saved</span>
                      </div>
                      <div className="flex gap-2 overflow-x-auto pb-1">
                        {thumbnailItems.length > 0 ? thumbnailItems.map((item) => {
                          const selected = item.id === selectedThumbnailId
                          const imageSrc = item.kind === 'asset'
                            ? item.asset.url ?? imageAssetURL(selectedThread.id, item.asset.id)
                            : item.kind === 'live'
                              ? item.preview.dataUrl
                              : ''
                          return (
                            <button
                              key={item.id}
                              type="button"
                              onClick={() => {
                                if (item.kind === 'asset') {
                                  followLivePreviewRef.current = false
                                  setSelectedLivePreviewId(null)
                                  setSelectedImageAssetId(item.asset.id)
                                } else if (item.kind === 'live') {
                                  followLivePreviewRef.current = true
                                  setSelectedImageAssetId(null)
                                  setSelectedLivePreviewId(item.preview.id)
                                }
                              }}
                              className={['group min-w-[112px] max-w-[112px] border bg-[var(--app-bg)] p-2 text-left transition hover:bg-[var(--app-surface-hover)]', selected ? 'border-[var(--app-border-accent)] ring-1 ring-[var(--app-border-accent)]' : 'border-[var(--app-border)]'].join(' ')}
                              aria-pressed={selected}
                            >
                              <div className="grid aspect-square place-items-center overflow-hidden border border-[var(--app-border)] bg-[var(--app-surface)]">
                                {imageSrc ? (
                                  <img src={imageSrc} alt={item.label} className="h-full w-full object-contain" />
                                ) : (
                                  <Sparkles className="animate-pulse text-[var(--app-primary)]" size={24} strokeWidth={1.4} />
                                )}
                              </div>
                              <p className="mt-2 truncate text-xs font-medium text-[var(--app-text)]">{item.label}</p>
                              <p className="mt-0.5 truncate text-[10px] text-[var(--app-text-subtle)]">{item.meta}</p>
                            </button>
                          )
                        }) : (
                          <div className="min-w-full rounded-xl border border-dashed border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-5 text-center text-xs text-[var(--app-text-muted)]">
                            Generated images will populate this scrollable carousel as they are saved.
                          </div>
                        )}
                      </div>
                    </div>
                  </div>
                </div>

                <aside className="min-h-0 overflow-y-auto border border-[var(--app-border)] bg-[var(--app-surface)] p-3 xl:h-full">
                  <div className="flex min-h-full flex-col gap-3">
                    <div className="flex items-center justify-between gap-3">
                      <div>
                        <p className="text-[10px] font-medium uppercase tracking-[0.22em] text-[var(--app-text-subtle)]">Controls</p>
                        <h3 className="mt-1 text-lg font-semibold tracking-[-0.045em] text-[var(--app-text)]">Prompt studio</h3>
                      </div>
                      <div className="grid grid-cols-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] p-1 text-[10px] font-bold uppercase tracking-[0.14em]">
                        <button
                          type="button"
                          className={['rounded-lg px-3 py-1.5 transition', generationControlMode === 'manual' ? 'bg-[var(--app-primary)] text-white shadow-sm' : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]'].join(' ')}
                          onClick={() => setGenerationControlMode('manual')}
                        >
                          Manual
                        </button>
                        <button
                          type="button"
                          className={['rounded-lg px-3 py-1.5 transition', generationControlMode === 'ai' ? 'bg-[var(--app-primary)] text-white shadow-sm' : 'text-[var(--app-text-subtle)] opacity-70'].join(' ')}
                          disabled
                          title="AI prompt mode coming soon"
                        >
                          AI soon
                        </button>
                      </div>
                    </div>

                    <label className="flex flex-col gap-1.5">
                      <span className="text-[10px] font-bold uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Prompt</span>
                      <Textarea
                        rows={8}
                        className="min-h-40 w-full resize-none rounded-xl border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 text-sm leading-6 focus:ring-1 focus:ring-[var(--app-primary)] xl:min-h-48"
                        value={promptText}
                        onChange={(event) => setPromptText(event.target.value)}
                        placeholder="Make me a swarm cube"
                      />
                    </label>

                    <div className="space-y-2">
                      <div className="flex items-center justify-between gap-3">
                        <span className="text-[10px] font-bold uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Shape</span>
                        <span className="text-[10px] text-[var(--app-text-muted)]">{isGoogleGeminiModel ? 'Gemini aspect' : 'Output size'}</span>
                      </div>
                      <div className="grid grid-cols-2 gap-2">
                        {isGoogleGeminiModel ? GOOGLE_GEMINI_ASPECT_RATIO_OPTIONS.map((option) => (
                          <button key={option.id} type="button" onClick={() => setSelectedGoogleAspectRatio(option.id)} className={['relative flex flex-col justify-center rounded-xl border p-2.5 text-left transition-all', selectedGoogleAspectRatio === option.id ? 'border-[var(--app-border-accent)] bg-[var(--app-surface-active)] ring-1 ring-[var(--app-border-accent)]' : 'border-[var(--app-border)] bg-[var(--app-bg)] hover:border-[var(--app-text-muted)]'].join(' ')} disabled={generatingImage}>
                            <span className="text-xs font-bold">{option.label}</span>
                            <span className="mt-0.5 text-[10px] text-[var(--app-text-muted)]">{option.helper}</span>
                            <div className={['absolute right-2 top-2 rounded-sm border', option.id === '1:1' ? 'h-4 w-4' : option.id === '16:9' || option.id === '21:9' || option.id === '3:2' || option.id === '4:3' ? 'h-3 w-5' : 'h-5 w-3', selectedGoogleAspectRatio === option.id ? 'border-[var(--app-primary)] bg-[var(--app-primary)]/20' : 'border-[var(--app-border)] bg-[var(--app-surface)]'].join(' ')} />
                          </button>
                        )) : OPENAI_IMAGE_SIZE_OPTIONS.map((option) => (
                          <button key={option.id} type="button" onClick={() => setSelectedOpenAIImageSize(option.id)} className={['relative flex flex-col justify-center rounded-xl border p-2.5 text-left transition-all', selectedOpenAIImageSize === option.id ? 'border-[var(--app-border-accent)] bg-[var(--app-surface-active)] ring-1 ring-[var(--app-border-accent)]' : 'border-[var(--app-border)] bg-[var(--app-bg)] hover:border-[var(--app-text-muted)]'].join(' ')} disabled={generatingImage}>
                            <span className="text-xs font-bold">{option.label}</span>
                            <span className="mt-0.5 text-[10px] text-[var(--app-text-muted)]">{option.helper}</span>
                            <span className="mt-0.5 text-[9px] text-[var(--app-text-subtle)]">{option.aspectRatio} · {option.size}</span>
                            <div className={['absolute right-2 top-2 rounded-sm border', option.aspectRatio === '1:1' ? 'h-4 w-4' : option.aspectRatio === '3:2' ? 'h-3 w-5' : 'h-5 w-3', selectedOpenAIImageSize === option.id ? 'border-[var(--app-primary)] bg-[var(--app-primary)]/20' : 'border-[var(--app-border)] bg-[var(--app-surface)]'].join(' ')} />
                          </button>
                        ))}
                      </div>
                    </div>

                    <div className="flex flex-col gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] p-3">
                      <div className="flex flex-col gap-1">
                        <span className="text-[10px] font-bold text-[var(--app-text-subtle)]">MODEL</span>
                        <Select className="h-9 rounded-lg border-[var(--app-border)] bg-[var(--app-surface)] px-2 text-xs font-medium disabled:cursor-not-allowed disabled:opacity-60" value={selectedImageModel} onChange={(event) => setSelectedImageModel(event.target.value)} disabled={generatingImage}>
                          {IMAGE_MODEL_OPTIONS.map((option) => (
                            <option key={option.id} value={option.id}>{option.label} · {option.provider === 'google_gemini' ? 'Google API key' : 'OAuth only'}</option>
                          ))}
                        </Select>
                      </div>

                      {!selectedProviderReady ? (
                        <div className="flex items-start gap-2 rounded-lg border border-[var(--app-warning)]/40 bg-[var(--app-warning)]/10 px-2.5 py-2 text-[10px] leading-snug text-[var(--app-text)]">
                          <TriangleAlert size={14} className="mt-0.5 shrink-0 text-[var(--app-warning)]" />
                          <span>{selectedProviderWarning}</span>
                        </div>
                      ) : null}

                      <div className="grid grid-cols-2 gap-2">
                        {isGoogleGeminiModel ? (
                          <label className="flex flex-col gap-1">
                            <span className="text-[10px] font-bold text-[var(--app-text-subtle)]">SIZE</span>
                            <Select
                              className="h-9 rounded-lg border-[var(--app-border)] bg-[var(--app-surface)] px-2 text-xs font-medium"
                              value={selectedGoogleImageSize}
                              onChange={(event) => setSelectedGoogleImageSize(event.target.value)}
                              disabled={generatingImage}
                            >
                              {GOOGLE_GEMINI_IMAGE_SIZE_OPTIONS.map((option) => (
                                <option key={option.id} value={option.id} disabled={option.id === '512' && selectedModelOption.model !== 'gemini-2.5-flash-image'}>{option.label} · {option.helper}</option>
                              ))}
                            </Select>
                          </label>
                        ) : null}

                        <label className="flex flex-col gap-1">
                          <span className="text-[10px] font-bold text-[var(--app-text-subtle)]">QUANTITY</span>
                          <Select
                            className="h-9 rounded-lg border-[var(--app-border)] bg-[var(--app-surface)] px-2 text-xs font-medium"
                            value={String(selectedFinalImageCount)}
                            onChange={(event) => setSelectedFinalImageCount(Number(event.target.value) as (typeof FINAL_IMAGE_COUNT_OPTIONS)[number])}
                            disabled={generatingImage}
                          >
                            {selectedCountOptions.map((count) => (
                              <option key={count} value={count}>{count} final image{count === 1 ? '' : 's'}</option>
                            ))}
                          </Select>
                        </label>
                      </div>
                    </div>

                    {isGoogleGeminiModel && (liveGenerationThinking || liveGenerationText || displayedChargeInfo) ? (
                      <div className="space-y-1 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-2.5 py-2 text-[10px] leading-snug text-[var(--app-text-muted)]">
                        {liveGenerationThinking ? <p><b className="text-[var(--app-text)]">Thinking:</b> {liveGenerationThinking}</p> : null}
                        {liveGenerationText ? <p><b className="text-[var(--app-text)]">Gemini:</b> {liveGenerationText}</p> : null}
                        {displayedChargeInfo ? <p><b className={displayedChargeInfo.hasCharge ? 'text-[var(--app-success)]' : 'text-[var(--app-text)]'}>{displayedChargeInfo.label}</b> · {displayedChargeInfo.detail}</p> : null}
                      </div>
                    ) : null}

                    <div className="mt-auto pt-1">
                      <Button className="h-11 w-full rounded-xl bg-[var(--app-primary)] text-white shadow-sm transition hover:bg-[var(--app-primary)]/90 disabled:bg-[var(--app-surface-hover)] disabled:text-[var(--app-text-muted)]" disabled={!canGenerateImage} onClick={() => void handleGenerateImage()}>
                        <Sparkles size={14} className="mr-2" />
                        <b>{generatingImage ? 'GENERATING…' : `GENERATE ${selectedFinalImageCount}`}</b>
                      </Button>
                    </div>
                  </div>
                </aside>
              </div>
            )}
          </section>
        </main>
        {imageLightboxOpen && selectedThread && selectedImageAsset ? (
          <Dialog className="z-[80] p-0">
            <DialogBackdrop className="bg-black/90" onClick={() => setImageLightboxOpen(false)} />
            <DialogPanel
              className="h-[100dvh] max-h-none w-screen max-w-none rounded-none border-0 bg-black p-0 text-white"
              style={{ height: '100dvh', maxHeight: 'none', width: '100vw' }}
            >
              <div className="grid h-full min-h-0 grid-rows-[auto_minmax(0,1fr)_auto]">
                <div className="relative z-10 flex items-center justify-between gap-3 border-b border-white/10 bg-black/70 px-4 py-3 backdrop-blur">
                  <div className="min-w-0">
                    <p className="text-[10px] font-bold uppercase tracking-[0.22em] text-white/50">Fullscreen preview</p>
                    <h2 className="mt-1 truncate text-sm font-semibold text-white">{selectedImageAsset.name}</h2>
                  </div>
                  <div className="flex shrink-0 flex-wrap items-center justify-end gap-2">
                    <span className="hidden rounded-full border border-white/15 bg-white/10 px-3 py-1 text-xs text-white/75 sm:inline-flex">
                      {activePreviewNumber} / {orderedImageAssets.length}
                    </span>
                    <Button variant="outline" className="h-9 rounded-full border-white/20 bg-white/10 px-3 text-xs text-white hover:bg-white/20" onClick={() => void handleDownloadSelectedImage()}>
                      <Download size={13} />Download
                    </Button>
                    <Button variant="outline" className="h-9 rounded-full border-white/20 bg-white/10 px-3 text-xs text-white hover:bg-white/20" onClick={() => void handleCopyFilePath(selectedCopyFilePath)} disabled={!selectedCopyFilePath}>
                      <Clipboard size={13} />Copy filepath
                    </Button>
                    <Button variant="outline" className="h-9 rounded-full border-white/20 bg-white/10 px-3 text-xs text-white hover:bg-white/20" onClick={handleOpenSelectedImage}>
                      <ExternalLink size={13} />Open full-res
                    </Button>
                    <Button variant="outline" className="h-9 rounded-full border-white/20 bg-white/10 px-3 text-xs text-white hover:bg-white/20" onClick={() => void handleRevealImageStorage(selectedImageAsset.id)} disabled={revealingStorage}>
                      <FolderOpen size={13} />{revealingStorage ? 'Opening…' : 'Reveal local'}
                    </Button>
                    <Button variant="outline" className="h-11 w-11 rounded-full border-white/20 bg-white/10 px-0 text-white hover:bg-white/20" onClick={() => setImageLightboxOpen(false)} aria-label="Close fullscreen preview">
                      <X size={24} strokeWidth={2.4} />
                    </Button>
                  </div>
                </div>

                <div className="relative flex min-h-0 justify-center overflow-auto px-4 py-4 sm:px-8 sm:py-6">
                  <Button variant="outline" className="absolute left-3 top-1/2 z-10 h-12 w-12 -translate-y-1/2 rounded-full border-white/20 bg-black/45 px-0 text-white backdrop-blur hover:bg-white/15" onClick={handlePreviousPreview} disabled={orderedImageAssets.length <= 1} aria-label="Previous image">
                    <ChevronLeft size={24} />
                  </Button>
                  <img
                    key={selectedImageAsset.id}
                    src={selectedImageSource}
                    alt={selectedImageAsset.name}
                    className="h-auto w-auto max-w-full select-none object-contain shadow-2xl shadow-black/70"
                    onLoad={(event) => setLightboxNaturalSize({ width: event.currentTarget.naturalWidth, height: event.currentTarget.naturalHeight })}
                  />
                  <Button variant="outline" className="absolute right-3 top-1/2 z-10 h-12 w-12 -translate-y-1/2 rounded-full border-white/20 bg-black/45 px-0 text-white backdrop-blur hover:bg-white/15" onClick={handleNextPreview} disabled={orderedImageAssets.length <= 1} aria-label="Next image">
                    <ChevronRight size={24} />
                  </Button>
                </div>

                <div className="border-t border-white/10 bg-black/80 px-3 py-3 backdrop-blur">
                  <div className="mb-2 flex flex-wrap items-center justify-between gap-2 text-xs text-white/60">
                    <span>Session images</span>
                    <span>{lightboxNaturalSize ? `${lightboxNaturalSize.width} × ${lightboxNaturalSize.height}px` : 'Resolution loading…'} · {formatBytes(selectedImageAsset.sizeBytes)}</span>
                  </div>
                  {(pathCopyStatus || imageActionStatus || lastStoragePath) ? (
                    <div className="mb-2 space-y-1 rounded-lg border border-white/10 bg-white/5 px-2 py-1.5 text-[10px] text-white/55">
                      {(pathCopyStatus || imageActionStatus) ? <p>{pathCopyStatus || imageActionStatus}</p> : null}
                      {lastStoragePath ? <p className="break-all">{lastStoragePath}</p> : null}
                    </div>
                  ) : null}
                  <div className="flex gap-2 overflow-x-auto pb-1">
                    {orderedImageAssets.map((asset, index) => {
                      const selected = asset.id === selectedImageAsset.id
                      const imageSrc = asset.url ?? imageAssetURL(selectedThread.id, asset.id)
                      return (
                        <button
                          key={asset.id}
                          type="button"
                          onClick={() => {
                            followLivePreviewRef.current = false
                            setSelectedLivePreviewId(null)
                            setSelectedImageAssetId(asset.id)
                          }}
                          className={['group min-w-[92px] max-w-[92px] rounded-xl border bg-white/5 p-1.5 text-left transition hover:bg-white/10', selected ? 'border-white ring-1 ring-white' : 'border-white/15'].join(' ')}
                          aria-pressed={selected}
                        >
                          <div className="grid aspect-square place-items-center overflow-hidden rounded-lg bg-white/5">
                            <img src={imageSrc} alt={asset.name} className="h-full w-full object-contain" />
                          </div>
                          <p className="mt-1 truncate text-[10px] font-medium text-white/80">{index + 1}. {asset.name}</p>
                        </button>
                      )
                    })}
                  </div>
                </div>
              </div>
            </DialogPanel>
          </Dialog>
        ) : null}
      </div>
    </div>
  )
}
