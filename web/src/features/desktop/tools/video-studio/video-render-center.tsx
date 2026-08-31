import { Download, Film, Loader2, RefreshCw, XCircle } from 'lucide-react'
import { Button } from '../../../../components/ui/button'

export type VideoRenderJobStatus = 'queued' | 'rendering' | 'ready' | 'failed' | 'cancelled' | 'stale'

export type VideoRenderJobSnapshotWire = {
  schema_version?: number
  id: string
  project_id: string
  revision_id: string
  revision_number: number
  session_id: string
  status: VideoRenderJobStatus
  progress: number
  progress_stage?: string
  render_quality: 'preview' | 'standard' | 'high' | 'master'
  render_fps: 30 | 60
  output_width?: number
  output_height?: number
  estimated_remaining_ms?: number
  estimated_remaining_seconds?: number
  eta_ms?: number
  estimated_completion_at?: number
  started_at?: number
  completed_at?: number
  failure_code?: string
  failure_reason?: string
  output_preset?: string
  output_duration_ms?: number
  output_size_bytes?: number
  output_digest_sha256?: string
  output_artifact?: {
    session_id?: string
    collection_id: string
    variant_id: string
    event_seq?: number
    media_type?: string
  }
  created_at: number
  updated_at: number
}

export const VIDEO_RENDER_PRESETS = [
  { id: 'preview', label: 'Preview', quality: 'preview', fps: 30, description: 'Fast 30 FPS review render with a smaller output.' },
  { id: 'standard', label: 'Standard', quality: 'standard', fps: 30, description: 'Balanced 30 FPS output for routine review and sharing.' },
  { id: 'high', label: 'High quality', quality: 'high', fps: 30, description: 'Full-resolution 30 FPS output. Recommended for most final videos.' },
  { id: 'master', label: 'Master motion', quality: 'master', fps: 60, description: 'Highest-quality 60 FPS output for motion that benefits from every authored frame.' },
] as const

export type VideoRenderPreset = typeof VIDEO_RENDER_PRESETS[number]

export function videoRenderJobActive(job: VideoRenderJobSnapshotWire): boolean {
  return job.status === 'queued' || job.status === 'rendering'
}

export function videoRenderElapsedMs(job: VideoRenderJobSnapshotWire, now = Date.now()): number {
  const start = job.started_at || job.created_at
  const end = job.completed_at || (videoRenderJobActive(job) ? now : job.updated_at)
  const scale = start > 0 && start < 10_000_000_000 ? 1_000 : 1
  return Math.max(0, (end - start) * scale)
}

export function videoRenderRemainingMs(job: VideoRenderJobSnapshotWire, now = Date.now()): number | null {
  if (!videoRenderJobActive(job)) return null
  if (typeof job.estimated_remaining_ms === 'number' && job.estimated_remaining_ms >= 0) return job.estimated_remaining_ms
  if (typeof job.estimated_remaining_seconds === 'number' && job.estimated_remaining_seconds >= 0) return job.estimated_remaining_seconds * 1_000
  const completion = job.eta_ms ?? job.estimated_completion_at
  if (typeof completion === 'number') {
    const eta = completion < 10_000_000_000 ? completion * 1_000 : completion
    if (eta > now) return eta - now
  }
  const progress = Math.max(0, Math.min(1, job.progress || 0))
  if (progress <= 0.01) return null
  return Math.max(0, Math.round(videoRenderElapsedMs(job, now) * (1 - progress) / progress))
}

export function formatVideoRenderDuration(valueMs: number | null): string {
  if (valueMs === null || !Number.isFinite(valueMs)) return 'Calculating…'
  const totalSeconds = Math.max(0, Math.round(valueMs / 1000))
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours > 0) return `${hours}h ${minutes}m`
  if (minutes > 0) return `${minutes}m ${seconds}s`
  return `${seconds}s`
}

function formatTimestamp(value?: number): string {
  if (!value) return '—'
  return new Date(value < 10_000_000_000 ? value * 1_000 : value).toLocaleString()
}

function formatBytes(value?: number): string {
  if (!value) return '—'
  const units = ['B', 'KB', 'MB', 'GB']
  let amount = value
  let index = 0
  while (amount >= 1024 && index < units.length - 1) { amount /= 1024; index += 1 }
  return `${amount.toFixed(index === 0 ? 0 : 1)} ${units[index]}`
}

export function VideoRenderCenter(props: {
  jobs: VideoRenderJobSnapshotWire[]
  loading: boolean
  error: string | null
  cancellingJobId: string | null
  onRefresh: () => void
  onCancel: (job: VideoRenderJobSnapshotWire) => void
  onOpenOutput: (job: VideoRenderJobSnapshotWire) => void
}) {
  const ordered = [...props.jobs].sort((left, right) => right.created_at - left.created_at)
  return <section className="min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-6" aria-label="Video renders">
    <div className="mx-auto w-full max-w-5xl">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div><p className="text-[10px] font-semibold uppercase tracking-[0.18em] text-[var(--app-primary)]">Durable render queue</p><h1 className="mt-1 text-2xl font-semibold tracking-[-0.04em]">Renders</h1><p className="mt-2 max-w-2xl text-sm text-[var(--app-text-muted)]">Jobs continue in the background if you leave this view. Multiple renders may run concurrently; status and output remain attached to this project.</p></div>
        <Button variant="outline" className="h-9 px-3 text-xs" disabled={props.loading} onClick={props.onRefresh}>{props.loading ? <Loader2 size={13} className="animate-spin" /> : <RefreshCw size={13} />}Refresh</Button>
      </div>
      {props.error ? <div className="mt-4 border border-amber-400/30 bg-amber-950/15 px-3 py-2 text-xs text-amber-200" role="status">Could not refresh right now: {props.error}. Existing jobs are retained and polling will retry automatically.</div> : null}
      {ordered.length === 0 && !props.loading ? <div className="mt-6 border border-dashed border-[var(--app-border)] p-8 text-center"><Film className="mx-auto text-[var(--app-text-subtle)]" size={32} /><p className="mt-3 text-sm font-medium">No renders for this project yet</p><p className="mt-1 text-xs text-[var(--app-text-muted)]">Return to the editor and choose Render to select quality and FPS.</p></div> : null}
      <div className="mt-6 grid gap-3">
        {ordered.map((job) => {
          const active = videoRenderJobActive(job)
          const progress = Math.max(0, Math.min(1, job.progress || 0))
          const remaining = videoRenderRemainingMs(job)
          return <article key={job.id} className="border border-[var(--app-border)] bg-[var(--app-surface)] p-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="min-w-0"><div className="flex flex-wrap items-center gap-2"><span className={`rounded-full border px-2 py-0.5 text-[9px] font-semibold uppercase tracking-[0.12em] ${job.status === 'ready' ? 'border-emerald-400/40 text-emerald-300' : active ? 'border-sky-400/40 text-sky-300' : job.status === 'failed' ? 'border-red-400/40 text-red-300' : 'border-[var(--app-border)] text-[var(--app-text-muted)]'}`}>{job.status}</span><span className="text-xs font-semibold">Revision {job.revision_number}</span><span className="text-[10px] text-[var(--app-text-muted)]">{job.render_quality} · {job.render_fps} FPS</span></div><p className="mt-2 truncate font-mono text-[10px] text-[var(--app-text-subtle)]">{job.id}</p></div>
              <div className="flex gap-2">{job.status === 'ready' ? <Button className="h-8 px-3 text-xs" onClick={() => props.onOpenOutput(job)}><Download size={13} />Open output</Button> : null}{active ? <Button variant="outline" className="h-8 px-3 text-xs text-red-300" disabled={props.cancellingJobId === job.id} onClick={() => props.onCancel(job)}>{props.cancellingJobId === job.id ? <Loader2 size={13} className="animate-spin" /> : <XCircle size={13} />}Cancel</Button> : null}</div>
            </div>
            <div className="mt-4 h-1.5 overflow-hidden rounded-full bg-[var(--app-bg)]"><div className={`h-full transition-[width] ${job.status === 'failed' ? 'bg-red-400' : job.status === 'cancelled' ? 'bg-[var(--app-text-subtle)]' : 'bg-[var(--app-primary)]'}`} style={{ width: `${Math.round(progress * 100)}%` }} /></div>
            <div className="mt-2 grid gap-2 text-[10px] text-[var(--app-text-muted)] sm:grid-cols-4"><span>Progress <b className="text-[var(--app-text)]">{Math.round(progress * 100)}%</b></span><span>Stage <b className="text-[var(--app-text)]">{job.progress_stage || (job.status === 'queued' ? 'Waiting for capacity' : job.status)}</b></span><span>Elapsed <b className="text-[var(--app-text)]">{formatVideoRenderDuration(videoRenderElapsedMs(job))}</b></span><span>ETA <b className="text-[var(--app-text)]">{active ? formatVideoRenderDuration(remaining) : '—'}</b></span></div>
            <dl className="mt-3 grid gap-2 border-t border-[var(--app-border)] pt-3 text-[10px] sm:grid-cols-3"><div><dt className="text-[var(--app-text-subtle)]">Created</dt><dd className="mt-0.5">{formatTimestamp(job.created_at)}</dd></div><div><dt className="text-[var(--app-text-subtle)]">Updated</dt><dd className="mt-0.5">{formatTimestamp(job.updated_at)}</dd></div><div><dt className="text-[var(--app-text-subtle)]">Output</dt><dd className="mt-0.5">{job.output_width && job.output_height ? `${job.output_width}×${job.output_height} · ` : ''}{job.output_size_bytes ? formatBytes(job.output_size_bytes) : job.status === 'ready' ? 'Managed artifact' : 'Pending'}</dd></div></dl>
            {job.failure_reason || job.failure_code ? <p className="mt-3 text-xs text-red-300">{job.failure_reason || job.failure_code}</p> : null}
            {job.output_digest_sha256 ? <p className="mt-2 truncate font-mono text-[9px] text-[var(--app-text-subtle)]">SHA-256 {job.output_digest_sha256}</p> : null}
          </article>
        })}
      </div>
    </div>
  </section>
}
