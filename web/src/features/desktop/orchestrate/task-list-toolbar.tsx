import React, { useEffect, useId, useRef, useState } from 'react'
import { Archive, GitBranch, Images, Plus, Search, X } from 'lucide-react'
import type { ProjectWorkspaceGit } from '../runtime/use-project-workspace-git'

export type TaskStatusFilter = 'all' | 'running' | 'needs_review' | 'queued' | 'completed' | 'media'
export type TaskSourceFilter = 'all' | 'worker'

export function WorkspaceGitState({ workspace }: { workspace: ProjectWorkspaceGit }) {
  const status = workspace.status
  if (workspace.error) return <span className="swarm-git-error" title={workspace.error}>Status unavailable</span>
  if (!status) return <span className="swarm-git-neutral">{workspace.loading ? 'Loading…' : 'Status unavailable'}</span>
  if (!status.has_git) return <span className="swarm-git-neutral">No Git repository</span>
  const danger = status.conflict_count > 0 || status.branch === 'detached'
  return <>
    {danger && <span className="swarm-git-dot swarm-git-danger" role="img" title={status.conflict_count > 0 ? 'Conflicts' : 'Detached HEAD'} aria-label={status.conflict_count > 0 ? 'Conflicts' : 'Detached HEAD'} />}
    {status.dirty_count > 0 && <span className="swarm-git-changes" aria-label={`${status.dirty_count} changed files`}><span className="swarm-git-dot" aria-hidden="true" />{status.dirty_count}</span>}
    {(status.ahead_count > 0 || status.behind_count > 0) && <span className="swarm-git-divergence" aria-label={`${status.ahead_count} ahead, ${status.behind_count} behind`}>{status.ahead_count > 0 && `↑${status.ahead_count}`}{status.ahead_count > 0 && status.behind_count > 0 && ' '}{status.behind_count > 0 && `↓${status.behind_count}`}</span>}
  </>
}

export function TaskListHeader({ workspaces, selectedWorkspace, onWorkspace, onRefreshGit, onNewTask }: {
  workspaces: ProjectWorkspaceGit[]
  selectedWorkspace: string | null
  onWorkspace: (key: string) => void
  onRefreshGit: () => void
  onNewTask: () => void
}) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const popover = useRef<HTMLDivElement>(null)
  const id = useId()
  useEffect(() => {
    if (!open) return
    popover.current?.focus()
    const outside = (event: PointerEvent) => { if (!root.current?.contains(event.target as Node)) setOpen(false) }
    const key = (event: KeyboardEvent) => {
      if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); setOpen(false); trigger.current?.focus() }
    }
    document.addEventListener('pointerdown', outside)
    document.addEventListener('keydown', key)
    return () => { document.removeEventListener('pointerdown', outside); document.removeEventListener('keydown', key) }
  }, [open])
  useEffect(() => { setOpen(false) }, [workspaces.map(workspace => workspace.key).join('|')])
  return <header className="swarm-task-list-header px-4 pt-4 pb-3">
    <div className="swarm-task-title-row">
      <h1>Swarm Local</h1>
      <button type="button" onClick={onNewTask} className="swarm-task-list-new-task flex shrink-0 items-center gap-1.5 rounded font-medium text-xs px-3 py-1.5"><Plus size={13} aria-hidden="true" />New task</button>
    </div>
    <div className="swarm-workspace-git" ref={root} onBlur={event => { if (!event.currentTarget.contains(event.relatedTarget)) setOpen(false) }}>
      <div className="swarm-workspace-git-row" onClick={event => { if (event.target === event.currentTarget) setOpen(value => !value) }}>
        {workspaces.slice(0, 3).map(workspace => <button key={workspace.key} type="button" className="swarm-workspace-chip" aria-pressed={selectedWorkspace === workspace.key} onClick={() => onWorkspace(workspace.key)} title={`Filter tasks in ${workspace.name}`}>
          <span className="swarm-workspace-name">{workspace.name}</span>
          <span className="swarm-git-branch"><GitBranch size={12} aria-hidden="true" />{workspace.status?.has_git ? workspace.status.branch || 'Branch unavailable' : '—'}</span>
          <WorkspaceGitState workspace={workspace} />
        </button>)}
        <button type="button" ref={trigger} className="swarm-workspace-more" aria-haspopup="dialog" aria-expanded={open} aria-controls={id} onClick={() => setOpen(value => !value)}>
          {workspaces.length > 3 && <span className="swarm-workspace-overflow">+{workspaces.length - 3} more · </span>}{workspaces.length} workspaces
        </button>
      </div>
      {open && <div id={id} role="dialog" aria-label="Workspace Git status" tabIndex={-1} ref={popover} className="swarm-workspace-popover">
        <div className="swarm-workspace-popover-heading"><strong>{workspaces.length} workspaces</strong><button type="button" onClick={onRefreshGit}>Refresh</button><button type="button" aria-label="Close workspace status" onClick={() => { setOpen(false); trigger.current?.focus() }}><X size={16} /></button></div>
        <div className="swarm-workspace-table-scroll"><table><thead><tr><th>Workspace</th><th>Branch</th><th>Ahead / behind</th><th>Changed files</th></tr></thead><tbody>
          {workspaces.map(workspace => {
            const status = workspace.status
            const available = status?.has_git && !workspace.error
            return <tr key={workspace.key} data-selected={selectedWorkspace === workspace.key} onClick={() => onWorkspace(workspace.key)}>
              <th scope="row"><button type="button" aria-pressed={selectedWorkspace === workspace.key} onClick={event => { event.stopPropagation(); onWorkspace(workspace.key) }}>{workspace.name}</button></th>
              <td className="swarm-git-branch-cell">{status?.has_git ? status.branch || 'Branch unavailable' : '—'}{available && (status.conflict_count > 0 || status.branch === 'detached') && <span className="swarm-git-dot swarm-git-danger" role="img" title={status.conflict_count > 0 ? 'Conflicts' : 'Detached HEAD'} aria-label={status.conflict_count > 0 ? 'Conflicts' : 'Detached HEAD'} />}</td>
              <td>{available ? `↑${status.ahead_count} ↓${status.behind_count}` : '—'}</td>
              <td>{available ? status.dirty_count : <WorkspaceGitState workspace={workspace} />}</td>
            </tr>
          })}
        </tbody></table></div>
        {workspaces.length === 0 && <p>No linked workspaces.</p>}
        <p>Choose a workspace to filter tasks; choose it again to clear.</p>
      </div>}
    </div>
  </header>
}

export function taskSearchShortcut(event: Pick<KeyboardEvent, 'key' | 'altKey' | 'ctrlKey' | 'metaKey' | 'defaultPrevented'>, active: Element | null): boolean {
  return event.key === '/' && !event.altKey && !event.ctrlKey && !event.metaKey && !event.defaultPrevented
    && !active?.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"]')
}
function TaskSearch({ search, onSearch }: { search: string; onSearch: (value: string) => void }) {
  const input = useRef<HTMLInputElement>(null)
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if (taskSearchShortcut(event, document.activeElement)) { event.preventDefault(); input.current?.focus() }
    }
    document.addEventListener('keydown', key)
    return () => document.removeEventListener('keydown', key)
  }, [])
  return <div className="swarm-task-search">
    <Search size={15} aria-hidden="true" />
    <input ref={input} type="search" value={search} onChange={event => onSearch(event.target.value)} placeholder="Search tasks" aria-label="Search tasks" aria-keyshortcuts="/" />
    <kbd aria-hidden="true">/</kbd>
  </div>
}

export function TaskListToolbar({ search, onSearch, source, onSource, status, onStatus, counts, mediaCount = 0, mediaCountIncomplete = false, total, selected, busy, integrationEligible = 0, integrationDisabled = false, onIntegrate, onSelectAll, onClear, onArchive, onDelete, onArchived, archivedRef }: {
  search: string; onSearch: (value: string) => void
  source: TaskSourceFilter; onSource: (value: TaskSourceFilter) => void
  status: TaskStatusFilter; onStatus: (value: TaskStatusFilter) => void
  counts: Record<Exclude<TaskStatusFilter, 'media'>, number>
  mediaCount?: number; mediaCountIncomplete?: boolean
  total: number; selected: number; busy: boolean
  integrationEligible?: number; integrationDisabled?: boolean; onIntegrate?: () => void
  onSelectAll: () => void; onClear: () => void; onArchive: () => void; onDelete: () => void; onArchived: () => void
  archivedRef?: React.Ref<HTMLButtonElement>
}) {
  const tabs = [
    { value: 'all', label: 'All', dot: '' },
    { value: 'running', label: 'Running', dot: 'running' },
    { value: 'needs_review', label: 'Review', dot: 'review' },
    { value: 'queued', label: 'Queued', dot: 'queued' },
    { value: 'completed', label: 'Done', dot: 'done' },
    { value: 'media', label: 'Media', dot: '' },
  ] as const
  const outline = 'swarm-outline-action flex items-center gap-1.5 rounded px-2.5 py-1 text-xs'
  return <div className="swarm-task-list-toolbar shrink-0 min-w-0 px-4">
    <div className="swarm-task-search-row">
      <TaskSearch search={search} onSearch={onSearch} />
      <div role="group" aria-label="Task scope" className="swarm-task-source-control">
        {([{ value: 'all', label: 'All' }, { value: 'worker', label: 'Workers' }] as const).map(item => <button key={item.value} type="button" data-testid={item.value === 'all' ? 'filter-all-tasks' : 'filter-worker-tasks'} aria-pressed={source === item.value} onClick={() => onSource(item.value)} className="swarm-task-source-filter">{item.label}</button>)}
      </div>
    </div>
    <div className="swarm-task-tabs-row">
      <div role="tablist" aria-label="Task status filter" className="swarm-task-tabs">
        {tabs.map(tab => {
          const count = tab.value === 'media' ? mediaCount : counts[tab.value]
          return <button key={tab.value} type="button" role="tab" aria-selected={status === tab.value} tabIndex={status === tab.value ? 0 : -1} onClick={() => onStatus(tab.value)} onKeyDown={event => {
            const index = tabs.findIndex(item => item.value === tab.value)
            const next = event.key === 'ArrowRight' ? (index + 1) % tabs.length : event.key === 'ArrowLeft' ? (index + tabs.length - 1) % tabs.length : event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : -1
            if (next >= 0) { event.preventDefault(); onStatus(tabs[next].value); event.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]')[next]?.focus() }
          }} className="swarm-task-tab">
            {tab.dot && <span aria-hidden="true" className={`swarm-task-tab-dot ${tab.dot}`} />}{tab.value === 'media' && <Images size={13} aria-hidden="true" />}{tab.label}
            <span className="swarm-task-count" data-zero={count === 0} title={tab.value === 'media' ? 'Loaded active media cards' : undefined}>{count}{tab.value === 'media' && mediaCountIncomplete ? '+' : ''}</span>
          </button>
        })}
      </div>
      <button type="button" ref={archivedRef} onClick={onArchived} className="swarm-task-archived"><Archive size={13} aria-hidden="true" />Archived</button>
    </div>
    {status !== 'media' && <div aria-label="Task management" className="swarm-task-management min-h-11 flex flex-wrap items-center justify-between gap-2 py-2">
      <div className="flex flex-wrap items-center gap-3 min-w-0">
        <label className="swarm-task-select-all"><input type="checkbox" checked={selected > 0} disabled={busy || total === 0} onChange={selected > 0 ? onClear : onSelectAll} />{selected > 0 ? 'Clear selection' : 'Select all'}</label>
        <span className="text-[11px] text-slate-400" role="status">{selected > 0 ? `${selected} selected` : `${total} tasks`}</span>
      </div>
      {selected > 0 && <div className="flex flex-wrap items-center gap-2 min-w-0">
        <button type="button" disabled={busy || integrationDisabled || integrationEligible === 0} onClick={onIntegrate} className={outline}>Integrate selected ({integrationEligible})</button>
        <button type="button" disabled={busy} onClick={onArchive} className={outline}>Archive</button>
        <button type="button" disabled={busy} onClick={onDelete} className="swarm-task-delete">Delete</button>
      </div>}
    </div>}
  </div>
}
