import { apiFetch, readErrorMessage } from '../../../app/api'

export type DesktopV3ArtifactCategory = 'plan' | 'visual' | 'document'
export type DesktopV3ArtifactStatus = '' | 'staging' | 'ready' | 'failed' | 'unavailable'

export interface DesktopV3ArtifactCollectionProgress {
  total: number
  staging: number
  ready: number
  failed: number
  unavailable: number
}

export interface DesktopV3ArtifactLineage {
  parentSessionId: string
  sourceSessionId: string
  sourceCollectionId: string
  sourceVariantId: string
  taskCallId: string
  programId: string
  programJobId: string
  childSessionId: string
  iterationId: string
  iterationIndex: number
  runId: string
  planId: string
  checkpointId: string
  attemptId: string
}

/** Opaque, portable managed-artifact reference. It never contains bytes or storage paths. */
export interface DesktopV3ArtifactSelection {
  session_id: string
  collection_id: string
  variant_id: string
  event_seq: number
}

/** Opaque message reference plus bounded display metadata and review intent. */
export interface DesktopV3ArtifactMessageSelection extends DesktopV3ArtifactSelection {
  label: string
  description?: string
  action: 'select' | 'use'
}

export const DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT = 16

export interface DesktopV3ArtifactCatalogEntry {
  artifactId: string
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
  content?: string
}

type DesktopV3ArtifactCatalogResponse = {
  ok?: unknown
  artifacts?: unknown
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

function normalizeArtifactLineage(value: unknown): DesktopV3ArtifactLineage | null {
  const record = artifactCatalogRecord(value)
  if (!record) return null
  return {
    parentSessionId: artifactCatalogString(record.parent_session_id),
    sourceSessionId: artifactCatalogString(record.source_session_id),
    sourceCollectionId: artifactCatalogString(record.source_collection_id),
    sourceVariantId: artifactCatalogString(record.source_variant_id),
    taskCallId: artifactCatalogString(record.task_call_id),
    programId: artifactCatalogString(record.program_id),
    programJobId: artifactCatalogString(record.program_job_id),
    childSessionId: artifactCatalogString(record.child_session_id),
    iterationId: artifactCatalogString(record.iteration_id),
    iterationIndex: artifactCatalogCount(record.iteration_index),
    runId: artifactCatalogString(record.run_id),
    planId: artifactCatalogString(record.plan_id),
    checkpointId: artifactCatalogString(record.checkpoint_id),
    attemptId: artifactCatalogString(record.attempt_id),
  }
}

export function normalizeDesktopV3ArtifactCatalogEntry(value: unknown): DesktopV3ArtifactCatalogEntry | null {
  const record = artifactCatalogRecord(value)
  if (!record) return null
  const artifactId = artifactCatalogString(record.artifact_id)
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
  return {
    artifactId,
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
    ...(typeof record.content === 'string' ? { content: record.content } : {}),
  }
}

export async function fetchDesktopV3ArtifactCatalog(signal?: AbortSignal): Promise<DesktopV3ArtifactCatalogEntry[]> {
  const response = await apiFetch('/v3/artifacts?limit=2000', { method: 'GET', signal })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  const payload = await response.json() as DesktopV3ArtifactCatalogResponse
  if (payload.ok !== true || !Array.isArray(payload.artifacts)) throw new Error('Artifact catalog returned an invalid response')
  return payload.artifacts.map(normalizeDesktopV3ArtifactCatalogEntry).filter((entry): entry is DesktopV3ArtifactCatalogEntry => entry !== null)
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

export function desktopV3ArtifactMessageSelection(
  entry: DesktopV3ArtifactCatalogEntry,
  action: 'select' | 'use' = 'select',
): DesktopV3ArtifactMessageSelection {
  return {
    ...desktopV3ArtifactSelection(entry),
    label: entry.label.trim() || entry.filename.trim() || 'Artifact',
    description: entry.description.trim() || undefined,
    action,
  }
}

export function desktopV3ArtifactMessageSelectionKey(
  selection: Pick<DesktopV3ArtifactSelection, 'session_id' | 'collection_id' | 'variant_id'>,
): string {
  return `${selection.session_id.trim()}\u0000${selection.collection_id.trim()}\u0000${selection.variant_id.trim()}`
}

export function normalizeDesktopV3ArtifactMessageSelection(value: unknown): DesktopV3ArtifactMessageSelection | null {
  const selection = normalizeDesktopV3ArtifactSelection(value)
  const record = artifactCatalogRecord(value)
  if (!selection || !record) return null
  const label = artifactCatalogString(record.label)
  const rawAction = artifactCatalogString(record.action).toLowerCase()
  if (!label || (rawAction !== 'select' && rawAction !== 'use')) return null
  return {
    ...selection,
    label,
    description: artifactCatalogString(record.description) || undefined,
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
    ? selections.map((selection) => selection.action === 'use'
        && selection.session_id === normalized.session_id
        && selection.collection_id === normalized.collection_id
      ? { ...selection, action: 'select' as const }
      : selection)
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
  selection: Pick<DesktopV3ArtifactSelection, 'session_id' | 'collection_id' | 'variant_id'>,
): DesktopV3ArtifactMessageSelection[] {
  const key = desktopV3ArtifactMessageSelectionKey(selection)
  return selections.filter((item) => desktopV3ArtifactMessageSelectionKey(item) !== key)
}

export function normalizeDesktopV3ArtifactSelection(value: unknown): DesktopV3ArtifactSelection | null {
  const record = artifactCatalogRecord(value)
  if (!record) return null
  const sessionId = artifactCatalogString(record.session_id)
  const collectionId = artifactCatalogString(record.collection_id)
  const variantId = artifactCatalogString(record.variant_id)
  const eventSeq = artifactCatalogEventSeq(record.event_seq)
  if (!sessionId || !collectionId || !variantId || eventSeq <= 0) return null
  return { session_id: sessionId, collection_id: collectionId, variant_id: variantId, event_seq: eventSeq }
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
    body: JSON.stringify({ client_request_id: clientRequestId, event_seq: normalized.event_seq, action }),
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

export function desktopV3ArtifactPreviewAccessEndpoint(sessionId: string): string {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Artifact preview access requires a session ID')
  return `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/artifacts/preview-access`
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

export function buildDesktopV3ArtifactSandboxDocument(source: string, sessionId: string, artifactId: string, previewToken: string): string {
  const packageBase = new URL(desktopV3ArtifactPackageBaseEndpoint(sessionId, artifactId, previewToken), window.location.origin)
  const packageEntry = new URL(desktopV3ArtifactPackageEntryEndpoint(sessionId, artifactId, previewToken), window.location.origin)
  const document = new DOMParser().parseFromString(source, 'text/html')
  const packageSource = packageBase.toString()
  const policy = document.createElement('meta')
  policy.httpEquiv = 'Content-Security-Policy'
  policy.content = [
    "default-src 'none'",
    "script-src 'unsafe-inline' blob:",
    `style-src 'unsafe-inline' ${packageSource}`,
    `img-src ${packageSource} data: blob:`,
    `font-src ${packageSource} data:`,
    `media-src ${packageSource} data: blob:`,
    `frame-src ${packageSource}`,
    "connect-src 'none'",
    "worker-src blob:",
    "object-src 'none'",
    `base-uri ${packageEntry.toString()}`,
    "form-action 'none'",
  ].join('; ')
  const base = document.createElement('base')
  base.href = packageEntry.toString()
  document.head.prepend(policy, base)
  return `<!doctype html>\n${document.documentElement.outerHTML}`
}

export async function fetchDesktopV3ArtifactPreviewToken(sessionId: string, artifactId: string, signal?: AbortSignal): Promise<string> {
  const response = await apiFetch(desktopV3ArtifactPreviewAccessEndpoint(sessionId), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ artifact_id: artifactId }),
    signal,
  })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  const payload = await response.json() as { token?: unknown }
  const token = typeof payload.token === 'string' ? payload.token.trim() : ''
  if (!token) throw new Error('Artifact preview access did not return a token')
  return token
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
