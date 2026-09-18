import { Film, Image as ImageIcon, Music } from 'lucide-react'
import { Card } from '../../../../components/ui/card'
import type { SessionUsageMediaSummary } from '../services/usage-api'

interface UsageMediaSectionProps {
  media: SessionUsageMediaSummary
}

export function UsageMediaSection({ media }: UsageMediaSectionProps) {
  const formatBytes = (bytes: number): string => {
    if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
    if (bytes >= 1024) return `${(bytes / 1024).toFixed(0)} KB`
    return `${bytes} B`
  }

  const formatRelativeTime = (ts: number): string => {
    if (!ts) return ''
    const diff = Date.now() - ts
    if (diff < 60_000) return 'just now'
    if (diff < 3600_000) return `${Math.floor(diff / 60_000)}m ago`
    if (diff < 86400_000) return `${Math.floor(diff / 3600_000)}h ago`
    return `${Math.floor(diff / 86400_000)}d ago`
  }

  return (
    <div className="space-y-4">
      {/* Three Media Type KPI Cards */}
      <div className="grid grid-cols-1 gap-3.5 sm:grid-cols-3">
        {/* Images */}
        <Card className="flex items-center gap-3.5 p-4">
          <div className="grid size-10 place-items-center rounded-xl bg-blue-500/10 text-blue-600 dark:text-blue-400">
            <ImageIcon size={20} />
          </div>
          <div>
            <p className="text-xs font-medium text-[var(--app-text-muted)]">Image Generations</p>
            <div className="flex items-baseline gap-2">
              <span className="text-xl font-bold text-[var(--app-text)]">{media.image_count}</span>
              <span className="font-mono text-xs text-emerald-600 dark:text-emerald-400">
                ${media.image_cost_usd.toFixed(2)}
              </span>
            </div>
            <p className="text-[10px] text-[var(--app-text-subtle)]">~$0.04 per generated image</p>
          </div>
        </Card>

        {/* Video */}
        <Card className="flex items-center gap-3.5 p-4">
          <div className="grid size-10 place-items-center rounded-xl bg-purple-500/10 text-purple-600 dark:text-purple-400">
            <Film size={20} />
          </div>
          <div>
            <p className="text-xs font-medium text-[var(--app-text-muted)]">Video Generations</p>
            <div className="flex items-baseline gap-2">
              <span className="text-xl font-bold text-[var(--app-text)]">{media.video_count}</span>
              <span className="font-mono text-xs text-emerald-600 dark:text-emerald-400">
                ${media.video_cost_usd.toFixed(2)}
              </span>
            </div>
            <p className="text-[10px] text-[var(--app-text-subtle)]">Google Veo 3.1 clips</p>
          </div>
        </Card>

        {/* Audio */}
        <Card className="flex items-center gap-3.5 p-4">
          <div className="grid size-10 place-items-center rounded-xl bg-amber-500/10 text-amber-600 dark:text-amber-400">
            <Music size={20} />
          </div>
          <div>
            <p className="text-xs font-medium text-[var(--app-text-muted)]">Audio & Music</p>
            <div className="flex items-baseline gap-2">
              <span className="text-xl font-bold text-[var(--app-text)]">{media.audio_count}</span>
              <span className="font-mono text-xs text-emerald-600 dark:text-emerald-400">
                ${media.audio_cost_usd.toFixed(2)}
              </span>
            </div>
            <p className="text-[10px] text-[var(--app-text-subtle)]">Google Lyria soundtracks</p>
          </div>
        </Card>
      </div>

      {/* Recent Media Table / List */}
      {media.recent_items && media.recent_items.length > 0 && (
        <Card className="overflow-hidden p-0">
          <div className="border-b border-[var(--app-border)] p-4">
            <h4 className="text-sm font-semibold text-[var(--app-text)]">Recent Media Generations</h4>
          </div>

          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead className="border-b border-[var(--app-border)] bg-[var(--app-surface-hover)] text-[11px] font-semibold text-[var(--app-text-muted)]">
                <tr>
                  <th className="py-2.5 pl-4 pr-3">Type</th>
                  <th className="px-3 py-2.5">Asset Label / File</th>
                  <th className="px-3 py-2.5">Size</th>
                  <th className="px-3 py-2.5">Created</th>
                  <th className="py-2.5 pl-3 pr-4 text-right">Est. Cost</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[var(--app-border)]">
                {media.recent_items.map((item) => {
                  return (
                    <tr key={item.id} className="hover:bg-[var(--app-surface-hover)] transition-colors">
                      <td className="py-2.5 pl-4 pr-3">
                        <div className="flex items-center gap-1.5 font-medium capitalize">
                          {item.kind === 'image' && <ImageIcon size={13} className="text-blue-500" />}
                          {item.kind === 'video' && <Film size={13} className="text-purple-500" />}
                          {item.kind === 'audio' && <Music size={13} className="text-amber-500" />}
                          <span>{item.kind}</span>
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="font-medium text-[var(--app-text)] truncate max-w-xs">{item.label}</div>
                        <div className="text-[10px] text-[var(--app-text-subtle)] font-mono">{item.media_type}</div>
                      </td>
                      <td className="px-3 py-2.5 font-mono text-[var(--app-text-muted)]">
                        {item.size > 0 ? formatBytes(item.size) : '—'}
                      </td>
                      <td className="px-3 py-2.5 text-[var(--app-text-muted)]">
                        {formatRelativeTime(item.created_at)}
                      </td>
                      <td className="py-2.5 pl-3 pr-4 text-right font-mono font-medium text-emerald-600 dark:text-emerald-400">
                        ${item.cost_usd.toFixed(2)}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </div>
  )
}
