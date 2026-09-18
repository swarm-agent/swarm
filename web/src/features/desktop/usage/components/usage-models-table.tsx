import { useMemo, useState } from 'react'
import { ArrowUpDown, Search } from 'lucide-react'
import { Card } from '../../../../components/ui/card'
import { Input } from '../../../../components/ui/input'
import type { SessionUsageModelItem } from '../services/usage-api'

interface UsageModelsTableProps {
  models: SessionUsageModelItem[]
}

type SortField = 'tokens' | 'cost' | 'turns' | 'name'

export function UsageModelsTable({ models }: UsageModelsTableProps) {
  const [search, setSearch] = useState('')
  const [sortField, setSortField] = useState<SortField>('tokens')
  const [sortAsc, setSortAsc] = useState(false)

  const handleSort = (field: SortField) => {
    if (sortField === field) {
      setSortAsc(!sortAsc)
    } else {
      setSortField(field)
      setSortAsc(false)
    }
  }

  const filtered = useMemo(() => {
    let list = models
    if (search.trim()) {
      const q = search.toLowerCase().trim()
      list = list.filter(
        (m) =>
          m.model.toLowerCase().includes(q) ||
          m.display_name.toLowerCase().includes(q) ||
          m.provider.toLowerCase().includes(q)
      )
    }

    return [...list].sort((a, b) => {
      let cmp = 0
      if (sortField === 'tokens') cmp = b.total_tokens - a.total_tokens
      else if (sortField === 'cost') cmp = b.cost_usd - a.cost_usd
      else if (sortField === 'turns') cmp = b.turns - a.turns
      else if (sortField === 'name') cmp = a.display_name.localeCompare(b.display_name)
      return sortAsc ? -cmp : cmp
    })
  }, [models, search, sortField, sortAsc])

  return (
    <Card className="overflow-hidden p-0">
      {/* Search Header */}
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)] p-4">
        <div className="flex items-center gap-2">
          <h3 className="text-sm font-semibold text-[var(--app-text)]">Models Usage Breakdown</h3>
          <span className="rounded-full bg-[var(--app-surface-hover)] px-2 py-0.5 text-xs text-[var(--app-text-muted)]">
            {models.length} models
          </span>
        </div>

        <div className="relative w-full max-w-xs">
          <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
          <Input
            type="text"
            placeholder="Search model or provider..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-8 pl-8 text-xs"
          />
        </div>
      </div>

      {/* Table */}
      <div className="overflow-x-auto">
        <table className="w-full text-left text-xs">
          <thead className="border-b border-[var(--app-border)] bg-[var(--app-surface-hover)] text-[11px] font-semibold text-[var(--app-text-muted)]">
            <tr>
              <th className="py-2.5 pl-4 pr-3 cursor-pointer select-none" onClick={() => handleSort('name')}>
                <div className="flex items-center gap-1">
                  <span>Model / Provider</span>
                  <ArrowUpDown size={11} />
                </div>
              </th>
              <th className="px-3 py-2.5 cursor-pointer select-none text-right" onClick={() => handleSort('tokens')}>
                <div className="flex items-center justify-end gap-1">
                  <span>Total Tokens</span>
                  <ArrowUpDown size={11} />
                </div>
              </th>
              <th className="px-3 py-2.5 text-right">Uncached Input</th>
              <th className="px-3 py-2.5 text-right">Prompt Cache</th>
              <th className="px-3 py-2.5 text-right">Output</th>
              <th className="px-3 py-2.5 cursor-pointer select-none text-right" onClick={() => handleSort('turns')}>
                <div className="flex items-center justify-end gap-1">
                  <span>Turns</span>
                  <ArrowUpDown size={11} />
                </div>
              </th>
              <th className="py-2.5 pl-3 pr-4 cursor-pointer select-none text-right" onClick={() => handleSort('cost')}>
                <div className="flex items-center justify-end gap-1">
                  <span>Est. Cost</span>
                  <ArrowUpDown size={11} />
                </div>
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[var(--app-border)]">
            {filtered.length === 0 ? (
              <tr>
                <td colSpan={7} className="py-8 text-center text-xs text-[var(--app-text-muted)]">
                  No models found matching your search.
                </td>
              </tr>
            ) : (
              filtered.map((item) => {
                const isCodex = item.provider === 'codex'
                const uncached = Math.max(0, item.input_tokens - item.cached_tokens)
                const cachePct = item.input_tokens > 0 ? (item.cached_tokens / item.input_tokens) * 100 : 0

                return (
                  <tr key={item.provider + ':' + item.model} className="hover:bg-[var(--app-surface-hover)] transition-colors">
                    <td className="py-2.5 pl-4 pr-3">
                      <div className="font-medium text-[var(--app-text)]">{item.display_name || item.model}</div>
                      <div className="text-[10px] text-[var(--app-text-subtle)] font-mono">
                        {item.provider} · {item.model}
                      </div>
                    </td>
                    <td className="px-3 py-2.5 text-right font-mono font-medium text-[var(--app-text)]">
                      {item.total_tokens.toLocaleString()}
                    </td>
                    <td className="px-3 py-2.5 text-right font-mono text-[var(--app-text-muted)]">
                      {uncached.toLocaleString()}
                    </td>
                    <td className="px-3 py-2.5 text-right font-mono">
                      {item.cached_tokens > 0 ? (
                        <span className="text-emerald-600 dark:text-emerald-400">
                          {item.cached_tokens.toLocaleString()} ({cachePct.toFixed(0)}%)
                        </span>
                      ) : (
                        <span className="text-[var(--app-text-subtle)]">—</span>
                      )}
                    </td>
                    <td className="px-3 py-2.5 text-right font-mono text-[var(--app-text-muted)]">
                      {item.output_tokens.toLocaleString()}
                    </td>
                    <td className="px-3 py-2.5 text-right font-mono text-[var(--app-text-muted)]">
                      {item.turns.toLocaleString()}
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
