import type { DesktopV3ArtifactCatalogEntry } from '../../session-v3/artifact-api'
import { desktopV3ArtifactDirectContentURL } from '../../session-v3/artifact-api'
import type {
  HistoricalDateBucket,
  MediaFilterState,
  MediaIterationGroup,
  MediaKind,
  MediaLibraryItem,
} from './types'

const IMAGE_EXTENSIONS = new Set(['png', 'jpg', 'jpeg', 'webp', 'gif', 'svg', 'avif', 'bmp', 'ico'])
const VIDEO_EXTENSIONS = new Set(['mp4', 'webm', 'mov', 'mkv', 'avi', 'm4v', 'ogv'])
const AUDIO_EXTENSIONS = new Set(['wav', 'mp3', 'm4a', 'aac', 'ogg', 'flac', 'weba', 'opus', 'aiff'])

function getFileExtension(filename: string): string {
  const parts = filename.split('.')
  return parts.length > 1 ? (parts.pop()?.toLowerCase() || '') : ''
}

export function classifyMediaKind(entry: DesktopV3ArtifactCatalogEntry): Exclude<MediaKind, 'all'> | null {
  const ext = getFileExtension(entry.filename || '')
  const mediaType = (entry.mediaType || '').toLowerCase()
  const kind = (entry.kind || '').toLowerCase()

  // 1. Check Audio
  if (kind === 'audio' || mediaType.startsWith('audio/') || AUDIO_EXTENSIONS.has(ext)) {
    return 'audio'
  }

  // 2. Check Video
  if (
    kind === 'video' ||
    mediaType.startsWith('video/') ||
    VIDEO_EXTENSIONS.has(ext) ||
    (entry.role === 'render_only' && (ext === 'mp4' || mediaType.includes('mp4')))
  ) {
    return 'video'
  }

  // 3. Check Animation / Interactive HTML
  if (
    Boolean(entry.animationProfile) ||
    (kind === 'html' && (Boolean(entry.animationProfile) || ext === 'html')) ||
    (mediaType === 'text/html' && Boolean(entry.animationProfile)) ||
    (kind === 'html' && /animation|motion|three|canvas|interactive/i.test(`${entry.label} ${entry.description} ${entry.filename}`))
  ) {
    return 'animation'
  }

  // 4. Check Images
  if (kind === 'image' || mediaType.startsWith('image/') || IMAGE_EXTENSIONS.has(ext)) {
    return 'image'
  }

  // Fallback for HTML artifacts without explicit animation profile if classified under kind html
  if (kind === 'html' || mediaType === 'text/html') {
    return 'animation'
  }

  return null
}

export function formatDayLabel(timestamp: number, referenceDate = new Date()): { dayKey: string; dayLabel: string } {
  const date = new Date(timestamp)
  if (Number.isNaN(date.getTime())) {
    return { dayKey: 'unknown', dayLabel: 'Earlier / Unknown Date' }
  }

  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  const dayKey = `${year}-${month}-${day}`

  const refYear = referenceDate.getFullYear()
  const refMonth = String(referenceDate.getMonth() + 1).padStart(2, '0')
  const refDay = String(referenceDate.getDate()).padStart(2, '0')
  const todayKey = `${refYear}-${refMonth}-${refDay}`

  const yesterdayDate = new Date(referenceDate)
  yesterdayDate.setDate(yesterdayDate.getDate() - 1)
  const yYear = yesterdayDate.getFullYear()
  const yMonth = String(yesterdayDate.getMonth() + 1).padStart(2, '0')
  const yDay = String(yesterdayDate.getDate()).padStart(2, '0')
  const yesterdayKey = `${yYear}-${yMonth}-${yDay}`

  const monthNames = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
  const weekdayNames = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
  const formattedDay = `${weekdayNames[date.getDay()]}, ${monthNames[date.getMonth()]} ${date.getDate()}, ${year}`

  if (dayKey === todayKey) {
    return { dayKey, dayLabel: `Today · ${monthNames[date.getMonth()]} ${date.getDate()}, ${year}` }
  }
  if (dayKey === yesterdayKey) {
    return { dayKey, dayLabel: `Yesterday · ${monthNames[date.getMonth()]} ${date.getDate()}, ${year}` }
  }

  return { dayKey, dayLabel: formattedDay }
}

export function formatMediaTime(timestamp: number): string {
  const date = new Date(timestamp)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

export function extractDimensions(entry: DesktopV3ArtifactCatalogEntry): string | undefined {
  if (entry.outputRequirements?.width && entry.outputRequirements?.height) {
    return `${entry.outputRequirements.width} × ${entry.outputRequirements.height}`
  }
  return undefined
}

export function toMediaLibraryItem(entry: DesktopV3ArtifactCatalogEntry, referenceDate = new Date()): MediaLibraryItem | null {
  const classifiedKind = classifyMediaKind(entry)
  if (!classifiedKind) return null

  const timestamp = entry.updatedAt || Date.now()
  const { dayKey, dayLabel } = formatDayLabel(timestamp, referenceDate)
  const formattedTime = formatMediaTime(timestamp)
  const formattedDate = new Date(timestamp).toLocaleDateString([], {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  })

  // Extract iteration group key and title
  let iterationGroupId: string | undefined
  let iterationGroupTitle: string | undefined
  let variantIndex: number | undefined
  let totalVariants: number | undefined

  if (entry.artifactChainId) {
    iterationGroupId = `chain:${entry.sessionId}:${entry.artifactChainId}`
    iterationGroupTitle = entry.chain?.name || entry.collectionName || 'Artifact Chain'
    variantIndex = entry.revisionNumber || entry.step?.revisionNumber
    totalVariants = entry.chain?.revisionCount
  } else if (entry.collectionId) {
    iterationGroupId = `collection:${entry.sessionId}:${entry.collectionId}`
    iterationGroupTitle = entry.collectionName || 'Media Generation Wave'
    totalVariants = entry.progress?.total
  }

  const directUrl = desktopV3ArtifactDirectContentURL(entry)

  return {
    artifact: entry,
    id: entry.artifactId,
    title: entry.label || entry.filename || entry.artifactId,
    filename: entry.filename || entry.label || `${entry.artifactId}.${classifiedKind}`,
    mediaType: entry.mediaType || '',
    kind: classifiedKind,
    createdAt: timestamp,
    formattedDate,
    formattedTime,
    dayKey,
    dayLabel,
    sessionId: entry.sessionId,
    sessionTitle: entry.sessionTitle || 'Untitled Session',
    workspacePath: entry.workspacePath || '',
    workspaceName: entry.workspaceName || '',
    iterationGroupId,
    iterationGroupTitle,
    variantIndex,
    totalVariants,
    dimensions: extractDimensions(entry),
    directUrl,
  }
}

export function normalizeMediaCatalogEntries(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  referenceDate = new Date(),
): MediaLibraryItem[] {
  const items: MediaLibraryItem[] = []
  for (const entry of entries) {
    const item = toMediaLibraryItem(entry, referenceDate)
    if (item) {
      items.push(item)
    }
  }
  return items
}

export function groupMediaByDate(items: readonly MediaLibraryItem[]): HistoricalDateBucket[] {
  const bucketsMap = new Map<string, HistoricalDateBucket>()

  for (const item of items) {
    let bucket = bucketsMap.get(item.dayKey)
    if (!bucket) {
      bucket = {
        dateKey: item.dayKey,
        dayLabel: item.dayLabel,
        timestamp: item.createdAt,
        items: [],
      }
      bucketsMap.set(item.dayKey, bucket)
    }
    bucket.items.push(item)
  }

  // Sort buckets by most recent timestamp
  const buckets = Array.from(bucketsMap.values())
  buckets.sort((a, b) => b.timestamp - a.timestamp)

  // Ensure items within each bucket are sorted by newest first
  for (const bucket of buckets) {
    bucket.items.sort((a, b) => b.createdAt - a.createdAt)
  }

  return buckets
}

export function groupMediaByIteration(items: readonly MediaLibraryItem[]): MediaIterationGroup[] {
  const groupsMap = new Map<string, MediaIterationGroup>()

  for (const item of items) {
    const groupId = item.iterationGroupId || `standalone:${item.sessionId}`
    const groupTitle = item.iterationGroupTitle || (item.iterationGroupId ? 'Iteration Wave' : `Session: ${item.sessionTitle}`)

    let group = groupsMap.get(groupId)
    if (!group) {
      group = {
        id: groupId,
        title: groupTitle,
        sessionId: item.sessionId,
        sessionTitle: item.sessionTitle,
        timestamp: item.createdAt,
        itemCount: 0,
        kinds: [],
        items: [],
      }
      groupsMap.set(groupId, group)
    }

    group.items.push(item)
    group.itemCount += 1
    group.timestamp = Math.max(group.timestamp, item.createdAt)
    if (!group.kinds.includes(item.kind)) {
      group.kinds.push(item.kind)
    }
  }

  const groups = Array.from(groupsMap.values())
  groups.sort((a, b) => b.timestamp - a.timestamp)

  for (const group of groups) {
    group.items.sort((a, b) => {
      if (a.variantIndex !== undefined && b.variantIndex !== undefined) {
        return a.variantIndex - b.variantIndex
      }
      return b.createdAt - a.createdAt
    })
  }

  return groups
}

export function filterAndSearchMedia(
  items: readonly MediaLibraryItem[],
  filters: Pick<MediaFilterState, 'kind' | 'searchQuery' | 'sortOrder' | 'selectedIterationGroupId'>,
): MediaLibraryItem[] {
  const query = filters.searchQuery.trim().toLowerCase()
  const kindFilter = filters.kind
  const selectedIterationGroupId = filters.selectedIterationGroupId

  const filtered = items.filter((item) => {
    // Filter by type/kind
    if (kindFilter !== 'all' && item.kind !== kindFilter) {
      return false
    }

    // Filter by iteration group if one is selected
    if (selectedIterationGroupId && item.iterationGroupId !== selectedIterationGroupId) {
      return false
    }

    // Filter by search query
    if (query) {
      const searchHaystack = [
        item.title,
        item.filename,
        item.artifact.description || '',
        item.sessionTitle,
        item.workspaceName,
        item.iterationGroupTitle || '',
        item.artifact.checkpointTitle || '',
        item.artifact.planTitle || '',
        item.kind,
        item.mediaType,
      ]
        .join(' ')
        .toLowerCase()

      if (!searchHaystack.includes(query)) {
        return false
      }
    }

    return true
  })

  // Sort
  const sorted = [...filtered]
  switch (filters.sortOrder) {
    case 'newest':
      sorted.sort((a, b) => b.createdAt - a.createdAt)
      break
    case 'oldest':
      sorted.sort((a, b) => a.createdAt - b.createdAt)
      break
    case 'title':
      sorted.sort((a, b) => a.title.localeCompare(b.title))
      break
    case 'type':
      sorted.sort((a, b) => a.kind.localeCompare(b.kind) || b.createdAt - a.createdAt)
      break
  }

  return sorted
}
