import { useMemo } from 'react'
import { CheckCircle2 } from 'lucide-react'
import { Card } from '../../../../components/ui/card'
import { Badge } from '../../../../components/ui/badge'
import type { SessionUsageProviderItem } from '../services/usage-api'

interface UsageProviderCardsProps {
  providers: SessionUsageProviderItem[]
  totalTokens: number
  selectedProvider?: string | null
  onSelectProvider?: (provider: string | null) => void
}

export function UsageProviderCards({
  providers,
  totalTokens,
  selectedProvider,
  onSelectProvider,
}: UsageProviderCardsProps) {
  const sorted = useMemo(() => {
    return [...providers].sort((a, b) => b.total_tokens - a.total_tokens)
  }, [providers])

  return (
    <div className="grid grid-cols-1 gap-3.5 sm:grid-cols-2 lg:grid-cols-3">
      {sorted.map((item) => {
        const isSelected = selectedProvider === item.provider
        const tokenShare = totalTokens > 0 ? (item.total_tokens / totalTokens) * 100 : 0
        const cacheShare = item.total_tokens > 0 ? (item.cached_tokens / item.total_tokens) * 100 : 0

        return (
          <Card
            key={item.provider}
            className={`cursor-pointer transition-all hover:border-[var(--app-primary)] ${
              isSelected ? 'ring-2 ring-[var(--app-primary)] bg-[var(--app-surface-hover)]' : ''
            } p-4`}
            onClick={() => onSelectProvider?.(isSelected ? null : item.provider)}
          >
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <h4 className="truncate text-sm font-semibold text-[var(--app-text)]">
                    {item.display_name}
                  </h4>
                  {isSelected && (
                    <CheckCircle2 size={14} className="text-[var(--app-primary)] shrink-0" />
                  )}
                </div>
                <p className="text-[11px] text-[var(--app-text-muted)]">
                  {item.sessions} {item.sessions === 1 ? 'session' : 'sessions'} · {item.turns} turns
                </p>
              </div>

              {item.is_subscription ? (
                <Badge tone="live" className="shrink-0 text-[10px]">
                  Subscription ($0 billed)
                </Badge>
              ) : (
                <span className="shrink-0 font-mono text-sm font-semibold text-emerald-600 dark:text-emerald-400">
                  ${item.cost_usd.toFixed(2)}
                </span>
              )}
            </div>

            {/* Token Share Progress Bar */}
            <div className="mt-3.5 space-y-1.5">
              <div className="flex justify-between text-[11px] text-[var(--app-text-muted)]">
                <span>Total Tokens</span>
                <span className="font-mono font-medium text-[var(--app-text)]">
                  {item.total_tokens.toLocaleString()} ({tokenShare.toFixed(1)}%)
                </span>
              </div>
              <div className="h-1.5 w-full overflow-hidden rounded-full bg-[var(--app-border)]">
                <div
                  className="h-full rounded-full bg-[var(--app-primary)] transition-all"
                  style={{ width: `${Math.min(100, Math.max(2, tokenShare))}%` }}
                />
              </div>
            </div>

            {/* Token Detail Metrics */}
            <div className="mt-3 grid grid-cols-2 gap-2 border-t border-[var(--app-border)] pt-2.5 text-[11px]">
              <div>
                <span className="text-[var(--app-text-muted)]">Prompt Cache:</span>
                <span className="ml-1 font-mono font-medium text-emerald-600 dark:text-emerald-400">
                  {cacheShare.toFixed(0)}%
                </span>
              </div>
              <div>
                <span className="text-[var(--app-text-muted)]">Output:</span>
                <span className="ml-1 font-mono font-medium text-[var(--app-text)]">
                  {item.output_tokens.toLocaleString()}
                </span>
              </div>
            </div>

            {/* Codex Nominal Cost Note */}
            {item.is_subscription && item.codex_nominal_cost_usd > 0 && (
              <p className="mt-2 text-[10px] text-[var(--app-text-subtle)]">
                Nominal API equivalent value: ~${item.codex_nominal_cost_usd.toFixed(2)}
              </p>
            )}

            {/* Models Pill List */}
            {item.models.length > 0 && (
              <div className="mt-2.5 flex flex-wrap gap-1">
                {item.models.slice(0, 3).map((m) => (
                  <span
                    key={m}
                    className="inline-block rounded bg-[var(--app-surface-hover)] px-1.5 py-0.5 font-mono text-[9.5px] text-[var(--app-text-muted)]"
                  >
                    {m}
                  </span>
                ))}
                {item.models.length > 3 && (
                  <span className="text-[9.5px] text-[var(--app-text-subtle)] self-center">
                    +{item.models.length - 3} more
                  </span>
                )}
              </div>
            )}
          </Card>
        )
      })}
    </div>
  )
}
