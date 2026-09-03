import { apiFetch, readErrorMessage } from '../../../app/api'

export type DesktopV3ArtifactV2State = 'allocated' | 'authoring' | 'building' | 'validating' | 'invalid' | 'ready' | 'iterating' | 'published_view' | 'cancelled'
export type DesktopV3ArtifactV2IterationState = 'open' | 'generating' | 'awaiting_selection' | 'selected' | 'closed_without_selection' | 'cancelled'

export interface DesktopV3ArtifactV2Diagnostic {
  code: string
  phase: string
  severity: string
  partId: string
  authoredLocator: string
  retryClass: string
  safeMessage: string
}

export interface DesktopV3ArtifactV2CompositionHead {
  compositionId: string
  headRevision: number
  digestSha256: string
  eventSeq: number
}

export interface DesktopV3ArtifactV2PublishedHeadReference {
  publishedHeadId: string
  compositionId: string
  digestSha256: string
  eventSeq: number
}

export interface DesktopV3ArtifactV2Working {
  id: string
  sessionId: string
  kind: string
  state: DesktopV3ArtifactV2State
  policyRevision: string
  capabilityClass: string
  intentReference: string
  revision: number
  eventSeq: number
  compositionHead?: DesktopV3ArtifactV2CompositionHead
  publishedHead?: DesktopV3ArtifactV2PublishedHeadReference
  latestBuildId: string
  latestValidationId: string
  activeIterationId: string
  latestDiagnostic?: DesktopV3ArtifactV2Diagnostic
  createdAt: number
  updatedAt: number
}

export interface DesktopV3ArtifactV2Projection {
  artifactId: string
  sessionId: string
  kind: string
  state: DesktopV3ArtifactV2State
  revision: number
  eventSeq: number
  partCount: number
  compositionHead?: DesktopV3ArtifactV2CompositionHead
  latestBuildId: string
  latestValidationId: string
  activeIterationId: string
  publishedHead?: DesktopV3ArtifactV2PublishedHeadReference
  latestDiagnostic?: DesktopV3ArtifactV2Diagnostic
  updatedAt: number
}

export interface DesktopV3ArtifactV2CatalogItem {
  schemaVersion: 1
  readOnly: true
  working: DesktopV3ArtifactV2Working
  projection: DesktopV3ArtifactV2Projection
}

export interface DesktopV3ArtifactV2Part {
  id: string
  artifactId: string
  key: string
  label: string
  role: string
  mediaClass: string
  locatorKind: string
  locatorValue: string
  order: number
  revision: number
  eventSeq: number
  createdAt: number
  updatedAt: number
}

export interface DesktopV3ArtifactV2PartRevision {
  id: string
  artifactId: string
  partId: string
  parentRevisionId: string
  mediaType: string
  digestSha256: string
  size: number
  revision: number
  eventSeq: number
  createdAt: number
}

export interface DesktopV3ArtifactV2CompositionPart {
  partId: string
  partRevisionId: string
  digestSha256: string
  locked: boolean
}

export interface DesktopV3ArtifactV2Composition {
  id: string
  artifactId: string
  parentCompositionId: string
  policyRevision: string
  constructionVersion: string
  parts: DesktopV3ArtifactV2CompositionPart[]
  digestSha256: string
  revision: number
  eventSeq: number
  createdAt: number
}

export interface DesktopV3ArtifactV2Build {
  id: string
  compositionId: string
  status: string
  compilerVersion: string
  diagnostics: DesktopV3ArtifactV2Diagnostic[]
  eventSeq: number
  createdAt: number
  completedAt: number
}

export interface DesktopV3ArtifactV2Validation {
  id: string
  buildId: string
  compositionId: string
  status: string
  validatorVersion: string
  diagnostics: DesktopV3ArtifactV2Diagnostic[]
  eventSeq: number
  createdAt: number
  completedAt: number
}

export interface DesktopV3ArtifactV2IterationCandidate {
  slotId: string
  compositionId: string
  status: string
  failureCode: string
  eventSeq: number
}

export interface DesktopV3ArtifactV2Iteration {
  id: string
  artifactId: string
  baseCompositionId: string
  baseCompositionDigest: string
  targetPartIds: string[]
  requestedCandidates: number
  status: DesktopV3ArtifactV2IterationState
  candidates: DesktopV3ArtifactV2IterationCandidate[]
  selectedSlotId: string
  revision: number
  eventSeq: number
  createdAt: number
  updatedAt: number
}

export interface DesktopV3ArtifactV2PublishedHead {
  id: string
  artifactId: string
  compositionId: string
  compositionDigest: string
  buildId: string
  validationId: string
  policyRevision: string
  previousHeadId: string
  authorizingActor: string
  revision: number
  eventSeq: number
  createdAt: number
}

export interface DesktopV3ArtifactV2Derivative { id: string; kind: string; status: string; mediaType: string; digestSha256: string; sourcePartId: string; sourcePartRevisionId: string; captureStateId: string; eventSeq: number; diagnostics: DesktopV3ArtifactV2Diagnostic[] }

export interface DesktopV3ArtifactV2Studio {
  schemaVersion: 1
  readOnly: true
  working: DesktopV3ArtifactV2Working
  projection: DesktopV3ArtifactV2Projection
  parts: DesktopV3ArtifactV2Part[]
  partRevisions: DesktopV3ArtifactV2PartRevision[]
  compositions: DesktopV3ArtifactV2Composition[]
  builds: DesktopV3ArtifactV2Build[]
  validations: DesktopV3ArtifactV2Validation[]
  derivatives: DesktopV3ArtifactV2Derivative[]
  iterations: DesktopV3ArtifactV2Iteration[]
  publishedHeads: DesktopV3ArtifactV2PublishedHead[]
}

function record(value: unknown): Record<string, unknown> | null { return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null }
function text(value: unknown): string { return typeof value === 'string' ? value.trim() : '' }
function integer(value: unknown): number { return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : 0 }
function diagnostic(value: unknown): DesktopV3ArtifactV2Diagnostic | undefined {
  const row = record(value); if (!row) return undefined
  const code = text(row.code); const safeMessage = text(row.safe_message)
  if (!code || !safeMessage) return undefined
  return { code, phase: text(row.phase), severity: text(row.severity), partId: text(row.part_id), authoredLocator: text(row.authored_locator), retryClass: text(row.retry_class), safeMessage }
}
function compositionHead(value: unknown): DesktopV3ArtifactV2CompositionHead | undefined {
  const row = record(value); if (!row) return undefined
  const compositionId = text(row.composition_id); const headRevision = integer(row.head_revision); const digestSha256 = text(row.digest_sha256); const eventSeq = integer(row.event_seq)
  return compositionId && headRevision && digestSha256 ? { compositionId, headRevision, digestSha256, eventSeq } : undefined
}
function publishedRef(value: unknown): DesktopV3ArtifactV2PublishedHeadReference | undefined {
  const row = record(value); if (!row) return undefined
  const publishedHeadId = text(row.published_head_id); const compositionId = text(row.composition_id); const digestSha256 = text(row.digest_sha256); const eventSeq = integer(row.event_seq)
  return publishedHeadId && compositionId && digestSha256 ? { publishedHeadId, compositionId, digestSha256, eventSeq } : undefined
}
const artifactStates = new Set<DesktopV3ArtifactV2State>(['allocated', 'authoring', 'building', 'validating', 'invalid', 'ready', 'iterating', 'published_view', 'cancelled'])
function working(value: unknown): DesktopV3ArtifactV2Working | undefined {
  const row = record(value); if (!row) return undefined
  const id = text(row.id); const sessionId = text(row.session_id); const state = text(row.state) as DesktopV3ArtifactV2State
  if (!id || !sessionId || !artifactStates.has(state)) return undefined
  return { id, sessionId, kind: text(row.kind), state, policyRevision: text(row.policy_revision), capabilityClass: text(row.capability_class), intentReference: text(row.intent_reference), revision: integer(row.revision), eventSeq: integer(row.event_seq), ...(compositionHead(row.composition_head) ? { compositionHead: compositionHead(row.composition_head) } : {}), ...(publishedRef(row.published_head) ? { publishedHead: publishedRef(row.published_head) } : {}), latestBuildId: text(row.latest_build_id), latestValidationId: text(row.latest_validation_id), activeIterationId: text(row.active_iteration_id), ...(diagnostic(row.latest_diagnostic) ? { latestDiagnostic: diagnostic(row.latest_diagnostic) } : {}), createdAt: integer(row.created_at), updatedAt: integer(row.updated_at) }
}
function projection(value: unknown): DesktopV3ArtifactV2Projection | undefined {
  const row = record(value); if (!row) return undefined
  const artifactId = text(row.artifact_id); const sessionId = text(row.session_id); const state = text(row.state) as DesktopV3ArtifactV2State
  if (!artifactId || !sessionId || !artifactStates.has(state)) return undefined
  return { artifactId, sessionId, kind: text(row.kind), state, revision: integer(row.revision), eventSeq: integer(row.event_seq), partCount: integer(row.part_count), ...(compositionHead(row.composition_head) ? { compositionHead: compositionHead(row.composition_head) } : {}), latestBuildId: text(row.latest_build_id), latestValidationId: text(row.latest_validation_id), activeIterationId: text(row.active_iteration_id), ...(publishedRef(row.published_head) ? { publishedHead: publishedRef(row.published_head) } : {}), ...(diagnostic(row.latest_diagnostic) ? { latestDiagnostic: diagnostic(row.latest_diagnostic) } : {}), updatedAt: integer(row.updated_at) }
}
export function normalizeDesktopV3ArtifactV2CatalogItem(value: unknown): DesktopV3ArtifactV2CatalogItem | null {
  const row = record(value); const normalizedWorking = working(row?.working); const normalizedProjection = projection(row?.projection)
  return row?.schema_version === 1 && row.read_only === true && normalizedWorking && normalizedProjection && normalizedWorking.id === normalizedProjection.artifactId ? { schemaVersion: 1, readOnly: true, working: normalizedWorking, projection: normalizedProjection } : null
}
function normalizePart(value: unknown): DesktopV3ArtifactV2Part | undefined {
  const row = record(value); if (!row) return undefined
  const id = text(row.id); const artifactId = text(row.artifact_id); if (!id || !artifactId) return undefined
  return { id, artifactId, key: text(row.key), label: text(row.label) || text(row.key) || id, role: text(row.role), mediaClass: text(row.media_class), locatorKind: text(row.locator_kind), locatorValue: text(row.locator_value), order: integer(row.order), revision: integer(row.revision), eventSeq: integer(row.event_seq), createdAt: integer(row.created_at), updatedAt: integer(row.updated_at) }
}
function normalizePartRevision(value: unknown): DesktopV3ArtifactV2PartRevision | undefined {
  const row = record(value); const blob = record(row?.blob); if (!row || !blob) return undefined
  const id = text(row.id); const artifactId = text(row.artifact_id); const partId = text(row.part_id); const digestSha256 = text(blob.digest_sha256)
  if (!id || !artifactId || !partId || !digestSha256) return undefined
  return { id, artifactId, partId, parentRevisionId: text(row.parent_revision_id), mediaType: text(blob.media_type), digestSha256, size: integer(blob.size), revision: integer(row.revision), eventSeq: integer(row.event_seq), createdAt: integer(row.created_at) }
}
function normalizeComposition(value: unknown): DesktopV3ArtifactV2Composition | undefined {
  const row = record(value); if (!row || !Array.isArray(row.parts)) return undefined
  const id = text(row.id); const artifactId = text(row.artifact_id); const parts = row.parts.flatMap((value): DesktopV3ArtifactV2CompositionPart[] => { const part = record(value); const partId = text(part?.part_id); const partRevisionId = text(part?.part_revision_id); const digestSha256 = text(part?.digest_sha256); return partId && partRevisionId && digestSha256 ? [{ partId, partRevisionId, digestSha256, locked: part?.locked === true }] : [] })
  if (!id || !artifactId || parts.length !== row.parts.length) return undefined
  return { id, artifactId, parentCompositionId: text(row.parent_composition_id), policyRevision: text(row.policy_revision), constructionVersion: text(row.construction_version), parts, digestSha256: text(row.digest_sha256), revision: integer(row.revision), eventSeq: integer(row.event_seq), createdAt: integer(row.created_at) }
}
function normalizeEvidence<T extends DesktopV3ArtifactV2Build | DesktopV3ArtifactV2Validation>(value: unknown, validation: boolean): T | undefined {
  const row = record(value); if (!row) return undefined
  const id = text(row.id); const compositionId = text(row.composition_id); if (!id || !compositionId) return undefined
  const shared = { id, compositionId, status: text(row.status), diagnostics: Array.isArray(row.diagnostics) ? row.diagnostics.flatMap((item) => diagnostic(item) ?? []) : [], eventSeq: integer(row.event_seq), createdAt: integer(row.created_at), completedAt: integer(row.completed_at) }
  return (validation ? { ...shared, buildId: text(row.build_id), validatorVersion: text(row.validator_version) } : { ...shared, compilerVersion: text(row.compiler_version) }) as T
}
function normalizeDerivative(value: unknown): DesktopV3ArtifactV2Derivative | undefined {
  const row = record(value); const output = record(row?.output); if (!row) return undefined
  const id = text(row.id); const kind = text(row.kind); const status = text(row.status); if (!id || !kind || !status) return undefined
  return { id, kind, status, mediaType: text(output?.media_type), digestSha256: text(output?.digest_sha256), sourcePartId: text(row.source_part_id), sourcePartRevisionId: text(row.source_part_revision_id), captureStateId: text(row.capture_state_id), eventSeq: integer(row.event_seq), diagnostics: Array.isArray(row.diagnostics) ? row.diagnostics.flatMap((item) => diagnostic(item) ?? []) : [] }
}
function normalizeIteration(value: unknown): DesktopV3ArtifactV2Iteration | undefined {
  const row = record(value); if (!row) return undefined
  const id = text(row.id); const artifactId = text(row.artifact_id); const status = text(row.status) as DesktopV3ArtifactV2IterationState
  if (!id || !artifactId || !['open', 'generating', 'awaiting_selection', 'selected', 'closed_without_selection', 'cancelled'].includes(status)) return undefined
  const candidates = Array.isArray(row.candidates) ? row.candidates.flatMap((value): DesktopV3ArtifactV2IterationCandidate[] => { const candidate = record(value); const slotId = text(candidate?.slot_id); return slotId ? [{ slotId, compositionId: text(candidate?.composition_id), status: text(candidate?.status), failureCode: text(candidate?.failure_code), eventSeq: integer(candidate?.event_seq) }] : [] }) : []
  return { id, artifactId, baseCompositionId: text(row.base_composition_id), baseCompositionDigest: text(row.base_composition_digest), targetPartIds: Array.isArray(row.target_part_ids) ? row.target_part_ids.map(text).filter(Boolean) : [], requestedCandidates: integer(row.requested_candidates), status, candidates, selectedSlotId: text(row.selected_slot_id), revision: integer(row.revision), eventSeq: integer(row.event_seq), createdAt: integer(row.created_at), updatedAt: integer(row.updated_at) }
}
function normalizePublished(value: unknown): DesktopV3ArtifactV2PublishedHead | undefined {
  const row = record(value); if (!row) return undefined
  const id = text(row.id); const artifactId = text(row.artifact_id); const compositionId = text(row.composition_id); if (!id || !artifactId || !compositionId) return undefined
  return { id, artifactId, compositionId, compositionDigest: text(row.composition_digest), buildId: text(row.build_id), validationId: text(row.validation_id), policyRevision: text(row.policy_revision), previousHeadId: text(row.previous_head_id), authorizingActor: text(row.authorizing_actor), revision: integer(row.revision), eventSeq: integer(row.event_seq), createdAt: integer(row.created_at) }
}
function list<T>(value: unknown, normalize: (value: unknown) => T | undefined): T[] { return Array.isArray(value) ? value.flatMap((item) => normalize(item) ?? []) : [] }
export function normalizeDesktopV3ArtifactV2Studio(value: unknown): DesktopV3ArtifactV2Studio | null {
  const row = record(value); const normalizedWorking = working(row?.working); const normalizedProjection = projection(row?.projection)
  if (row?.schema_version !== 1 || row.read_only !== true || !normalizedWorking || !normalizedProjection) return null
  return { schemaVersion: 1, readOnly: true, working: normalizedWorking, projection: normalizedProjection, parts: list(row.parts, normalizePart), partRevisions: list(row.part_revisions, normalizePartRevision), compositions: list(row.compositions, normalizeComposition), builds: list(row.builds, (item) => normalizeEvidence<DesktopV3ArtifactV2Build>(item, false)), validations: list(row.validations, (item) => normalizeEvidence<DesktopV3ArtifactV2Validation>(item, true)), derivatives: list(row.derivatives, normalizeDerivative), iterations: list(row.iterations, normalizeIteration), publishedHeads: list(row.published_heads, normalizePublished) }
}

export function desktopV3ArtifactV2PreviewEndpoint(sessionId: string, artifactId: string): string { return `${desktopV3ArtifactV2Endpoint(sessionId, artifactId)}/preview` }

export function desktopV3ArtifactV2Endpoint(sessionId: string, artifactId = ''): string {
  const session = sessionId.trim(); const artifact = artifactId.trim(); if (!session) throw new Error('Artifact V2 requires a session ID')
  return `/v3/sessions/${encodeURIComponent(session)}/artifact-v2${artifact ? `/${encodeURIComponent(artifact)}` : ''}`
}
export async function fetchDesktopV3ArtifactV2Catalog(sessionId: string, signal?: AbortSignal): Promise<DesktopV3ArtifactV2CatalogItem[]> {
  const response = await apiFetch(desktopV3ArtifactV2Endpoint(sessionId), { method: 'GET', signal }); if (!response.ok) throw new Error(await readErrorMessage(response))
  const payload = record(await response.json() as unknown); if (payload?.ok !== true || !Array.isArray(payload.artifacts)) throw new Error('Artifact V2 catalog returned an invalid response')
  return payload.artifacts.map(normalizeDesktopV3ArtifactV2CatalogItem).filter((item): item is DesktopV3ArtifactV2CatalogItem => item !== null)
}
export async function fetchDesktopV3ArtifactV2Studio(sessionId: string, artifactId: string, signal?: AbortSignal): Promise<DesktopV3ArtifactV2Studio> {
  const response = await apiFetch(desktopV3ArtifactV2Endpoint(sessionId, artifactId), { method: 'GET', signal }); if (!response.ok) throw new Error(await readErrorMessage(response))
  const payload = record(await response.json() as unknown); const studio = normalizeDesktopV3ArtifactV2Studio(payload?.artifact); if (payload?.ok !== true || !studio) throw new Error('Artifact V2 Studio returned an invalid response')
  return studio
}
export function desktopV3ArtifactV2StateLabel(state: DesktopV3ArtifactV2State): string {
  return ({ allocated: 'Working', authoring: 'Working', building: 'Building', validating: 'Validating', invalid: 'Needs repair', ready: 'Ready', iterating: 'Iterating', published_view: 'Published', cancelled: 'Cancelled' } satisfies Record<DesktopV3ArtifactV2State, string>)[state]
}
