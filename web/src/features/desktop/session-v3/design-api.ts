import { apiFetch, readErrorMessage } from '../../../app/api'
import type { MessageSnapshot } from '../state/desktop-v3-cache-types'

export interface DesignEditRequest { messageId: string; clientRequestId: string; base: DesignRef; brief: string }
export function designEditRequests(messages: readonly MessageSnapshot[]): DesignEditRequest[] {
  return messages.flatMap(message => {
    const value = message.metadata?.design_edit_request as { client_request_id?: string; base?: DesignRef; state?: string } | undefined
    if (message.role !== 'user' || value?.state !== 'requested' || !value.client_request_id || !value.base?.artifact_id || !value.base.sha256 || !Number.isInteger(value.base.revision)) return []
    return [{ messageId: message.id, clientRequestId: value.client_request_id, base: value.base, brief: message.content }]
  })
}
export interface DesignEditPage { edits: DesignEditRequest[]; nextBefore: number }
export async function fetchDesignEdits(session: string, before = 0, signal?: AbortSignal): Promise<DesignEditPage> {
  const query = before ? `before_seq=${before}` : 'tail=true'
  const page = await (await checked(`/v3/sessions/${encodeURIComponent(session)}/messages?limit=100&${query}`, { signal })).json() as { messages?: MessageSnapshot[]; has_more_older?: boolean; next_before_seq?: number }
  if (page.has_more_older && (!Number.isInteger(page.next_before_seq) || (page.next_before_seq ?? 0) <= 0)) throw new Error('Design edit history continuation is missing')
  return { edits: designEditRequests(page.messages ?? []), nextBefore: page.has_more_older ? page.next_before_seq! : 0 }
}

/** Independent design authority; never convert these references to Artifact V3. */
export interface DesignRef { artifact_id: string; revision: number; sha256: string }
export interface DesignPreviewRef {
  output: { request_id: string; candidate: number; attempt: number; child_session_id: string; run_id: string; sha256: string }
  sha256: string
}
export interface DesignAttempt {
  number: number; state: string; reason_code?: string; router_alert?: string; result?: DesignRef
  validation?: { passed: boolean; code: string; preview?: DesignPreviewRef }
}
export interface DesignCandidate {
  archived?: boolean; archive_version?: number
  spec: { artifact_id: string; kind: string; base?: DesignRef; plan_source?: DesignRef }
  state: string; failure_reason?: string; router_alert?: string; attempts?: DesignAttempt[]
}
export interface DesignRequest { id: string; parent_session_id?: string; revision?: number; source_message_id?: string; client_request_id?: string; state: string; candidates: DesignCandidate[] }
export interface ProjectDesign { project_id: string; task_id?: string; attempt_id?: string; title: string; request: DesignRequest & { parent_session_id: string } }
export interface ProjectDesignCatalog { designs: ProjectDesign[]; next_cursor: string }
export interface DesignArtifact { archived?: boolean; archive_version?: number; id: string; kind: string; revision_count: number; selection_version: number; selected?: DesignRef }
export interface DesignRevision { ref: DesignRef; request_id?: string; candidate?: number; kind: string; base?: DesignRef; plan_source?: DesignRef; attempt: DesignAttempt }
export interface DesignHistory { artifact: DesignArtifact; revisions: DesignRevision[] }
export interface DesignCatalog { requests: DesignRequest[]; next_cursor: string }
export const designRefKey = (ref: DesignRef) => JSON.stringify([ref.artifact_id, ref.revision, ref.sha256])
export const designSandbox = ''
export const designDownloadName = (revision: DesignRevision) => `design-r${revision.ref.revision}.${revision.kind === 'plan' ? 'txt' : 'html'}`
const root = (session: string) => `/v3/sessions/${encodeURIComponent(session)}/designs`
const artifactURL = (session: string, artifact: string) => `${root(session)}/artifacts/${encodeURIComponent(artifact)}`
async function checked(url: string, init?: RequestInit) {
  const response = await apiFetch(url, { ...init, cache: 'no-store' })
  if (!response.ok) throw new Error(`${response.status}: ${await readErrorMessage(response)}`)
  return response
}
export async function fetchProjectDesigns(project: string, after = '', signal?: AbortSignal, view: 'active' | 'archived' = 'active'): Promise<ProjectDesignCatalog> {
  const value = await (await checked(`/v3/projects/${encodeURIComponent(project)}/designs?view=${view}&limit=20&after=${encodeURIComponent(after)}`, { signal })).json() as ProjectDesignCatalog
  return { ...value, designs: value.designs ?? [] }
}
export async function fetchDesignCatalog(session: string, after = '', signal?: AbortSignal): Promise<DesignCatalog> {
  const value = await (await checked(`${root(session)}?limit=20&after=${encodeURIComponent(after)}`, { signal })).json() as DesignCatalog
  return { ...value, requests: value.requests ?? [] }
}
export async function fetchDesignHistory(session: string, artifact: string, after = 0, signal?: AbortSignal): Promise<DesignHistory> {
  const value = await (await checked(`${artifactURL(session, artifact)}?after=${after}`, { signal })).json() as DesignHistory
  return { ...value, revisions: value.revisions ?? [] }
}
export function designEditBody(ref: DesignRef, brief: string, key: string) {
  return { action: 'edit', ref: { ...ref }, brief, idempotency_key: key }
}
export function designSelectionBody(ref: DesignRef, artifact: DesignArtifact, key: string) {
  return { action: 'select', ref: { ...ref }, expected_version: artifact.selection_version, expected_current: artifact.selected ?? null, idempotency_key: key }
}
export async function postDesign(session: string, ref: DesignRef, body: object, signal?: AbortSignal) {
  return checked(artifactURL(session, ref.artifact_id), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body), signal })
}
export async function setDesignArchived(session: string, ref: DesignRef, expectedVersion: number, archived: boolean, key: string, signal?: AbortSignal): Promise<DesignArtifact> {
  const response = await postDesign(session, ref, { action: archived ? 'archive' : 'restore', ref, expected_version: expectedVersion, idempotency_key: key }, signal)
  const { artifact } = await response.json() as { artifact: DesignArtifact }
  if (artifact.id !== ref.artifact_id || Boolean(artifact.archived) !== archived || typeof artifact.archive_version !== 'number') throw new Error('Invalid design archive receipt')
  return artifact
}
export async function fetchDesignView(session: string, revision: DesignRevision, signal?: AbortSignal) {
  // Only the server-authored PNG wrapper may enter srcDoc. Authored HTML is download-only.
  const action = revision.kind === 'plan' ? 'read' : 'preview_html'
  return (await postDesign(session, revision.ref, { action, ref: revision.ref }, signal)).text()
}
