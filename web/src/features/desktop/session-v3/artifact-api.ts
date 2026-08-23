import { apiFetch, readErrorMessage } from '../../../app/api'
import type { DesktopV3ArtifactLocalRuntimeAssets } from './artifact-animation-runtime-assets'

export type DesktopV3ArtifactCategory = 'plan' | 'visual' | 'document'
export type DesktopV3ArtifactStatus = '' | 'staging' | 'ready' | 'failed' | 'unavailable'
export type DesktopV3ArtifactOutputOrientation = 'landscape' | 'portrait' | 'square'

/** Trusted, server-resolved output intent. This describes the requested target, not measured binary metadata. */
export interface DesktopV3ArtifactOutputRequirements {
  presetId: string
  width: number
  height: number
  aspectRatio: string
  orientation: DesktopV3ArtifactOutputOrientation
  resolutionSource: string
  registryVersion: string
}

export type DesktopV3ArtifactAnimationProfileID = 'motion_ui' | 'spatial_3d' | 'vector_playback' | 'final_render'

export interface DesktopV3ArtifactAnimationBudgets {
  maxSimultaneousLivePreviews: number
  maxWebGLContexts: number
  maxDevicePixelRatio: number
  maxCanvasPixels: number
  maxParticles: number
  maxDrawCallsPerFrame: number
  pauseWhenOffscreen: true
  stopWhenDocumentHidden: true
  reducedMotionBehavior: 'static_first_frame'
  networkAllowed: false
}

/** Immutable execution contract resolved by the server from the closed profile registry. */
export interface DesktopV3ArtifactAnimationProfile {
  profileId: DesktopV3ArtifactAnimationProfileID
  registryVersion: string
  runtimeKind: string
  runtimePackage: string
  runtimeVersion: string
  secondaryRuntimePackage: string
  secondaryRuntimeVersion: string
  heavy: boolean
  importedPlaybackOnly: boolean
  editableSourceRequired: boolean
  budgets: DesktopV3ArtifactAnimationBudgets
}

export interface DesktopV3ArtifactCollectionProgress {
  total: number
  staging: number
  ready: number
  failed: number
  unavailable: number
}

export interface DesktopV3ArtifactPart {
  id: string
  label: string
  kind: 'temporal' | 'spatial' | 'page' | 'state' | 'selector' | 'semantic'
  description: string
  startMs: number
  endMs: number
  x: number
  y: number
  width: number
  height: number
  page: number
  stateId: string
  selector: string
}

export interface DesktopV3ArtifactChainReference {
  sessionId: string
  collectionId: string
  variantId: string
  eventSeq: number
}

export interface DesktopV3ArtifactChain {
  id: string
  name: string
  root: DesktopV3ArtifactChainReference
  head: DesktopV3ArtifactChainReference
  revisionCount: number
  lastRoundId: string
}

export interface DesktopV3ArtifactLineage {
  parentSessionId: string
  sourceSessionId: string
  sourceCollectionId: string
  sourceVariantId: string
  sourceEventSeq?: number
  taskCallId: string
  programId: string
  programJobId: string
  childSessionId: string
  iterationGroupId: string
  iterationGroup: string
  iterationId: string
  iterationIndex: number
  iterationLabel: string
  iterationTheme: string
  iterationSectionId: string
  iterationSectionLabel: string
  iterationSectionStartMs: number
  iterationSectionEndMs: number
  runId: string
  planId: string
  checkpointId: string
  attemptId: string
  partId?: string
  partLabel?: string
  partKind?: string
}

/** Opaque, portable managed-artifact reference. It never contains bytes or storage paths. */
export interface DesktopV3ArtifactSelection {
  session_id: string
  collection_id: string
  variant_id: string
  event_seq: number
  part_id?: string
}

/** Opaque message reference plus bounded display metadata and review intent. */
export interface DesktopV3ArtifactMessageSelection extends DesktopV3ArtifactSelection {
  label: string
  description?: string
  /** Hidden provider instruction queued by Artifact Studio; never rendered as composer or chat text. */
  pending_request?: string
  action: 'select' | 'use'
}

export const DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT = 16

export interface DesktopV3ArtifactCatalogEntry {
  artifactId: string
  sourceRef?: string
  collectionId?: string
  sessionId: string
  sessionTitle: string
  workspacePath: string
  workspaceName: string
  planId: string
  planTitle: string
  checkpointId: string
  checkpointTitle: string
  label: string
  description: string
  collectionName: string
  collectionDescription: string
  filename: string
  mediaType: string
  kind: string
  status?: DesktopV3ArtifactStatus
  failureCode?: string
  previewable: boolean
  selected?: boolean
  category: DesktopV3ArtifactCategory
  updatedAt: number
  eventSeq?: number
  progress?: DesktopV3ArtifactCollectionProgress | null
  lineage?: DesktopV3ArtifactLineage | null
  outputRequirements?: DesktopV3ArtifactOutputRequirements
  animationProfile?: DesktopV3ArtifactAnimationProfile
  chain?: DesktopV3ArtifactChain
  artifactChainId?: string
  revisionNumber?: number
  revisionRoundId?: string
  candidateIndex?: number
  parts?: DesktopV3ArtifactPart[]
  localRevealAvailable?: boolean
  content?: string
}

type DesktopV3ArtifactCatalogResponse = {
  ok?: unknown
  artifacts?: unknown
  local_reveal_available?: unknown
}

export interface DesktopV3ArtifactCatalogResult {
  artifacts: DesktopV3ArtifactCatalogEntry[]
  localRevealAvailable: boolean
}

export const DESKTOP_V3_HTML_STILL_EXPORT_PROMPT = 'Export this HTML concept\'s declared capture states as video-ready stills, then use the returned exact PNG references in a pending video plan. Do not accept the proposal or start final rendering.'

/** The server remains authoritative for manifest validation; this gates only the explicit gallery bridge. */
export function desktopV3ArtifactCanExportHTMLStills(
  artifact: Pick<DesktopV3ArtifactCatalogEntry, 'collectionId' | 'eventSeq' | 'kind' | 'mediaType' | 'previewable' | 'status'>,
): boolean {
  const mediaType = artifact.mediaType.split(';', 1)[0]?.trim().toLowerCase()
  const kind = artifact.kind.trim().toLowerCase()
  const compatibleSource = mediaType === 'text/html' || (mediaType === 'application/zip' && (artifact.previewable || kind === 'html' || kind === 'package'))
  return compatibleSource && artifact.status === 'ready' && Boolean(artifact.collectionId) && (artifact.eventSeq ?? 0) > 0
}

function artifactCatalogRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null
}

function artifactCatalogString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function artifactCatalogCount(value: unknown): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : 0
}

function artifactCatalogEventSeq(value: unknown): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? value : 0
}

function normalizeArtifactProgress(value: unknown): DesktopV3ArtifactCollectionProgress | null {
  const record = artifactCatalogRecord(value)
  if (!record) return null
  return {
    total: artifactCatalogCount(record.total),
    staging: artifactCatalogCount(record.staging),
    ready: artifactCatalogCount(record.ready),
    failed: artifactCatalogCount(record.failed),
    unavailable: artifactCatalogCount(record.unavailable),
  }
}

function artifactCatalogPositiveInteger(value: unknown): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? value : 0
}

function artifactCatalogNonNegativeInteger(value: unknown): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : -1
}

function artifactCatalogPositiveNumber(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : 0
}

function artifactCatalogGreatestCommonDivisor(left: number, right: number): number {
  let a = left
  let b = right
  while (b !== 0) {
    [a, b] = [b, a % b]
  }
  return a
}

export function normalizeDesktopV3ArtifactOutputRequirements(value: unknown): DesktopV3ArtifactOutputRequirements | null {
  const record = artifactCatalogRecord(value)
  if (!record) return null
  const presetId = artifactCatalogString(record.preset_id)
  const width = artifactCatalogPositiveInteger(record.width)
  const height = artifactCatalogPositiveInteger(record.height)
  const aspectRatio = artifactCatalogString(record.aspect_ratio)
  const orientation = artifactCatalogString(record.orientation)
  const resolutionSource = artifactCatalogString(record.resolution_source)
  const registryVersion = artifactCatalogString(record.registry_version)
  if (!width || !height || !resolutionSource || !registryVersion) return null
  if (orientation !== 'landscape' && orientation !== 'portrait' && orientation !== 'square') return null
  const expectedOrientation: DesktopV3ArtifactOutputOrientation = width === height ? 'square' : width > height ? 'landscape' : 'portrait'
  if (orientation !== expectedOrientation) return null
  const divisor = artifactCatalogGreatestCommonDivisor(width, height)
  const normalizedRatio = `${width / divisor}:${height / divisor}`
  if (aspectRatio !== normalizedRatio) return null
  return { presetId, width, height, aspectRatio, orientation, resolutionSource, registryVersion }
}

const desktopV3ArtifactAnimationRuntimeContract: Record<DesktopV3ArtifactAnimationProfileID, {
  runtimeKind: string
  runtimePackage: string
  runtimeVersion: string
  secondaryRuntimePackage: string
  secondaryRuntimeVersion: string
  heavy: boolean
  importedPlaybackOnly: boolean
  editableSourceRequired: boolean
}> = {
  motion_ui: { runtimeKind: 'native_css_waapi_svg', runtimePackage: '', runtimeVersion: '', secondaryRuntimePackage: '', secondaryRuntimeVersion: '', heavy: false, importedPlaybackOnly: false, editableSourceRequired: false },
  spatial_3d: { runtimeKind: 'three_webgl', runtimePackage: 'three', runtimeVersion: '0.185.1', secondaryRuntimePackage: '', secondaryRuntimeVersion: '', heavy: true, importedPlaybackOnly: false, editableSourceRequired: false },
  vector_playback: { runtimeKind: 'imported_vector_playback', runtimePackage: '@lottiefiles/dotlottie-web', runtimeVersion: '0.79.0', secondaryRuntimePackage: '@rive-app/canvas', secondaryRuntimeVersion: '2.39.2', heavy: false, importedPlaybackOnly: true, editableSourceRequired: false },
  final_render: { runtimeKind: 'mp4_playback', runtimePackage: '', runtimeVersion: '', secondaryRuntimePackage: '', secondaryRuntimeVersion: '', heavy: false, importedPlaybackOnly: false, editableSourceRequired: true },
}

export function normalizeDesktopV3ArtifactAnimationProfile(value: unknown): DesktopV3ArtifactAnimationProfile | null {
  const record = artifactCatalogRecord(value)
  const budgetsRecord = artifactCatalogRecord(record?.budgets)
  if (!record || !budgetsRecord) return null
  const profileId = artifactCatalogString(record.profile_id) as DesktopV3ArtifactAnimationProfileID
  const contract = desktopV3ArtifactAnimationRuntimeContract[profileId]
  const registryVersion = artifactCatalogString(record.registry_version)
  if (!contract || !registryVersion) return null
  const runtimePackage = artifactCatalogString(record.runtime_package)
  const runtimeVersion = artifactCatalogString(record.runtime_version)
  const secondaryRuntimePackage = artifactCatalogString(record.secondary_runtime_package)
  const secondaryRuntimeVersion = artifactCatalogString(record.secondary_runtime_version)
  if (artifactCatalogString(record.runtime_kind) !== contract.runtimeKind
    || runtimePackage !== contract.runtimePackage
    || runtimeVersion !== contract.runtimeVersion
    || secondaryRuntimePackage !== contract.secondaryRuntimePackage
    || secondaryRuntimeVersion !== contract.secondaryRuntimeVersion
    || (record.heavy === true) !== contract.heavy
    || (record.imported_playback_only === true) !== contract.importedPlaybackOnly
    || (record.editable_source_required === true) !== contract.editableSourceRequired) return null
  const maxSimultaneousLivePreviews = artifactCatalogPositiveInteger(budgetsRecord.max_simultaneous_live_previews)
  const maxWebGLContexts = artifactCatalogNonNegativeInteger(budgetsRecord.max_webgl_contexts)
  const maxDevicePixelRatio = artifactCatalogPositiveNumber(budgetsRecord.max_device_pixel_ratio)
  const maxCanvasPixels = artifactCatalogPositiveInteger(budgetsRecord.max_canvas_pixels)
  const maxParticles = artifactCatalogNonNegativeInteger(budgetsRecord.max_particles)
  const maxDrawCallsPerFrame = artifactCatalogNonNegativeInteger(budgetsRecord.max_draw_calls_per_frame)
  if (!maxSimultaneousLivePreviews || maxWebGLContexts < 0 || !maxDevicePixelRatio || !maxCanvasPixels || maxParticles < 0 || maxDrawCallsPerFrame < 0
    || budgetsRecord.pause_when_offscreen !== true || budgetsRecord.stop_when_document_hidden !== true
    || budgetsRecord.reduced_motion_behavior !== 'static_first_frame' || budgetsRecord.network_allowed !== false) return null
  return {
    profileId, registryVersion, ...contract,
    budgets: { maxSimultaneousLivePreviews, maxWebGLContexts, maxDevicePixelRatio, maxCanvasPixels, maxParticles, maxDrawCallsPerFrame, pauseWhenOffscreen: true, stopWhenDocumentHidden: true, reducedMotionBehavior: 'static_first_frame', networkAllowed: false },
  }
}

export function formatDesktopV3ArtifactAnimationProfile(profile?: DesktopV3ArtifactAnimationProfile | null): string {
  if (!profile) return ''
  return ({ motion_ui: 'CSS / WAAPI', spatial_3d: 'Three.js 3D', vector_playback: 'Vector playback', final_render: 'MP4 playback' } satisfies Record<DesktopV3ArtifactAnimationProfileID, string>)[profile.profileId]
}

function artifactOutputPresetLabel(presetId: string): string {
  const normalized = presetId.trim().toLowerCase()
  if (normalized === 'x_video' || normalized === 'twitter_video' || normalized === 'x_video_landscape' || normalized === 'landscape_video' || normalized === 'full_hd_landscape') return 'Landscape video'
  if (normalized === 'x_video_portrait' || normalized === 'portrait_video' || normalized === 'vertical_video') return 'Portrait video'
  const words = normalized.split(/[_-]+/).filter(Boolean)
  if (words.length === 0) return ''
  return words.map((word, index) => {
    if (word === 'twitter' || word === 'x') return 'X'
    return index === 0 ? `${word.charAt(0).toUpperCase()}${word.slice(1)}` : word
  }).join(' ')
}

export function formatDesktopV3ArtifactOutputRequirements(requirements?: DesktopV3ArtifactOutputRequirements | null): string {
  if (!requirements) return ''
  const preset = artifactOutputPresetLabel(requirements.presetId)
  return [preset, `${requirements.width} × ${requirements.height}`, requirements.aspectRatio].filter(Boolean).join(' · ')
}

function normalizeArtifactChainReference(value: unknown): DesktopV3ArtifactChainReference | null {
  const record = artifactCatalogRecord(value)
  if (!record) return null
  const sessionId = artifactCatalogString(record.session_id)
  const collectionId = artifactCatalogString(record.collection_id)
  const variantId = artifactCatalogString(record.variant_id)
  const eventSeq = artifactCatalogEventSeq(record.event_seq)
  return sessionId && collectionId && variantId && eventSeq ? { sessionId, collectionId, variantId, eventSeq } : null
}

function normalizeArtifactChain(value: unknown): DesktopV3ArtifactChain | null {
  const record = artifactCatalogRecord(value)
  if (!record) return null
  const id = artifactCatalogString(record.id)
  const root = normalizeArtifactChainReference(record.root)
  const head = normalizeArtifactChainReference(record.head)
  if (!id || !root || !head) return null
  return { id, name: artifactCatalogString(record.name), root, head, revisionCount: artifactCatalogCount(record.revision_count), lastRoundId: artifactCatalogString(record.last_round_id) }
}

function normalizeArtifactParts(value: unknown): DesktopV3ArtifactPart[] {
  if (!Array.isArray(value)) return []
  return value.flatMap((raw): DesktopV3ArtifactPart[] => {
    const record = artifactCatalogRecord(raw)
    if (!record) return []
    const id = artifactCatalogString(record.id)
    const label = artifactCatalogString(record.label)
    const kind = artifactCatalogString(record.kind) as DesktopV3ArtifactPart['kind']
    if (!id || !label || !['temporal', 'spatial', 'page', 'state', 'selector', 'semantic'].includes(kind)) return []
    const number = (input: unknown) => typeof input === 'number' && Number.isFinite(input) ? input : 0
    return [{ id, label, kind, description: artifactCatalogString(record.description), startMs: number(record.start_ms), endMs: number(record.end_ms), x: number(record.x), y: number(record.y), width: number(record.width), height: number(record.height), page: artifactCatalogCount(record.page), stateId: artifactCatalogString(record.state_id), selector: artifactCatalogString(record.selector) }]
  })
}

function normalizeArtifactLineage(value: unknown): DesktopV3ArtifactLineage | null {
  const record = artifactCatalogRecord(value)
  if (!record) return null
  return {
    parentSessionId: artifactCatalogString(record.parent_session_id),
    sourceSessionId: artifactCatalogString(record.source_session_id),
    sourceCollectionId: artifactCatalogString(record.source_collection_id),
    sourceVariantId: artifactCatalogString(record.source_variant_id),
    sourceEventSeq: artifactCatalogEventSeq(record.source_event_seq),
    taskCallId: artifactCatalogString(record.task_call_id),
    programId: artifactCatalogString(record.program_id),
    programJobId: artifactCatalogString(record.program_job_id),
    childSessionId: artifactCatalogString(record.child_session_id),
    iterationGroupId: artifactCatalogString(record.iteration_group_id),
    iterationGroup: artifactCatalogString(record.iteration_group),
    iterationId: artifactCatalogString(record.iteration_id),
    iterationIndex: artifactCatalogCount(record.iteration_index),
    iterationLabel: artifactCatalogString(record.iteration_label),
    iterationTheme: artifactCatalogString(record.iteration_theme),
    iterationSectionId: artifactCatalogString(record.iteration_section_id),
    iterationSectionLabel: artifactCatalogString(record.iteration_section_label),
    iterationSectionStartMs: artifactCatalogNonNegativeInteger(record.iteration_section_start_ms),
    iterationSectionEndMs: artifactCatalogNonNegativeInteger(record.iteration_section_end_ms),
    runId: artifactCatalogString(record.run_id),
    planId: artifactCatalogString(record.plan_id),
    checkpointId: artifactCatalogString(record.checkpoint_id),
    attemptId: artifactCatalogString(record.attempt_id),
    ...(artifactCatalogString(record.part_id) ? { partId: artifactCatalogString(record.part_id), partLabel: artifactCatalogString(record.part_label), partKind: artifactCatalogString(record.part_kind) } : {}),
  }
}

export function normalizeDesktopV3ArtifactCatalogEntry(value: unknown): DesktopV3ArtifactCatalogEntry | null {
  const record = artifactCatalogRecord(value)
  if (!record) return null
  const artifactId = artifactCatalogString(record.artifact_id) || artifactCatalogString(record.id) || artifactCatalogString(record.variant_id)
  const sessionId = artifactCatalogString(record.session_id)
  if (!artifactId || !sessionId) return null
  const rawCategory = artifactCatalogString(record.category)
  const category: DesktopV3ArtifactCategory = rawCategory === 'plan' || rawCategory === 'visual' ? rawCategory : 'document'
  const rawStatus = artifactCatalogString(record.status)
  const status: DesktopV3ArtifactStatus = rawStatus === 'staging' || rawStatus === 'ready' || rawStatus === 'failed' || rawStatus === 'unavailable'
    ? rawStatus
    : ''
  const rawUpdatedAt = record.updated_at
  const updatedAt = typeof rawUpdatedAt === 'number' && Number.isFinite(rawUpdatedAt)
    ? rawUpdatedAt
    : typeof rawUpdatedAt === 'string' && rawUpdatedAt.trim()
      ? Date.parse(rawUpdatedAt)
      : 0
  const outputRequirements = normalizeDesktopV3ArtifactOutputRequirements(record.output_requirements)
  const animationProfile = normalizeDesktopV3ArtifactAnimationProfile(record.animation_profile)
  const chain = normalizeArtifactChain(record.chain)
  return {
    artifactId,
    sourceRef: artifactCatalogString(record.source_ref),
    collectionId: artifactCatalogString(record.collection_id),
    sessionId,
    sessionTitle: artifactCatalogString(record.session_title),
    workspacePath: artifactCatalogString(record.workspace_path),
    workspaceName: artifactCatalogString(record.workspace_name),
    planId: artifactCatalogString(record.plan_id),
    planTitle: artifactCatalogString(record.plan_title),
    checkpointId: artifactCatalogString(record.checkpoint_id),
    checkpointTitle: artifactCatalogString(record.checkpoint_title),
    label: artifactCatalogString(record.label) || artifactCatalogString(record.filename) || 'Artifact',
    description: artifactCatalogString(record.description),
    collectionName: artifactCatalogString(record.collection_name),
    collectionDescription: artifactCatalogString(record.collection_description),
    filename: artifactCatalogString(record.filename),
    mediaType: artifactCatalogString(record.media_type) || 'application/octet-stream',
    kind: artifactCatalogString(record.kind),
    status,
    failureCode: artifactCatalogString(record.failure_code),
    previewable: record.previewable === true,
    selected: record.selected === true,
    category,
    updatedAt: Number.isFinite(updatedAt) ? updatedAt : 0,
    eventSeq: artifactCatalogEventSeq(record.event_seq),
    progress: normalizeArtifactProgress(record.progress),
    lineage: normalizeArtifactLineage(record.lineage),
    ...(chain ? { chain } : {}),
    ...((artifactCatalogString(record.artifact_chain_id) || chain?.id) ? { artifactChainId: artifactCatalogString(record.artifact_chain_id) || chain?.id } : {}),
    ...(artifactCatalogCount(record.revision_number) ? { revisionNumber: artifactCatalogCount(record.revision_number) } : {}),
    ...(artifactCatalogString(record.revision_round_id) ? { revisionRoundId: artifactCatalogString(record.revision_round_id) } : {}),
    ...(artifactCatalogCount(record.candidate_index) ? { candidateIndex: artifactCatalogCount(record.candidate_index) } : {}),
    ...(Array.isArray(record.parts) ? { parts: normalizeArtifactParts(record.parts) } : {}),
    ...(outputRequirements ? { outputRequirements } : {}),
    ...(animationProfile ? { animationProfile } : {}),
    ...(typeof record.content === 'string' ? { content: record.content } : {}),
  }
}

export async function fetchDesktopV3ArtifactCatalogResult(signal?: AbortSignal, sessionId = ''): Promise<DesktopV3ArtifactCatalogResult> {
  const search = new URLSearchParams({ limit: '2000' })
  if (sessionId.trim()) search.set('session_id', sessionId.trim())
  const response = await apiFetch(`/v3/artifacts?${search.toString()}`, { method: 'GET', signal })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  const payload = await response.json() as DesktopV3ArtifactCatalogResponse
  if (payload.ok !== true || !Array.isArray(payload.artifacts)) throw new Error('Artifact catalog returned an invalid response')
  const localRevealAvailable = payload.local_reveal_available === true
  return {
    artifacts: payload.artifacts
      .map(normalizeDesktopV3ArtifactCatalogEntry)
      .filter((entry): entry is DesktopV3ArtifactCatalogEntry => entry !== null)
      .map((entry) => ({ ...entry, localRevealAvailable })),
    localRevealAvailable,
  }
}

export async function fetchDesktopV3ArtifactCatalog(signal?: AbortSignal, sessionId = ''): Promise<DesktopV3ArtifactCatalogEntry[]> {
  return (await fetchDesktopV3ArtifactCatalogResult(signal, sessionId)).artifacts
}

export function desktopV3ArtifactSelection(entry: DesktopV3ArtifactCatalogEntry): DesktopV3ArtifactSelection {
  const selection = normalizeDesktopV3ArtifactSelection({
    session_id: entry.sessionId,
    collection_id: entry.collectionId ?? '',
    variant_id: entry.artifactId,
    event_seq: entry.eventSeq ?? 0,
  })
  if (!selection || entry.status !== 'ready') {
    throw new Error('Artifact selection requires a ready managed variant with a durable event sequence')
  }
  return selection
}

export function desktopV3ArtifactVariantLabel(entry: DesktopV3ArtifactCatalogEntry): string {
  const iterationIndex = entry.lineage?.iterationIndex ?? 0
  const iterationLabel = entry.lineage?.iterationLabel?.trim() || entry.lineage?.iterationTheme?.trim() || ''
  const artifactLabel = entry.label.trim()
  const collectionLabel = entry.collectionName.trim()
  const distinctArtifactLabel = artifactLabel && artifactLabel !== collectionLabel ? artifactLabel : ''
  const label = iterationLabel || distinctArtifactLabel || entry.filename.trim()
  if (iterationIndex > 0) {
    const positionLabel = `Iteration ${iterationIndex}`
    return label && label.toLowerCase() !== positionLabel.toLowerCase() ? `${positionLabel}: ${label}` : positionLabel
  }
  return label || 'Artifact variant'
}

export function desktopV3ArtifactVariantDescription(entry: DesktopV3ArtifactCatalogEntry): string | undefined {
  const collectionLabel = entry.collectionName.trim() || entry.lineage?.iterationGroup?.trim() || ''
  const iterationIndex = entry.lineage?.iterationIndex ?? 0
  const total = entry.progress?.total ?? 0
  const iterationPosition = iterationIndex > 0
    ? `Iteration ${iterationIndex}${total > 0 ? ` of ${total}` : ''}`
    : ''
  const variantDescription = entry.description.trim()
  const distinctDescription = variantDescription
    && variantDescription !== entry.collectionDescription.trim()
    && variantDescription !== entry.label.trim()
    ? variantDescription
    : ''
  const description = [collectionLabel, iterationPosition, distinctDescription].filter(Boolean).join(' · ')
  return description || undefined
}

export function desktopV3ArtifactMessageSelection(
  entry: DesktopV3ArtifactCatalogEntry,
  action: 'select' | 'use' = 'select',
): DesktopV3ArtifactMessageSelection {
  return {
    ...desktopV3ArtifactSelection(entry),
    label: desktopV3ArtifactVariantLabel(entry),
    description: desktopV3ArtifactVariantDescription(entry),
    action,
  }
}

export function desktopV3ArtifactMessageSelectionKey(
  selection: Pick<DesktopV3ArtifactSelection, 'session_id' | 'collection_id' | 'variant_id'> & Partial<Pick<DesktopV3ArtifactSelection, 'part_id'>>,
): string {
  return `${selection.session_id.trim()}\u0000${selection.collection_id.trim()}\u0000${selection.variant_id.trim()}\u0000${selection.part_id?.trim() ?? ''}`
}

export function desktopV3ArtifactCatalogEntryKey(entry: Pick<DesktopV3ArtifactCatalogEntry, 'sessionId' | 'collectionId' | 'artifactId'>): string {
  return desktopV3ArtifactMessageSelectionKey({
    session_id: entry.sessionId,
    collection_id: entry.collectionId ?? '',
    variant_id: entry.artifactId,
  })
}

export function desktopV3ArtifactCatalogEntryForKey(
  artifacts: readonly DesktopV3ArtifactCatalogEntry[],
  key: string,
): DesktopV3ArtifactCatalogEntry | undefined {
  return artifacts.find((artifact) => desktopV3ArtifactCatalogEntryKey(artifact) === key)
}

export interface DesktopV3ArtifactViewerSearch {
  artifactSession: string
  collection?: string
  artifact?: string
}

export interface DesktopV3ArtifactViewerLocation {
  sessionId: string
  collectionId?: string
  artifactId?: string
}

export interface DesktopV3ArtifactCollectionViewerTarget {
  sessionId: string
  collectionId: string
}

function desktopV3ArtifactViewerPath(
  workspaceSlug: string,
  sessionId: string,
  viewerSearch: DesktopV3ArtifactViewerSearch,
): string {
  const normalizedWorkspaceSlug = workspaceSlug.trim()
  const normalizedSessionId = sessionId.trim()
  if (!normalizedWorkspaceSlug || !normalizedSessionId || viewerSearch.artifactSession !== normalizedSessionId) {
    throw new Error('Artifact viewer URL requires a workspace and matching session ID')
  }
  const search = new URLSearchParams({ artifactSession: viewerSearch.artifactSession })
  if (viewerSearch.collection) search.set('collection', viewerSearch.collection)
  if (viewerSearch.artifact) search.set('artifact', viewerSearch.artifact)
  return `/${encodeURIComponent(normalizedWorkspaceSlug)}/${encodeURIComponent(normalizedSessionId)}?${search.toString()}`
}

/** Canonical search identity for a collection landing page. */
export function desktopV3ArtifactCollectionViewerSearch(
  collection: DesktopV3ArtifactCollectionViewerTarget,
): DesktopV3ArtifactViewerSearch {
  const sessionId = collection.sessionId.trim()
  const collectionId = collection.collectionId.trim()
  if (!sessionId || !collectionId) throw new Error('Artifact collection URL requires a session and collection ID')
  return { artifactSession: sessionId, collection: collectionId }
}

/** Canonical search identity for one exact artifact or managed iteration. */
export function desktopV3ArtifactViewerSearch(
  artifact: Pick<DesktopV3ArtifactCatalogEntry, 'sessionId' | 'collectionId' | 'artifactId'>,
): DesktopV3ArtifactViewerSearch {
  const sessionId = artifact.sessionId.trim()
  const artifactId = artifact.artifactId.trim()
  const collectionId = artifact.collectionId?.trim() ?? ''
  if (!sessionId || !artifactId) throw new Error('Artifact viewer URL requires a session and artifact ID')
  return {
    artifactSession: sessionId,
    ...(collectionId ? { collection: collectionId } : {}),
    artifact: artifactId,
  }
}

export function desktopV3ArtifactCollectionViewerHref(
  workspaceSlug: string,
  collection: DesktopV3ArtifactCollectionViewerTarget,
): string {
  return desktopV3ArtifactViewerPath(
    workspaceSlug,
    collection.sessionId,
    desktopV3ArtifactCollectionViewerSearch(collection),
  )
}

export function desktopV3ArtifactViewerHref(
  workspaceSlug: string,
  artifact: Pick<DesktopV3ArtifactCatalogEntry, 'sessionId' | 'collectionId' | 'artifactId'>,
): string {
  return desktopV3ArtifactViewerPath(workspaceSlug, artifact.sessionId, desktopV3ArtifactViewerSearch(artifact))
}

/** Parses collection-base and iteration-specific viewer URLs without crossing session authority. */
export function desktopV3ArtifactViewerLocation(
  sessionId: string,
  search: { artifactSession?: unknown; artifact?: unknown; collection?: unknown },
): DesktopV3ArtifactViewerLocation | null {
  const normalizedSessionId = sessionId.trim()
  const artifactSessionId = typeof search.artifactSession === 'string' ? search.artifactSession.trim() : ''
  const artifactId = typeof search.artifact === 'string' ? search.artifact.trim() : ''
  const collectionId = typeof search.collection === 'string' ? search.collection.trim() : ''
  if (!normalizedSessionId || artifactSessionId !== normalizedSessionId || (!artifactId && !collectionId)) return null
  return {
    sessionId: normalizedSessionId,
    ...(collectionId ? { collectionId } : {}),
    ...(artifactId ? { artifactId } : {}),
  }
}

/** Resolves only an exact iteration. Collection-base URLs remain collection-level viewer state. */
export function desktopV3ArtifactCatalogEntryForViewerLocation(
  artifacts: readonly DesktopV3ArtifactCatalogEntry[],
  location: DesktopV3ArtifactViewerLocation,
): DesktopV3ArtifactCatalogEntry | undefined {
  if (!location.artifactId) return undefined
  const sessionArtifacts = artifacts.filter((artifact) => artifact.sessionId === location.sessionId
    || artifact.lineage?.parentSessionId === location.sessionId)
  return sessionArtifacts.find((artifact) => artifact.artifactId === location.artifactId
    && (!location.collectionId || artifact.collectionId === location.collectionId))
}

export function normalizeDesktopV3ArtifactMessageSelection(value: unknown): DesktopV3ArtifactMessageSelection | null {
  const selection = normalizeDesktopV3ArtifactSelection(value)
  const record = artifactCatalogRecord(value)
  if (!selection || !record) return null
  const label = artifactCatalogString(record.label)
  const rawAction = artifactCatalogString(record.action).toLowerCase()
  const pendingRequest = artifactCatalogString(record.pending_request)
  if (!label || (rawAction !== 'select' && rawAction !== 'use') || (pendingRequest && rawAction !== 'use')) return null
  return {
    ...selection,
    label,
    description: artifactCatalogString(record.description) || undefined,
    ...(pendingRequest ? { pending_request: pendingRequest } : {}),
    action: rawAction,
  }
}

export function appendDesktopV3ArtifactMessageSelection(
  selections: readonly DesktopV3ArtifactMessageSelection[],
  incoming: DesktopV3ArtifactMessageSelection,
): DesktopV3ArtifactMessageSelection[] {
  const normalized = normalizeDesktopV3ArtifactMessageSelection(incoming)
  if (!normalized) throw new Error('Artifact chip requires a complete opaque selection, visible label, and action')
  const singularSelections = normalized.action === 'use'
    ? selections.filter((selection) => selection.action !== 'use')
    : selections
  const key = desktopV3ArtifactMessageSelectionKey(normalized)
  const index = singularSelections.findIndex((selection) => desktopV3ArtifactMessageSelectionKey(selection) === key)
  if (index < 0) {
    if (singularSelections.length >= DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT) {
      throw new Error(`A message supports at most ${DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT} artifact selections`)
    }
    return [...singularSelections, normalized]
  }
  return singularSelections.map((selection, selectionIndex) => selectionIndex === index ? normalized : selection)
}

export function appendDesktopV3ArtifactMessageSelections(
  selections: readonly DesktopV3ArtifactMessageSelection[],
  incoming: readonly DesktopV3ArtifactMessageSelection[],
): DesktopV3ArtifactMessageSelection[] {
  const remainingCapacity = DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT - selections.length
  const normalizedIncoming = incoming.map((selection) => {
    const normalized = normalizeDesktopV3ArtifactMessageSelection(selection)
    if (!normalized) throw new Error('Artifact chip requires a complete opaque selection, visible label, and action')
    return normalized
  })
  const uniqueIncomingKeys = new Set(
    normalizedIncoming
      .map(desktopV3ArtifactMessageSelectionKey)
      .filter((key) => !selections.some((selection) => desktopV3ArtifactMessageSelectionKey(selection) === key)),
  )
  if (uniqueIncomingKeys.size > remainingCapacity) {
    throw new Error(`A message supports at most ${DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT} artifact selections`)
  }
  return normalizedIncoming.reduce(appendDesktopV3ArtifactMessageSelection, [...selections])
}

export function removeDesktopV3ArtifactMessageSelection(
  selections: readonly DesktopV3ArtifactMessageSelection[],
  selection: Pick<DesktopV3ArtifactSelection, 'session_id' | 'collection_id' | 'variant_id'> & Partial<Pick<DesktopV3ArtifactSelection, 'part_id'>>,
): DesktopV3ArtifactMessageSelection[] {
  const key = desktopV3ArtifactMessageSelectionKey(selection)
  return selections.filter((item) => desktopV3ArtifactMessageSelectionKey(item) !== key)
}

export function normalizeDesktopV3ArtifactSelection(value: unknown): DesktopV3ArtifactSelection | null {
  const record = artifactCatalogRecord(value)
  if (!record) return null
  const sessionId = artifactCatalogString(record.session_id)
  const collectionId = artifactCatalogString(record.collection_id)
  const variantId = artifactCatalogString(record.variant_id) || artifactCatalogString(record.id) || artifactCatalogString(record.artifact_id)
  const eventSeq = artifactCatalogEventSeq(record.event_seq)
  if (!sessionId || !collectionId || !variantId || eventSeq <= 0) return null
  const partId = artifactCatalogString(record.part_id)
  return { session_id: sessionId, collection_id: collectionId, variant_id: variantId, event_seq: eventSeq, ...(partId ? { part_id: partId } : {}) }
}

export function desktopV3ArtifactSelectionEndpoint(sessionId: string, variantId: string): string {
  const normalizedSessionId = sessionId.trim()
  const normalizedVariantId = variantId.trim()
  if (!normalizedSessionId || !normalizedVariantId) throw new Error('Artifact action requires a session and variant ID')
  return `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/artifacts/${encodeURIComponent(normalizedVariantId)}/selection`
}

async function postDesktopV3ArtifactSelection(
  action: 'select' | 'use',
  selection: DesktopV3ArtifactSelection,
  signal?: AbortSignal,
): Promise<DesktopV3ArtifactSelection> {
  const normalized = normalizeDesktopV3ArtifactSelection(selection)
  if (!normalized) throw new Error('Artifact action requires a complete opaque selection')
  const clientRequestId = `desktop-v3-artifact-${action}:${crypto.randomUUID()}`
  const response = await apiFetch(desktopV3ArtifactSelectionEndpoint(normalized.session_id, normalized.variant_id), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ client_request_id: clientRequestId, event_seq: normalized.event_seq, action, ...(normalized.part_id ? { part_id: normalized.part_id } : {}) }),
    signal,
  })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  const payloadValue = await response.json() as unknown
  const payload = artifactCatalogRecord(payloadValue)
  if (!payload || payload.ok !== true) throw new Error(`Artifact ${action} returned an invalid response`)
  const returned = normalizeDesktopV3ArtifactSelection(payload.selection ?? payload.artifact_selection)
  if (!returned) throw new Error(`Artifact ${action} did not return a selection`)
  return returned
}

export function selectDesktopV3Artifact(selection: DesktopV3ArtifactSelection, signal?: AbortSignal): Promise<DesktopV3ArtifactSelection> {
  return postDesktopV3ArtifactSelection('select', selection, signal)
}

export function useDesktopV3Artifact(selection: DesktopV3ArtifactSelection, signal?: AbortSignal): Promise<DesktopV3ArtifactSelection> {
  return postDesktopV3ArtifactSelection('use', selection, signal)
}

export function desktopV3ArtifactEndpoint(sessionId: string, artifactId: string): string {
  const normalizedSessionId = sessionId.trim()
  const normalizedArtifactId = artifactId.trim()
  if (!normalizedSessionId || !normalizedArtifactId) {
    throw new Error('Artifact preview requires a session and artifact ID')
  }
  return `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/artifacts/${encodeURIComponent(normalizedArtifactId)}`
}

export function desktopV3ArtifactBundleEndpoint(sessionId: string, artifactId: string): string {
  return `${desktopV3ArtifactEndpoint(sessionId, artifactId)}/bundle`
}

export function desktopV3ArtifactCollectionBundleEndpoint(sessionId: string, collectionId: string): string {
  const normalizedSessionId = sessionId.trim()
  const normalizedCollectionId = collectionId.trim()
  if (!normalizedSessionId || !normalizedCollectionId) throw new Error('Artifact collection download requires a session and collection ID')
  return `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/artifacts/collections/${encodeURIComponent(normalizedCollectionId)}/bundle`
}

export function desktopV3ArtifactRequiresBundle(
  artifact: Pick<DesktopV3ArtifactCatalogEntry, 'kind' | 'mediaType'>,
): boolean {
  const mediaType = artifact.mediaType.split(';', 1)[0]?.trim().toLowerCase()
  return artifact.kind.trim().toLowerCase() === 'package' || mediaType === 'application/zip'
}

function desktopV3ArtifactFilenameExtension(mediaType: string): string {
  switch (mediaType.split(';', 1)[0]?.trim().toLowerCase()) {
    case 'image/gif': return '.gif'
    case 'image/jpeg': return '.jpg'
    case 'image/png': return '.png'
    case 'image/svg+xml': return '.svg'
    case 'image/webp': return '.webp'
    case 'video/mp4': return '.mp4'
    case 'video/quicktime': return '.mov'
    case 'video/webm': return '.webm'
    default: return ''
  }
}

function desktopV3ArtifactSafeFilename(value: string): string {
  return value.trim().split(/[\\/]/).pop()?.trim().replace(/[<>:"|?*]+/g, '-') ?? ''
}

export function desktopV3ArtifactDownloadName(
  artifact: Pick<DesktopV3ArtifactCatalogEntry, 'filename' | 'kind' | 'label' | 'mediaType'>,
): string {
  const filename = desktopV3ArtifactSafeFilename(artifact.filename)
  if (desktopV3ArtifactRequiresBundle(artifact)) {
    if (/\.zip$/i.test(filename)) return filename
    const label = desktopV3ArtifactSafeFilename(artifact.label).replace(/\.[a-z0-9]{1,8}$/i, '')
    return `${label || 'artifact'}.zip`
  }
  if (filename) return filename
  const label = desktopV3ArtifactSafeFilename(artifact.label) || 'artifact'
  const extension = desktopV3ArtifactFilenameExtension(artifact.mediaType)
  return extension && !label.toLowerCase().endsWith(extension) ? `${label}${extension}` : label
}

export function desktopV3ArtifactPreviewAccessEndpoint(sessionId: string): string {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Artifact preview access requires a session ID')
  return `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/artifacts/preview-access`
}

export interface DesktopV3ArtifactPreviewAccess {
  url: string
  expiresAt: number
  mediaType: string
  sandbox: 'allow-scripts'
  opaqueOrigin: true
}

export function desktopV3ArtifactDirectContentURL(
  artifact: Pick<DesktopV3ArtifactCatalogEntry, 'artifactId' | 'sessionId' | 'sourceRef'>,
): string {
  if (artifact.sourceRef?.trim()) {
    const search = new URLSearchParams({ source_ref: artifact.sourceRef.trim() })
    return `/v3/sessions/${encodeURIComponent(artifact.sessionId.trim())}/video/sources/media?${search.toString()}`
  }
  return desktopV3ArtifactEndpoint(artifact.sessionId, artifact.artifactId)
}

/** Refreshes Desktop auth before a browser-owned media element receives a protected URL. */
export async function preflightDesktopV3ArtifactDirectContent(
  artifact: Pick<DesktopV3ArtifactCatalogEntry, 'artifactId' | 'sessionId' | 'sourceRef'>,
  signal?: AbortSignal,
): Promise<string> {
  const url = desktopV3ArtifactDirectContentURL(artifact)
  const response = await apiFetch(url, { method: 'HEAD', signal })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  return url
}

export function desktopV3ArtifactRevealEndpoint(sessionId: string, artifactId: string): string {
  return `${desktopV3ArtifactEndpoint(sessionId, artifactId)}/reveal`
}

export function desktopV3ArtifactCollectionRevealEndpoint(sessionId: string, collectionId: string): string {
  const normalizedSessionId = sessionId.trim()
  const normalizedCollectionId = collectionId.trim()
  if (!normalizedSessionId || !normalizedCollectionId) throw new Error('Artifact collection reveal requires a session and collection ID')
  return `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/artifacts/collections/${encodeURIComponent(normalizedCollectionId)}/reveal`
}

export interface DesktopV3ArtifactRevealResult {
  method: string
  displayLocation: string
}

async function postDesktopV3ArtifactReveal(endpoint: string, signal?: AbortSignal): Promise<DesktopV3ArtifactRevealResult> {
  const response = await apiFetch(endpoint, { method: 'POST', signal })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  const payload = await response.json() as Record<string, unknown>
  const method = artifactCatalogString(payload.method)
  const displayLocation = artifactCatalogString(payload.display_location)
  if (payload.ok !== true || !method || !displayLocation) {
    throw new Error('Artifact reveal did not confirm that the native file manager opened')
  }
  return { method, displayLocation }
}

export function revealDesktopV3Artifact(sessionId: string, artifactId: string, signal?: AbortSignal): Promise<DesktopV3ArtifactRevealResult> {
  return postDesktopV3ArtifactReveal(desktopV3ArtifactRevealEndpoint(sessionId, artifactId), signal)
}

export function revealDesktopV3ArtifactCollection(sessionId: string, collectionId: string, signal?: AbortSignal): Promise<DesktopV3ArtifactRevealResult> {
  return postDesktopV3ArtifactReveal(desktopV3ArtifactCollectionRevealEndpoint(sessionId, collectionId), signal)
}

export async function fetchDesktopV3ArtifactPreviewAccess(
  sessionId: string,
  artifactId: string,
  signal?: AbortSignal,
): Promise<DesktopV3ArtifactPreviewAccess> {
  const response = await apiFetch(desktopV3ArtifactPreviewAccessEndpoint(sessionId), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ artifact_id: artifactId }),
    signal,
  })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  const payload = await response.json() as Record<string, unknown>
  const url = typeof payload.preview_url === 'string' ? payload.preview_url.trim() : ''
  const expiresAt = typeof payload.expires_at === 'number' && Number.isSafeInteger(payload.expires_at) ? payload.expires_at : 0
  const mediaType = typeof payload.media_type === 'string' ? payload.media_type.trim() : ''
  if (payload.ok !== true || !url || !url.startsWith('/') || !expiresAt || mediaType !== 'text/html; charset=utf-8'
    || payload.sandbox !== 'allow-scripts' || payload.opaque_origin !== true) {
    throw new Error('Artifact preview access returned an invalid runtime contract')
  }
  return { url, expiresAt, mediaType, sandbox: 'allow-scripts', opaqueOrigin: true }
}

const desktopV3ArtifactPackageEntryPath = '__swarm_artifact_entry__.html'

export function desktopV3ArtifactPackageBaseEndpoint(sessionId: string, artifactId: string, previewToken: string): string {
  const normalizedToken = previewToken.trim()
  if (!normalizedToken) throw new Error('Artifact preview access token is required')
  return `${desktopV3ArtifactEndpoint(sessionId, artifactId)}/content/access/${encodeURIComponent(normalizedToken)}/`
}

export function desktopV3ArtifactPackageEntryEndpoint(sessionId: string, artifactId: string, previewToken: string): string {
  return `${desktopV3ArtifactPackageBaseEndpoint(sessionId, artifactId, previewToken)}${desktopV3ArtifactPackageEntryPath}`
}

function desktopV3ArtifactSameOriginURL(value: string): string {
  try {
    const url = new URL(value, window.location.origin)
    return url.origin === window.location.origin ? url.toString() : ''
  } catch {
    return ''
  }
}

export function buildDesktopV3ArtifactSandboxDocument(
  source: string,
  sessionId: string,
  artifactId: string,
  previewToken: string,
  runtimeAssets?: DesktopV3ArtifactLocalRuntimeAssets,
): string {
  const packageBase = new URL(desktopV3ArtifactPackageBaseEndpoint(sessionId, artifactId, previewToken), window.location.origin)
  const packageEntry = new URL(desktopV3ArtifactPackageEntryEndpoint(sessionId, artifactId, previewToken), window.location.origin)
  const document = new DOMParser().parseFromString(source, 'text/html')
  const packageSource = packageBase.toString()
  const runtimeAssetPaths = new Set([...(runtimeAssets?.scripts ?? []), ...Object.values(runtimeAssets?.modules ?? {}), ...Object.values(runtimeAssets?.wasm ?? {})])
  const reviewedRuntimePath = '/swarm-animation-runtime/'
  if ([...runtimeAssetPaths].some((value) => {
    try {
      const url = new URL(value, window.location.origin)
      const runtimeFilename = url.pathname.slice(reviewedRuntimePath.length)
      return url.origin !== window.location.origin
        || !url.pathname.startsWith(reviewedRuntimePath)
        || !runtimeFilename
        || runtimeFilename.includes('/')
        || url.username !== ''
        || url.password !== ''
        || url.search !== ''
        || url.hash !== ''
    } catch {
      return true
    }
  })) throw new Error('Animation runtime assets must use the reviewed local runtime path')
  const runtimeScripts = (runtimeAssets?.scripts ?? []).map(desktopV3ArtifactSameOriginURL).filter(Boolean)
  const runtimeModules = Object.fromEntries(Object.entries(runtimeAssets?.modules ?? {}).map(([name, value]) => [name, desktopV3ArtifactSameOriginURL(value)]).filter((entry): entry is [string, string] => Boolean(entry[1])))
  const runtimeWasm = Object.fromEntries(Object.entries(runtimeAssets?.wasm ?? {}).map(([name, value]) => [name, desktopV3ArtifactSameOriginURL(value)]).filter((entry): entry is [string, string] => Boolean(entry[1])))
  const scriptNonce = globalThis.crypto?.randomUUID?.()
  if (!scriptNonce) throw new Error('Secure animation preview nonce generation is unavailable')
  if (runtimeScripts.length !== (runtimeAssets?.scripts.length ?? 0) || Object.keys(runtimeModules).length !== Object.keys(runtimeAssets?.modules ?? {}).length || Object.keys(runtimeWasm).length !== Object.keys(runtimeAssets?.wasm ?? {}).length) {
    throw new Error('Animation runtime assets must be same-install URLs')
  }
  const runtimeSourcePolicy = runtimeScripts.length || Object.keys(runtimeModules).length || Object.keys(runtimeWasm).length ? window.location.origin : ''
  const policy = document.createElement('meta')
  policy.httpEquiv = 'Content-Security-Policy'
  policy.content = [
    "default-src 'none'",
    `script-src 'nonce-${scriptNonce}' blob:${runtimeSourcePolicy ? ` ${runtimeSourcePolicy}` : ''}`,
    `style-src 'unsafe-inline' ${packageSource}`,
    `img-src ${packageSource} data: blob:`,
    `font-src ${packageSource} data:`,
    `media-src ${packageSource}${runtimeSourcePolicy ? ` ${runtimeSourcePolicy}` : ''} data: blob:`,
    `frame-src ${packageSource}`,
    `child-src ${runtimeSourcePolicy || "'none'"} blob:`,
    "connect-src 'none'",
    `worker-src ${runtimeSourcePolicy || "'none'"} blob:`,
    "object-src 'none'",
    `base-uri ${packageEntry.toString()}`,
    "form-action 'none'",
  ].join('; ')
  const base = document.createElement('base')
  base.href = packageEntry.toString()
  const runtimeConfig = document.createElement('script')
  runtimeConfig.nonce = scriptNonce
  runtimeConfig.textContent = `globalThis.__SWARM_ANIMATION_RUNTIME__=${JSON.stringify({ modules: runtimeModules, wasm: runtimeWasm }).replace(/</g, '\\u003c')};`
  const importMap = document.createElement('script')
  importMap.nonce = scriptNonce
  importMap.type = 'importmap'
  importMap.textContent = JSON.stringify({ imports: runtimeModules }).replace(/</g, '\\u003c')
  const scripts = runtimeScripts.map((src) => {
    const script = document.createElement('script')
    script.src = src
    script.nonce = scriptNonce
    script.defer = false
    return script
  })
  document.head.prepend(policy, base, runtimeConfig, importMap, ...scripts)
  for (const script of document.querySelectorAll('script')) script.setAttribute('nonce', scriptNonce)
  return `<!doctype html>\n${document.documentElement.outerHTML}`
}

export async function fetchDesktopV3ArtifactPreviewToken(sessionId: string, artifactId: string, signal?: AbortSignal): Promise<string> {
  const access = await fetchDesktopV3ArtifactPreviewAccess(sessionId, artifactId, signal)
  const marker = '/content/access/'
  const token = access.url.split(marker, 2)[1]?.split('/', 1)[0]?.trim() ?? ''
  if (!token) throw new Error('Artifact preview access did not return a token')
  return token
}

const desktopV3ArtifactTextPreviewMaxBytes = 2 << 20

export async function fetchDesktopV3ArtifactTextPreview(
  artifact: Pick<DesktopV3ArtifactCatalogEntry, 'artifactId' | 'sessionId' | 'sourceRef'>,
  signal?: AbortSignal,
): Promise<string> {
  const response = await apiFetch(desktopV3ArtifactDirectContentURL(artifact), { method: 'GET', signal })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  const declaredLength = Number(response.headers.get('Content-Length') ?? 0)
  if (Number.isFinite(declaredLength) && declaredLength > desktopV3ArtifactTextPreviewMaxBytes) {
    throw new Error('Artifact text preview exceeds the 2 MB inline limit')
  }
  if (!response.body) {
    const text = await response.text()
    if (new TextEncoder().encode(text).byteLength > desktopV3ArtifactTextPreviewMaxBytes) throw new Error('Artifact text preview exceeds the 2 MB inline limit')
    return text
  }
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let received = 0
  let text = ''
  try {
    while (true) {
      const { value, done } = await reader.read()
      if (done) break
      received += value.byteLength
      if (received > desktopV3ArtifactTextPreviewMaxBytes) throw new Error('Artifact text preview exceeds the 2 MB inline limit')
      text += decoder.decode(value, { stream: true })
    }
    return text + decoder.decode()
  } finally {
    reader.releaseLock()
  }
}

export async function fetchDesktopV3ArtifactBundle(sessionId: string, artifactId: string, signal?: AbortSignal): Promise<Blob> {
  const response = await apiFetch(desktopV3ArtifactBundleEndpoint(sessionId, artifactId), {
    method: 'GET',
    signal,
  })
  if (!response.ok) {
    throw new Error(await readErrorMessage(response))
  }
  return response.blob()
}

export async function fetchDesktopV3ArtifactCollectionBundle(sessionId: string, collectionId: string, signal?: AbortSignal): Promise<Blob> {
  const response = await apiFetch(desktopV3ArtifactCollectionBundleEndpoint(sessionId, collectionId), {
    method: 'GET',
    signal,
  })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  return response.blob()
}

export async function fetchDesktopV3ArtifactDownload(
  artifact: Pick<DesktopV3ArtifactCatalogEntry, 'artifactId' | 'kind' | 'mediaType' | 'sessionId' | 'sourceRef'>,
  signal?: AbortSignal,
): Promise<Blob> {
  if (artifact.sourceRef) {
    const search = new URLSearchParams({ source_ref: artifact.sourceRef })
    const response = await apiFetch(`/v3/sessions/${encodeURIComponent(artifact.sessionId)}/video/sources/media?${search.toString()}`, { method: 'GET', signal })
    if (!response.ok) throw new Error(await readErrorMessage(response))
    return response.blob()
  }
  if (desktopV3ArtifactRequiresBundle(artifact)) {
    return fetchDesktopV3ArtifactBundle(artifact.sessionId, artifact.artifactId, signal)
  }
  return fetchDesktopV3Artifact(artifact.sessionId, artifact.artifactId, signal)
}

export async function fetchDesktopV3Artifact(sessionId: string, artifactId: string, signal?: AbortSignal): Promise<Blob> {
  const response = await apiFetch(desktopV3ArtifactEndpoint(sessionId, artifactId), {
    method: 'GET',
    signal,
  })
  if (!response.ok) {
    throw new Error(await readErrorMessage(response))
  }
  return response.blob()
}
