import { GitCommitHorizontal, Loader2, TriangleAlert } from 'lucide-react'

import { type DesktopV3NativeArtifactSummary } from '../../session-v3/artifact-v3-api'

export function DesktopV3ArtifactV3Sidebar({ artifacts, loading = false, error = '', embedded = false, onOpenArtifact }: {
  artifacts: DesktopV3NativeArtifactSummary[]
  loading?: boolean
  error?: string
  embedded?: boolean
  onOpenArtifact: (artifact: DesktopV3NativeArtifactSummary) => void
}) {
  return <aside aria-label="Artifact V3 session sidebar" data-testid="desktop-session-artifact-v3-sidebar" className={embedded ? 'w-full bg-[var(--app-bg-alt)] p-3' : 'min-h-0 w-full bg-[var(--app-bg-alt)] p-3'}>
    <header className="mb-2 flex items-center gap-2"><GitCommitHorizontal className="size-4 text-[var(--app-primary)]" /><div><h2 className="text-xs font-semibold">Artifact V3 projects</h2><p className="text-[9px] text-[var(--app-text-subtle)]">Complete Git revisions · {artifacts.length}</p></div></header>
    {loading && artifacts.length === 0 ? <div className="grid h-16 place-items-center"><Loader2 className="size-4 animate-spin text-[var(--app-primary)]" /></div> : null}
    {error && artifacts.length === 0 ? <p className="rounded-lg border border-[var(--app-danger)] bg-[var(--app-danger-bg)] p-2 text-[10px] text-[var(--app-danger)]">{error}</p> : null}
    <div className="grid gap-1.5">{artifacts.map((artifact) => <button key={artifact.artifactId} type="button" className="flex min-w-0 items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-2 text-left hover:border-[var(--app-border-active)] hover:bg-[var(--app-surface-hover)]" onClick={() => onOpenArtifact(artifact)} data-artifact-v3-sidebar-id={artifact.artifactId}><span className="grid size-7 shrink-0 place-items-center rounded-md bg-[var(--app-bg-alt)]">{artifact.status === 'failed' || artifact.status === 'unavailable' ? <TriangleAlert className="size-3.5 text-[var(--app-danger)]" /> : artifact.status === 'working' ? <Loader2 className="size-3.5 animate-spin text-[var(--app-primary)]" /> : <GitCommitHorizontal className="size-3.5 text-[var(--app-success)]" />}</span><span className="min-w-0 flex-1"><span className="block truncate text-[10px] font-semibold">{artifact.label}</span><span className="block truncate text-[9px] text-[var(--app-text-subtle)]">{artifact.head ? `${artifact.turnCount} turns · head ${artifact.head.commitOid.slice(0, 8)}` : 'Authoring first revision'} · {artifact.partCount} parts</span></span><span className="shrink-0 rounded-full bg-[var(--app-surface-active)] px-1.5 py-0.5 text-[8px] font-semibold">{artifact.status}</span></button>)}</div>
  </aside>
}
