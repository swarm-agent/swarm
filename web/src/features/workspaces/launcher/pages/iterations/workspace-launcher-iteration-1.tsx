import { Folder, HardDrive, RefreshCw } from 'lucide-react'
import { Button } from '../../../../../components/ui/button'
import { Card } from '../../../../../components/ui/card'
import { SavedWorkspaceSection, DiscoveredDirectorySection } from './shared'
import type { WorkspaceLauncherIterationProps } from './types'

export function WorkspaceLauncherIteration1(props: WorkspaceLauncherIterationProps & { refreshing?: boolean; onRefresh?: () => void }) {
  return (
    <div className="grid gap-6">
      <Card className="flex flex-col gap-4 px-5 py-5 sm:px-6 lg:flex-row lg:items-start lg:justify-between">
        <div className="grid gap-2">
          <h2 className="text-xl font-semibold text-[var(--app-text)]">Workspace Launcher</h2>
          <p className="text-sm leading-6 text-[var(--app-text-muted)]">Browse folders on this computer, use one temporarily, or save it as a workspace.</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" onClick={props.onRefresh} disabled={props.refreshing}>
            <RefreshCw size={14} className={props.refreshing ? 'animate-spin' : undefined} />
            Refresh
          </Button>
          <span className="inline-flex min-h-9 items-center gap-2 rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 text-sm text-[var(--app-text-muted)]">
            <Folder size={14} /> {props.workspaces.length} saved
          </span>
          <span className="inline-flex min-h-9 items-center gap-2 rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 text-sm text-[var(--app-text-muted)]">
            <HardDrive size={14} /> {props.discovered.length} found
          </span>
        </div>
      </Card>

      <SavedWorkspaceSection
        {...props}
        title="Saved workspaces"
        description="Your saved workspaces. Drag to reorder or remove Swarm metadata without touching files on disk."
        columns={3}
      />
      <DiscoveredDirectorySection
        {...props}
        title="Folders on this computer"
        description="Signals like AGENTS.md, CLAUDE.md, and git help discovery, but you can also use a folder temporarily without saving it."
      />
    </div>
  )
}
