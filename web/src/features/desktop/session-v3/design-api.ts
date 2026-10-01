import { apiFetch, readErrorMessage } from '../../../app/api'

/** Independent design authority; never convert these references to Artifact V3. */
export interface DesignRef { artifact_id: string; revision: number; sha256: string }
export interface DesignPreviewRef {
  output: { request_id: string; candidate: number; attempt: number; child_session_id: string; run_id: string; sha256: string }
  sha256: string
}
export interface DesignAttempt {
  number: number; state: string; reason_code?: string; result?: DesignRef
  validation?: { passed: boolean; code: string; preview?: DesignPreviewRef }
}
export interface DesignCandidate {
  spec: { artifact_id: string; kind: string; base?: DesignRef; plan_source?: DesignRef }
  state: string; failure_reason?: string; router_alert?: string; attempts?: DesignAttempt[]
}
export interface DesignRequest { id: string; state: string; candidates: DesignCandidate[] }
export interface DesignArtifact { id: string; kind: string; revision_count: number; selection_version: number; selected?: DesignRef }
export interface DesignRevision { ref: DesignRef; kind: string; base?: DesignRef; plan_source?: DesignRef; attempt: DesignAttempt }
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
export async function fetchDesignCatalog(session: string, after = ''): Promise<DesignCatalog> {
  const value = await (await checked(`${root(session)}?limit=20&after=${encodeURIComponent(after)}`)).json() as DesignCatalog
  return { ...value, requests: value.requests ?? [] }
}
export async function fetchDesignHistory(session: string, artifact: string, after = 0): Promise<DesignHistory> {
  const value = await (await checked(`${artifactURL(session, artifact)}?after=${after}`)).json() as DesignHistory
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
export async function fetchDesignView(session: string, revision: DesignRevision, signal?: AbortSignal) {
  // Only the server-authored PNG wrapper may enter srcDoc. Authored HTML is download-only.
  const action = revision.kind === 'plan' ? 'read' : 'preview_html'
  return (await postDesign(session, revision.ref, { action, ref: revision.ref }, signal)).text()
}
