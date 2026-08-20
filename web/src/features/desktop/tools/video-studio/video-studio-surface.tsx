import { useCallback, useEffect, useMemo, useState } from 'react'
import { Check, Loader2, MessageSquare, Send, X } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Textarea } from '../../../../components/ui/textarea'
import { requestJson } from '../../../../app/api'
import { fetchSessionMessages } from '../../chat/queries/chat-queries'
import { createDesktopV3ExistingMessageOperation, continueDesktopV3Conversation } from '../../session-v3/existing-session-flow'
import type { ChatMessageRecord } from '../../chat/types/chat'

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

export type VideoEditProposalWire = {
  id: string
  project_id: string
  base_revision_id: string
  base_revision_number: number
  status: 'pending' | 'accepted' | 'rejected'
  title?: string
  rationale?: string
  operations: VideoEditOperationWire[]
  accepted_operation_ids?: string[]
  accepted_revision_id?: string
  created_at: number
  updated_at: number
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

export async function listVideoEditProposals(sessionId: string, projectId: string): Promise<VideoEditProposalWire[]> {
  const response = await requestJson<{ proposals?: VideoEditProposalWire[] }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects/${encodeURIComponent(projectId)}/edit-proposals`,
  )
  return Array.isArray(response.proposals) ? response.proposals : []
}

export async function acceptVideoEditProposal(input: {
  sessionId: string
  projectId: string
  proposalId: string
  selectedOperationIds: string[]
}): Promise<{ proposal: VideoEditProposalWire; revision: unknown; project: unknown }> {
  return requestJson<{ proposal: VideoEditProposalWire; revision: unknown; project: unknown }>(`/v3/sessions/${encodeURIComponent(input.sessionId)}/video/projects/${encodeURIComponent(input.projectId)}/edit-proposals/${encodeURIComponent(input.proposalId)}/accept`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      selected_operation_ids: input.selectedOperationIds,
      change_summary: `Accepted ${input.selectedOperationIds.length} proposed video edit${input.selectedOperationIds.length === 1 ? '' : 's'}`,
    }),
  })
}

export async function rejectVideoEditProposal(sessionId: string, projectId: string, proposalId: string): Promise<void> {
  await requestJson(`/v3/sessions/${encodeURIComponent(sessionId)}/video/projects/${encodeURIComponent(projectId)}/edit-proposals/${encodeURIComponent(proposalId)}/reject`, {
    method: 'POST',
  })
}

export async function requestVideoRenderCancellation(sessionId: string, jobId: string): Promise<void> {
  await requestJson(`/v3/sessions/${encodeURIComponent(sessionId)}/video/render-jobs/${encodeURIComponent(jobId)}/cancel`, { method: 'POST' })
}

function operationLabel(operation: VideoEditOperationWire): string {
  const subject = operation.clip?.name || operation.clip_id || operation.transition?.kind || operation.transition_id || 'timeline'
  return `${operation.type.replace(/_/g, ' ')} · ${String(subject)}`
}

export function VideoProposalReview(props: {
  sessionId: string
  projectId: string
  currentRevisionId: string
  onAccepted: () => Promise<void> | void
  onFeedback: (message: string) => Promise<void> | void
}) {
  const [proposals, setProposals] = useState<VideoEditProposalWire[]>([])
  const [selected, setSelected] = useState<Record<string, boolean>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setProposals(await listVideoEditProposals(props.sessionId, props.projectId))
      setError(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    }
  }, [props.projectId, props.sessionId])

  useEffect(() => { void load() }, [load])
  const pending = useMemo(() => proposals.filter((proposal) => proposal.status === 'pending'), [proposals])

  if (pending.length === 0 && !error) return null
  return (
    <section className="mt-5 border border-[var(--app-border)] bg-[var(--app-surface)] p-4" aria-label="AI edit proposals">
      <div className="flex items-center justify-between gap-3">
        <div><p className="text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Review before applying</p><h2 className="mt-1 text-sm font-semibold">AI edit proposals</h2></div>
        <Button variant="ghost" className="h-8 px-2 text-xs" onClick={() => void load()} disabled={busy}>Refresh</Button>
      </div>
      {error ? <p className="mt-3 text-xs text-red-400">{error}</p> : null}
      <div className="mt-3 grid gap-3">
        {pending.map((proposal) => {
          const stale = proposal.base_revision_id !== props.currentRevisionId
          const selectedIds = proposal.operations.filter((operation) => selected[`${proposal.id}:${operation.id}`] !== false).map((operation) => operation.id)
          return <article key={proposal.id} className="border border-[var(--app-border)] bg-[var(--app-bg)] p-3">
            <div className="flex items-start justify-between gap-3"><div><h3 className="text-sm font-medium">{proposal.title || 'Suggested timeline edits'}</h3><p className="mt-1 text-xs text-[var(--app-text-muted)]">{proposal.rationale || 'Review each typed operation before it becomes a revision.'}</p></div><span className={`text-[10px] ${stale ? 'text-amber-400' : 'text-[var(--app-primary)]'}`}>{stale ? 'stale base' : `base r${proposal.base_revision_number}`}</span></div>
            <div className="mt-3 grid gap-1">
              {proposal.operations.map((operation) => <label key={operation.id} className="flex items-center gap-2 border border-[var(--app-border)] px-2 py-2 text-xs"><input type="checkbox" checked={selected[`${proposal.id}:${operation.id}`] !== false} onChange={(event) => setSelected((current) => ({ ...current, [`${proposal.id}:${operation.id}`]: event.target.checked }))} /><span>{operationLabel(operation)}</span></label>)}
            </div>
            <div className="mt-3 flex flex-wrap gap-2">
              <Button className="h-8 px-3 text-xs" disabled={busy || stale || selectedIds.length === 0} onClick={() => void (async () => { setBusy(true); try { await acceptVideoEditProposal({ sessionId: props.sessionId, projectId: props.projectId, proposalId: proposal.id, selectedOperationIds: selectedIds }); await props.onAccepted(); await load() } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)) } finally { setBusy(false) } })()}><Check size={13} />Accept selected</Button>
              <Button variant="outline" className="h-8 px-3 text-xs" disabled={busy || stale} onClick={() => { for (const operation of proposal.operations) setSelected((current) => ({ ...current, [`${proposal.id}:${operation.id}`]: true })) }}>Select all</Button>
              <Button variant="ghost" className="h-8 px-3 text-xs" disabled={busy} onClick={() => void (async () => { setBusy(true); try { await rejectVideoEditProposal(props.sessionId, props.projectId, proposal.id); await props.onFeedback(`I rejected “${proposal.title || 'the edit proposal'}”. Please revise the approach against revision ${props.currentRevisionId}.`); await load() } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)) } finally { setBusy(false) } })()}><X size={13} />Reject with feedback</Button>
            </div>
          </article>
        })}
      </div>
    </section>
  )
}

export function VideoSessionAISidecar(props: { sessionId: string; revisionId: string; onSent?: () => void }) {
  const [messages, setMessages] = useState<ChatMessageRecord[]>([])
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    const result = await fetchSessionMessages(props.sessionId, undefined, 0, { sessionApi: 'v3', tail: true, limit: 30 })
    setMessages(result.messages)
  }, [props.sessionId])
  useEffect(() => { void refresh().catch(() => undefined) }, [refresh])

  const send = useCallback(async (content: string) => {
    const prompt = content.trim()
    if (!prompt || busy) return
    setBusy(true); setError(null)
    try {
      await continueDesktopV3Conversation(createDesktopV3ExistingMessageOperation({ sessionId: props.sessionId, prompt, metadata: { creative_mode: 'video', video_revision_id: props.revisionId } }))
      setDraft(''); props.onSent?.(); await refresh()
    } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)) } finally { setBusy(false) }
  }, [busy, props, refresh])

  return <aside className="flex min-h-0 w-[320px] shrink-0 flex-col border-l border-[var(--app-border)] bg-[var(--app-surface)]" aria-label="Video session AI">
    <div className="border-b border-[var(--app-border)] px-3 py-3"><div className="flex items-center gap-2"><MessageSquare size={14} className="text-[var(--app-primary)]" /><h2 className="text-sm font-semibold">Session AI</h2></div><p className="mt-1 text-[10px] text-[var(--app-text-subtle)]">Same durable session · proposal-only edits</p></div>
    <div className="min-h-0 flex-1 space-y-2 overflow-y-auto p-3">{messages.slice(-12).map((message) => <div key={message.id} className={`border px-2 py-2 text-xs leading-5 ${message.role === 'user' ? 'border-[var(--app-primary)]/30 bg-[var(--app-bg)]' : 'border-[var(--app-border)]'}`}><p className="mb-1 text-[9px] uppercase text-[var(--app-text-subtle)]">{message.role}</p><p className="line-clamp-5 whitespace-pre-wrap">{message.content}</p></div>)}</div>
    <div className="border-t border-[var(--app-border)] p-3">{error ? <p className="mb-2 text-xs text-red-400">{error}</p> : null}<Textarea value={draft} onChange={(event) => setDraft(event.target.value)} placeholder="Ask for trims, pacing, transitions…" className="min-h-20 text-xs" /><Button className="mt-2 h-8 w-full text-xs" disabled={busy || !draft.trim()} onClick={() => void send(draft)}>{busy ? <Loader2 size={13} className="animate-spin" /> : <Send size={13} />}Send to this session</Button></div>
  </aside>
}
