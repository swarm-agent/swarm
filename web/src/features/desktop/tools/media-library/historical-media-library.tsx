import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Calendar,
  Film,
  FolderOpen,
  Image as ImageIcon,
  Layers,
  LayoutGrid,
  List as ListIcon,
  Loader2,
  Music,
  RefreshCw,
  Search,
  Sparkles,
  X,
} from 'lucide-react'
import { fetchDesktopV3ArtifactCatalogResult } from '../../session-v3/artifact-api'
import { MediaGridView } from './media-grid-view'
import { MediaIterationGroupsView } from './media-iteration-groups-view'
import { MediaListView } from './media-list-view'
import { MediaViewerModal } from './media-viewer-modal'
import {
  filterAndSearchMedia,
  groupMediaByDate,
  groupMediaByIteration,
  normalizeMediaCatalogEntries,
} from './media-classifier'
import type {
  MediaGroupingMode,
  MediaKind,
  MediaLibraryItem,
  MediaSortOrder,
  MediaThumbnailSize,
  MediaViewMode,
} from './types'

interface HistoricalMediaLibraryProps {
  initialKind?: MediaKind
  initialQuery?: string
  workspaceSlug?: string
  onOpenSession?: (sessionId: string) => void
  onClose?: () => void
  onTagMedia?: (item: MediaLibraryItem) => void
  taggedMediaIds?: Set<string>
  onIterateSwarm?: (item: MediaLibraryItem) => void
  onFineTune?: (item: MediaLibraryItem, editPrompt: string, autoDeploy: boolean) => void
  onIterate?: (item: MediaLibraryItem, variantCount: number, stylePrompt: string, autoDeploy: boolean) => void
  onGenerateVideo?: (item: MediaLibraryItem, prompt: string, autoDeploy: boolean) => void
  onContinueVideo?: (item: MediaLibraryItem, prompt: string, autoDeploy: boolean) => void
  extraItems?: readonly MediaLibraryItem[]
}

export function HistoricalMediaLibrary({
  initialKind = 'all',
  initialQuery = '',
  workspaceSlug: _workspaceSlug,
  onOpenSession,
  onClose: _onClose,
  onTagMedia,
  taggedMediaIds,
  onIterateSwarm,
  onFineTune,
  onIterate,
  onGenerateVideo,
  onContinueVideo,
  extraItems,
}: HistoricalMediaLibraryProps) {
  const [items, setItems] = useState<MediaLibraryItem[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Filters and view controls
  const [kind, setKind] = useState<MediaKind>(initialKind)
  const [searchQuery, setSearchQuery] = useState(initialQuery)
  const [viewMode, setViewMode] = useState<MediaViewMode>('grid')
  const [thumbnailSize, setThumbnailSize] = useState<MediaThumbnailSize>('md')
  const [groupingMode, setGroupingMode] = useState<MediaGroupingMode>('date')
  const [sortOrder, setSortOrder] = useState<MediaSortOrder>('newest')
  const [selectedIterationGroupId, setSelectedIterationGroupId] = useState<string | undefined>()

  // Active viewer modal item
  const [activeItem, setActiveItem] = useState<MediaLibraryItem | null>(null)

  // Fetch all artifacts
  const loadArtifacts = useCallback(async (isRefresh = false) => {
    if (isRefresh) setRefreshing(true)
    else setLoading(true)
    setError(null)

    try {
      const result = await fetchDesktopV3ArtifactCatalogResult()
      const normalized = normalizeMediaCatalogEntries(result.artifacts)
      let combined = normalized
      if (extraItems && extraItems.length > 0) {
        const seen = new Set(normalized.map((i) => i.id))
        const extras = extraItems.filter((i) => !seen.has(i.id))
        combined = [...extras, ...normalized]
      }
      setItems(combined)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load media artifacts')
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }, [])

  useEffect(() => {
    void loadArtifacts()
  }, [loadArtifacts])

  // Count items by kind
  const counts = useMemo(() => {
    const summary = { all: items.length, image: 0, video: 0, audio: 0, animation: 0 }
    for (const item of items) {
      summary[item.kind] = (summary[item.kind] || 0) + 1
    }
    return summary
  }, [items])

  // Filter and search items
  const filteredItems = useMemo(() => {
    return filterAndSearchMedia(items, {
      kind,
      searchQuery,
      sortOrder,
      selectedIterationGroupId,
    })
  }, [items, kind, searchQuery, sortOrder, selectedIterationGroupId])

  // Date-grouped buckets
  const dateBuckets = useMemo(() => {
    return groupMediaByDate(filteredItems)
  }, [filteredItems])

  // Iteration groups
  const iterationGroups = useMemo(() => {
    return groupMediaByIteration(filteredItems)
  }, [filteredItems])

  const handleClearFilters = () => {
    setKind('all')
    setSearchQuery('')
    setSelectedIterationGroupId(undefined)
  }

  return (
    <div className="flex h-full w-full flex-col overflow-hidden bg-[var(--app-bg)] text-[var(--app-text)]">
      {/* Explorer Toolbar */}
      <div className="flex shrink-0 flex-col gap-2.5 border-b border-[var(--app-border)] bg-[var(--app-surface)] p-3 sm:px-5 sm:py-3 shadow-xs">
        {/* Top Row: Type Pills & Search Box */}
        <div className="flex flex-wrap items-center justify-between gap-3">
          {/* Type Filter Pills */}
          <div className="flex items-center gap-1 overflow-x-auto py-0.5" role="tablist" aria-label="Media types">
            <button
              type="button"
              role="tab"
              aria-selected={kind === 'all'}
              onClick={() => {
                setKind('all')
                setSelectedIterationGroupId(undefined)
              }}
              className={`inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition cursor-pointer ${
                kind === 'all'
                  ? 'bg-[var(--app-primary)] text-white shadow-xs'
                  : 'bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'
              }`}
            >
              <Layers size={13} />
              <span>All Media</span>
              <span className={`rounded-full px-1.5 py-0.2 font-mono text-[10px] ${kind === 'all' ? 'bg-white/20 text-white' : 'bg-[var(--app-border)] text-[var(--app-text-subtle)]'}`}>
                {counts.all}
              </span>
            </button>

            <button
              type="button"
              role="tab"
              aria-selected={kind === 'image'}
              onClick={() => {
                setKind('image')
                setSelectedIterationGroupId(undefined)
              }}
              className={`inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition cursor-pointer ${
                kind === 'image'
                  ? 'bg-[var(--app-primary)] text-white shadow-xs'
                  : 'bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'
              }`}
            >
              <ImageIcon size={13} />
              <span>Images</span>
              <span className={`rounded-full px-1.5 py-0.2 font-mono text-[10px] ${kind === 'image' ? 'bg-white/20 text-white' : 'bg-[var(--app-border)] text-[var(--app-text-subtle)]'}`}>
                {counts.image}
              </span>
            </button>

            <button
              type="button"
              role="tab"
              aria-selected={kind === 'video'}
              onClick={() => {
                setKind('video')
                setSelectedIterationGroupId(undefined)
              }}
              className={`inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition cursor-pointer ${
                kind === 'video'
                  ? 'bg-[var(--app-primary)] text-white shadow-xs'
                  : 'bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'
              }`}
            >
              <Film size={13} />
              <span>Videos</span>
              <span className={`rounded-full px-1.5 py-0.2 font-mono text-[10px] ${kind === 'video' ? 'bg-white/20 text-white' : 'bg-[var(--app-border)] text-[var(--app-text-subtle)]'}`}>
                {counts.video}
              </span>
            </button>

            <button
              type="button"
              role="tab"
              aria-selected={kind === 'audio'}
              onClick={() => {
                setKind('audio')
                setSelectedIterationGroupId(undefined)
              }}
              className={`inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition cursor-pointer ${
                kind === 'audio'
                  ? 'bg-[var(--app-primary)] text-white shadow-xs'
                  : 'bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'
              }`}
            >
              <Music size={13} />
              <span>Audio</span>
              <span className={`rounded-full px-1.5 py-0.2 font-mono text-[10px] ${kind === 'audio' ? 'bg-white/20 text-white' : 'bg-[var(--app-border)] text-[var(--app-text-subtle)]'}`}>
                {counts.audio}
              </span>
            </button>

            <button
              type="button"
              role="tab"
              aria-selected={kind === 'animation'}
              onClick={() => {
                setKind('animation')
                setSelectedIterationGroupId(undefined)
              }}
              className={`inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition cursor-pointer ${
                kind === 'animation'
                  ? 'bg-[var(--app-primary)] text-white shadow-xs'
                  : 'bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'
              }`}
            >
              <Sparkles size={13} />
              <span>Animations</span>
              <span className={`rounded-full px-1.5 py-0.2 font-mono text-[10px] ${kind === 'animation' ? 'bg-white/20 text-white' : 'bg-[var(--app-border)] text-[var(--app-text-subtle)]'}`}>
                {counts.animation}
              </span>
            </button>
          </div>

          {/* Search Box */}
          <div className="relative flex-1 sm:max-w-xs min-w-[200px]">
            <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-[var(--app-text-subtle)] pointer-events-none" />
            <input
              type="text"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              placeholder="Search by name, session, prompt..."
              aria-label="Search media artifacts"
              className="h-8.5 w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] pl-8.5 pr-8 text-xs text-[var(--app-text)] placeholder:text-[var(--app-text-subtle)] focus:border-[var(--app-primary)] focus:outline-none focus:ring-1 focus:ring-[var(--app-primary)] transition"
            />
            {searchQuery && (
              <button
                type="button"
                onClick={() => setSearchQuery('')}
                aria-label="Clear search"
                className="absolute right-2.5 top-1/2 -translate-y-1/2 text-[var(--app-text-subtle)] hover:text-[var(--app-text)]"
              >
                <X size={13} />
              </button>
            )}
          </div>
        </div>

        {/* Bottom Row: Controls (Grouping, View Mode, Thumbnail Size, Sort, Refresh) */}
        <div className="flex flex-wrap items-center justify-between gap-3 pt-1 border-t border-[var(--app-border)]/60 text-xs">
          {/* Left: Grouping Switcher */}
          <div className="flex items-center gap-1">
            <span className="text-[10px] font-medium uppercase tracking-wider text-[var(--app-text-subtle)] mr-1">
              Group by:
            </span>
            <div className="inline-flex rounded-lg bg-[var(--app-surface-subtle)] p-0.5">
              <button
                type="button"
                onClick={() => setGroupingMode('date')}
                aria-pressed={groupingMode === 'date'}
                className={`flex items-center gap-1 rounded-md px-2.5 py-1 text-xs font-medium transition ${
                  groupingMode === 'date'
                    ? 'bg-[var(--app-surface)] text-[var(--app-text)] shadow-xs'
                    : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                }`}
              >
                <Calendar size={12} />
                <span>Date & Day</span>
              </button>

              <button
                type="button"
                onClick={() => setGroupingMode('iteration')}
                aria-pressed={groupingMode === 'iteration'}
                className={`flex items-center gap-1 rounded-md px-2.5 py-1 text-xs font-medium transition ${
                  groupingMode === 'iteration'
                    ? 'bg-[var(--app-surface)] text-[var(--app-text)] shadow-xs'
                    : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                }`}
              >
                <Layers size={12} />
                <span>Iteration Groups</span>
              </button>
            </div>

            {selectedIterationGroupId && (
              <div className="flex items-center gap-1.5 ml-2 rounded-lg bg-amber-500/10 px-2 py-0.5 text-xs text-amber-600 dark:text-amber-400">
                <span className="font-medium truncate max-w-44">Filtered to group</span>
                <button
                  type="button"
                  onClick={() => setSelectedIterationGroupId(undefined)}
                  title="Clear iteration group filter"
                  className="hover:text-amber-800 dark:hover:text-amber-200"
                >
                  <X size={12} />
                </button>
              </div>
            )}
          </div>

          {/* Right: View mode & Thumbnail Size & Refresh */}
          <div className="flex items-center gap-3">
            {/* Sort order */}
            <div className="flex items-center gap-1">
              <span className="text-[10px] font-medium text-[var(--app-text-subtle)]">Sort:</span>
              <select
                value={sortOrder}
                onChange={(e) => setSortOrder(e.target.value as MediaSortOrder)}
                className="rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1 text-[11px] text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]"
                aria-label="Sort order"
              >
                <option value="newest">Newest First</option>
                <option value="oldest">Oldest First</option>
                <option value="title">Name (A-Z)</option>
                <option value="type">By Type</option>
              </select>
            </div>

            {/* Thumbnail size (when in grid mode) */}
            {viewMode === 'grid' && groupingMode === 'date' && (
              <div className="hidden sm:inline-flex rounded-lg bg-[var(--app-surface-subtle)] p-0.5" title="Thumbnail size">
                <button
                  type="button"
                  onClick={() => setThumbnailSize('sm')}
                  title="Small thumbnails"
                  aria-label="Small thumbnails"
                  className={`rounded-md px-1.5 py-1 text-[10px] font-mono transition ${thumbnailSize === 'sm' ? 'bg-[var(--app-surface)] text-[var(--app-text)] shadow-xs' : 'text-[var(--app-text-muted)]'}`}
                >
                  S
                </button>
                <button
                  type="button"
                  onClick={() => setThumbnailSize('md')}
                  title="Medium thumbnails"
                  aria-label="Medium thumbnails"
                  className={`rounded-md px-1.5 py-1 text-[10px] font-mono transition ${thumbnailSize === 'md' ? 'bg-[var(--app-surface)] text-[var(--app-text)] shadow-xs' : 'text-[var(--app-text-muted)]'}`}
                >
                  M
                </button>
                <button
                  type="button"
                  onClick={() => setThumbnailSize('lg')}
                  title="Large thumbnails"
                  aria-label="Large thumbnails"
                  className={`rounded-md px-1.5 py-1 text-[10px] font-mono transition ${thumbnailSize === 'lg' ? 'bg-[var(--app-surface)] text-[var(--app-text)] shadow-xs' : 'text-[var(--app-text-muted)]'}`}
                >
                  L
                </button>
              </div>
            )}

            {/* View Mode (Grid vs List) */}
            <div className="inline-flex rounded-lg bg-[var(--app-surface-subtle)] p-0.5">
              <button
                type="button"
                onClick={() => setViewMode('grid')}
                title="Thumbnails view"
                aria-label="Thumbnails view"
                aria-pressed={viewMode === 'grid'}
                className={`p-1 rounded-md transition ${
                  viewMode === 'grid'
                    ? 'bg-[var(--app-surface)] text-[var(--app-text)] shadow-xs'
                    : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                }`}
              >
                <LayoutGrid size={14} />
              </button>
              <button
                type="button"
                onClick={() => setViewMode('list')}
                title="List view"
                aria-label="List view"
                aria-pressed={viewMode === 'list'}
                className={`p-1 rounded-md transition ${
                  viewMode === 'list'
                    ? 'bg-[var(--app-surface)] text-[var(--app-text)] shadow-xs'
                    : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                }`}
              >
                <ListIcon size={14} />
              </button>
            </div>

            {/* Refresh Button */}
            <button
              type="button"
              onClick={() => void loadArtifacts(true)}
              disabled={refreshing}
              title="Refresh media catalog"
              aria-label="Refresh media catalog"
              className="p-1.5 rounded-lg text-[var(--app-text-muted)] hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] transition disabled:opacity-50"
            >
              <RefreshCw size={14} className={refreshing ? 'animate-spin' : ''} />
            </button>
          </div>
        </div>
      </div>

      {/* Main Content Area */}
      <div className="flex-1 min-h-0 overflow-y-auto px-4 py-6 sm:px-8">
        {loading ? (
          <div className="flex h-64 flex-col items-center justify-center gap-3 text-[var(--app-text-muted)]">
            <Loader2 className="size-6 animate-spin text-[var(--app-primary)]" />
            <p className="text-xs font-medium">Scanning historical media artifacts...</p>
          </div>
        ) : error ? (
          <div className="flex h-64 flex-col items-center justify-center gap-3 text-center">
            <p className="text-xs text-red-500">{error}</p>
            <button
              type="button"
              onClick={() => void loadArtifacts()}
              className="rounded-lg bg-[var(--app-surface-subtle)] px-3 py-1.5 text-xs font-medium text-[var(--app-text)] hover:bg-[var(--app-surface-hover)]"
            >
              Retry
            </button>
          </div>
        ) : filteredItems.length === 0 ? (
          <div className="flex h-64 flex-col items-center justify-center gap-3 text-center text-[var(--app-text-muted)]">
            <div className="flex size-12 items-center justify-center rounded-2xl bg-[var(--app-surface-subtle)] text-[var(--app-text-subtle)]">
              <FolderOpen size={24} />
            </div>
            <div>
              <h3 className="font-semibold text-sm text-[var(--app-text)]">No media artifacts found</h3>
              <p className="mt-1 text-xs max-w-sm text-[var(--app-text-subtle)]">
                {searchQuery || kind !== 'all' || selectedIterationGroupId
                  ? 'No media matches your current search or filter criteria.'
                  : 'Start a session with Swarm to generate images, videos, audio soundtracks, or animations.'}
              </p>
            </div>
            {(searchQuery || kind !== 'all' || selectedIterationGroupId) && (
              <button
                type="button"
                onClick={handleClearFilters}
                className="mt-2 rounded-lg bg-[var(--app-primary)]/10 text-[var(--app-primary)] px-3 py-1.5 text-xs font-medium hover:bg-[var(--app-primary)]/20 transition"
              >
                Reset filters
              </button>
            )}
          </div>
        ) : (
          <div>
            {groupingMode === 'iteration' ? (
              <MediaIterationGroupsView
                groups={iterationGroups}
                onSelectItem={(item) => setActiveItem(item)}
                onFocusGroup={(groupId) => {
                  setSelectedIterationGroupId(groupId)
                  setGroupingMode('date')
                }}
              />
            ) : viewMode === 'grid' ? (
              <MediaGridView
                buckets={dateBuckets}
                thumbnailSize={thumbnailSize}
                onSelectItem={(item) => setActiveItem(item)}
                selectedItemId={activeItem?.id}
              />
            ) : (
              <MediaListView
                items={filteredItems}
                onSelectItem={(item) => setActiveItem(item)}
                selectedItemId={activeItem?.id}
              />
            )}
          </div>
        )}
      </div>

      {/* Modal Media Viewer */}
      <MediaViewerModal
        item={activeItem}
        items={filteredItems}
        onClose={() => setActiveItem(null)}
        onSelect={(item) => setActiveItem(item)}
        onOpenSession={onOpenSession}
        isTagged={activeItem ? taggedMediaIds?.has(activeItem.id) : false}
        onToggleTag={onTagMedia}
        onIterateSwarm={onIterateSwarm}
        onFineTune={onFineTune}
        onIterate={onIterate}
        onGenerateVideo={onGenerateVideo}
        onContinueVideo={onContinueVideo}
      />
    </div>
  )
}
