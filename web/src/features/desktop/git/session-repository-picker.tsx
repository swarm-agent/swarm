import type { SessionRepository } from './types'
import { repositoryGroupKey, repositoryKey, type RepositoryInventoryState } from '../state/session-repositories'

export function SessionRepositoryPicker({ inventory, onSelect, onRefresh, onLoadMore }: {
  inventory: RepositoryInventoryState
  onSelect: (key: string) => void
  onRefresh: () => void
  onLoadMore: () => void
}) {
  const groups = new Map<string, SessionRepository[]>()
  for (const row of inventory.items) {
    const key = repositoryGroupKey(row)
    groups.set(key, [...(groups.get(key) || []), row])
  }
  return <div data-testid="session-repository-inventory" className="mb-2 min-h-0 shrink-0 text-xs">
    <div className="flex items-center justify-between gap-2"><strong>Git repositories</strong><button type="button" disabled={inventory.loading} onClick={onRefresh}>Refresh</button></div>
    {inventory.loading && !inventory.items.length ? <p role="status">Loading repositories…</p> : null}
    {inventory.stale && inventory.error && inventory.items.length > 0 ? <p role="status">Git status could not be updated. Refresh before making changes.</p> : null}
    {inventory.error ? <p role="alert" className="break-words text-[var(--app-warning)]">{inventory.error}</p> : null}
    <div className="mb-2 border-b border-[var(--app-border)] pb-2">
      {[...groups].map(([key, rows]) => <fieldset key={key} className="my-2 min-w-0 border-t border-[var(--app-border)]">
        <legend className="max-w-full break-all font-semibold">{rows[0].workspace_name || rows[0].source_path}</legend>
        <div className="break-all text-[10px] text-[var(--app-text-subtle)]">{rows[0].source_path}</div>
        {rows.map(row => <button type="button" key={repositoryKey(row)} aria-pressed={inventory.selectedKey === repositoryKey(row)} onClick={() => onSelect(repositoryKey(row))} className="my-1 block w-full rounded border border-[var(--app-border)] p-2 text-left aria-pressed:border-[var(--app-primary)]">
          <span className="flex flex-wrap items-center justify-between gap-1 font-medium"><span>{row.branch || row.status?.branch || 'No branch'}</span><span className="text-[10px] text-[var(--app-text-muted)]">{row.kind === 'parent' ? 'Session worktree' : row.kind === 'source' ? 'Source checkout' : row.kind === 'worker' ? 'Worker worktree' : 'Repository lane'}</span></span>
          <span title={row.workspace_path} className="block truncate text-[10px] text-[var(--app-text-muted)]">{row.workspace_path}</span>
          {row.kind === 'worker' ? <span className="block text-[10px] text-[var(--app-text-muted)]">{row.lifecycle}{row.retained ? ' · Retained' : ''}</span> : null}
          {row.availability !== 'available' ? <span className="block break-words">{row.availability}: {row.error || 'Status unavailable'}</span>
            : row.status?.has_git ? <span className="block">{row.status.staged_count} staged · {row.status.modified_count} unstaged · {row.status.untracked_count} untracked · {row.status.conflict_count} conflicts{row.files_truncated ? ' · File list truncated' : ''}</span>
              : <span>No Git repository</span>}
        </button>)}
      </fieldset>)}
    </div>
    {!inventory.loading && !inventory.error && inventory.items.length === 0 ? <p>No repositories returned.</p> : null}
    {inventory.selectedKey && !inventory.items.some(row => repositoryKey(row) === inventory.selectedKey) ? <p role="status">Selected repository is not in the loaded inventory. Load more or select a repository explicitly.</p> : null}
    
    {inventory.nextCursor ? <><p role="status">Partial inventory — more repositories remain.</p><button type="button" disabled={inventory.loading || inventory.stale} onClick={onLoadMore}>Load more repositories</button></> : null}
    
  </div>
}
