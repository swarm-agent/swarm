import { apiFetch, readErrorMessage } from '../../../app/api'

export type DesktopV3NativeArtifactStatus = 'working' | 'ready' | 'failed' | 'unavailable'
export type DesktopV3NativeTurnStatus = 'working' | 'building' | 'validating' | 'ready' | 'awaiting_selection' | 'selected' | 'failed' | 'cancelled'

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
  pendingTurns?: DesktopV3NativeArtifactTurn[]
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
  parts?: DesktopV3NativeArtifactPart[]
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
  status: DesktopV3NativeTurnStatus
  revision: DesktopV3NativeArtifactRevision | null
  selected: boolean
  diagnostics: DesktopV3NativeArtifactDiagnostic[]
}

export interface DesktopV3NativeArtifactTurn {
  turnId: string
  turnRef: string
  revision: number
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

function inferredArtifactStatus(item: JsonRecord): DesktopV3NativeArtifactStatus {
  const explicit = statusValue(item.status)
  if (stringValue(item.status)) return explicit
  const head = record(item.head ?? field(item, 'current_revision', 'currentRevision'))
  const build = record(head?.build)
  const validation = record(head?.validation)
  return stringValue(build?.status) === 'succeeded' && stringValue(validation?.status) === 'valid' ? 'ready' : head ? 'working' : explicit
}

function turnStatusValue(value: unknown): DesktopV3NativeTurnStatus {
  const status = stringValue(value)
  if (status === 'working' || status === 'building' || status === 'validating' || status === 'ready' || status === 'awaiting_selection' || status === 'selected' || status === 'failed' || status === 'cancelled') return status
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
    generation: numberValue(item.generation) || numberValue(item.revision),
    selectedEventSeq: numberValue(field(item, 'selected_event_seq', 'selectedEventSeq')) || numberValue(item.revision),
  }
}

export function normalizeDesktopV3NativeArtifactSummary(value: unknown, fallbackSessionId = ''): DesktopV3NativeArtifactSummary | null {
  const item = record(value)
  if (!item) return null
  const artifactId = stringValue(field(item, 'artifact_id', 'artifactId')) || stringValue(item.id)
  const ownerSessionId = stringValue(field(item, 'owner_session_id', 'ownerSessionId')) || stringValue(field(item, 'session_id', 'sessionId')) || fallbackSessionId.trim()
  if (!artifactId || !ownerSessionId) return null
  return {
    artifactId,
    // The native HTTP contract currently identifies the artifact by its stable ID.
    // Preserve an explicit artifact_ref when the service supplies one, otherwise use
    // that canonical ID instead of dropping the complete catalog entry.
    artifactRef: stringValue(field(item, 'artifact_ref', 'artifactRef')) || artifactId,
    ownerSessionId,
    label: stringValue(item.label) || stringValue(item.title) || 'Untitled artifact',
    description: stringValue(item.description),
    status: inferredArtifactStatus(item),
    head: (() => {
      const head = normalizeHead(item.head) || normalizeHead(field(item, 'current_revision', 'currentRevision'))
      if (!head) return null
      const revision = numberValue(item.revision)
      return { ...head, generation: head.generation || revision, selectedEventSeq: head.selectedEventSeq || revision }
    })(),
    partCount: numberValue(field(item, 'part_count', 'partCount')) || (Array.isArray(item.parts) ? item.parts.length : 0),
    turnCount: numberValue(field(item, 'turn_count', 'turnCount')) || (Array.isArray(item.turns) ? item.turns.length : 0),
    pendingTurns: pendingDesktopV3NativeArtifactTurns(Array.isArray(item.turns) ? item.turns.map(normalizeTurn).filter((turn): turn is DesktopV3NativeArtifactTurn => turn !== null) : []),
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
  const build = record(item.build)
  const validation = record(item.validation)
  const rawChangedFiles = field(item, 'changed_files', 'changedFiles')
  const changedFiles = Array.isArray(rawChangedFiles)
    ? rawChangedFiles.map((entry) => typeof entry === 'string'
      ? normalizeChangedFile({ path: entry, status: 'modified' })
      : normalizeChangedFile(entry)).filter((file): file is DesktopV3NativeArtifactChangedFile => file !== null)
    : []
  const buildDiagnostics = build?.diagnostics
  const validationDiagnostics = validation?.diagnostics
  const rawDiagnostics = [
    ...(Array.isArray(item.diagnostics) ? item.diagnostics : []),
    ...(Array.isArray(buildDiagnostics) ? buildDiagnostics : []),
    ...(Array.isArray(validationDiagnostics) ? validationDiagnostics : []),
  ]
  const explicitStatus = stringValue(item.status)
  const inferredStatus = explicitStatus || (stringValue(validation?.status) === 'valid' && stringValue(build?.status) === 'succeeded' ? 'ready' : 'working')
  return {
    revisionRef,
    commitOid,
    treeOid: stringValue(field(item, 'tree_oid', 'treeOid')),
    manifestBlobOid: stringValue(field(item, 'manifest_blob_oid', 'manifestBlobOid')),
    parts: (() => {
      const parts = item.parts ?? record(item.manifest)?.parts
      return Array.isArray(parts) ? parts.map(normalizePart).filter((part): part is DesktopV3NativeArtifactPart => part !== null) : undefined
    })(),
    parentCommitOids: strings(field(item, 'parent_commit_oids', 'parentCommitOids')).length
      ? strings(field(item, 'parent_commit_oids', 'parentCommitOids'))
      : strings(item.parents),
    status: statusValue(inferredStatus),
    createdAt: numberValue(field(item, 'created_at', 'createdAt')),
    turnId: stringValue(field(item, 'turn_id', 'turnId')),
    candidateId: stringValue(field(item, 'candidate_id', 'candidateId')),
    previewRef: stringValue(field(item, 'preview_ref', 'previewRef')) || revisionRef,
    buildId: stringValue(field(item, 'build_id', 'buildId')) || stringValue(build?.id),
    validationId: stringValue(field(item, 'validation_id', 'validationId')) || stringValue(validation?.id),
    changedFiles,
    affectedPartIds: strings(field(item, 'affected_part_ids', 'affectedPartIds')).length
      ? strings(field(item, 'affected_part_ids', 'affectedPartIds'))
      : strings(field(item, 'changed_parts', 'changedParts')),
    diagnostics: rawDiagnostics.map(normalizeDiagnostic).filter((diagnostic): diagnostic is DesktopV3NativeArtifactDiagnostic => diagnostic !== null),
  }
}

function normalizeCandidate(value: unknown): DesktopV3NativeArtifactCandidate | null {
  const item = record(value)
  if (!item) return null
  const candidateId = stringValue(field(item, 'candidate_id', 'candidateId')) || stringValue(item?.id)
  if (!candidateId) return null
  const build = record(item.build)
  const validation = record(item.validation)
  const buildDiagnostics = build?.diagnostics
  const validationDiagnostics = validation?.diagnostics
  return {
    candidateId,
    status: turnStatusValue(item.status),
    revision: normalizeDesktopV3NativeArtifactRevision(item.revision ?? item),
    selected: item.selected === true,
    diagnostics: [
      ...(Array.isArray(item.diagnostics) ? item.diagnostics : []),
      ...(Array.isArray(buildDiagnostics) ? buildDiagnostics : []),
      ...(Array.isArray(validationDiagnostics) ? validationDiagnostics : []),
    ].map(normalizeDiagnostic).filter((diagnostic): diagnostic is DesktopV3NativeArtifactDiagnostic => diagnostic !== null),
  }
}

function normalizeTurn(value: unknown): DesktopV3NativeArtifactTurn | null {
  const item = record(value)
  if (!item) return null
  const turnId = stringValue(field(item, 'turn_id', 'turnId')) || stringValue(item?.id)
  if (!turnId) return null
  const baseRevision = record(field(item, 'base_revision', 'baseRevision'))
  const selectedCandidateId = stringValue(field(item, 'selected_candidate_id', 'selectedCandidateId'))
  const candidates = Array.isArray(item.candidates)
    ? item.candidates.map(normalizeCandidate).filter((candidate): candidate is DesktopV3NativeArtifactCandidate => candidate !== null)
      .map((candidate) => selectedCandidateId && candidate.candidateId === selectedCandidateId ? { ...candidate, selected: true } : candidate)
    : []
  return {
    turnId,
    turnRef: stringValue(field(item, 'turn_ref', 'turnRef')) || turnId,
    revision: numberValue(item.revision),
    status: turnStatusValue(item.status),
    baseRevisionRef: stringValue(field(item, 'base_revision_ref', 'baseRevisionRef')) || stringValue(field(baseRevision, 'revision_ref', 'revisionRef')),
    baseCommitOid: stringValue(field(item, 'base_commit_oid', 'baseCommitOid')),
    targetPartIds: strings(field(item, 'target_part_ids', 'targetPartIds')),
    candidates,
    createdAt: numberValue(field(item, 'created_at', 'createdAt')),
  }
}

// Canonical pending-turn projection shared by catalog display and Studio entry.
// A ready unchosen sibling from an already selected turn is history, not pending.
export function pendingDesktopV3NativeArtifactTurns(turns: readonly DesktopV3NativeArtifactTurn[]): DesktopV3NativeArtifactTurn[] {
  return turns.filter((turn) => ['working', 'building', 'validating', 'ready', 'awaiting_selection'].includes(turn.status) && !turn.candidates.some((candidate) => candidate.selected))
    .sort((a, b) => b.createdAt - a.createdAt || a.turnId.localeCompare(b.turnId))
}

export function firstDesktopV3NativeArtifactPendingCandidate(turns: readonly DesktopV3NativeArtifactTurn[]) {
  for (const turn of pendingDesktopV3NativeArtifactTurns(turns)) {
    const candidate = turn.candidates.find((candidate) => candidate.status === 'ready' && candidate.revision?.status === 'ready' && !candidate.selected)
    if (candidate) return { turn, candidate }
  }
  return null
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
  const detail = record(payload?.artifact ?? payload)
  const artifact = normalizeDesktopV3NativeArtifactSummary(detail, sessionId)
  if (payload?.ok !== true || !artifact || !detail) throw new Error('Artifact V3 Studio returned an invalid response')
  const detailRevisions = Array.isArray(detail.revisions)
    ? detail.revisions.map(normalizeDesktopV3NativeArtifactRevision).filter((revision): revision is DesktopV3NativeArtifactRevision => revision !== null)
    : []
  const turns = Array.isArray(detail.turns) ? detail.turns.map(normalizeTurn).filter((turn): turn is DesktopV3NativeArtifactTurn => turn !== null).sort((a, b) => a.createdAt - b.createdAt || a.turnId.localeCompare(b.turnId)) : []
  // Candidate previews must not depend on whether their commit happens to be
  // present in the first bounded history page. Keep each exact revision intact.
  const revisions = new Map<string, DesktopV3NativeArtifactRevision>()
  for (const revision of [...detailRevisions, ...revisionPage.revisions, ...turns.flatMap((turn) => turn.candidates.flatMap((candidate) => candidate.revision ? [candidate.revision] : []))]) {
    revisions.set(revision.revisionRef, revision)
  }
  return {
    artifact,
    parts: Array.isArray(detail.parts) ? detail.parts.map(normalizePart).filter((part): part is DesktopV3NativeArtifactPart => part !== null) : [],
    turns,
    revisions: [...revisions.values()],
  }
}

export function desktopV3NativeArtifactPreviewEndpoint(sessionId: string, artifactId: string, revisionRef: string): string {
  const revision = revisionRef.trim()
  if (!revision) throw new Error('Artifact V3 preview requires an exact revision reference')
  const search = new URLSearchParams({ revision })
  return `${desktopV3NativeArtifactEndpoint(sessionId, artifactId)}/preview?${search.toString()}`
}

export async function preflightDesktopV3NativeArtifactPreview(sessionId: string, artifactId: string, revisionRef: string, signal?: AbortSignal): Promise<string> {
  const revision = revisionRef.trim()
  if (!revision) throw new Error('Artifact V3 preview requires an exact revision reference')
  const response = await apiFetch(`${desktopV3NativeArtifactEndpoint(sessionId, artifactId)}/preview/access`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ revision_ref: revision }),
    signal,
  })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  const payload = await response.json() as { preview_url?: unknown }
  const previewURL = typeof payload.preview_url === 'string' ? payload.preview_url.trim() : ''
  if (!previewURL.startsWith('/v3/sessions/') || !previewURL.includes('/artifacts-v3/') || !previewURL.includes('/preview/access/')) {
    throw new Error('Artifact V3 preview access response is invalid')
  }
  return previewURL
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
  candidateId: string
  expectedHead: DesktopV3NativeArtifactHead | null
  expectedTurnRevision: number
}, signal?: AbortSignal): Promise<DesktopV3NativeArtifactHead> {
  if (!input.candidateId.trim()) throw new Error('Artifact V3 selection requires a candidate ID')
  if (!input.expectedHead?.revisionRef.trim() || input.expectedTurnRevision <= 0) throw new Error('Artifact V3 selection requires exact head and turn revisions')
  const response = await apiFetch(desktopV3NativeArtifactCandidateSelectionEndpoint(input.sessionId, input.artifactId, input.turnId), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      client_request_id: `desktop-artifact-v3-select-${crypto.randomUUID()}`,
      candidate_id: input.candidateId,
      expected_head_ref: input.expectedHead.revisionRef,
      expected_turn_revision: input.expectedTurnRevision,
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

/** Stage intent through the normal composer envelope; never replace or submit the draft. */
export function desktopV3NativeArtifactIterationSelection(studio: DesktopV3NativeArtifactStudio, partIds: readonly string[]): import('./artifact-api').DesktopV3ArtifactMessageSelection {
  const head = studio.artifact.head
  if (!head || !/^revision-[a-f0-9]{40}$/.test(head.revisionRef) || head.revisionRef !== `revision-${head.commitOid}`) throw new Error('Artifact V3 iteration requires an exact current head')
  const ids = partIds.map((id) => id.trim())
  if (ids.length > 256 || new Set(ids).size !== ids.length || ids.some((id) => !studio.parts.some((part) => part.id === id))) throw new Error('Unknown or duplicate Artifact V3 Part')
  const label = ids.length ? ids.map((id) => studio.parts.find((part) => part.id === id)!.label || id).join(', ') : studio.artifact.label || 'Artifact'
  return {
    session_id: studio.artifact.ownerSessionId,
    artifact_id: studio.artifact.artifactId,
    revision_ref: head.revisionRef,
    target_part_ids: ids,
    label: label.length <= 256 ? label : 'Selected artifact Parts',
    action: 'use',
  }
}
