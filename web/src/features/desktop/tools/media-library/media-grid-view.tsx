import { Eye, Film, Image as ImageIcon, Music, Sparkles } from 'lucide-react'
import type { HistoricalDateBucket, MediaLibraryItem, MediaThumbnailSize } from './types'

interface MediaGridViewProps {
  buckets: readonly HistoricalDateBucket[]
  thumbnailSize: MediaThumbnailSize
  onSelectItem: (item: MediaLibraryItem) => void
  selectedItemId?: string
}

export function MediaGridView({
  buckets,
  thumbnailSize,
  onSelectItem,
  selectedItemId,
}: MediaGridViewProps) {
  // Grid column class based on thumbnail size
  const gridClasses = {
    sm: 'grid-cols-2 sm:grid-cols-4 md:grid-cols-6 lg:grid-cols-8 gap-2.5',
    md: 'grid-cols-1 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 gap-3.5',
    lg: 'grid-cols-1 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-4',
  }[thumbnailSize]

  const aspectClass = {
    sm: 'aspect-square',
    md: 'aspect-[4/3]',
    lg: 'aspect-[16/10]',
  }[thumbnailSize]

  return (
    <div className="space-y-8 pb-12">
      {buckets.map((bucket) => (
        <section key={bucket.dateKey} className="space-y-3" aria-labelledby={`date-header-${bucket.dateKey}`}>
          {/* Day / Date Header */}
          <div className="sticky top-0 z-10 flex items-center justify-between border-b border-[var(--app-border)] bg-[var(--app-bg)]/90 py-2.5 backdrop-blur-md">
            <div className="flex items-center gap-2">
              <h2 id={`date-header-${bucket.dateKey}`} className="text-xs font-semibold uppercase tracking-wider text-[var(--app-text)]">
                {bucket.dayLabel}
              </h2>
              <span className="rounded-full bg-[var(--app-surface-subtle)] px-2 py-0.5 text-[10px] font-medium text-[var(--app-text-muted)]">
                {bucket.items.length} {bucket.items.length === 1 ? 'item' : 'items'}
              </span>
            </div>
            <div className="hidden sm:flex items-center gap-1.5 text-[10px] text-[var(--app-text-subtle)]">
              {typeSummary(bucket.items)}
            </div>
          </div>

          {/* Grid of Thumbnails */}
          <div className={`grid ${gridClasses}`}>
            {bucket.items.map((item) => (
              <MediaThumbnailCard
                key={item.id}
                item={item}
                aspectClass={aspectClass}
                onSelect={() => onSelectItem(item)}
                isSelected={selectedItemId === item.id}
              />
            ))}
          </div>
        </section>
      ))}
    </div>
  )
}

function typeSummary(items: readonly MediaLibraryItem[]): string {
  const counts: Record<string, number> = {}
  for (const item of items) {
    counts[item.kind] = (counts[item.kind] || 0) + 1
  }
  return Object.entries(counts)
    .map(([kind, count]) => `${count} ${kind}${count > 1 ? 's' : ''}`)
    .join(' · ')
}

interface MediaThumbnailCardProps {
  item: MediaLibraryItem
  aspectClass: string
  onSelect: () => void
  isSelected: boolean
}

function MediaThumbnailCard({
  item,
  aspectClass,
  onSelect,
  isSelected,
}: MediaThumbnailCardProps) {
  return (
    <article
      onClick={onSelect}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onSelect()
        }
      }}
      aria-label={`View ${item.title}`}
      className={`group relative flex flex-col overflow-hidden rounded-xl border bg-[var(--app-surface)] text-left cursor-pointer transition-all duration-150 hover:shadow-md hover:border-[var(--app-primary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] ${
        isSelected
          ? 'border-[var(--app-primary)] ring-2 ring-[var(--app-primary)]/40 bg-[var(--app-surface-active)]'
          : 'border-[var(--app-border)] hover:bg-[var(--app-surface-hover)]'
      }`}
    >
      {/* Thumbnail Container */}
      <div className={`relative ${aspectClass} w-full overflow-hidden bg-black/40 flex items-center justify-center`}>
        {item.kind === 'image' && (
          <img
            src={item.directUrl}
            alt=""
            loading="lazy"
            className="size-full object-cover transition-transform duration-200 group-hover:scale-105"
            onError={(e) => {
              // Fallback to placeholder icon
              e.currentTarget.style.display = 'none'
            }}
          />
        )}

        {item.kind === 'video' && (
          <div className="relative size-full flex items-center justify-center bg-zinc-950">
            <video
              src={item.directUrl}
              preload="metadata"
              muted
              playsInline
              className="size-full object-cover opacity-80 group-hover:opacity-100 transition-opacity"
            />
            <div className="absolute inset-0 flex items-center justify-center pointer-events-none">
              <div className="flex size-9 items-center justify-center rounded-full bg-black/60 text-white shadow-lg backdrop-blur-sm group-hover:scale-110 transition-transform">
                <Film size={16} />
              </div>
            </div>
          </div>
        )}

        {item.kind === 'audio' && (
          <div className="flex size-full flex-col items-center justify-center bg-gradient-to-br from-indigo-950/50 to-purple-950/50 p-3">
            <div className="flex size-10 items-center justify-center rounded-full bg-[var(--app-primary)]/20 text-[var(--app-primary)] shadow-sm">
              <Music size={20} />
            </div>
            <span className="mt-2 text-[10px] font-mono text-white/60 truncate max-w-full">
              {item.mediaType.replace('audio/', '')}
            </span>
          </div>
        )}

        {item.kind === 'animation' && (
          <div className="relative size-full flex items-center justify-center bg-zinc-900">
            <iframe
              title={item.title}
              src={item.directUrl}
              sandbox="allow-scripts"
              tabIndex={-1}
              className="pointer-events-none size-full origin-top-left scale-100 border-0 opacity-70 group-hover:opacity-100 transition-opacity"
            />
            <div className="absolute top-2 left-2 z-10 flex size-6 items-center justify-center rounded-md bg-black/60 text-amber-300">
              <Sparkles size={12} />
            </div>
          </div>
        )}

        {/* Kind badge in corner */}
        <div className="absolute top-2 right-2 z-10 flex items-center gap-1 rounded bg-black/70 px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wider text-white shadow backdrop-blur-xs">
          {item.kind === 'image' && <ImageIcon size={10} />}
          {item.kind === 'video' && <Film size={10} />}
          {item.kind === 'audio' && <Music size={10} />}
          {item.kind === 'animation' && <Sparkles size={10} />}
          <span>{item.kind}</span>
        </div>

        {/* Hover overlay with eye icon */}
        <div className="absolute inset-0 flex items-center justify-center bg-black/30 opacity-0 group-hover:opacity-100 transition-opacity pointer-events-none">
          <div className="flex size-8 items-center justify-center rounded-full bg-white text-zinc-900 shadow-lg">
            <Eye size={15} />
          </div>
        </div>
      </div>

      {/* Card Info Footer */}
      <div className="flex flex-col p-2 sm:p-2.5 min-w-0">
        <span className="truncate text-xs font-medium text-[var(--app-text)] group-hover:text-[var(--app-primary)] transition-colors">
          {item.title}
        </span>
        <div className="mt-1 flex items-center justify-between gap-1 text-[10px] text-[var(--app-text-muted)]">
          <span className="truncate max-w-[65%]">{item.sessionTitle}</span>
          <span className="shrink-0 font-mono text-[9px] text-[var(--app-text-subtle)]">
            {item.formattedTime}
          </span>
        </div>
        {item.dimensions && (
          <span className="mt-0.5 font-mono text-[9px] text-[var(--app-text-subtle)] truncate">
            {item.dimensions}
          </span>
        )}
      </div>
    </article>
  )
}
