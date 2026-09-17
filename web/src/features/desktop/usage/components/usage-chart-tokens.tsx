import { useMemo, useState } from 'react'
import type { SessionUsageDailyItem } from '../services/usage-api'

interface UsageChartTokensProps {
  daily: SessionUsageDailyItem[]
  selectedDate?: string | null
  onSelectDate?: (date: string | null) => void
}

type ChartMetric = 'stacked' | 'tokens' | 'cost'

export function UsageChartTokens({ daily, selectedDate, onSelectDate }: UsageChartTokensProps) {
  const [metric, setMetric] = useState<ChartMetric>('stacked')
  const [hoveredIndex, setHoveredIndex] = useState<number | null>(null)

  const items = useMemo(() => {
    if (!daily || daily.length === 0) return []
    return daily
  }, [daily])

  const maxVal = useMemo(() => {
    if (items.length === 0) return 1
    if (metric === 'cost') {
      const maxCost = Math.max(...items.map((d) => d.cost_usd + (d.media_cost_usd || 0)))
      return maxCost > 0 ? maxCost : 1
    }
    const maxTokens = Math.max(...items.map((d) => d.total_tokens))
    return maxTokens > 0 ? maxTokens : 1000
  }, [items, metric])

  const formatNumber = (num: number): string => {
    if (num >= 1_000_000) return `${(num / 1_000_000).toFixed(1)}M`
    if (num >= 1_000) return `${(num / 1_000).toFixed(0)}K`
    return num.toLocaleString()
  }

  const formatCost = (val: number): string => {
    if (val >= 100) return `$${val.toFixed(0)}`
    if (val >= 1) return `$${val.toFixed(2)}`
    return `$${val.toFixed(4)}`
  }

  // Chart layout dimensions
  const chartHeight = 220
  const chartWidth = 720
  const padding = { top: 20, right: 16, bottom: 32, left: 54 }
  const innerWidth = chartWidth - padding.left - padding.right
  const innerHeight = chartHeight - padding.top - padding.bottom

  const barWidth = useMemo(() => {
    if (items.length === 0) return 12
    const available = innerWidth / items.length
    return Math.max(4, Math.min(28, available * 0.7))
  }, [innerWidth, items.length])

  const hoveredItem = hoveredIndex !== null && items[hoveredIndex] ? items[hoveredIndex] : null

  return (
    <div className="space-y-4">
      {/* Chart Control Bar */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2 text-xs">
          <span className="font-medium text-[var(--app-text)]">View:</span>
          <div className="inline-flex rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-0.5">
            <button
              type="button"
              onClick={() => setMetric('stacked')}
              className={`rounded-md px-2.5 py-1 text-xs font-medium transition-colors ${
                metric === 'stacked'
                  ? 'bg-[var(--app-primary)] text-[var(--app-primary-foreground)]'
                  : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
              }`}
            >
              Stacked Breakdown
            </button>
            <button
              type="button"
              onClick={() => setMetric('tokens')}
              className={`rounded-md px-2.5 py-1 text-xs font-medium transition-colors ${
                metric === 'tokens'
                  ? 'bg-[var(--app-primary)] text-[var(--app-primary-foreground)]'
                  : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
              }`}
            >
              Total Tokens
            </button>
            <button
              type="button"
              onClick={() => setMetric('cost')}
              className={`rounded-md px-2.5 py-1 text-xs font-medium transition-colors ${
                metric === 'cost'
                  ? 'bg-[var(--app-primary)] text-[var(--app-primary-foreground)]'
                  : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
              }`}
            >
              Estimated Cost ($)
            </button>
          </div>
        </div>

        {/* Legend */}
        {metric === 'stacked' && (
          <div className="flex flex-wrap items-center gap-3 text-[11px] text-[var(--app-text-muted)]">
            <div className="flex items-center gap-1.5">
              <span className="size-2.5 rounded-sm bg-[#6366f1]" />
              <span>Uncached Input</span>
            </div>
            <div className="flex items-center gap-1.5">
              <span className="size-2.5 rounded-sm bg-[#10b981]" />
              <span>Cached Input</span>
            </div>
            <div className="flex items-center gap-1.5">
              <span className="size-2.5 rounded-sm bg-[#f59e0b]" />
              <span>Output</span>
            </div>
            <div className="flex items-center gap-1.5">
              <span className="size-2.5 rounded-sm bg-[#a855f7]" />
              <span>Thinking</span>
            </div>
          </div>
        )}
      </div>

      {/* SVG Canvas */}
      <div className="relative overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
        {items.length === 0 ? (
          <div className="flex h-[220px] items-center justify-center text-xs text-[var(--app-text-muted)]">
            No session usage data recorded for this time range.
          </div>
        ) : (
          <div className="relative w-full">
            <svg
              viewBox={`0 0 ${chartWidth} ${chartHeight}`}
              className="w-full overflow-visible"
              preserveAspectRatio="xMidYMid meet"
              onMouseLeave={() => setHoveredIndex(null)}
            >
              <defs>
                <linearGradient id="tokenLineGrad" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="#6366f1" stopOpacity="0.3" />
                  <stop offset="100%" stopColor="#6366f1" stopOpacity="0.0" />
                </linearGradient>
                <linearGradient id="costLineGrad" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="#10b981" stopOpacity="0.3" />
                  <stop offset="100%" stopColor="#10b981" stopOpacity="0.0" />
                </linearGradient>
              </defs>

              {/* Horizontal Gridlines & Y Labels */}
              {[0, 0.25, 0.5, 0.75, 1].map((ratio) => {
                const y = padding.top + innerHeight * (1 - ratio)
                const val = maxVal * ratio
                const label = metric === 'cost' ? formatCost(val) : formatNumber(val)
                return (
                  <g key={ratio}>
                    <line
                      x1={padding.left}
                      y1={y}
                      x2={chartWidth - padding.right}
                      y2={y}
                      stroke="var(--app-border)"
                      strokeDasharray="3 3"
                      strokeOpacity={0.6}
                    />
                    <text
                      x={padding.left - 8}
                      y={y + 3.5}
                      textAnchor="end"
                      fontSize="10"
                      fill="var(--app-text-muted)"
                    >
                      {label}
                    </text>
                  </g>
                )
              })}

              {/* Bars or Curves */}
              {items.map((item, idx) => {
                const x = padding.left + (idx + 0.5) * (innerWidth / items.length)
                const barX = x - barWidth / 2
                const isHovered = hoveredIndex === idx
                const isSelected = selectedDate === item.date

                if (metric === 'cost') {
                  const dayCost = item.cost_usd + (item.media_cost_usd || 0)
                  const height = Math.max(2, (dayCost / maxVal) * innerHeight)
                  const y = padding.top + innerHeight - height
                  return (
                    <g
                      key={item.date}
                      className="cursor-pointer transition-opacity"
                      onMouseEnter={() => setHoveredIndex(idx)}
                      onClick={() => onSelectDate?.(isSelected ? null : item.date)}
                    >
                      <rect
                        x={barX}
                        y={y}
                        width={barWidth}
                        height={height}
                        rx={barWidth > 8 ? 3 : 1.5}
                        fill="#10b981"
                        opacity={isHovered || isSelected ? 1 : 0.8}
                      />
                      {isSelected && (
                        <rect
                          x={barX - 2}
                          y={y - 2}
                          width={barWidth + 4}
                          height={height + 4}
                          rx={4}
                          fill="none"
                          stroke="var(--app-primary)"
                          strokeWidth="2"
                        />
                      )}
                    </g>
                  )
                }

                if (metric === 'tokens') {
                  const height = Math.max(2, (item.total_tokens / maxVal) * innerHeight)
                  const y = padding.top + innerHeight - height
                  return (
                    <g
                      key={item.date}
                      className="cursor-pointer transition-opacity"
                      onMouseEnter={() => setHoveredIndex(idx)}
                      onClick={() => onSelectDate?.(isSelected ? null : item.date)}
                    >
                      <rect
                        x={barX}
                        y={y}
                        width={barWidth}
                        height={height}
                        rx={barWidth > 8 ? 3 : 1.5}
                        fill="#6366f1"
                        opacity={isHovered || isSelected ? 1 : 0.8}
                      />
                      {isSelected && (
                        <rect
                          x={barX - 2}
                          y={y - 2}
                          width={barWidth + 4}
                          height={height + 4}
                          rx={4}
                          fill="none"
                          stroke="var(--app-primary)"
                          strokeWidth="2"
                        />
                      )}
                    </g>
                  )
                }

                // Stacked bar view:
                // Stack order: Uncached Input (bottom), Cached Input, Thinking, Output (top)
                const uncachedInput = Math.max(0, item.input_tokens - item.cached_tokens)
                const uncachedH = (uncachedInput / maxVal) * innerHeight
                const cachedH = (item.cached_tokens / maxVal) * innerHeight
                const thinkingH = (item.thinking_tokens / maxVal) * innerHeight
                const outputH = (item.output_tokens / maxVal) * innerHeight

                let currentY = padding.top + innerHeight

                const uncachedY = currentY - uncachedH
                currentY -= uncachedH

                const cachedY = currentY - cachedH
                currentY -= cachedH

                const thinkingY = currentY - thinkingH
                currentY -= thinkingH

                const outputY = currentY - outputH

                return (
                  <g
                    key={item.date}
                    className="cursor-pointer transition-opacity"
                    onMouseEnter={() => setHoveredIndex(idx)}
                    onClick={() => onSelectDate?.(isSelected ? null : item.date)}
                  >
                    {/* Uncached Input Bar */}
                    {uncachedH > 0 && (
                      <rect
                        x={barX}
                        y={uncachedY}
                        width={barWidth}
                        height={Math.max(1, uncachedH)}
                        fill="#6366f1"
                        opacity={isHovered || isSelected ? 1 : 0.85}
                      />
                    )}
                    {/* Cached Input Bar */}
                    {cachedH > 0 && (
                      <rect
                        x={barX}
                        y={cachedY}
                        width={barWidth}
                        height={Math.max(1, cachedH)}
                        fill="#10b981"
                        opacity={isHovered || isSelected ? 1 : 0.85}
                      />
                    )}
                    {/* Thinking Bar */}
                    {thinkingH > 0 && (
                      <rect
                        x={barX}
                        y={thinkingY}
                        width={barWidth}
                        height={Math.max(1, thinkingH)}
                        fill="#a855f7"
                        opacity={isHovered || isSelected ? 1 : 0.85}
                      />
                    )}
                    {/* Output Bar */}
                    {outputH > 0 && (
                      <rect
                        x={barX}
                        y={outputY}
                        width={barWidth}
                        height={Math.max(1, outputH)}
                        rx={barWidth > 8 ? 2 : 1}
                        fill="#f59e0b"
                        opacity={isHovered || isSelected ? 1 : 0.85}
                      />
                    )}
                    {isSelected && (
                      <rect
                        x={barX - 2}
                        y={outputY - 2}
                        width={barWidth + 4}
                        height={padding.top + innerHeight - outputY + 4}
                        rx={3}
                        fill="none"
                        stroke="var(--app-primary)"
                        strokeWidth="2"
                      />
                    )}
                  </g>
                )
              })}

              {/* X Axis Date Labels (Sampled to prevent overlapping) */}
              {items.map((item, idx) => {
                const step = Math.max(1, Math.floor(items.length / 8))
                if (idx % step !== 0 && idx !== items.length - 1) return null
                const x = padding.left + (idx + 0.5) * (innerWidth / items.length)
                const label = item.date.slice(5) // MM-DD
                return (
                  <text
                    key={item.date}
                    x={x}
                    y={chartHeight - 10}
                    textAnchor="middle"
                    fontSize="9.5"
                    fill="var(--app-text-subtle)"
                  >
                    {label}
                  </text>
                )
              })}
            </svg>

            {/* Hover Tooltip Overlay */}
            {hoveredItem && (
              <div
                className="pointer-events-none absolute z-20 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-3 shadow-lg backdrop-blur-sm"
                style={{
                  top: 10,
                  left: Math.min(
                    chartWidth - 220,
                    Math.max(
                      10,
                      padding.left + ((hoveredIndex ?? 0) + 0.5) * (innerWidth / items.length) - 90
                    )
                  ),
                }}
              >
                <div className="flex items-center justify-between gap-4 border-b border-[var(--app-border)] pb-1.5 text-xs font-semibold text-[var(--app-text)]">
                  <span>{hoveredItem.date}</span>
                  <span className="text-[var(--app-primary)]">{hoveredItem.turns} turns</span>
                </div>
                <div className="mt-2 space-y-1 text-xs">
                  <div className="flex justify-between gap-4 text-[var(--app-text-muted)]">
                    <span>Total Tokens:</span>
                    <span className="font-mono font-medium text-[var(--app-text)]">
                      {hoveredItem.total_tokens.toLocaleString()}
                    </span>
                  </div>
                  <div className="flex justify-between gap-4 text-[#6366f1]">
                    <span>Uncached Input:</span>
                    <span className="font-mono">
                      {Math.max(0, hoveredItem.input_tokens - hoveredItem.cached_tokens).toLocaleString()}
                    </span>
                  </div>
                  <div className="flex justify-between gap-4 text-[#10b981]">
                    <span>Cached Tokens:</span>
                    <span className="font-mono">
                      {hoveredItem.cached_tokens.toLocaleString()}
                    </span>
                  </div>
                  <div className="flex justify-between gap-4 text-[#f59e0b]">
                    <span>Output Tokens:</span>
                    <span className="font-mono">
                      {hoveredItem.output_tokens.toLocaleString()}
                    </span>
                  </div>
                  {hoveredItem.thinking_tokens > 0 && (
                    <div className="flex justify-between gap-4 text-[#a855f7]">
                      <span>Thinking Tokens:</span>
                      <span className="font-mono">
                        {hoveredItem.thinking_tokens.toLocaleString()}
                      </span>
                    </div>
                  )}
                  <div className="border-t border-[var(--app-border)] pt-1 flex justify-between gap-4 font-medium text-[var(--app-text)]">
                    <span>Estimated Cost:</span>
                    <span className="font-mono text-emerald-500">
                      ${(hoveredItem.cost_usd + (hoveredItem.media_cost_usd || 0)).toFixed(4)}
                    </span>
                  </div>
                  {hoveredItem.media_calls > 0 && (
                    <div className="flex justify-between gap-4 text-xs text-[var(--app-text-muted)]">
                      <span>Media Gen:</span>
                      <span>{hoveredItem.media_calls} calls (${hoveredItem.media_cost_usd.toFixed(2)})</span>
                    </div>
                  )}
                </div>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
