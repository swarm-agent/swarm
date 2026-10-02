import type { DesktopV3ArtifactCatalogEntry, VideoProvenance } from '../../session-v3/artifact-api'

export type MediaKind = 'all' | 'image' | 'video' | 'audio' | 'animation' | 'document'

export type MediaViewMode = 'grid' | 'list'

export type MediaThumbnailSize = 'sm' | 'md' | 'lg'

export type MediaGroupingMode = 'date' | 'iteration'

export type MediaSortOrder = 'newest' | 'oldest' | 'title' | 'type'

export type MediaLibraryItem = MediaLibraryFields & (
  | { source?: 'artifact'; artifact: DesktopV3ArtifactCatalogEntry; design?: never }
  | { source: 'independent-design'; artifact?: never; design: { projectId: string; requestId: string; candidate: number; revision: import('../../session-v3/design-api').DesignRevision } }
)

export interface MediaLibraryFields {
  id: string
  title: string
  filename: string
  mediaType: string
  kind: Exclude<MediaKind, 'all'>
  createdAt: number
  formattedDate: string
  formattedTime: string
  dayKey: string
  dayLabel: string
  sessionId: string
  sessionTitle: string
  workspacePath: string
  workspaceName: string
  iterationGroupId?: string
  iterationGroupTitle?: string
  variantIndex?: number
  totalVariants?: number
  dimensions?: string
  durationMs?: number
  sizeBytes?: number
  directUrl: string
  parentId?: string
  sourceMediaRef?: string
  legacyRequestedSettings?: { model?: string; aspectRatio?: string; resolution?: string; durationSeconds?: number }
  model?: string
  aspectRatio?: string
  resolution?: string
  durationSeconds?: number
  videoProvenance?: VideoProvenance | null
}

export interface HistoricalDateBucket {
  dateKey: string
  dayLabel: string
  timestamp: number
  items: MediaLibraryItem[]
}

export interface MediaIterationGroup {
  id: string
  title: string
  sessionId: string
  sessionTitle: string
  timestamp: number
  itemCount: number
  kinds: Array<Exclude<MediaKind, 'all'>>
  items: MediaLibraryItem[]
}

export interface MediaFilterState {
  kind: MediaKind
  searchQuery: string
  viewMode: MediaViewMode
  thumbnailSize: MediaThumbnailSize
  groupingMode: MediaGroupingMode
  sortOrder: MediaSortOrder
  selectedIterationGroupId?: string
}
