import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Check, ChevronDown, ChevronRight, Paperclip, RotateCcw } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { requestJson } from '../../../../app/api'
import { DesktopV3ExistingConversationPane } from '../../chat/components/desktop-v3-existing-conversation-pane'
import { isDesktopV3SessionTailReady, selectRenderedSessionMessages, type RenderedSessionMessages } from '../../state/desktop-v3-cache-selectors'
import { useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import type { DesktopV3CacheState } from '../../state/desktop-v3-cache-types'
import { selectAndHydrateDesktopV3Session } from '../../state/desktop-v3-session-hydrator'
import type { DesktopChatRoute } from '../../chat/services/chat-routing'
import type { DesktopV3ArtifactCatalogEntry, DesktopV3ArtifactMessageSelection } from '../../session-v3/artifact-api'

export const VIDEO_TRANSITION_KINDS = ['cut', 'fade_through_black', 'crossfade', 'fade_to_black', 'fade_from_black'] as const
export type VideoTransitionKind = typeof VIDEO_TRANSITION_KINDS[number]

export type VideoTransitionWire = {
  id: string
  kind: VideoTransitionKind
  from_clip_id: string
  to_clip_id: string
  duration_ms?: number
}

export type VideoEditOperationWire = {
  id: string
  type: 'add_clip' | 'update_clip' | 'replace_clip' | 'remove_clip' | 'add_transition' | 'update_transition' | 'remove_transition'
  clip_id?: string
  clip?: Record<string, unknown>
  transition_id?: string
  transition?: VideoTransitionWire
}

export type VideoPlanVisualWire = {
  session_id: string
  collection_id: string
  variant_id: string
  event_seq: number
  label?: string
  description?: string
}

export type VideoPlanPartWire = {
  id: string
  title: string
  duration_ms: number
  narration?: string
  on_screen_text?: string
  visual_direction?: string
  transition_in?: string
  visual?: VideoPlanVisualWire
  visual_media_type?: string
}

export type VideoPlanProposalWire = {
  kind: 'initial' | 'revision'
  summary?: string
  parts: VideoPlanPartWire[]
}

export type VideoEditProposalWire = {
  id: string
  project_id: string
  base_revision_id: string
  base_revision_number: number
  working_revision_id?: string
  working_revision_number?: number
  status: 'pending' | 'accepted' | 'rejected'
  title?: string
  rationale?: string
  plan?: VideoPlanProposalWire
  operations: VideoEditOperationWire[]
  affected_ranges?: Array<{ start_ms: number; end_ms: number }>
  accepted_operation_ids?: string[]
  accepted_revision_id?: string
  rejection_feedback?: string
  created_at: number
  updated_at: number
}

export function videoPlanPartArtifact(part: VideoPlanPartWire): DesktopV3ArtifactCatalogEntry | null {
  if (!part.visual?.session_id || !part.visual.collection_id || !part.visual.variant_id || !part.visual.event_seq) return null
  return {
    artifactId: part.visual.variant_id,
    collectionId: part.visual.collection_id,
    sessionId: part.visual.session_id,
    sessionTitle: '', workspacePath: '', workspaceName: '', planId: '', planTitle: '', checkpointId: '', checkpointTitle: '',
    label: part.visual.label || part.title,
    description: part.visual.description || part.visual_direction || '',
    collectionName: '', collectionDescription: '', filename: part.title, mediaType: part.visual_media_type || 'image/png', kind: 'visual',
    status: 'ready', previewable: true, category: 'visual', updatedAt: 0, eventSeq: part.visual.event_seq,
  }
}

export function videoPlanPartMessageSelection(part: VideoPlanPartWire): DesktopV3ArtifactMessageSelection {
  if (!part.visual?.session_id || !part.visual.collection_id || !part.visual.variant_id || !part.visual.event_seq) throw new Error('Visual plan feedback requires the exact accepted visual')
  return {
    session_id: part.visual.session_id,
    collection_id: part.visual.collection_id,
    variant_id: part.visual.variant_id,
    event_seq: part.visual.event_seq,
    label: part.visual.label || part.title,
    description: part.visual.description || part.visual_direction || undefined,
    action: 'select',
  }
}

export function videoPlanTransitionMessageSelection(part: VideoPlanPartWire, transition?: VideoTransitionWire): DesktopV3ArtifactMessageSelection {
  const selection = videoPlanPartMessageSelection(part)
  const transitionSummary = transition
    ? `${transition.kind}; ${typeof transition.duration_ms === 'number' ? `${Math.max(0, Math.round(transition.duration_ms))}ms` : 'default duration'}; ${transition.from_clip_id} → ${transition.to_clip_id}`
    : `none into ${part.id}`
  return {
    ...selection,
    label: `Transition · ${part.title}`,
    description: `Stable part ${part.id}. Current transition: ${transitionSummary}.`,
  }
}

export function renderedVideoArtifactUrl(sessionId: string, job: { output_artifact?: { collection_id: string; variant_id: string } }): string {
  const reference = job.output_artifact
  if (!reference?.collection_id || !reference.variant_id) return ''
  return `/v3/sessions/${encodeURIComponent(sessionId)}/artifacts/${encodeURIComponent(reference.variant_id)}`
}

export function transitionLabel(kind: VideoTransitionKind): string {
  return ({
    cut: 'Cut',
    fade_through_black: 'Fade through black',
    crossfade: 'Crossfade',
    fade_to_black: 'Fade to black',
    fade_from_black: 'Fade from black',
  })[kind]
}

export function videoProposalProjectionSequence(
  state: Pick<DesktopV3CacheState, 'eventsBySession'>,
  sessionId: string,
): number {
  const events = state.eventsBySession[sessionId] ?? []
  for (let index = events.length - 1; index >= 0; index -= 1) {
    const event = events[index]
    if (event.event_type.startsWith('session.video_project.')) return event.seq
  }
  return 0
}

export async function listVideoEditProposals(sessionId: string, projectId: string): Promise<VideoEditProposalWire[]> {
  const response = await requestJson<{ proposals?: VideoEditProposalWire[] }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects/${encodeURIComponent(projectId)}/edit-proposals`,
  )
  return Array.isArray(response.proposals)
    ? response.proposals.map((proposal) => ({
      ...proposal,
      operations: Array.isArray(proposal.operations) ? proposal.operations : [],
      plan: proposal.plan && Array.isArray(proposal.plan.parts) ? proposal.plan : undefined,
    }))
    : []
}

export async function loadLatestVideoEditProposals(input: {
  sessionId: string
  projectId: string
  requestSequence: { current: number }
  onLoaded: (proposals: VideoEditProposalWire[]) => void
  onError: (error: string | null) => void
  loader?: (sessionId: string, projectId: string) => Promise<VideoEditProposalWire[]>
}): Promise<void> {
  const requestId = ++input.requestSequence.current
  try {
    const proposals = await (input.loader ?? listVideoEditProposals)(input.sessionId, input.projectId)
    if (requestId !== input.requestSequence.current) return
    input.onLoaded(proposals)
    input.onError(null)
  } catch (cause) {
    if (requestId !== input.requestSequence.current) return
    input.onError(cause instanceof Error ? cause.message : String(cause))
  }
}

export async function createVideoEditProposal(input: {
  sessionId: string
  projectId: string
  baseRevisionId: string
  title: string
  rationale?: string
  operations: VideoEditOperationWire[]
  affectedRanges: Array<{ start_ms: number; end_ms: number }>
}): Promise<VideoEditProposalWire> {
  const response = await requestJson<{ proposal?: VideoEditProposalWire }>(`/v3/sessions/${encodeURIComponent(input.sessionId)}/video/projects/${encodeURIComponent(input.projectId)}/edit-proposals`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      base_revision_id: input.baseRevisionId,
      title: input.title,
      rationale: input.rationale,
      operations: input.operations,
      affected_ranges: input.affectedRanges,
    }),
  })
  if (!response.proposal) throw new Error('Video edit proposal response returned no proposal')
  return response.proposal
}

export async function acceptVideoEditProposal(input: {
  sessionId: string
  projectId: string
  proposalId: string
  selectedOperationIds: string[]
  changeSummary?: string
}): Promise<{ proposal: VideoEditProposalWire; revision: unknown; project: unknown }> {
  return requestJson<{ proposal: VideoEditProposalWire; revision: unknown; project: unknown }>(`/v3/sessions/${encodeURIComponent(input.sessionId)}/video/projects/${encodeURIComponent(input.projectId)}/edit-proposals/${encodeURIComponent(input.proposalId)}/accept`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      selected_operation_ids: input.selectedOperationIds,
      change_summary: input.changeSummary?.trim() || `Kept ${input.selectedOperationIds.length || 1} proposed video change${input.selectedOperationIds.length === 1 ? '' : 's'}`,
    }),
  })
}

export async function rejectVideoEditProposal(sessionId: string, projectId: string, proposalId: string, feedback: string): Promise<void> {
  await requestJson(`/v3/sessions/${encodeURIComponent(sessionId)}/video/projects/${encodeURIComponent(projectId)}/edit-proposals/${encodeURIComponent(proposalId)}/reject`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ feedback }),
  })
}

export async function requestVideoRenderCancellation(sessionId: string, jobId: string): Promise<void> {
  await requestJson(`/v3/sessions/${encodeURIComponent(sessionId)}/video/render-jobs/${encodeURIComponent(jobId)}/cancel`, { method: 'POST' })
}

export function videoProposalFocusClipId(proposal: VideoEditProposalWire): string {
  if (proposal.plan?.parts.length) return proposal.plan.parts[proposal.plan.parts.length - 1].id
  for (let index = proposal.operations.length - 1; index >= 0; index -= 1) {
    const operation = proposal.operations[index]
    const clipId = String(operation.clip?.id ?? operation.clip_id ?? operation.transition?.to_clip_id ?? '').trim()
    if (clipId) return clipId
  }
  return ''
}

export type VideoStepEditAction = 'visual' | 'transition' | 'source' | 'move_earlier' | 'move_later'

export function proposedVideoPlanClipDetails(clip: Record<string, unknown>): {
  timing: string
  title: string
  narration: string
  still: string
  onScreenText: string
  kind: 'title' | 'transition' | 'section'
} {
  const parts = String(clip.name ?? '').split(' | ').map((part) => part.trim())
  const heading = parts[0] ?? ''
  const headingParts = heading.split(' — ')
  const title = headingParts.slice(1).join(' — ').trim() || 'Planned section'
  const captions = Array.isArray(clip.captions) ? clip.captions : []
  const onScreenText = captions
    .map((caption) => caption && typeof caption === 'object' ? String((caption as Record<string, unknown>).text ?? '').trim() : '')
    .filter(Boolean)
    .join(' · ')
  const normalizedTitle = title.toLowerCase()
  return {
    timing: headingParts[0]?.trim() || 'Timing not set',
    title,
    narration: (parts.find((part) => part.startsWith('Narration:')) ?? '').replace(/^Narration:\s*/, ''),
    still: (parts.find((part) => part.startsWith('Planned still:')) ?? '').replace(/^Planned still:\s*/, ''),
    onScreenText,
    kind: normalizedTitle.includes('title card') ? 'title' : normalizedTitle.includes('transition still') ? 'transition' : 'section',
  }
}

function operationLabel(operation: VideoEditOperationWire): string {
  if (operation.transition) return `${operation.type.replace(/_/g, ' ')} · ${transitionLabel(operation.transition.kind)} · ${operation.transition.from_clip_id} → ${operation.transition.to_clip_id}`
  if (operation.transition_id) return `${operation.type.replace(/_/g, ' ')} · transition ${operation.transition_id}`
  return `${operation.type.replace(/_/g, ' ')} · ${String(operation.clip?.name || operation.clip?.id || operation.clip_id || 'timeline')}`
}

export function selectedVideoProposalChangeIDs(proposal: VideoEditProposalWire, selected: Record<string, boolean>): string[] {
  if (proposal.plan?.kind === 'initial') return []
  if (proposal.plan) return proposal.plan.parts.filter((part) => selected[`${proposal.id}:part:${part.id}`] !== false).map((part) => part.id)
  return proposal.operations.filter((operation) => selected[`${proposal.id}:${operation.id}`] !== false).map((operation) => operation.id)
}

export type VideoIterationRevisionWire = {
  id: string
  revision_number: number
  parent_revision_id?: string
  origin_proposal_id?: string
  change_summary?: string
  created_at: number
}

export type VideoIterationChange = {
  id: string
  key: string
  label: string
  clipId: string
  startMs: number
  endMs: number
  artifact: VideoPlanVisualWire | null
  planPart?: VideoPlanPartWire
  operation?: VideoEditOperationWire
}

export type VideoIterationEntry = {
  id: string
  proposal: VideoEditProposalWire | null
  parentRevisionId: string
  candidateRevisionId: string
  candidateRevisionNumber?: number
  status: VideoEditProposalWire['status'] | 'revision'
  title: string
  createdAt: number
  changes: VideoIterationChange[]
}

export type VideoIterationComposerContext = {
  proposalId: string
  parentRevisionId: string
  candidateRevisionId: string
  changeId: string
  anchorClipId: string
  startMs: number
  endMs: number
  label: string
  artifact: VideoPlanVisualWire | null
}

function proposalChanges(proposal: VideoEditProposalWire): VideoIterationChange[] {
  const plan = proposal.plan
  const changes = plan
    ? (() => {
      let cursor = 0
      return plan.parts.map((part) => {
        const startMs = cursor
        cursor += Math.max(0, part.duration_ms)
        return { id: part.id, key: `${proposal.id}:part:${part.id}`, label: `Changed clip · ${part.title}`, clipId: part.id, startMs, endMs: cursor, artifact: part.visual ?? null, planPart: part }
      })
    })()
    : proposal.operations.map((operation, index) => {
      const clip = operation.clip ?? {}
      const fallback = proposal.affected_ranges?.[Math.min(index, (proposal.affected_ranges?.length ?? 1) - 1)] ?? proposal.affected_ranges?.[0]
      const startMs = typeof clip.timeline_start_ms === 'number' ? clip.timeline_start_ms : fallback?.start_ms ?? 0
      const endMs = typeof clip.timeline_end_ms === 'number' ? clip.timeline_end_ms : fallback?.end_ms ?? startMs
      const clipId = String(clip.id ?? operation.clip_id ?? operation.transition?.to_clip_id ?? operation.transition_id ?? '').trim()
      return { id: operation.id, key: `${proposal.id}:${operation.id}`, label: `Changed clip · ${operationLabel(operation)}`, clipId, startMs, endMs, artifact: null, operation }
    })
  if (proposal.status !== 'accepted' || !proposal.accepted_operation_ids?.length) return changes
  const acceptedIds = new Set(proposal.accepted_operation_ids)
  return changes.filter((change) => acceptedIds.has(change.id))
}

export function buildVideoIterationTimeline(proposals: VideoEditProposalWire[], revisions: VideoIterationRevisionWire[]): VideoIterationEntry[] {
  const revisionByProposal = new Map<string, VideoIterationRevisionWire>()
  for (const revision of revisions) if (revision.origin_proposal_id) revisionByProposal.set(revision.origin_proposal_id, revision)
  const proposalIds = new Set(proposals.map((proposal) => proposal.id))
  const proposalEntries = proposals.map((proposal): VideoIterationEntry => {
    const acceptedRevision = revisionByProposal.get(proposal.id)
    return {
      id: `proposal:${proposal.id}`,
      proposal,
      parentRevisionId: proposal.base_revision_id,
      candidateRevisionId: proposal.accepted_revision_id || acceptedRevision?.id || proposal.working_revision_id || '',
      candidateRevisionNumber: acceptedRevision?.revision_number ?? proposal.working_revision_number,
      status: proposal.status,
      title: proposal.title || proposal.plan?.summary || proposal.rationale || 'AI video iteration',
      createdAt: proposal.created_at,
      changes: proposalChanges(proposal),
    }
  })
  const revisionEntries = revisions.filter((revision) => !revision.origin_proposal_id || !proposalIds.has(revision.origin_proposal_id)).map((revision): VideoIterationEntry => ({
    id: `revision:${revision.id}`,
    proposal: null,
    parentRevisionId: revision.parent_revision_id || '',
    candidateRevisionId: revision.id,
    candidateRevisionNumber: revision.revision_number,
    status: 'revision',
    title: revision.change_summary || `Revision ${revision.revision_number}`,
    createdAt: revision.created_at,
    changes: [],
  }))
  return [...proposalEntries, ...revisionEntries].sort((left, right) => right.createdAt - left.createdAt || right.id.localeCompare(left.id))
}

function rangeLabel(startMs: number, endMs: number): string {
  const seconds = (value: number) => `${Math.max(0, value / 1000).toFixed(1)}s`
  return `${seconds(startMs)}–${seconds(endMs)}`
}

export const VideoIterationSidebar = memo(function VideoIterationSidebar(props: {
  sessionId: string
  projectId: string
  currentRevisionId: string
  revisions: VideoIterationRevisionWire[]
  onAccepted: () => Promise<void> | void
  onFeedback: (message: string) => Promise<void> | void
  onPreviewProposal: (proposal: VideoEditProposalWire | null, selectedChangeIds: string[]) => void
  onPreviewRevision: (revisionId: string | null) => void
  onFocusChange: (clipId: string, playheadMs: number) => void
  onAttachChange: (context: VideoIterationComposerContext) => void
}) {
  const [proposals, setProposals] = useState<VideoEditProposalWire[]>([])
  const [selected, setSelected] = useState<Record<string, boolean>>({})
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [previewId, setPreviewId] = useState<string | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const loadRequestSequence = useRef(0)
  const projectionSequence = useDesktopV3CacheSelector(useCallback((state) => videoProposalProjectionSequence(state, props.sessionId), [props.sessionId]))
  const load = useCallback(async () => loadLatestVideoEditProposals({ sessionId: props.sessionId, projectId: props.projectId, requestSequence: loadRequestSequence, onLoaded: setProposals, onError: setError }), [props.projectId, props.sessionId])
  useEffect(() => { void load() }, [load, projectionSequence])
  const iterations = useMemo(() => buildVideoIterationTimeline(proposals, props.revisions), [proposals, props.revisions])
  const newestPendingIterationId = useMemo(() => iterations.find((iteration) => iteration.proposal?.status === 'pending')?.id ?? null, [iterations])

  useEffect(() => {
    const selectedIteration = previewId ? iterations.find((iteration) => iteration.id === previewId) : null
    if (selectedIteration?.proposal?.status === 'pending') return
    if (!previewId || !selectedIteration) setPreviewId(newestPendingIterationId)
  }, [iterations, newestPendingIterationId, previewId])

  const previewProposal = useMemo(() => {
    const proposal = iterations.find((iteration) => iteration.id === previewId)?.proposal
    return proposal?.status === 'pending' ? proposal : null
  }, [iterations, previewId])
  const selectedChangeIds = useMemo(() => previewProposal ? selectedVideoProposalChangeIDs(previewProposal, selected) : [], [previewProposal, selected])
  useEffect(() => { props.onPreviewProposal(previewProposal, selectedChangeIds) }, [previewProposal, selectedChangeIds, props.onPreviewProposal])

  return <section className="mt-4 min-h-0 border-t border-[var(--app-border)] pt-3" aria-label="Video iterations">
    <div className="mb-2 flex items-center justify-between px-2"><p className="text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Iterations</p><button type="button" className="text-[10px] hover:text-[var(--app-text)]" onClick={() => void load()}>Refresh</button></div>
    {error ? <p className="px-2 py-2 text-[10px] text-red-400">{error}</p> : null}
    {iterations.length === 0 ? <p className="px-2 py-3 text-[11px] text-[var(--app-text-subtle)]">No AI iterations yet.</p> : null}
    <div className="max-h-[42vh] space-y-1 overflow-y-auto pr-1 lg:max-h-none">
      {iterations.map((iteration) => {
        const open = expanded[iteration.id] ?? iteration.status === 'pending'
        const previewing = previewId === iteration.id
        const proposal = iteration.proposal
        const stale = Boolean(proposal?.working_revision_id) && proposal?.working_revision_id !== props.currentRevisionId
        const enabledIds = proposal ? selectedVideoProposalChangeIDs(proposal, selected) : []
        return <article key={iteration.id} className={`border ${previewing ? 'border-amber-300 bg-amber-950/20' : 'border-[var(--app-border)] bg-[var(--app-bg)]'}`}>
          <div className="flex items-start gap-1 p-2">
            <button type="button" className="mt-0.5 shrink-0 p-0.5 disabled:cursor-default disabled:opacity-30" disabled={iteration.changes.length === 0} onClick={() => setExpanded((current) => ({ ...current, [iteration.id]: !open }))} aria-label={`${open ? 'Collapse' : 'Expand'} ${iteration.title}`}>{open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}</button>
            <button type="button" className="min-w-0 flex-1 text-left" onClick={() => { if (proposal?.status === 'pending') { setPreviewId(iteration.id); setExpanded((current) => ({ ...current, [iteration.id]: true })) } else if (iteration.candidateRevisionId) { setPreviewId(iteration.id); props.onPreviewProposal(null, []); props.onPreviewRevision(iteration.candidateRevisionId === props.currentRevisionId ? null : iteration.candidateRevisionId) } }}>
              <span className="block truncate text-[11px] font-medium text-[var(--app-text)]">{iteration.title}</span>
              <span className="mt-1 block truncate text-[9px] text-[var(--app-text-subtle)]">{iteration.candidateRevisionId === props.currentRevisionId ? 'current' : iteration.status} · parent {iteration.parentRevisionId || 'none'} · candidate {iteration.candidateRevisionId || 'working'}</span>
            </button>
            <span className={`shrink-0 text-[9px] uppercase ${iteration.status === 'pending' ? 'text-amber-300' : iteration.status === 'accepted' ? 'text-green-400' : 'text-[var(--app-text-subtle)]'}`}>{iteration.candidateRevisionNumber ? `r${iteration.candidateRevisionNumber}` : iteration.status}</span>
          </div>
          {open ? <div className="border-t border-[var(--app-border)] px-2 pb-2">
            {iteration.changes.map((change) => {
              const enabled = !proposal || proposal.plan?.kind === 'initial' || selected[change.key] !== false
              return <div key={change.key} className={`mt-2 border-l-2 pl-2 ${enabled ? 'border-[var(--app-primary)]' : 'border-[var(--app-border)] opacity-55'}`}>
                <div className="flex items-start gap-2">
                  {proposal?.status === 'pending' && proposal.plan?.kind !== 'initial' ? <input className="mt-0.5" type="checkbox" checked={enabled} aria-label={`Enable ${change.label}`} onChange={(event) => { setSelected((current) => ({ ...current, [change.key]: event.target.checked })); setPreviewId(iteration.id) }} /> : null}
                  <button type="button" className="min-w-0 flex-1 text-left" onClick={() => props.onFocusChange(change.clipId, change.startMs)}><span className="block truncate text-[10px] text-[var(--app-text)]">{change.label}</span><span className="mt-0.5 block font-mono text-[9px] text-[var(--app-text-subtle)]">{change.id} · {rangeLabel(change.startMs, change.endMs)}</span></button>
                  {proposal ? <button type="button" className="shrink-0 p-1 text-[var(--app-primary)] hover:bg-[var(--app-surface-hover)]" aria-label={`Attach ${change.label} to AI composer`} onClick={() => { props.onFocusChange(change.clipId, change.startMs); props.onAttachChange({ proposalId: proposal.id, parentRevisionId: iteration.parentRevisionId, candidateRevisionId: iteration.candidateRevisionId, changeId: change.id, anchorClipId: change.clipId, startMs: change.startMs, endMs: change.endMs, label: change.label, artifact: change.artifact }) }}><Paperclip size={12} /></button> : null}
                </div>
              </div>
            })}
            {proposal?.status === 'pending' ? <div className="mt-3 grid gap-1">
              <Button className="h-7 px-2 text-[10px]" disabled={Boolean(busyId) || stale || (proposal.plan?.kind !== 'initial' && enabledIds.length === 0)} onClick={() => void (async () => { setBusyId(proposal.id); try { await acceptVideoEditProposal({ sessionId: props.sessionId, projectId: props.projectId, proposalId: proposal.id, selectedOperationIds: enabledIds, changeSummary: proposal.title || proposal.plan?.summary || proposal.rationale }); setPreviewId(null); await props.onAccepted(); await load() } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)) } finally { setBusyId(null) } })()}><Check size={12} />Confirm enabled changes</Button>
              <Button variant="ghost" className="h-7 px-2 text-[10px]" disabled={Boolean(busyId)} onClick={() => void (async () => { const feedback = `Restore the accepted parent of iteration ${proposal.id} and revise only the changes I describe: `; setBusyId(proposal.id); try { await rejectVideoEditProposal(props.sessionId, props.projectId, proposal.id, feedback); setPreviewId(null); await props.onFeedback(feedback); await load() } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)) } finally { setBusyId(null) } })()}><RotateCcw size={12} />Restore parent and revise</Button>
            </div> : null}
          </div> : null}
        </article>
      })}
    </div>
  </section>
})

function videoSessionRenderedMessagesEqual(left: RenderedSessionMessages, right: RenderedSessionMessages): boolean {
  return left.committed === right.committed
    && left.pendingUser === right.pendingUser
    && left.liveRuns === right.liveRuns
    && left.runIntents === right.runIntents
    && left.currentRunIntent === right.currentRunIntent
    && left.latestRunIntent === right.latestRunIntent
}

export const VideoSessionAISidecar = memo(function VideoSessionAISidecar(props: {
  sessionId: string
  projectId?: string
  revisionId?: string
  anchorClipId?: string
  playheadMs?: number
  selectionKind?: 'visual' | 'transition' | 'iteration'
  transition?: VideoTransitionWire | null
  iterationContext?: VideoIterationComposerContext | null
  routeOptions?: DesktopChatRoute[]
  draftRequest?: { id: number; draft: string }
  artifactSelectionRequest?: DesktopV3ArtifactMessageSelection | null
  contextChip?: { id: string; label: string; kind: string; description?: string } | null
  onContextChipRemove?: () => void
  onArtifactSelectionRequestHandled?: () => void
  artifactReviewPortalTarget?: HTMLElement | null
  onActivity?: () => void
}) {
  const [hydrateError, setHydrateError] = useState(false)
  const activityKeyRef = useRef('')
  const renderedMessages = useDesktopV3CacheSelector(
    useCallback((state) => selectRenderedSessionMessages(state, props.sessionId), [props.sessionId]),
    videoSessionRenderedMessagesEqual,
  )
  const messagesLoaded = useDesktopV3CacheSelector(
    useCallback((state) => isDesktopV3SessionTailReady(state, props.sessionId), [props.sessionId]),
  )
  const loadedMessageCount = useDesktopV3CacheSelector(
    useCallback((state) => state.messagesBySession[props.sessionId]?.items.length ?? 0, [props.sessionId]),
  )
  const hydrating = useDesktopV3CacheSelector(
    useCallback((state) => (state.hydrateInFlightBySession[props.sessionId] ?? 0) > 0, [props.sessionId]),
  )

  useEffect(() => {
    setHydrateError(false)
    activityKeyRef.current = ''
    void selectAndHydrateDesktopV3Session(props.sessionId).catch(() => setHydrateError(true))
  }, [props.sessionId])

  useEffect(() => {
    const latestCommitted = renderedMessages.committed[renderedMessages.committed.length - 1]
    const activityKey = `${latestCommitted?.id ?? ''}:${renderedMessages.liveRuns.map((run) => run.runId).join(',')}`
    if (!activityKey || activityKey === ':') return
    if (activityKeyRef.current && activityKeyRef.current !== activityKey) props.onActivity?.()
    activityKeyRef.current = activityKey
  }, [props.onActivity, renderedMessages.committed, renderedMessages.liveRuns])

  return (
    <aside className="flex min-h-[70dvh] w-full min-w-0 shrink-0 border-t border-[var(--app-border)] bg-[var(--app-bg)] lg:min-h-0 lg:w-[440px] lg:min-w-[360px] lg:max-w-[42vw] lg:border-l lg:border-t-0" aria-label="Video session AI">
      <DesktopV3ExistingConversationPane
        sessionId={props.sessionId}
        initialHydrateStatus={hydrateError ? 'error' : hydrating ? 'loading' : messagesLoaded ? 'ready' : 'cached'}
        renderedMessages={renderedMessages}
        messagesLoaded={messagesLoaded}
        loadedMessageCount={loadedMessageCount}
        routeOptions={props.routeOptions}
        metadata={{
          creative_mode: 'video',
          ...(props.projectId ? { video_project_id: props.projectId } : {}),
          ...(props.revisionId ? { video_revision_id: props.revisionId } : {}),
          ...(props.anchorClipId ? { video_anchor_clip_id: props.anchorClipId } : {}),
          ...(typeof props.playheadMs === 'number' ? { video_playhead_ms: Math.max(0, Math.round(props.playheadMs)) } : {}),
          ...(props.selectionKind ? { video_selection_kind: props.selectionKind } : {}),
          ...(props.transition?.id ? { video_transition_id: props.transition.id } : {}),
          ...(props.transition?.kind ? { video_transition_kind: props.transition.kind } : {}),
          ...(props.transition?.from_clip_id ? { video_transition_from_clip_id: props.transition.from_clip_id } : {}),
          ...(props.transition?.to_clip_id ? { video_transition_to_clip_id: props.transition.to_clip_id } : {}),
          ...(typeof props.transition?.duration_ms === 'number' ? { video_transition_duration_ms: Math.max(0, Math.round(props.transition.duration_ms)) } : {}),
          ...(props.iterationContext ? {
            video_iteration_proposal_id: props.iterationContext.proposalId,
            video_iteration_parent_revision_id: props.iterationContext.parentRevisionId,
            video_iteration_candidate_revision_id: props.iterationContext.candidateRevisionId,
            video_iteration_change_id: props.iterationContext.changeId,
            video_iteration_range_start_ms: props.iterationContext.startMs,
            video_iteration_range_end_ms: props.iterationContext.endMs,
          } : {}),
        }}
        presentation="sidebar"
        artifactReviewPresentation="embedded"
        artifactReviewPortalTarget={props.artifactReviewPortalTarget}
        composerDraftRequest={props.draftRequest}
        artifactSelectionRequest={props.artifactSelectionRequest}
        contextChip={props.contextChip}
        onContextChipRemove={props.onContextChipRemove}
        onArtifactSelectionRequestHandled={props.onArtifactSelectionRequestHandled}
        onMessageSent={props.onActivity}
      />
    </aside>
  )
})
