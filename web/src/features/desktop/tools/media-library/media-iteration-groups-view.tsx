import { ChevronRight, Film, Layers, Music, Sparkles } from 'lucide-react'
import type { MediaIterationGroup, MediaLibraryItem } from './types'

interface MediaIterationGroupsViewProps {
  groups: readonly MediaIterationGroup[]
  onSelectItem: (item: MediaLibraryItem) => void
  onFocusGroup?: (groupId: string) => void
}

export function MediaIterationGroupsView({
  groups,
  onSelectItem,
  onFocusGroup,
}: MediaIterationGroupsViewProps) {
  return (
    <div className="space-y-6 pb-12">
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-5">
        {groups.map((group) => (
          <article
            key={group.id}
            className="flex flex-col overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xs transition hover:border-[var(--app-primary)] hover:shadow-md"
          >
            {/* Group Header */}
            <div className="flex items-start justify-between gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3.5">
              <div className="min-w-0">
                <div className="flex items-center gap-1.5">
                  <Layers size={14} className="text-[var(--app-primary)] shrink-0" />
                  <h3 className="truncate font-semibold text-xs text-[var(--app-text)]">{group.title}</h3>
                </div>
                <p className="mt-0.5 truncate text-[11px] text-[var(--app-text-muted)]">{group.sessionTitle}</p>
              </div>
              <span className="shrink-0 rounded-full bg-[var(--app-primary)]/10 text-[var(--app-primary)] px-2 py-0.5 text-[10px] font-semibold">
                {group.itemCount} {group.itemCount === 1 ? 'variant' : 'variants'}
              </span>
            </div>

            {/* Variants Thumbnail Collage / Row */}
            <div className="p-3.5">
              <div className="grid grid-cols-3 sm:grid-cols-4 gap-2">
                {group.items.slice(0, 8).map((item, idx) => (
                  <button
                    key={item.id}
                    type="button"
                    onClick={() => onSelectItem(item)}
                    title={`Variant ${idx + 1}: ${item.title}`}
                    aria-label={`Variant ${idx + 1}: ${item.title}`}
                    className="group relative aspect-square w-full overflow-hidden rounded-lg border border-[var(--app-border)] bg-black/40 text-left transition hover:border-[var(--app-primary)] hover:scale-105"
                  >
                    {item.kind === 'image' && (
                      <img src={item.directUrl} alt="" className="size-full object-cover" />
                    )}
                    {item.kind === 'video' && (
                      <div className="flex size-full items-center justify-center bg-zinc-950 text-white">
                        <Film size={14} />
                      </div>
                    )}
                    {item.kind === 'audio' && (
                      <div className="flex size-full items-center justify-center bg-purple-950 text-[var(--app-primary)]">
                        <Music size={14} />
                      </div>
                    )}
                    {item.kind === 'animation' && (
                      <div className="flex size-full items-center justify-center bg-zinc-900 text-amber-400">
                        <Sparkles size={14} />
                      </div>
                    )}
                    <span className="absolute bottom-1 right-1 rounded bg-black/70 px-1 font-mono text-[8px] text-white">
                      #{item.variantIndex !== undefined ? item.variantIndex : idx + 1}
                    </span>
                  </button>
                ))}
              </div>
            </div>

            {/* Card Footer */}
            <div className="mt-auto flex items-center justify-between border-t border-[var(--app-border)] px-3.5 py-2 text-[10px] text-[var(--app-text-muted)] bg-[var(--app-surface-subtle)]">
              <span>{group.items[0]?.formattedDate || 'Recent'}</span>
              {onFocusGroup && (
                <button
                  type="button"
                  onClick={() => onFocusGroup(group.id)}
                  className="inline-flex items-center gap-0.5 font-medium text-[var(--app-primary)] hover:underline"
                >
                  <span>View all</span>
                  <ChevronRight size={12} />
                </button>
              )}
            </div>
          </article>
        ))}
      </div>
    </div>
  )
}
