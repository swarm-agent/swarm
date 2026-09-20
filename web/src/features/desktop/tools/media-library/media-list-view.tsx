import { Eye, Film, Image as ImageIcon, Music, Sparkles } from 'lucide-react'
import type { MediaLibraryItem } from './types'

interface MediaListViewProps {
  items: readonly MediaLibraryItem[]
  onSelectItem: (item: MediaLibraryItem) => void
  selectedItemId?: string
}

export function MediaListView({
  items,
  onSelectItem,
  selectedItemId,
}: MediaListViewProps) {
  return (
    <div className="w-full overflow-x-auto rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xs">
      <table className="w-full border-collapse text-left text-xs">
        <thead>
          <tr className="border-b border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
            <th className="py-2.5 pl-4 pr-3">Name</th>
            <th className="px-3 py-2.5">Type</th>
            <th className="hidden sm:table-cell px-3 py-2.5">Dimensions / Info</th>
            <th className="hidden md:table-cell px-3 py-2.5">Iteration Group</th>
            <th className="hidden lg:table-cell px-3 py-2.5">Session</th>
            <th className="px-3 py-2.5">Date Generated</th>
            <th className="py-2.5 pl-3 pr-4 text-right">Action</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-[var(--app-border)] text-[var(--app-text)]">
          {items.map((item) => {
            const isSelected = selectedItemId === item.id
            return (
              <tr
                key={item.id}
                onClick={() => onSelectItem(item)}
                onDoubleClick={() => onSelectItem(item)}
                className={`group cursor-pointer transition-colors hover:bg-[var(--app-surface-hover)] ${
                  isSelected ? 'bg-[var(--app-surface-active)] font-medium' : ''
                }`}
              >
                {/* Name + Thumbnail */}
                <td className="py-2.5 pl-4 pr-3">
                  <div className="flex items-center gap-3 min-w-0 max-w-sm">
                    {/* Mini thumbnail */}
                    <div className="relative flex size-9 shrink-0 items-center justify-center overflow-hidden rounded-md border border-[var(--app-border)] bg-black/30">
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
                    </div>

                    <div className="min-w-0">
                      <p className="truncate font-medium text-[var(--app-text)] group-hover:text-[var(--app-primary)] transition-colors">
                        {item.title}
                      </p>
                      <p className="truncate font-mono text-[10px] text-[var(--app-text-muted)]">
                        {item.filename}
                      </p>
                    </div>
                  </div>
                </td>

                {/* Type */}
                <td className="px-3 py-2.5">
                  <span className="inline-flex items-center gap-1 rounded bg-[var(--app-surface-subtle)] px-2 py-0.5 text-[10px] font-medium uppercase tracking-wider text-[var(--app-text-muted)]">
                    {item.kind === 'image' && <ImageIcon size={10} />}
                    {item.kind === 'video' && <Film size={10} />}
                    {item.kind === 'audio' && <Music size={10} />}
                    {item.kind === 'animation' && <Sparkles size={10} />}
                    <span>{item.kind}</span>
                  </span>
                </td>

                {/* Dimensions / Info */}
                <td className="hidden sm:table-cell px-3 py-2.5 font-mono text-[11px] text-[var(--app-text-muted)]">
                  {item.dimensions || item.mediaType}
                </td>

                {/* Iteration Group */}
                <td className="hidden md:table-cell px-3 py-2.5">
                  {item.iterationGroupTitle ? (
                    <span className="truncate rounded bg-amber-500/10 text-amber-600 dark:text-amber-400 px-2 py-0.5 text-[10px] font-medium">
                      {item.iterationGroupTitle}
                    </span>
                  ) : (
                    <span className="text-[10px] text-[var(--app-text-subtle)]">Single</span>
                  )}
                </td>

                {/* Session */}
                <td className="hidden lg:table-cell px-3 py-2.5 text-[var(--app-text-muted)] max-w-xs truncate">
                  {item.sessionTitle}
                </td>

                {/* Date */}
                <td className="px-3 py-2.5 text-[var(--app-text-muted)] whitespace-nowrap">
                  <span>{item.formattedDate}</span>
                  <span className="text-[10px] text-[var(--app-text-subtle)] ml-1.5">{item.formattedTime}</span>
                </td>

                {/* Action button */}
                <td className="py-2.5 pl-3 pr-4 text-right whitespace-nowrap">
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      onSelectItem(item)
                    }}
                    title="Preview media"
                    aria-label={`Preview ${item.title}`}
                    className="inline-flex items-center gap-1 rounded-md bg-[var(--app-surface-subtle)] px-2.5 py-1 text-[11px] font-medium text-[var(--app-text)] hover:bg-[var(--app-surface-active)] hover:text-[var(--app-primary)] transition"
                  >
                    <Eye size={12} />
                    <span>View</span>
                  </button>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
