import { type CSSProperties, useCallback, useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, ChevronLeft, ChevronRight, Clock3, Image, Moon, Sparkles } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
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
    target?: {
      thread?: ImageThreadWire
    }
  }
}

type LiveGenerationEvent = {
  type?: string
  partial_image_b64?: string
  partial_image_index?: number
  sequence_number?: number
  output_format?: string
  size?: string
  quality?: string
  background?: string
}

type LiveGenerationPreview = {
  id: string
  index: number
  dataUrl: string
  label: string
}

type GenerationStage = 'idle' | 'queued' | 'generating' | 'partial' | 'final' | 'error'

const IMAGE_TOOL_BLACK_MODE_STORAGE_KEY = 'swarm.imageTool.blackMode'
const DEFAULT_IMAGE_SESSION_TITLE = 'Swarm image session'

const IMAGE_MODEL_OPTIONS = [
  { id: 'codex-openai-gpt-image', provider: 'codex_openai', model: 'gpt-5.5', label: 'GPT Image via Codex', helper: 'Uses Codex OAuth and OpenAI Responses image_generation', kind: 'openai-gpt-image' },
  { id: 'google-imagen', provider: 'google_imagen', model: 'imagen-4.0-generate-preview-06-06', label: 'Imagen (Google)', helper: 'Uses Google aspect ratio + 1K/2K', kind: 'google-imagen' },
] as const

const OPENAI_IMAGE_SIZE_OPTIONS = [
  { id: 'auto', label: 'Auto', helper: 'Model chooses', aspectRatio: '1:1' },
  { id: '1024x1024', label: 'Square', helper: '1024 × 1024', aspectRatio: '1:1' },
  { id: '1536x1024', label: 'Landscape', helper: '1536 × 1024', aspectRatio: '3:2' },
  { id: '1024x1536', label: 'Portrait', helper: '1024 × 1536', aspectRatio: '2:3' },
]

const GOOGLE_IMAGE_ASPECT_RATIO_OPTIONS = [
  { id: '1:1', label: 'Square', helper: 'Default' },
  { id: '3:4', label: 'Portrait', helper: 'Media' },
  { id: '4:3', label: 'Landscape', helper: 'Photo' },
  { id: '9:16', label: 'Story', helper: 'Mobile' },
  { id: '16:9', label: 'Wide', helper: 'Landscape' },
]

const GOOGLE_IMAGE_SIZE_OPTIONS = [
  { id: '1K', label: '1K', helper: 'Default' },
  { id: '2K', label: '2K', helper: 'Standard/Ultra only' },
]

function livePreviewFromEvent(event: LiveGenerationEvent): LiveGenerationPreview | null {
  const base64 = String(event.partial_image_b64 ?? '').trim()
  if (!base64) return null
  const index = typeof event.partial_image_index === 'number' ? event.partial_image_index : 0
  const format = String(event.output_format ?? 'png').trim() || 'png'
  return {
    id: `${typeof event.sequence_number === 'number' ? event.sequence_number : Date.now()}-${index}`,
    index,
    dataUrl: `data:image/${format};base64,${base64}`,
    label: `Partial ${index + 1}`,
  }
}

function livePreviewFromPartial(partial: ImageGenerationPartial): LiveGenerationPreview | null {
  const dataUrl = String(partial.data_url ?? '').trim()
  const base64 = String(partial.base64_image ?? '').trim()
  if (!dataUrl && !base64) return null
  const index = typeof partial.partial_image_index === 'number' ? partial.partial_image_index : 0
  const format = String(partial.output_format ?? 'png').trim() || 'png'
  return {
    id: `final-partial-${typeof partial.sequence_number === 'number' ? partial.sequence_number : index}`,
    index,
    dataUrl: dataUrl || `data:image/${format};base64,${base64}`,
    label: `Partial ${index + 1}`,
  }
}

function mergeLivePreview(current: LiveGenerationPreview[], next: LiveGenerationPreview): LiveGenerationPreview[] {
  const withoutSameIndex = current.filter((preview) => preview.index !== next.index)
  return [...withoutSameIndex, next].sort((a, b) => a.index - b.index)
}

function generationTimeline(stage: GenerationStage, partialCount: number) {
  return [
    { id: 'queued', label: 'Queued', status: stage === 'idle' ? 'Ready' : 'Done', detail: 'Prompt and settings locked in' },
    { id: 'generating', label: 'Generating', status: stage === 'generating' ? 'Live' : stage === 'partial' || stage === 'final' ? 'Done' : 'Next', detail: 'OpenAI image generation tool is running' },
    { id: 'partial', label: 'Preview', status: stage === 'partial' ? 'Live' : stage === 'final' ? 'Done' : partialCount > 0 ? 'Ready' : 'Waiting', detail: partialCount > 0 ? `${partialCount} streamed partial image${partialCount === 1 ? '' : 's'}` : 'Waiting for first partial image' },
    { id: 'final', label: 'Final', status: stage === 'final' ? 'Ready' : stage === 'error' ? 'Blocked' : 'Waiting', detail: stage === 'final' ? 'Final PNG stored in managed session storage' : 'Waiting for completed result' },
  ]
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

function imagePreviewAspectClass(aspectRatio: string): string {
  switch (aspectRatio) {
    case '2:3':
      return 'aspect-[2/3]'
    case '3:2':
      return 'aspect-[3/2]'
    case '3:4':
      return 'aspect-[3/4]'
    case '4:3':
      return 'aspect-[4/3]'
    case '9:16':
      return 'aspect-[9/16]'
    case '16:9':
      return 'aspect-[16/9]'
    default:
      return 'aspect-square'
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
  const workspaceImageToolMatch = matchRoute({ to: '/$workspaceSlug/tools/image', fuzzy: false })
  const routeWorkspaceSlug = workspaceImageToolMatch ? workspaceImageToolMatch.workspaceSlug.trim() : ''
  const activeSessionId = useDesktopStore((state) => state.activeSessionId)
  const activeWorkspacePath = useDesktopStore((state) => state.activeWorkspacePath)

  const [createError, setCreateError] = useState<string | null>(null)
  const [generationError, setGenerationError] = useState<string | null>(null)
  const [newSessionTitle, setNewSessionTitle] = useState('')
  const [creatingSession, setCreatingSession] = useState(false)
  const [generatingImage, setGeneratingImage] = useState(false)
  const [generationStage, setGenerationStage] = useState<GenerationStage>('idle')
  const [livePreviews, setLivePreviews] = useState<LiveGenerationPreview[]>([])
  const [selectedLivePreviewId, setSelectedLivePreviewId] = useState<string | null>(null)
  const [selectedThreadId, setSelectedThreadId] = useState<string | null>(null)
  const [selectedImageAssetId, setSelectedImageAssetId] = useState<string | null>(null)
  const [selectedImageModel, setSelectedImageModel] = useState<string>(IMAGE_MODEL_OPTIONS[0]?.id ?? '')
  const [selectedOpenAIImageSize, setSelectedOpenAIImageSize] = useState('auto')
  const [selectedGoogleAspectRatio, setSelectedGoogleAspectRatio] = useState('1:1')
  const [selectedGoogleImageSize, setSelectedGoogleImageSize] = useState('1K')
  const [promptText, setPromptText] = useState('')
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
    if (orderedImageAssets.length === 0) return null
    return orderedImageAssets.find((asset) => asset.id === selectedImageAssetId) ?? orderedImageAssets[0]
  }, [orderedImageAssets, selectedImageAssetId])

  const selectedImageAssetIndex = selectedImageAsset
    ? orderedImageAssets.findIndex((asset) => asset.id === selectedImageAsset.id)
    : -1
  const activePreviewNumber = selectedImageAssetIndex >= 0 ? selectedImageAssetIndex + 1 : 1
  const selectedModelOption = IMAGE_MODEL_OPTIONS.find((option) => option.id === selectedImageModel) ?? IMAGE_MODEL_OPTIONS[0]
  const selectedProviderStatus = (imageProvidersQuery.data ?? []).find((provider) => provider.id === selectedModelOption.provider)
  const selectedProviderReady = selectedProviderStatus?.ready === true
  const selectedProviderUnavailableReason = selectedProviderStatus?.reason || 'Image provider is unavailable'
  const isGoogleImagenModel = selectedModelOption.kind === 'google-imagen'
  const selectedOpenAISizeOption = OPENAI_IMAGE_SIZE_OPTIONS.find((option) => option.id === selectedOpenAIImageSize) ?? OPENAI_IMAGE_SIZE_OPTIONS[0]
  const selectedGoogleSizeOption = GOOGLE_IMAGE_SIZE_OPTIONS.find((option) => option.id === selectedGoogleImageSize) ?? GOOGLE_IMAGE_SIZE_OPTIONS[0]
  const selectedShapeLabel = isGoogleImagenModel ? selectedGoogleAspectRatio : selectedOpenAISizeOption.aspectRatio
  const selectedSizeLabel = isGoogleImagenModel ? selectedGoogleImageSize : selectedOpenAIImageSize
  const previewAspectClass = imagePreviewAspectClass(selectedShapeLabel)
  const selectedModelLabel = selectedModelOption.label
  const selectedProviderControlLabel = isGoogleImagenModel ? 'Google Imagen controls' : 'OpenAI GPT Image controls'
  const selectedSizeDisplayLabel = isGoogleImagenModel
    ? selectedGoogleSizeOption.label + ' · ' + selectedGoogleAspectRatio
    : selectedOpenAISizeOption.helper
  const canGenerateImage = Boolean(selectedThread && promptText.trim() && selectedProviderReady && !generatingImage)
  const activeLivePreview = selectedLivePreviewId
    ? livePreviews.find((preview) => preview.id === selectedLivePreviewId) ?? livePreviews[livePreviews.length - 1] ?? null
    : livePreviews[livePreviews.length - 1] ?? null
  const timelineItems = generationTimeline(generationStage, livePreviews.length)

  useEffect(() => {
    if (orderedImageAssets.length === 0) {
      if (selectedImageAssetId !== null) {
        setSelectedImageAssetId(null)
      }
      return
    }
    if (!selectedImageAssetId || !orderedImageAssets.some((asset) => asset.id === selectedImageAssetId)) {
      setSelectedImageAssetId(orderedImageAssets[0].id)
    }
  }, [orderedImageAssets, selectedImageAssetId])

  useEffect(() => {
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(IMAGE_TOOL_BLACK_MODE_STORAGE_KEY, blackModeEnabled ? 'true' : 'false')
    }
    applyWorkspaceTheme(blackModeEnabled ? 'black' : userThemeId)
  }, [blackModeEnabled, userThemeId])

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
      setNewSessionTitle('')
      await queryClient.invalidateQueries({ queryKey: ['image-tool-threads', selectedWorkspacePath] })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setCreatingSession(false)
    }
  }, [newSessionTitle, queryClient, selectedWorkspaceName, selectedWorkspacePath])

  const handlePreviousPreview = useCallback(() => {
    if (orderedImageAssets.length === 0) return
    const currentIndex = selectedImageAssetIndex >= 0 ? selectedImageAssetIndex : 0
    const nextIndex = (currentIndex - 1 + orderedImageAssets.length) % orderedImageAssets.length
    setSelectedImageAssetId(orderedImageAssets[nextIndex].id)
  }, [orderedImageAssets, selectedImageAssetIndex])

  const handleNextPreview = useCallback(() => {
    if (orderedImageAssets.length === 0) return
    const currentIndex = selectedImageAssetIndex >= 0 ? selectedImageAssetIndex : 0
    const nextIndex = (currentIndex + 1) % orderedImageAssets.length
    setSelectedImageAssetId(orderedImageAssets[nextIndex].id)
  }, [orderedImageAssets, selectedImageAssetIndex])

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
    setGeneratingImage(true)
    setGenerationStage('queued')
    setLivePreviews([])
    setSelectedLivePreviewId(null)
    setGenerationError(null)
    try {
      const response = await apiFetch('/v1/image/generations?stream=true', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
        body: JSON.stringify({
          provider: selectedModelOption.provider,
          model: selectedModelOption.model,
          prompt: promptText.trim(),
          count: 1,
          partial_images: 3,
          size: isGoogleImagenModel ? selectedGoogleImageSize : selectedOpenAIImageSize,
          settings: isGoogleImagenModel
            ? { aspect_ratio: selectedGoogleAspectRatio, size: selectedGoogleImageSize }
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
        const payload = JSON.parse(dataText) as { ok?: boolean; error?: string; event?: LiveGenerationEvent; result?: ImageGenerationResponse['result'] }
        if (eventName === 'error' || payload.ok === false) {
          throw new Error(payload.error || 'Image generation failed')
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
            setSelectedLivePreviewId(preview.id)
          }
        }
        if (eventName === 'completed' && payload.result) {
          finalResponse = { result: payload.result }
          const finalPartials = (payload.result.partials ?? [])
            .map(livePreviewFromPartial)
            .filter((preview): preview is LiveGenerationPreview => Boolean(preview))
          if (finalPartials.length > 0) {
            setLivePreviews(finalPartials)
            setSelectedLivePreviewId(finalPartials[finalPartials.length - 1].id)
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
      const generatedAssetId = completedResponse.result?.assets?.[0]?.id ?? updatedThread.imageAssetOrder[updatedThread.imageAssetOrder.length - 1]
      if (generatedAssetId) {
        setSelectedImageAssetId(generatedAssetId)
      }
      setSelectedThreadId(updatedThread.id)
      await queryClient.invalidateQueries({ queryKey: ['image-tool-threads', selectedWorkspacePath] })
    } catch (error) {
      setGenerationStage('error')
      setGenerationError(error instanceof Error ? error.message : String(error))
    } finally {
      setGeneratingImage(false)
    }
  }, [isGoogleImagenModel, promptText, queryClient, selectedGoogleAspectRatio, selectedGoogleImageSize, selectedOpenAIImageSize, selectedModelOption.model, selectedModelOption.provider, selectedProviderReady, selectedProviderUnavailableReason, selectedThread, selectedWorkspacePath])

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
            onSelectSession={setSelectedThreadId}
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
                  <div className="mt-4 grid grid-cols-2 gap-2 text-[11px]">
                    <div className="border border-[var(--app-border)] bg-[var(--app-surface)] p-2"><div className="text-[10px] uppercase text-[var(--app-text-subtle)]">Folders</div><div className="mt-1 text-[var(--app-text)]">{selectedThread.imageFolders.length}</div></div>
                    <div className="border border-[var(--app-border)] bg-[var(--app-surface)] p-2"><div className="text-[10px] uppercase text-[var(--app-text-subtle)]">Assets</div><div className="mt-1 text-[var(--app-text)]">{selectedThread.imageAssets.length}</div></div>
                  </div>
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
              <div className="flex min-h-full flex-col gap-4">
                <div className="flex min-h-[460px] flex-1 flex-col border border-[var(--app-border)] bg-[var(--app-surface)]">
                  <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)] px-4 py-3">
                    <div className="min-w-0">
                      <p className="text-[10px] font-medium uppercase tracking-[0.22em] text-[var(--app-text-subtle)]">Generation area</p>
                      <h2 className="mt-1 truncate text-xl font-semibold tracking-[-0.045em] text-[var(--app-text)]">{selectedThread.title || 'Image thread'}</h2>
                    </div>
                    <div className="flex items-center gap-2 text-xs text-[var(--app-text-muted)]">
                      <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-bg)] px-2.5 py-1">{selectedModelLabel}</span>
                      <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-bg)] px-2.5 py-1">{selectedShapeLabel}</span>
                      <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-bg)] px-2.5 py-1">{selectedSizeLabel}</span>
                    </div>
                  </div>

                  <div className="grid min-h-0 flex-1 grid-rows-[minmax(0,1fr)_auto]">
                    <div className="relative grid min-h-[340px] place-items-center overflow-hidden bg-[radial-gradient(circle_at_top,var(--app-surface-hover),transparent_34%),var(--app-bg)] px-5 py-6">
                      <div className="absolute left-4 top-4 rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1 text-xs text-[var(--app-text-muted)]">
                        Image {activePreviewNumber} of {Math.max(orderedImageAssets.length, 1)}
                      </div>
                      <div className="absolute right-4 top-4 rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1 text-xs text-[var(--app-text-muted)]">
                        {generatingImage ? generationStage === 'partial' ? 'Streaming partial image' : 'Generating…' : 'Carousel preview'}
                      </div>
                      <Button variant="outline" className="absolute left-4 top-1/2 h-10 w-10 -translate-y-1/2 rounded-full px-0" onClick={handlePreviousPreview} disabled={orderedImageAssets.length <= 1} aria-label="Previous image">
                        <ChevronLeft size={18} />
                      </Button>
                      <div className={`grid ${previewAspectClass} max-h-[68vh] w-full max-w-[760px] place-items-center overflow-hidden border border-[var(--app-border)] bg-[linear-gradient(135deg,var(--app-surface)_0%,var(--app-bg)_52%,var(--app-surface-hover)_100%)] shadow-2xl shadow-black/10`}>
                        {activeLivePreview ? (
                          <div className="relative flex h-full w-full flex-col items-center justify-center gap-3 p-4 text-center">
                            <img src={activeLivePreview.dataUrl} alt={activeLivePreview.label} className="max-h-full max-w-full object-contain" />
                            <div className="absolute bottom-3 left-3 rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1 text-xs text-[var(--app-text-muted)]">
                              {activeLivePreview.label} · generating
                            </div>
                          </div>
                        ) : selectedImageAsset ? (
                          <div className="flex h-full w-full flex-col items-center justify-center gap-3 p-4 text-center">
                            <img src={selectedImageAsset.url ?? imageAssetURL(selectedThread.id, selectedImageAsset.id)} alt={selectedImageAsset.name} className="max-h-full max-w-full object-contain" />
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
                              <p className="mt-3 text-sm leading-6 text-[var(--app-text-muted)]">Waiting for the first streamed partial image from OpenAI.</p>
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

                    <div className="border-t border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3">
                      <div className="mb-2 flex items-center gap-2 text-[10px] font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">
                        <Clock3 size={13} />Progression / iterations
                      </div>
                      <div className="grid gap-2 md:grid-cols-3">
                        {timelineItems.map((iteration, index) => (
                          <div key={iteration.id} className="border border-[var(--app-border)] bg-[var(--app-bg)] p-3">
                            <div className="flex items-center justify-between gap-2">
                              <span className="text-[10px] text-[var(--app-text-subtle)]">{String(index + 1).padStart(2, '0')}</span>
                              <span className="rounded-full border border-[var(--app-border)] px-2 py-0.5 text-[10px] text-[var(--app-text-muted)]">{iteration.status}</span>
                            </div>
                            <p className="mt-2 text-sm font-medium text-[var(--app-text)]">{iteration.label}</p>
                            <p className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">{iteration.detail}</p>
                          </div>
                        ))}
                      </div>
                    </div>
                  </div>
                </div>

                <div className="border border-[var(--app-border)] bg-[var(--app-surface)] p-4">
                  <div className="grid gap-4">
                    <div className="grid gap-4">
                      <label className="block text-xs font-medium text-[var(--app-text-muted)]">
                        Prompt
                        <Textarea
                          className="mt-1.5 min-h-[132px] resize-none text-sm"
                          value={promptText}
                          onChange={(event) => setPromptText(event.target.value)}
                          placeholder="Describe the image you want to generate…"
                        />
                      </label>

                      <div className="grid min-w-0 gap-3">
                        <label className="block min-w-0 text-xs font-medium text-[var(--app-text-muted)]">
                          Image Model
                          <Select className="mt-1.5 min-w-0 truncate" value={selectedImageModel} onChange={(event) => setSelectedImageModel(event.target.value)}>
                            {IMAGE_MODEL_OPTIONS.map((option) => {
                              const provider = (imageProvidersQuery.data ?? []).find((entry) => entry.id === option.provider)
                              return <option key={option.id} value={option.id}>{option.label}{provider?.ready === false ? ' (unavailable)' : ''}</option>
                            })}
                          </Select>
                        </label>

                        <div className="min-w-0">
                          <p className="text-[11px] text-[var(--app-text-subtle)]">{selectedProviderControlLabel}</p>
                          {isGoogleImagenModel ? (
                            <div className="mt-1.5 grid gap-3">
                              <div>
                                <p className="text-xs font-medium text-[var(--app-text-muted)]">Aspect ratio</p>
                                <div className="mt-1.5 grid grid-cols-3 gap-2 sm:grid-cols-5">
                                  {GOOGLE_IMAGE_ASPECT_RATIO_OPTIONS.map((option) => (
                                    <button key={option.id} type="button" onClick={() => setSelectedGoogleAspectRatio(option.id)} className={['min-w-0 border px-2 py-2 text-center transition hover:bg-[var(--app-surface-hover)]', selectedGoogleAspectRatio === option.id ? 'border-[var(--app-border-accent)] bg-[var(--app-surface-active)] text-[var(--app-text)]' : 'border-[var(--app-border)] bg-[var(--app-bg)] text-[var(--app-text-muted)]'].join(' ')}>
                                      <span className="block text-xs font-semibold">{option.id}</span>
                                      <span className="mt-0.5 block truncate text-[10px]">{option.label}</span>
                                    </button>
                                  ))}
                                </div>
                              </div>
                              <div>
                                <p className="text-xs font-medium text-[var(--app-text-muted)]">Image size</p>
                                <div className="mt-1.5 grid grid-cols-2 gap-2">
                                  {GOOGLE_IMAGE_SIZE_OPTIONS.map((option) => (
                                    <button key={option.id} type="button" onClick={() => setSelectedGoogleImageSize(option.id)} className={['border px-3 py-2 text-center transition hover:bg-[var(--app-surface-hover)]', selectedGoogleImageSize === option.id ? 'border-[var(--app-border-accent)] bg-[var(--app-surface-active)] text-[var(--app-text)]' : 'border-[var(--app-border)] bg-[var(--app-bg)] text-[var(--app-text-muted)]'].join(' ')}>
                                      <span className="block text-xs font-semibold">{option.label}</span>
                                      <span className="mt-0.5 block truncate text-[10px]">{option.helper}</span>
                                    </button>
                                  ))}
                                </div>
                              </div>
                            </div>
                          ) : (
                            <div className="mt-1.5">
                              <p className="text-xs font-medium text-[var(--app-text-muted)]">Output size</p>
                              <div className="mt-1.5 grid grid-cols-2 gap-2 sm:grid-cols-4 xl:grid-cols-2 2xl:grid-cols-4">
                                {OPENAI_IMAGE_SIZE_OPTIONS.map((option) => (
                                  <button key={option.id} type="button" onClick={() => setSelectedOpenAIImageSize(option.id)} className={['min-w-0 border px-3 py-2 text-center transition hover:bg-[var(--app-surface-hover)]', selectedOpenAIImageSize === option.id ? 'border-[var(--app-border-accent)] bg-[var(--app-surface-active)] text-[var(--app-text)]' : 'border-[var(--app-border)] bg-[var(--app-bg)] text-[var(--app-text-muted)]'].join(' ')}>
                                    <span className="block text-xs font-semibold">{option.label}</span>
                                    <span className="mt-0.5 block truncate text-[10px]">{option.helper}</span>
                                  </button>
                                ))}
                              </div>
                            </div>
                          )}
                        </div>
                      </div>
                    </div>

                    <div className="grid gap-2 border-t border-dashed border-[var(--app-border)] pt-4 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
                      <div className="rounded-xl border border-dashed border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 text-[11px] leading-5 text-[var(--app-text-subtle)]">
                        {selectedProviderReady ? selectedSizeDisplayLabel + '. Manual single-image path. Advanced controls later.' : selectedProviderUnavailableReason}
                      </div>
                      <Button className="h-11 w-full rounded-xl px-4 sm:w-auto" disabled={!canGenerateImage} onClick={() => void handleGenerateImage()}>
                        <Sparkles size={15} />{generatingImage ? 'Generating…' : 'Generate 1 image'}
                      </Button>
                    </div>
                  </div>
                </div>
              </div>
            )}
          </section>
        </main>
      </div>
    </div>
  )
}
