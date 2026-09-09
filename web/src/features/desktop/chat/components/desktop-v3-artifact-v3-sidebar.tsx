import { GitCommitHorizontal, Loader2, TriangleAlert } from 'lucide-react'
import { groupNativeArtifactSidebar } from '../../session-v3/artifact-v3-groups'
import { writeNativeArtifactNavigation } from '../../session-v3/artifact-v3-navigation'

import { artifactStatusLabel, type DesktopV3NativeArtifactSummary } from '../../session-v3/artifact-v3-api'

export function DesktopV3ArtifactV3Sidebar({ artifacts, loading = false, error = '', embedded = false, onOpenArtifact, onRetry }: {
  artifacts: DesktopV3NativeArtifactSummary[]
  onRetry?: () => void
  loading?: boolean
  error?: string
  embedded?: boolean
  onOpenArtifact: (artifact: DesktopV3NativeArtifactSummary) => void
}) {
  const groups = groupNativeArtifactSidebar(artifacts)
  const renderArtifact = (artifact: DesktopV3NativeArtifactSummary, waveId = '') => {
      const pending = artifact.pendingTurns ?? []
      const ready = pending.reduce((count, turn) => count + turn.candidates.filter((candidate) => candidate.status === 'ready' && candidate.revision?.status === 'ready').length, 0)
      return <button key={artifact.artifactId} type="button" className="flex w-full min-w-0 items-start gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-2 text-left hover:border-[var(--app-border-active)] hover:bg-[var(--app-surface-hover)]" onClick={() => { writeNativeArtifactNavigation({ artifactId: artifact.artifactId, revisionRef: '', waveId }); onOpenArtifact(artifact) }} data-artifact-v3-sidebar-id={artifact.artifactId} data-artifact-v3-id={artifact.artifactId}>
        <span className="grid size-7 shrink-0 place-items-center rounded-md bg-[var(--app-bg-alt)]">{artifact.status === 'error' || artifact.status === 'failed' || artifact.status === 'unavailable' ? <TriangleAlert className="size-3.5 text-[var(--app-danger)]" /> : artifact.status !== 'ready' ? <Loader2 className="size-3.5 animate-spin text-[var(--app-primary)]" /> : <GitCommitHorizontal className="size-3.5 text-[var(--app-success)]" />}</span>
        <span className="min-w-0 flex-1">
          <span className="block break-words text-[10px] font-semibold">{waveId ? `Option ${artifact.generations?.find((member) => member.waveId === waveId)?.index ?? ''} · ` : ''}{artifact.label}</span>
          <span className="block text-[9px] text-[var(--app-text-subtle)]">{artifact.turnCount} turns · {artifact.partCount} parts</span>
          {pending.length ? <span className="mt-1 block text-[9px] font-semibold text-[var(--app-primary)]" data-artifact-v3-pending>{pending.length} pending {pending.length === 1 ? 'turn' : 'turns'} · {ready ? `${ready} ${ready === 1 ? 'option' : 'options'} to review` : 'changes in progress'}<span className="block font-normal">{ready ? 'Open pending changes →' : 'Open turn progress →'}</span></span> : null}
        </span>
        <span className="shrink-0 rounded-full bg-[var(--app-surface-active)] px-1.5 py-0.5 text-[8px] font-semibold">{ready ? 'Review' : artifactStatusLabel(artifact)}</span>
      </button>
  }
  return <aside aria-label="Artifacts session sidebar" data-testid="desktop-session-artifact-v3-sidebar" className={embedded ? 'w-full bg-[var(--app-bg-alt)] p-3' : 'min-h-0 w-full bg-[var(--app-bg-alt)] p-3'}>
    <header className="mb-2 flex items-center gap-2"><GitCommitHorizontal className="size-4 text-[var(--app-primary)]" /><div><h2 className="text-xs font-semibold">Artifacts</h2><p className="text-[9px] text-[var(--app-text-subtle)]">{groups.length} {groups.length === 1 ? 'group' : 'groups'} · {artifacts.length} artifacts</p></div></header>
    {loading && artifacts.length === 0 ? <div className="grid h-16 place-items-center"><Loader2 className="size-4 animate-spin text-[var(--app-primary)]" /></div> : null}
    {error ? <p role="alert" className="rounded-lg border border-[var(--app-danger)] bg-[var(--app-danger-bg)] p-2 text-[10px] text-[var(--app-danger)]">{error}{onRetry ? <button type="button" className="ml-2 underline" onClick={onRetry}>Retry loading artifacts</button> : null}</p> : null}
    <div className="grid gap-1.5">{groups.map((group) => group.waveId ? <details key={group.key} data-artifact-v3-group={group.waveId} className="min-w-0 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)]">
      <summary className="cursor-pointer break-words p-3 text-xs font-semibold hover:bg-[var(--app-surface-hover)]">{group.artifacts[0]?.label}<span className="mt-1 block text-[10px] font-normal text-[var(--app-primary)]">{group.count} alternatives · Expand to compare</span></summary>
      <div className="grid gap-1.5 border-t border-[var(--app-border)] p-2">{group.artifacts.map((artifact) => renderArtifact(artifact, group.waveId))}</div>
    </details> : renderArtifact(group.artifacts[0]!))}</div>
  </aside>
}
