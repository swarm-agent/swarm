import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Check, X } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { requestJson } from '../../../../app/api'
import { DesktopV3ExistingConversationPane } from '../../chat/components/desktop-v3-existing-conversation-pane'
import { isDesktopV3SessionTailReady, selectRenderedSessionMessages, type RenderedSessionMessages } from '../../state/desktop-v3-cache-selectors'
import { useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import type { DesktopV3CacheState } from '../../state/desktop-v3-cache-types'
import { selectAndHydrateDesktopV3Session } from '../../state/desktop-v3-session-hydrator'
import type { DesktopChatRoute } from '../../chat/services/chat-routing'
import { DesktopV3ArtifactPreviewThumbnail } from '../../chat/components/desktop-v3-artifact-preview-thumbnail'
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
  type: 'add_clip' | 'update_clip' | 'remove_clip' | 'add_transition' | 'update_transition' | 'remove_transition'
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
  return `/v3/sessions/${encodeURIComponent(sessionId)}/artifacts/collections/${encodeURIComponent(reference.collection_id)}/variants/${encodeURIComponent(reference.variant_id)}/content/output.mp4`
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
  if (operation.transition) {
    const duration = typeof operation.transition.duration_ms === 'number' ? ` · ${operation.transition.duration_ms}ms` : ''
    return `${operation.type.replace(/_/g, ' ')} · ${transitionLabel(operation.transition.kind)} · ${operation.transition.from_clip_id} → ${operation.transition.to_clip_id}${duration}`
  }
  if (operation.transition_id) return `${operation.type.replace(/_/g, ' ')} · transition ${operation.transition_id}`
  const clip = operation.clip
  const subject = String(clip?.name || clip?.id || operation.clip_id || 'timeline')
  const sourceStart = typeof clip?.source_start_ms === 'number' ? clip.source_start_ms : null
  const sourceEnd = typeof clip?.source_end_ms === 'number' ? clip.source_end_ms : null
  const range = sourceStart !== null && sourceEnd !== null ? ` · source ${sourceStart}–${sourceEnd}ms` : ''
  return `${operation.type.replace(/_/g, ' ')} · ${subject}${range}`
}

export function selectedVideoProposalChangeIDs(proposal: VideoEditProposalWire, selected: Record<string, boolean>): string[] {
  if (proposal.plan?.kind === 'initial') return []
  if (proposal.plan) return proposal.plan.parts
    .filter((part) => selected[`${proposal.id}:part:${part.id}`] !== false)
    .map((part) => part.id)
  return proposal.operations
    .filter((operation) => selected[`${proposal.id}:${operation.id}`] !== false)
    .map((operation) => operation.id)
}

export function VideoProposalReview(props: {
  sessionId: string
  projectId: string
  currentRevisionId: string
  acceptedPlan?: VideoPlanProposalWire | null
  acceptedClips?: Array<Record<string, unknown>>
  onAccepted: () => Promise<void> | void
  onFeedback: (message: string) => Promise<void> | void
  onPendingChange?: (proposal: VideoEditProposalWire | null, selectedChangeIds: string[]) => void
  onFocusStep?: (clipId: string, playheadMs: number) => void
}) {
  const [proposals, setProposals] = useState<VideoEditProposalWire[]>([])
  const [selected, setSelected] = useState<Record<string, boolean>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const loadRequestSequence = useRef(0)
  const projectionSequence = useDesktopV3CacheSelector(
    useCallback((state) => videoProposalProjectionSequence(state, props.sessionId), [props.sessionId]),
  )

  const load = useCallback(async () => {
    await loadLatestVideoEditProposals({
      sessionId: props.sessionId,
      projectId: props.projectId,
      requestSequence: loadRequestSequence,
      onLoaded: setProposals,
      onError: setError,
    })
  }, [props.projectId, props.sessionId])

  useEffect(() => { void load() }, [load, projectionSequence])
  const pending = useMemo(() => proposals
    .filter((proposal) => proposal.status === 'pending')
    .sort((left, right) => left.created_at - right.created_at || left.id.localeCompare(right.id)), [proposals])
  const [previewProposalId, setPreviewProposalId] = useState<string | null>(null)
  const previewProposal = useMemo(() => pending.find((proposal) => proposal.id === previewProposalId) ?? null, [pending, previewProposalId])
  useEffect(() => {
    if (pending.length === 0) {
      if (previewProposalId) setPreviewProposalId(null)
      return
    }
    if (!previewProposalId || !pending.some((proposal) => proposal.id === previewProposalId)) {
      setPreviewProposalId(pending[pending.length - 1].id)
    }
  }, [pending, previewProposalId])
  const previewSelectedChangeIds = useMemo(() => previewProposal ? selectedVideoProposalChangeIDs(previewProposal, selected) : [], [previewProposal, selected])
  useEffect(() => { props.onPendingChange?.(previewProposal, previewSelectedChangeIds) }, [previewProposal, previewSelectedChangeIds, props.onPendingChange])

  if (pending.length === 0 && !error) return null
  return (
    <section className="mt-5 border border-[var(--app-border)] bg-[var(--app-surface)] p-4" aria-label="AI edit proposals">
      <div className="flex items-center justify-between gap-3">
        <div><p className="text-[10px] uppercase tracking-[0.18em] text-[var(--app-primary)]">New change added</p><h2 className="mt-1 text-sm font-semibold">Working changes</h2><p className="mt-1 text-xs text-[var(--app-text-muted)]">The newest change is already visible. Keep everything, or restore only the sections you do not want.</p></div>
        <Button variant="ghost" className="h-8 px-2 text-xs" onClick={() => void load()} disabled={busy}>Refresh</Button>
      </div>
      {error ? <p className="mt-3 text-xs text-red-400">{error}</p> : null}
      <div className="mt-3 grid gap-3">
        {pending.map((proposal) => {
          const stale = Boolean(proposal.working_revision_id) && proposal.working_revision_id !== props.currentRevisionId
          const isPlan = Boolean(proposal.plan)
          const isPlanRevision = proposal.plan?.kind === 'revision'
          const selectedPlanPartIds = proposal.plan ? selectedVideoProposalChangeIDs(proposal, selected) : []
          const selectedIds = proposal.plan ? [] : selectedVideoProposalChangeIDs(proposal, selected)
          const planOperations = proposal.operations.filter((operation) => (operation.type === 'add_clip' || operation.type === 'update_clip') && operation.clip?.source_kind === 'text')
          const supportingOperations = proposal.operations.filter((operation) => !planOperations.includes(operation))
          const changedClipOperations = proposal.operations.filter((operation) => (operation.type === 'add_clip' || operation.type === 'update_clip') && operation.clip)
          const acceptedById = new Map((props.acceptedClips ?? []).map((clip) => [String(clip.id ?? ''), clip]))
          const rejectionDraft = `I rejected “${proposal.title || (isPlan ? 'the video plan' : 'the edit proposal')}”. Preserve the stable part IDs and current accepted visuals. Create new ready replacement visuals only for the parts I name, then return one plan.kind=revision proposal. Requested changes: `
          const previewing = proposal.id === previewProposalId
          return <article key={proposal.id} className={`border bg-[var(--app-bg)] p-3 ${previewing ? 'border-amber-300 ring-1 ring-amber-300/40' : 'border-[var(--app-border)]'}`}>
            <div className="flex items-start justify-between gap-3"><div><p className="text-[10px] font-semibold uppercase tracking-[0.14em] text-amber-400">Working · visible now</p><h3 className="mt-1 text-sm font-medium">{proposal.title || (isPlan ? 'Video plan change' : planOperations.length > 0 ? 'Script + still change' : 'Timeline changes')}</h3><p className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">{proposal.rationale || 'This change is already in the working player. Confirmation makes your selected sections the kept checkpoint.'}</p></div><span className={`shrink-0 text-[10px] ${stale ? 'text-amber-400' : 'text-[var(--app-primary)]'}`}>{stale ? 'older working change' : `working r${proposal.working_revision_number ?? proposal.base_revision_number} · kept from r${proposal.base_revision_number}`}</span></div>
            {proposal.plan ? (
              <div className="mt-4" aria-label="Atomic video plan proposal">
                <div className="mb-3 flex items-center justify-between gap-3"><div><p className="text-xs font-semibold text-[var(--app-text)]">{isPlanRevision ? 'Changed sections' : 'Working visual plan'} · {proposal.plan.parts.length} parts</p><p className="mt-1 text-[11px] text-[var(--app-text-muted)]">{isPlanRevision ? 'All replacements are kept by default. Uncheck any section to restore its prior version when you confirm.' : 'The complete plan is already visible. Confirm it to establish the first kept checkpoint.'}</p></div><span className="text-[10px] uppercase tracking-[0.12em] text-amber-400">Visible in player</span></div>
                {proposal.plan.summary ? <p className="mb-3 border border-[var(--app-border)] bg-[var(--app-surface)] p-3 text-xs leading-5 text-[var(--app-text-muted)]">{proposal.plan.summary}</p> : null}
                <ol className="grid max-h-[34rem] gap-3 overflow-y-auto pr-1 xl:grid-cols-2">
                  {proposal.plan.parts.map((part, index) => <li key={part.id} className="border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
                    <div className="flex items-center justify-between gap-3"><span className="text-[10px] font-semibold uppercase tracking-[0.14em] text-[var(--app-primary)]">Part {index + 1}</span><span className="font-mono text-[10px] text-[var(--app-text-subtle)]">{part.id}</span></div>
                    {isPlanRevision ? <label className="mt-2 flex items-center gap-2 text-[11px] text-[var(--app-text-muted)]"><input type="checkbox" checked={selected[`${proposal.id}:part:${part.id}`] !== false} onChange={(event) => setSelected((current) => ({ ...current, [`${proposal.id}:part:${part.id}`]: event.target.checked }))} />{selected[`${proposal.id}:part:${part.id}`] !== false ? 'Keep changed section' : 'Restore prior section'}</label> : null}
                    {isPlanRevision && props.acceptedPlan?.parts.some((acceptedPart) => acceptedPart.id === part.id) ? <div className="mt-3 grid gap-3 md:grid-cols-2"><div><p className="mb-1 text-[10px] uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">Prior kept section</p>{videoPlanPartArtifact(props.acceptedPlan.parts.find((acceptedPart) => acceptedPart.id === part.id)!) ? <DesktopV3ArtifactPreviewThumbnail artifact={videoPlanPartArtifact(props.acceptedPlan.parts.find((acceptedPart) => acceptedPart.id === part.id)!)!} presentation="wide" /> : null}</div><div><p className="mb-1 text-[10px] uppercase tracking-[0.12em] text-amber-300">Working replacement</p>{videoPlanPartArtifact(part) ? <DesktopV3ArtifactPreviewThumbnail artifact={videoPlanPartArtifact(part)!} presentation="wide" /> : null}</div></div> : <div className="mt-3">{videoPlanPartArtifact(part) ? <DesktopV3ArtifactPreviewThumbnail artifact={videoPlanPartArtifact(part)!} presentation="wide" /> : <div className="grid aspect-video place-items-center border border-red-400/40 bg-red-950/20 px-4 text-center text-xs text-red-300">This legacy proposal has no attached visual and cannot satisfy the visual-plan contract.</div>}</div>}
                    <h4 className="mt-3 text-sm font-semibold text-[var(--app-text)]">{part.title}</h4>
                    <p className="mt-1 text-[10px] tabular-nums text-[var(--app-primary)]">{Math.round(part.duration_ms / 100) / 10}s</p>
                    <dl className="mt-3 grid gap-2 text-xs leading-5">
                      <div><dt className="uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">Narration / purpose</dt><dd className="text-[var(--app-text-muted)]">{part.narration || 'To be developed after approval.'}</dd></div>
                      <div><dt className="uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">On-screen text</dt><dd className="text-[var(--app-text-muted)]">{part.on_screen_text || 'Not set.'}</dd></div>
                      <div><dt className="uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">Visual direction</dt><dd className="text-[var(--app-text-muted)]">{part.visual_direction || 'To be matched with source media.'}</dd></div>
                      {part.transition_in ? <div><dt className="uppercase tracking-[0.1em] text-amber-400">Proposed transition · review only</dt><dd className="text-[var(--app-text-muted)]">{part.transition_in}</dd></div> : null}
                    </dl>
                  </li>)}
                </ol>
              </div>
            ) : null}
            {planOperations.length > 0 ? (
              <div className="mt-4">
                <div className="mb-2 flex items-center justify-between gap-3"><p className="text-xs font-medium text-[var(--app-text)]">Script structure and planned stills</p><span className="text-[10px] text-[var(--app-text-subtle)]">Accept selected to make this the current cut</span></div>
                <div className="grid max-h-[34rem] gap-3 overflow-y-auto pr-1 xl:grid-cols-2">
                  {planOperations.map((operation) => {
                    const details = proposedVideoPlanClipDetails(operation.clip ?? {})
                    const clipId = String(operation.clip?.id ?? operation.clip_id ?? '').trim()
                    const playheadMs = typeof operation.clip?.timeline_start_ms === 'number' ? operation.clip.timeline_start_ms : proposal.affected_ranges?.[0]?.start_ms ?? 0
                    return <label key={operation.id} className="block cursor-pointer border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
                      <div className="flex items-start gap-3">
                        <input className="mt-1" type="checkbox" checked={selected[`${proposal.id}:${operation.id}`] !== false} onChange={(event) => setSelected((current) => ({ ...current, [`${proposal.id}:${operation.id}`]: event.target.checked }))} />
                        <div className="min-w-0 flex-1">
                          <div className="flex flex-wrap items-baseline justify-between gap-2"><div className="flex items-center gap-2"><span className="border border-[var(--app-border)] bg-[var(--app-bg)] px-1.5 py-0.5 text-[9px] uppercase tracking-[0.12em] text-[var(--app-primary)]">{details.kind === 'title' ? 'Opening title still' : details.kind === 'transition' ? 'Transition still' : 'Content section'}</span><h4 className="text-xs font-semibold text-[var(--app-text)]">{details.title}</h4></div><span className="text-[10px] tabular-nums text-[var(--app-primary)]">{details.timing}</span></div>
                          <div className="mt-2 flex flex-wrap items-center gap-2 text-[10px]">
                            <button type="button" className="border border-[var(--app-border)] bg-[var(--app-bg)] px-2 py-1 font-mono text-[var(--app-primary)]" onClick={(event) => { event.preventDefault(); event.stopPropagation(); props.onFocusStep?.(clipId, playheadMs) }}>anchor · {clipId}</button>
                            <span className="uppercase tracking-[0.12em] text-amber-400">{operation.type === 'add_clip' ? 'pending add' : 'pending change'}</span>
                          </div>
                          <dl className="mt-3 grid gap-2 text-xs leading-5">
                            <div><dt className="font-medium uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">Narration</dt><dd className="text-[var(--app-text-muted)]">{details.narration || 'No narration specified.'}</dd></div>
                            <div><dt className="font-medium uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">On-screen text</dt><dd className="font-medium text-[var(--app-text)]">{details.onScreenText || 'No on-screen text specified.'}</dd></div>
                            <div><dt className="font-medium uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">Planned still / frame</dt><dd className="text-[var(--app-text-muted)]">{details.still || 'No frame direction specified.'}</dd></div>
                          </dl>
                        </div>
                      </div>
                    </label>
                  })}
                </div>
              </div>
            ) : null}
            {changedClipOperations.length > 0 ? (
              <div className="mt-4 grid gap-3" aria-label="Prior and proposed slide comparison">
                {changedClipOperations.map((operation) => {
                  const proposed = operation.clip ?? {}
                  const clipId = String(proposed.id ?? operation.clip_id ?? '').trim()
                  const prior = acceptedById.get(clipId)
                  const priorDetails = prior ? proposedVideoPlanClipDetails(prior) : null
                  const proposedDetails = proposedVideoPlanClipDetails(proposed)
                  const playheadMs = typeof proposed.timeline_start_ms === 'number' ? proposed.timeline_start_ms : proposal.affected_ranges?.[0]?.start_ms ?? 0
                  return <div key={`${operation.id}-comparison`} className="border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
                    <div className="flex items-center justify-between gap-3"><button type="button" className="font-mono text-[10px] text-[var(--app-primary)]" onClick={() => props.onFocusStep?.(clipId, playheadMs)}>slide · {clipId}</button><span className="text-[10px] uppercase tracking-[0.12em] text-amber-400">Confirm this iteration</span></div>
                    <div className="mt-3 grid gap-3 md:grid-cols-2">
                      <div className="border border-[var(--app-border)] bg-[var(--app-bg)] p-3"><p className="text-[10px] font-semibold uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">Prior · accepted r{proposal.base_revision_number}</p><p className="mt-2 text-xs font-medium text-[var(--app-text)]">{priorDetails?.title || String(prior?.name ?? 'No prior slide')}</p><p className="mt-1 text-[11px] leading-5 text-[var(--app-text-muted)]">{priorDetails?.onScreenText || String(prior?.source_kind ?? 'This is a new slide.')}</p></div>
                      <div className="border border-amber-300/40 bg-amber-950/20 p-3"><p className="text-[10px] font-semibold uppercase tracking-[0.14em] text-amber-300">Proposed · pending</p><p className="mt-2 text-xs font-medium text-[var(--app-text)]">{proposedDetails.title || String(proposed.name ?? 'Updated slide')}</p><p className="mt-1 text-[11px] leading-5 text-[var(--app-text-muted)]">{proposedDetails.onScreenText || String(proposed.source_kind ?? 'Updated content')}</p></div>
                    </div>
                  </div>
                })}
              </div>
            ) : null}
            {supportingOperations.length > 0 ? (
              <details className="mt-3 border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2">
                <summary className="cursor-pointer text-xs text-[var(--app-text-muted)]">Supporting timeline changes ({supportingOperations.length})</summary>
                <div className="mt-2 grid gap-1">
                  {supportingOperations.map((operation) => <label key={operation.id} className="flex items-center gap-2 border border-[var(--app-border)] bg-[var(--app-bg)] px-2 py-2 text-xs"><input type="checkbox" checked={selected[`${proposal.id}:${operation.id}`] !== false} onChange={(event) => setSelected((current) => ({ ...current, [`${proposal.id}:${operation.id}`]: event.target.checked }))} /><span>{operationLabel(operation)}</span></label>)}
                </div>
              </details>
            ) : null}
            <div className="mt-3 flex flex-wrap gap-2">
              <Button variant={previewing ? 'outline' : 'ghost'} className="h-8 px-3 text-xs" disabled={busy || stale} aria-pressed={previewing} onClick={() => setPreviewProposalId(proposal.id)}>{previewing ? 'Showing working change' : 'Show this change'}</Button>
              <Button className="h-8 px-3 text-xs" disabled={busy || stale || (isPlanRevision ? selectedPlanPartIds.length === 0 : !isPlan && selectedIds.length === 0)} onClick={() => void (async () => { setBusy(true); try { await acceptVideoEditProposal({ sessionId: props.sessionId, projectId: props.projectId, proposalId: proposal.id, selectedOperationIds: isPlanRevision ? selectedPlanPartIds : isPlan ? [] : selectedIds, changeSummary: proposal.title || proposal.plan?.summary || proposal.rationale }); setPreviewProposalId(null); await props.onAccepted(); await load() } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)) } finally { setBusy(false) } })()}><Check size={13} />{isPlanRevision ? 'Confirm selected sections' : isPlan ? 'Confirm working plan' : 'Confirm selected changes'}</Button>
              {!isPlan ? <Button variant="outline" className="h-8 px-3 text-xs" disabled={busy || stale} onClick={() => { for (const operation of proposal.operations) setSelected((current) => ({ ...current, [`${proposal.id}:${operation.id}`]: true })) }}>Select all</Button> : null}
              <Button variant="ghost" className="h-8 px-3 text-xs" disabled={busy} onClick={() => void (async () => { setBusy(true); try { await rejectVideoEditProposal(props.sessionId, props.projectId, proposal.id, rejectionDraft); if (previewProposalId === proposal.id) setPreviewProposalId(null); await props.onFeedback(rejectionDraft); await load() } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)) } finally { setBusy(false) } })()}><X size={13} />Restore prior version and ask again</Button>
            </div>
          </article>
        })}
      </div>
    </section>
  )
}

function videoSessionRenderedMessagesEqual(left: RenderedSessionMessages, right: RenderedSessionMessages): boolean {
  return left.committed === right.committed
    && left.pendingUser === right.pendingUser
    && left.liveRuns === right.liveRuns
    && left.runIntents === right.runIntents
    && left.currentRunIntent === right.currentRunIntent
    && left.latestRunIntent === right.latestRunIntent
}

export function VideoSessionAISidecar(props: {
  sessionId: string
  projectId?: string
  revisionId?: string
  anchorClipId?: string
  playheadMs?: number
  selectionKind?: 'visual' | 'transition'
  transition?: VideoTransitionWire | null
  routeOptions?: DesktopChatRoute[]
  draftRequest?: { id: number; draft: string }
  artifactSelectionRequest?: DesktopV3ArtifactMessageSelection | null
  contextChip?: { id: string; label: string; kind: string; description?: string } | null
  onContextChipRemove?: () => void
  onArtifactSelectionRequestHandled?: () => void
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
        }}
        presentation="sidebar"
        composerDraftRequest={props.draftRequest}
        artifactSelectionRequest={props.artifactSelectionRequest}
        contextChip={props.contextChip}
        onContextChipRemove={props.onContextChipRemove}
        onArtifactSelectionRequestHandled={props.onArtifactSelectionRequestHandled}
        onMessageSent={props.onActivity}
      />
    </aside>
  )
}
