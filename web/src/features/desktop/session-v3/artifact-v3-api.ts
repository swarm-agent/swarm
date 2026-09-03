import { apiFetch, readErrorMessage } from '../../../app/api'

export type DesktopV3NativeArtifactStatus = 'working' | 'ready' | 'failed' | 'unavailable'
export type DesktopV3NativeTurnStatus = 'working' | 'building' | 'validating' | 'awaiting_selection' | 'selected' | 'failed' | 'cancelled'

export interface DesktopV3NativeArtifactHead {
  revisionRef: string
  commitOid: string
  treeOid: string
  generation: number
  selectedEventSeq: number
}

export interface DesktopV3NativeArtifactSummary {
  artifactId: string
  artifactRef: string
  ownerSessionId: string
  label: string
  description: string
  status: DesktopV3NativeArtifactStatus
  head: DesktopV3NativeArtifactHead | null
  partCount: number
  turnCount: number
  updatedAt: number
}

export interface DesktopV3NativeArtifactPart {
  id: string
  label: string
  description: string
  locator: {
    kind: 'file' | 'selector' | 'state' | 'semantic'
    path: string
    value: string
    paths: string[]
  }
}

export interface DesktopV3NativeArtifactChangedFile {
  path: string
  status: 'added' | 'modified' | 'deleted' | 'renamed'
  previousPath: string
  additions: number
  deletions: number
  affectedPartIds: string[]
  shared: boolean
}

export interface DesktopV3NativeArtifactDiagnostic {
  id: string
  severity: 'info' | 'warning' | 'error'
  phase: string
  code: string
  message: string
  path: string
  line: number
  column: number
  partIds: string[]
}

export interface DesktopV3NativeArtifactRevision {
  revisionRef: string
  commitOid: string
  treeOid: string
  manifestBlobOid: string
  parentCommitOids: string[]
  status: DesktopV3NativeArtifactStatus
  createdAt: number
  turnId: string
  candidateId: string
  previewRef: string
  buildId: string
  validationId: string
  changedFiles: DesktopV3NativeArtifactChangedFile[]
  affectedPartIds: string[]
  diagnostics: DesktopV3NativeArtifactDiagnostic[]
}

export interface DesktopV3NativeArtifactCandidate {
  candidateId: string
  candidateRef: string
  status: DesktopV3NativeTurnStatus
  revision: DesktopV3NativeArtifactRevision | null
  selected: boolean
  diagnostics: DesktopV3NativeArtifactDiagnostic[]
}

export interface DesktopV3NativeArtifactTurn {
  turnId: string
  turnRef: string
  status: DesktopV3NativeTurnStatus
  baseRevisionRef: string
  baseCommitOid: string
  targetPartIds: string[]
  candidates: DesktopV3NativeArtifactCandidate[]
  createdAt: number
}

export interface DesktopV3NativeArtifactStudio {
  artifact: DesktopV3NativeArtifactSummary
  parts: DesktopV3NativeArtifactPart[]
  turns: DesktopV3NativeArtifactTurn[]
  revisions: DesktopV3NativeArtifactRevision[]
}

export interface DesktopV3NativeArtifactRevisionPage {
  revisions: DesktopV3NativeArtifactRevision[]
  nextCursor: string
}

type JsonRecord = Record<string, unknown>

function record(value: unknown): JsonRecord | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as JsonRecord : null
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : 0
}

function strings(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return [...new Set(value.map(stringValue).filter(Boolean))]
}

function field(source: JsonRecord | null, snake: string, camel: string): unknown {
  return source?.[snake] ?? source?.[camel]
}

function statusValue(value: unknown): DesktopV3NativeArtifactStatus {
  const status = stringValue(value)
  if (status === 'working' || status === 'ready' || status === 'failed' || status === 'unavailable') return status
  return 'working'
}

function turnStatusValue(value: unknown): DesktopV3NativeTurnStatus {
  const status = stringValue(value)
  if (status === 'working' || status === 'building' || status === 'validating' || status === 'awaiting_selection' || status === 'selected' || status === 'failed' || status === 'cancelled') return status
  return 'working'
}

function normalizeHead(value: unknown): DesktopV3NativeArtifactHead | null {
  const item = record(value)
  if (!item) return null
  const revisionRef = stringValue(field(item, 'revision_ref', 'revisionRef'))
  const commitOid = stringValue(field(item, 'commit_oid', 'commitOid'))
  if (!revisionRef || !commitOid) return null
  return {
    revisionRef,
    commitOid,
    treeOid: stringValue(field(item, 'tree_oid', 'treeOid')),
    generation: numberValue(item.generation),
    selectedEventSeq: numberValue(field(item, 'selected_event_seq', 'selectedEventSeq')),
  }
}

export function normalizeDesktopV3NativeArtifactSummary(value: unknown, fallbackSessionId = ''): DesktopV3NativeArtifactSummary | null {
  const item = record(value)
  if (!item) return null
  const artifactId = stringValue(field(item, 'artifact_id', 'artifactId')) || stringValue(item.id)
  const artifactRef = stringValue(field(item, 'artifact_ref', 'artifactRef'))
  const ownerSessionId = stringValue(field(item, 'owner_session_id', 'ownerSessionId')) || stringValue(field(item, 'session_id', 'sessionId')) || fallbackSessionId.trim()
  if (!artifactId || !artifactRef || !ownerSessionId) return null
  return {
    artifactId,
    artifactRef,
    ownerSessionId,
    label: stringValue(item.label) || 'Artifact',
    description: stringValue(item.description),
    status: statusValue(item.status),
    head: normalizeHead(item.head),
    partCount: numberValue(field(item, 'part_count', 'partCount')),
    turnCount: numberValue(field(item, 'turn_count', 'turnCount')),
    updatedAt: numberValue(field(item, 'updated_at', 'updatedAt')),
  }
}

function normalizePart(value: unknown): DesktopV3NativeArtifactPart | null {
  const item = record(value)
  const locator = record(item?.locator)
  const id = stringValue(item?.id)
  const label = stringValue(item?.label)
  const kind = stringValue(locator?.kind)
  if (!id || !label || !['file', 'selector', 'state', 'semantic'].includes(kind)) return null
  return {
    id,
    label,
    description: stringValue(item?.description),
    locator: {
      kind: kind as DesktopV3NativeArtifactPart['locator']['kind'],
      path: stringValue(locator?.path),
      value: stringValue(locator?.value) || stringValue(field(locator, 'state_id', 'stateId')),
      paths: strings(locator?.paths),
    },
  }
}

function normalizeChangedFile(value: unknown): DesktopV3NativeArtifactChangedFile | null {
  const item = record(value)
  const path = stringValue(item?.path)
  const rawStatus = stringValue(item?.status)
  const status = ['added', 'modified', 'deleted', 'renamed'].includes(rawStatus) ? rawStatus as DesktopV3NativeArtifactChangedFile['status'] : 'modified'
  if (!path) return null
  return {
    path,
    status,
    previousPath: stringValue(field(item, 'previous_path', 'previousPath')),
    additions: numberValue(item?.additions),
    deletions: numberValue(item?.deletions),
    affectedPartIds: strings(field(item, 'affected_part_ids', 'affectedPartIds')),
    shared: item?.shared === true,
  }
}

function normalizeDiagnostic(value: unknown): DesktopV3NativeArtifactDiagnostic | null {
  const item = record(value)
  const code = stringValue(item?.code)
  const message = stringValue(item?.message) || stringValue(field(item, 'safe_message', 'safeMessage'))
  if (!code || !message) return null
  const rawSeverity = stringValue(item?.severity)
  return {
    id: stringValue(item?.id) || `${code}:${stringValue(item?.path)}:${numberValue(item?.line)}`,
    severity: rawSeverity === 'error' || rawSeverity === 'warning' ? rawSeverity : 'info',
    phase: stringValue(item?.phase),
    code,
    message,
    path: stringValue(item?.path),
    line: numberValue(item?.line),
    column: numberValue(item?.column),
    partIds: strings(field(item, 'part_ids', 'partIds')),
  }
}

export function normalizeDesktopV3NativeArtifactRevision(value: unknown): DesktopV3NativeArtifactRevision | null {
  const item = record(value)
  if (!item) return null
  const revisionRef = stringValue(field(item, 'revision_ref', 'revisionRef'))
  const commitOid = stringValue(field(item, 'commit_oid', 'commitOid'))
  if (!revisionRef || !commitOid) return null
  return {
    revisionRef,
    commitOid,
    treeOid: stringValue(field(item, 'tree_oid', 'treeOid')),
    manifestBlobOid: stringValue(field(item, 'manifest_blob_oid', 'manifestBlobOid')),
    parentCommitOids: strings(field(item, 'parent_commit_oids', 'parentCommitOids')),
    status: statusValue(item.status),
    createdAt: numberValue(field(item, 'created_at', 'createdAt')),
    turnId: stringValue(field(item, 'turn_id', 'turnId')),
    candidateId: stringValue(field(item, 'candidate_id', 'candidateId')),
    previewRef: stringValue(field(item, 'preview_ref', 'previewRef')),
    buildId: stringValue(field(item, 'build_id', 'buildId')),
    validationId: stringValue(field(item, 'validation_id', 'validationId')),
    changedFiles: Array.isArray(field(item, 'changed_files', 'changedFiles')) ? (field(item, 'changed_files', 'changedFiles') as unknown[]).map(normalizeChangedFile).filter((file): file is DesktopV3NativeArtifactChangedFile => file !== null) : [],
    affectedPartIds: strings(field(item, 'affected_part_ids', 'affectedPartIds')),
    diagnostics: Array.isArray(item.diagnostics) ? item.diagnostics.map(normalizeDiagnostic).filter((diagnostic): diagnostic is DesktopV3NativeArtifactDiagnostic => diagnostic !== null) : [],
  }
}

function normalizeCandidate(value: unknown): DesktopV3NativeArtifactCandidate | null {
  const item = record(value)
  if (!item) return null
  const candidateId = stringValue(field(item, 'candidate_id', 'candidateId')) || stringValue(item?.id)
  const candidateRef = stringValue(field(item, 'candidate_ref', 'candidateRef'))
  if (!candidateId || !candidateRef) return null
  return {
    candidateId,
    candidateRef,
    status: turnStatusValue(item?.status),
    revision: normalizeDesktopV3NativeArtifactRevision(item?.revision ?? item),
    selected: item?.selected === true,
    diagnostics: Array.isArray(item?.diagnostics) ? item.diagnostics.map(normalizeDiagnostic).filter((diagnostic): diagnostic is DesktopV3NativeArtifactDiagnostic => diagnostic !== null) : [],
  }
}

function normalizeTurn(value: unknown): DesktopV3NativeArtifactTurn | null {
  const item = record(value)
  if (!item) return null
  const turnId = stringValue(field(item, 'turn_id', 'turnId')) || stringValue(item?.id)
  const turnRef = stringValue(field(item, 'turn_ref', 'turnRef'))
  if (!turnId || !turnRef) return null
  return {
    turnId,
    turnRef,
    status: turnStatusValue(item?.status),
    baseRevisionRef: stringValue(field(item, 'base_revision_ref', 'baseRevisionRef')),
    baseCommitOid: stringValue(field(item, 'base_commit_oid', 'baseCommitOid')),
    targetPartIds: strings(field(item, 'target_part_ids', 'targetPartIds')),
    candidates: Array.isArray(item?.candidates) ? item.candidates.map(normalizeCandidate).filter((candidate): candidate is DesktopV3NativeArtifactCandidate => candidate !== null) : [],
    createdAt: numberValue(field(item, 'created_at', 'createdAt')),
  }
}

export function desktopV3NativeArtifactCatalogEndpoint(sessionId: string): string {
  const session = sessionId.trim()
  if (!session) throw new Error('Artifact V3 catalog requires a session ID')
  return `/v3/sessions/${encodeURIComponent(session)}/artifacts-v3`
}

export function desktopV3NativeArtifactEndpoint(sessionId: string, artifactId: string): string {
  const artifact = artifactId.trim()
  if (!artifact) throw new Error('Artifact V3 request requires an artifact ID')
  return `${desktopV3NativeArtifactCatalogEndpoint(sessionId)}/${encodeURIComponent(artifact)}`
}

export async function fetchDesktopV3NativeArtifactCatalog(sessionId: string, signal?: AbortSignal): Promise<DesktopV3NativeArtifactSummary[]> {
  const response = await apiFetch(desktopV3NativeArtifactCatalogEndpoint(sessionId), { method: 'GET', signal })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  const payload = record(await response.json() as unknown)
  const items = payload?.artifacts
  if (payload?.ok !== true || !Array.isArray(items)) throw new Error('Artifact V3 catalog returned an invalid response')
  return items.map((item) => normalizeDesktopV3NativeArtifactSummary(item, sessionId)).filter((item): item is DesktopV3NativeArtifactSummary => item !== null)
}

export async function fetchDesktopV3NativeArtifactRevisions(sessionId: string, artifactId: string, cursor = '', signal?: AbortSignal): Promise<DesktopV3NativeArtifactRevisionPage> {
  const search = new URLSearchParams({ limit: '100' })
  if (cursor.trim()) search.set('cursor', cursor.trim())
  const response = await apiFetch(`${desktopV3NativeArtifactEndpoint(sessionId, artifactId)}/revisions?${search.toString()}`, { method: 'GET', signal })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  const payload = record(await response.json() as unknown)
  if (payload?.ok !== true || !Array.isArray(payload.revisions)) throw new Error('Artifact V3 revision history returned an invalid response')
  return {
    revisions: payload.revisions.map(normalizeDesktopV3NativeArtifactRevision).filter((revision): revision is DesktopV3NativeArtifactRevision => revision !== null),
    nextCursor: stringValue(field(payload, 'next_cursor', 'nextCursor')),
  }
}

export async function fetchDesktopV3NativeArtifactStudio(sessionId: string, artifactId: string, signal?: AbortSignal): Promise<DesktopV3NativeArtifactStudio> {
  const [detailResponse, revisionPage] = await Promise.all([
    apiFetch(desktopV3NativeArtifactEndpoint(sessionId, artifactId), { method: 'GET', signal }),
    fetchDesktopV3NativeArtifactRevisions(sessionId, artifactId, '', signal),
  ])
  if (!detailResponse.ok) throw new Error(await readErrorMessage(detailResponse))
  const payload = record(await detailResponse.json() as unknown)
  const artifact = normalizeDesktopV3NativeArtifactSummary(payload?.artifact ?? payload, sessionId)
  if (payload?.ok !== true || !artifact) throw new Error('Artifact V3 Studio returned an invalid response')
  return {
    artifact,
    parts: Array.isArray(payload.parts) ? payload.parts.map(normalizePart).filter((part): part is DesktopV3NativeArtifactPart => part !== null) : [],
    turns: Array.isArray(payload.turns) ? payload.turns.map(normalizeTurn).filter((turn): turn is DesktopV3NativeArtifactTurn => turn !== null) : [],
    revisions: revisionPage.revisions,
  }
}

export function desktopV3NativeArtifactPreviewEndpoint(sessionId: string, artifactId: string, revisionRef: string): string {
  const revision = revisionRef.trim()
  if (!revision) throw new Error('Artifact V3 preview requires an exact revision reference')
  const search = new URLSearchParams({ revision })
  return `${desktopV3NativeArtifactEndpoint(sessionId, artifactId)}/preview?${search.toString()}`
}

export async function preflightDesktopV3NativeArtifactPreview(sessionId: string, artifactId: string, revisionRef: string, signal?: AbortSignal): Promise<string> {
  const url = desktopV3NativeArtifactPreviewEndpoint(sessionId, artifactId, revisionRef)
  const response = await apiFetch(url, { method: 'GET', headers: { Accept: 'text/html' }, signal })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  await response.body?.cancel()
  return url
}

export function desktopV3NativeArtifactCandidateSelectionEndpoint(sessionId: string, artifactId: string, turnId: string): string {
  const turn = turnId.trim()
  if (!turn) throw new Error('Artifact V3 selection requires a turn ID')
  return `${desktopV3NativeArtifactEndpoint(sessionId, artifactId)}/turns/${encodeURIComponent(turn)}/select`
}

export async function selectDesktopV3NativeArtifactCandidate(input: {
  sessionId: string
  artifactId: string
  turnId: string
  candidateRef: string
  expectedHead: DesktopV3NativeArtifactHead | null
}, signal?: AbortSignal): Promise<DesktopV3NativeArtifactHead> {
  if (!input.candidateRef.trim()) throw new Error('Artifact V3 selection requires an exact candidate reference')
  const response = await apiFetch(desktopV3NativeArtifactCandidateSelectionEndpoint(input.sessionId, input.artifactId, input.turnId), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      client_request_id: `desktop-artifact-v3-select:${crypto.randomUUID()}`,
      candidate_ref: input.candidateRef,
      expected_head_generation: input.expectedHead?.generation ?? 0,
      expected_head_commit_oid: input.expectedHead?.commitOid ?? '',
    }),
    signal,
  })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  const payload = record(await response.json() as unknown)
  const head = normalizeHead(payload?.head)
  if (payload?.ok !== true || !head) throw new Error('Artifact V3 selection returned an invalid head')
  return head
}

export function desktopV3NativeArtifactIterationPrompt(studio: DesktopV3NativeArtifactStudio, partIds: readonly string[]): string {
  const head = studio.artifact.head
  if (!head) throw new Error('Artifact V3 iteration requires an exact current head')
  const normalizedPartIds = [...new Set(partIds.map((partId) => partId.trim()).filter((partId) => studio.parts.some((part) => part.id === partId)))]
  const targets = normalizedPartIds.length
    ? normalizedPartIds.map((partId) => studio.parts.find((part) => part.id === partId)?.label || partId).join(', ')
    : 'the complete artifact'
  return [
    `Iterate ${targets} in the existing Artifact V3 project.`,
    `Use exact artifact reference: ${studio.artifact.artifactRef}`,
    `Start from exact revision reference: ${head.revisionRef}`,
    normalizedPartIds.length ? `Target part IDs (intent only): ${normalizedPartIds.join(', ')}` : 'Target: whole artifact.',
    'Keep the complete project tree available, make any necessary shared-file or cross-part changes, and build, preview, and repair the whole candidate before finishing.',
  ].join('\n')
}
