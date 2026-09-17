import { useMemo, useState } from 'react'
import { ArrowUpRight, MessageSquare, Search } from 'lucide-react'
import { Card } from '../../../../components/ui/card'
import { Input } from '../../../../components/ui/input'
import type { SessionUsageSessionItem } from '../services/usage-api'

interface UsageSessionsTableProps {
  sessions: SessionUsageSessionItem[]
  onOpenSession?: (sessionId: string) => void
}

export function UsageSessionsTable({ sessions, onOpenSession }: UsageSessionsTableProps) {
  const [search, setSearch] = useState('')

  const filtered = useMemo(() => {
    if (!search.trim()) return sessions
    const q = search.toLowerCase().trim()
    return sessions.filter(
      (s) =>
        s.title.toLowerCase().includes(q) ||
        s.session_id.toLowerCase().includes(q) ||
        s.model.toLowerCase().includes(q) ||
        s.provider.toLowerCase().includes(q)
    )
  }, [sessions, search])

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
        <div className="flex items-center gap-2">
          <h3 className="text-sm font-semibold text-[var(--app-text)]">Active Sessions Usage</h3>
          <span className="rounded-full bg-[var(--app-surface-hover)] px-2 py-0.5 text-xs text-[var(--app-text-muted)]">
            {sessions.length} sessions
          </span>
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
