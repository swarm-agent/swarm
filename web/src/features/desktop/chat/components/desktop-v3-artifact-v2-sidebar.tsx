import { AlertTriangle, CheckCircle2, CircleDashed, GalleryHorizontal, Loader2, Sparkles } from 'lucide-react'

import { cn } from '../../../../lib/cn'
import { desktopV3ArtifactV2StateLabel, type DesktopV3ArtifactV2CatalogItem } from '../../session-v3/artifact-v2-api'
import { desktopV3ArtifactV2SidebarGroups } from '../../session-v3/artifact-v2-studio-model'

export interface DesktopV3ArtifactV2SidebarProps {
  artifacts: DesktopV3ArtifactV2CatalogItem[]
  loading?: boolean
  error?: string
  embedded?: boolean
  onOpenArtifact: (artifact: DesktopV3ArtifactV2CatalogItem) => void
}

const sectionLabels = { working: 'Working artifacts', invalid: 'Validation failures', iterations: 'Iteration rounds', published: 'Published heads', storyboards: 'Storyboard compositions', ready: 'Ready compositions' } as const
const sectionOrder = ['working', 'invalid', 'iterations', 'ready', 'published', 'storyboards'] as const

function StateIcon({ state }: { state: DesktopV3ArtifactV2CatalogItem['projection']['state'] }) {
  if (state === 'invalid') return <AlertTriangle className="size-4 text-[var(--app-danger)]" aria-hidden="true" />
  if (state === 'ready' || state === 'published_view') return <CheckCircle2 className="size-4 text-[var(--app-success)]" aria-hidden="true" />
  if (state === 'iterating') return <Sparkles className="size-4 text-[var(--app-primary)]" aria-hidden="true" />
  if (state === 'building' || state === 'validating') return <Loader2 className="size-4 animate-spin text-[var(--app-primary)]" aria-hidden="true" />
  return <CircleDashed className="size-4 text-[var(--app-text-muted)]" aria-hidden="true" />
}

export function DesktopV3ArtifactV2Sidebar({ artifacts, loading = false, error = '', embedded = false, onOpenArtifact }: DesktopV3ArtifactV2SidebarProps) {
  const groups = desktopV3ArtifactV2SidebarGroups(artifacts)
  return <aside aria-label="Artifact V2 session sidebar" data-testid="desktop-session-artifact-v2-sidebar" className={cn('min-h-0 min-w-0 bg-[var(--app-bg-alt)] text-[var(--app-text)]', embedded ? 'w-full' : 'hidden h-full w-[372px] max-w-[372px] flex-1 flex-col overflow-hidden border-l border-[var(--app-border)]/60 p-4 min-[1300px]:flex')}>
    <header className="flex shrink-0 items-center gap-2 pb-3"><GalleryHorizontal className="size-4 text-[var(--app-primary)]" aria-hidden="true" /><div><h2 className="text-sm font-semibold">Artifacts</h2><p className="text-[10px] text-[var(--app-text-subtle)]">Artifact V2 · {artifacts.length}</p></div></header>
    {loading && artifacts.length === 0 ? <div className="grid flex-1 place-items-center"><Loader2 className="size-5 animate-spin text-[var(--app-primary)]" aria-label="Loading Artifact V2 catalog" /></div> : null}
    {error && artifacts.length === 0 ? <p className="rounded-lg border border-[var(--app-danger)]/40 bg-[var(--app-danger-bg)] p-3 text-xs text-[var(--app-danger)]">{error}</p> : null}
    {!loading && !error && artifacts.length === 0 ? <p className="p-3 text-center text-xs text-[var(--app-text-muted)]">Working Designer artifacts appear here immediately.</p> : null}
    {groups.length > 0 ? <div className="min-h-0 flex-1 space-y-3 overflow-y-auto [scrollbar-gutter:stable]">{sectionOrder.map((section) => {
      const rows = groups.filter((group) => group.section === section); if (rows.length === 0) return null
      return <section key={section} data-artifact-v2-sidebar-section={section}><h3 className="mb-1.5 text-[10px] font-semibold uppercase tracking-wide text-[var(--app-text-subtle)]">{sectionLabels[section]}</h3><div className="grid gap-1.5">{rows.map((group) => <button key={group.key} type="button" className="flex min-w-0 items-center gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-2.5 text-left transition hover:border-[var(--app-border-active)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onClick={() => onOpenArtifact(group.item)} data-artifact-v2-id={group.item.working.id} data-artifact-v2-state={group.item.projection.state}><span className="grid size-8 shrink-0 place-items-center rounded-lg bg-[var(--app-bg-alt)]"><StateIcon state={group.item.projection.state} /></span><span className="min-w-0 flex-1"><span className="block truncate text-xs font-semibold">{group.label}</span><span className="block truncate text-[10px] text-[var(--app-text-subtle)]">{group.detail}</span></span><span className="shrink-0 rounded-full bg-[var(--app-surface-active)] px-1.5 py-0.5 text-[9px] font-semibold">{desktopV3ArtifactV2StateLabel(group.item.projection.state)}</span></button>)}</div></section>
    })}</div> : null}
  </aside>
}
