import { useMemo, useState } from 'react'
import { Archive, ArrowUpRight, MessageSquare, Search } from 'lucide-react'
import { Card } from '../../../../components/ui/card'
import { Input } from '../../../../components/ui/input'
import type { SessionUsageSessionItem } from '../services/usage-api'

interface UsageSessionsTableProps {
  sessions: SessionUsageSessionItem[]
  onOpenSession?: (sessionId: string) => void
  title?: string
}

export function UsageSessionsTable({ sessions, onOpenSession, title = 'Sessions Usage' }: UsageSessionsTableProps) {
  const [search, setSearch] = useState('')
  const [statusFilter, setStatusFilter] = useState<'all' | 'active' | 'archived'>('all')

  const counts = useMemo(() => {
    let active = 0
    let archived = 0
    for (const s of sessions) {
      if (s.archived) {
        archived++
      } else {
        active++
      }
    }
    return { all: sessions.length, active, archived }
  }, [sessions])

  const filtered = useMemo(() => {
    let result = sessions
    if (statusFilter === 'active') {
      result = result.filter((s) => !s.archived)
    } else if (statusFilter === 'archived') {
      result = result.filter((s) => s.archived)
    }
    if (!search.trim()) return result
    const q = search.toLowerCase().trim()
    return result.filter(
      (s) =>
        s.title.toLowerCase().includes(q) ||
        s.session_id.toLowerCase().includes(q) ||
        s.model.toLowerCase().includes(q) ||
        s.provider.toLowerCase().includes(q)
    )
  }, [sessions, statusFilter, search])

  const formatRelativeTime = (ts: number): string => {
    if (!ts) return ''
    const diff = Date.now() - ts
    if (diff < 60_000) return 'just now'
    if (diff < 3600_000) return `${Math.floor(diff / 60_000)}m ago`
    if (diff < 86400_000) return `${Math.floor(diff / 3600_000)}h ago`
    return `${Math.floor(diff / 86400_000)}d ago`
  }

  return (
    <Card className="overflow-hidden p-0">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)] p-4">
        <div className="flex flex-wrap items-center gap-3">
          <div className="flex items-center gap-2">
            <h3 className="text-sm font-semibold text-[var(--app-text)]">{title}</h3>
            <span className="rounded-full bg-[var(--app-surface-hover)] px-2 py-0.5 text-xs text-[var(--app-text-muted)]">
              {filtered.length === sessions.length
                ? `${sessions.length} sessions`
                : `${filtered.length} of ${sessions.length}`}
            </span>
          </div>

          {/* Status filter buttons: All, Active, Archived */}
          <div className="inline-flex rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-0.5 text-xs font-medium">
            <button
              type="button"
              onClick={() => setStatusFilter('all')}
              className={`rounded-md px-2.5 py-1 transition-colors ${
                statusFilter === 'all'
                  ? 'bg-[var(--app-primary)] text-[var(--app-primary-foreground)]'
                  : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
              }`}
            >
              All ({counts.all})
            </button>
            <button
              type="button"
              onClick={() => setStatusFilter('active')}
              className={`rounded-md px-2.5 py-1 transition-colors ${
                statusFilter === 'active'
                  ? 'bg-[var(--app-primary)] text-[var(--app-primary-foreground)]'
                  : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
              }`}
            >
              Active ({counts.active})
            </button>
            <button
              type="button"
              onClick={() => setStatusFilter('archived')}
              className={`rounded-md px-2.5 py-1 transition-colors flex items-center gap-1 ${
                statusFilter === 'archived'
                  ? 'bg-[var(--app-primary)] text-[var(--app-primary-foreground)]'
                  : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
              }`}
            >
              <Archive size={11} className="opacity-70" />
              Archived ({counts.archived})
            </button>
          </div>
        </div>

        <div className="relative w-full max-w-xs">
          <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
          <Input
            type="text"
            placeholder="Search session title or ID..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-8 pl-8 text-xs"
          />
        </div>
      </div>

      <div className="overflow-x-auto">
        <table className="w-full text-left text-xs">
          <thead className="border-b border-[var(--app-border)] bg-[var(--app-surface-hover)] text-[11px] font-semibold text-[var(--app-text-muted)]">
            <tr>
              <th className="py-2.5 pl-4 pr-3">Session</th>
              <th className="px-3 py-2.5">Active Model</th>
              <th className="px-3 py-2.5 text-right">Tokens</th>
              <th className="px-3 py-2.5 text-right">Prompt Cache</th>
              <th className="px-3 py-2.5 text-right">Turns</th>
              <th className="px-3 py-2.5 text-right">Last Active</th>
              <th className="py-2.5 pl-3 pr-4 text-right">Est. Cost</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[var(--app-border)]">
            {filtered.length === 0 ? (
              <tr>
                <td colSpan={7} className="py-8 text-center text-xs text-[var(--app-text-muted)]">
                  No sessions found matching your search.
                </td>
              </tr>
            ) : (
              filtered.map((item) => {
                const isCodex = item.provider === 'codex'
                const cachePct = item.input_tokens > 0 ? (item.cached_tokens / item.input_tokens) * 100 : 0

                return (
                  <tr
                    key={item.session_id}
                    className="hover:bg-[var(--app-surface-hover)] transition-colors cursor-pointer group"
                    onClick={() => onOpenSession?.(item.session_id)}
                  >
                    <td className="py-2.5 pl-4 pr-3">
                      <div className="flex items-center gap-2">
                        <MessageSquare size={13} className="text-[var(--app-text-muted)] shrink-0" />
                        <span className="font-medium text-[var(--app-text)] group-hover:text-[var(--app-primary)] transition-colors truncate max-w-xs">
                          {item.title}
                        </span>
                        {item.archived && (
                          <span className="inline-flex items-center gap-1 rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.2 text-[10px] font-medium text-amber-600 dark:text-amber-400 shrink-0">
                            <Archive size={10} />
                            Archived
                          </span>
                        )}
                        <ArrowUpRight
                          size={12}
                          className="text-[var(--app-text-subtle)] opacity-0 group-hover:opacity-100 transition-opacity"
                        />
                      </div>
                      <div className="text-[10px] font-mono text-[var(--app-text-subtle)] pl-5">
                        {item.session_id.slice(0, 16)}...
                      </div>
                    </td>
                    <td className="px-3 py-2.5">
                      <div className="font-mono text-[11px] text-[var(--app-text)]">{item.model}</div>
                      <div className="text-[10px] text-[var(--app-text-subtle)] capitalize">{item.provider}</div>
                    </td>
                    <td className="px-3 py-2.5 text-right font-mono font-medium text-[var(--app-text)]">
                      {item.total_tokens.toLocaleString()}
                    </td>
                    <td className="px-3 py-2.5 text-right font-mono">
                      {item.cached_tokens > 0 ? (
                        <span className="text-emerald-600 dark:text-emerald-400">
                          {cachePct.toFixed(0)}%
                        </span>
                      ) : (
                        <span className="text-[var(--app-text-subtle)]">—</span>
                      )}
                    </td>
                    <td className="px-3 py-2.5 text-right font-mono text-[var(--app-text-muted)]">
                      {item.turn_count}
                    </td>
                    <td className="px-3 py-2.5 text-right text-[var(--app-text-muted)]">
                      {formatRelativeTime(item.last_active_at)}
                    </td>
                    <td className="py-2.5 pl-3 pr-4 text-right font-mono font-medium">
                      {isCodex ? (
                        <span className="text-[10.5px] text-[var(--app-text-muted)] bg-[var(--app-surface-hover)] px-2 py-0.5 rounded">
                          $0 (sub)
                        </span>
                      ) : item.cost_usd > 0 ? (
                        <span className="text-emerald-600 dark:text-emerald-400">
                          ${item.cost_usd >= 1 ? item.cost_usd.toFixed(2) : item.cost_usd.toFixed(4)}
                        </span>
                      ) : (
                        <span className="text-[var(--app-text-subtle)]">$0.00</span>
                      )}
                    </td>
                  </tr>
                )
              })
            )}
          </tbody>
        </table>
      </div>
    </Card>
  )
}
