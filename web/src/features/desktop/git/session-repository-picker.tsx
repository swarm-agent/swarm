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
    <div className="flex items-center justify-between gap-2"><strong>Session repositories</strong><button type="button" disabled={inventory.loading} onClick={onRefresh}>Refresh</button></div>
    {inventory.loading ? <p role="status">Loading repositories…</p> : null}
    {inventory.stale && inventory.items.length > 0 ? <p role="status">Stale inventory — operations disabled until refreshed.</p> : null}
    {inventory.error ? <p role="alert" className="break-words text-[var(--app-warning)]">{inventory.error}</p> : null}
    <div className="max-h-56 overflow-y-auto">
      {[...groups].map(([key, rows]) => <fieldset key={key} className="my-2 min-w-0 border-t border-[var(--app-border)]">
        <legend className="max-w-full break-all font-semibold">{rows[0].workspace_name || rows[0].source_path}</legend>
        <div className="break-all text-[10px] text-[var(--app-text-subtle)]">{rows[0].source_path}</div>
        {rows.map(row => <button type="button" key={repositoryKey(row)} aria-pressed={inventory.selectedKey === repositoryKey(row)} onClick={() => onSelect(repositoryKey(row))} className="my-1 block w-full rounded border border-[var(--app-border)] p-2 text-left aria-pressed:border-[var(--app-primary)]">
          <span className="block break-all">{row.kind} · {row.branch || row.status?.branch || 'No branch'}{row.default ? ' · Default' : ''}{row.attached ? ' · Attached' : ''}</span>
          <span className="block break-all text-[10px]">{row.workspace_path}</span>
          <span className="block break-all text-[10px]">Owner {row.session_id} · {row.lifecycle}{row.retained ? ' · Retained' : ''}</span>
          {row.availability !== 'available' ? <span className="block break-words">{row.availability}: {row.error || 'Status unavailable'}</span>
            : row.status?.has_git ? <span className="block">{row.status.staged_count} staged · {row.status.modified_count} unstaged · {row.status.untracked_count} untracked · {row.status.conflict_count} conflicts{row.files_truncated ? ' · File list truncated' : ''}</span>
              : <span>No Git repository</span>}
        </button>)}
      </fieldset>)}
    </div>
    {!inventory.loading && !inventory.error && inventory.items.length === 0 ? <p>No repositories returned.</p> : null}
    {inventory.selectedKey && !inventory.items.some(row => repositoryKey(row) === inventory.selectedKey) ? <p role="status">Selected repository is not in the loaded inventory. Load more or select a repository explicitly.</p> : null}
    {inventory.nextCursor ? <button type="button" disabled={inventory.loading || inventory.stale} onClick={onLoadMore}>Load more repositories</button> : null}
    <p className="break-words text-[10px] text-[var(--app-text-subtle)]">History: {inventory.historyCoverage || 'not loaded'}</p>
  </div>
}
