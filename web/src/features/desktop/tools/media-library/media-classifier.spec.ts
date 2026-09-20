import assert from 'node:assert/strict'
import test from 'node:test'
import type { DesktopV3ArtifactCatalogEntry } from '../../session-v3/artifact-api'
import {
  classifyMediaKind,
  filterAndSearchMedia,
  formatDayLabel,
  groupMediaByDate,
  groupMediaByIteration,
  normalizeMediaCatalogEntries,
  toMediaLibraryItem,
} from './media-classifier'

function mockArtifact(overrides: Partial<DesktopV3ArtifactCatalogEntry> = {}): DesktopV3ArtifactCatalogEntry {
  return {
    artifactId: 'art-1',
    sessionId: 'sess-1',
    sessionTitle: 'Logo Generation Session',
    workspacePath: '/workspaces/demo',
    workspaceName: 'demo',
    planId: 'p-1',
    planTitle: 'Brand Assets',
    checkpointId: 'cp-1',
    checkpointTitle: 'Design Assets',
    label: 'Cybernetic Logo',
    description: 'Neon geometric emblem',
    collectionName: 'Logos Wave',
    collectionDescription: 'Wave of 5 logos',
    filename: 'logo.png',
    mediaType: 'image/png',
    kind: 'image',
    previewable: true,
    category: 'media',
    updatedAt: 1789752154269,
    ...overrides,
  }
}

test('classifyMediaKind correctly identifies images, videos, audio, and animations', () => {
  // Image
  assert.equal(classifyMediaKind(mockArtifact({ kind: 'image', mediaType: 'image/png', filename: 'test.png' })), 'image')
  assert.equal(classifyMediaKind(mockArtifact({ kind: 'image', mediaType: 'image/jpeg', filename: 'photo.jpg' })), 'image')
  assert.equal(classifyMediaKind(mockArtifact({ kind: 'other', mediaType: 'image/webp', filename: 'art.webp' })), 'image')

  // Video
  assert.equal(classifyMediaKind(mockArtifact({ kind: 'video', mediaType: 'video/mp4', filename: 'clip.mp4' })), 'video')
  assert.equal(classifyMediaKind(mockArtifact({ kind: 'other', mediaType: 'video/webm', filename: 'movie.webm' })), 'video')
  assert.equal(classifyMediaKind(mockArtifact({ kind: 'other', role: 'render_only', mediaType: 'video/mp4', filename: 'final.mp4' })), 'video')

  // Audio
  assert.equal(classifyMediaKind(mockArtifact({ kind: 'audio', mediaType: 'audio/wav', filename: 'soundtrack.wav' })), 'audio')
  assert.equal(classifyMediaKind(mockArtifact({ kind: 'other', mediaType: 'audio/mpeg', filename: 'track.mp3' })), 'audio')

  // Animation / Interactive
  assert.equal(
    classifyMediaKind(
      mockArtifact({
        kind: 'html',
        mediaType: 'text/html',
        filename: 'banner.html',
        animationProfile: {
          profileId: 'motion_ui',
          registryVersion: '1',
          runtimePackage: 'css',
          runtimeVersion: '1',
          budgets: {
            maxCanvasPixels: 1920 * 1080,
            maxDevicePixelRatio: 2,
            maxDrawCallsPerFrame: 100,
            maxParticles: 1000,
            maxSimultaneousLivePreviews: 1,
            maxWebGLContexts: 1,
          },
        },
      }),
    ),
    'animation',
  )

  // Non-media artifact
  assert.equal(classifyMediaKind(mockArtifact({ kind: 'code', mediaType: 'text/typescript', filename: 'app.ts' })), null)
  assert.equal(classifyMediaKind(mockArtifact({ kind: 'text', mediaType: 'application/json', filename: 'data.json' })), null)
})

test('formatDayLabel identifies today, yesterday, and historical calendar days', () => {
  const refDate = new Date(2026, 8, 20, 12, 0, 0) // Sep 20, 2026
  const todayTimestamp = new Date(2026, 8, 20, 10, 30, 0).getTime()
  const yesterdayTimestamp = new Date(2026, 8, 19, 15, 0, 0).getTime()
  const pastTimestamp = new Date(2026, 8, 15, 8, 0, 0).getTime()

  const todayLabel = formatDayLabel(todayTimestamp, refDate)
  assert.equal(todayLabel.dayKey, '2026-09-20')
  assert.match(todayLabel.dayLabel, /^Today · Sep 20, 2026/)

  const yesterdayLabel = formatDayLabel(yesterdayTimestamp, refDate)
  assert.equal(yesterdayLabel.dayKey, '2026-09-19')
  assert.match(yesterdayLabel.dayLabel, /^Yesterday · Sep 19, 2026/)

  const pastLabel = formatDayLabel(pastTimestamp, refDate)
  assert.equal(pastLabel.dayKey, '2026-09-15')
  assert.match(pastLabel.dayLabel, /Sep 15, 2026/)
})

test('groupMediaByDate orders days chronologically descending and nests items', () => {
  const refDate = new Date(2026, 8, 20, 12, 0, 0)
  const item1 = toMediaLibraryItem(
    mockArtifact({ artifactId: 'a1', updatedAt: new Date(2026, 8, 20, 10, 0, 0).getTime() }),
    refDate,
  )!
  const item2 = toMediaLibraryItem(
    mockArtifact({ artifactId: 'a2', updatedAt: new Date(2026, 8, 20, 11, 0, 0).getTime() }),
    refDate,
  )!
  const item3 = toMediaLibraryItem(
    mockArtifact({ artifactId: 'a3', updatedAt: new Date(2026, 8, 18, 9, 0, 0).getTime() }),
    refDate,
  )!

  const buckets = groupMediaByDate([item1, item2, item3])
  assert.equal(buckets.length, 2)
  assert.equal(buckets[0].dateKey, '2026-09-20')
  assert.equal(buckets[0].items.length, 2)
  assert.equal(buckets[0].items[0].id, 'a2') // Newer item first within the bucket
  assert.equal(buckets[0].items[1].id, 'a1')

  assert.equal(buckets[1].dateKey, '2026-09-18')
  assert.equal(buckets[1].items.length, 1)
})

test('groupMediaByIteration groups variants belonging to the same collection or chain', () => {
  const refDate = new Date(2026, 8, 20, 12, 0, 0)
  const waveItem1 = toMediaLibraryItem(
    mockArtifact({
      artifactId: 'logo-1',
      sessionId: 'sess-logo',
      collectionId: 'col-wave-1',
      collectionName: 'Cyber Logos',
      label: 'Cyber Logo 1',
      updatedAt: 100,
    }),
    refDate,
  )!
  const waveItem2 = toMediaLibraryItem(
    mockArtifact({
      artifactId: 'logo-2',
      sessionId: 'sess-logo',
      collectionId: 'col-wave-1',
      collectionName: 'Cyber Logos',
      label: 'Cyber Logo 2',
      updatedAt: 110,
    }),
    refDate,
  )!
  const standaloneItem = toMediaLibraryItem(
    mockArtifact({
      artifactId: 'audio-solo',
      sessionId: 'sess-solo',
      label: 'Theme Sound',
      kind: 'audio',
      mediaType: 'audio/wav',
      filename: 'theme.wav',
      updatedAt: 200,
    }),
    refDate,
  )!

  const groups = groupMediaByIteration([waveItem1, waveItem2, standaloneItem])
  assert.equal(groups.length, 2)

  const waveGroup = groups.find((g) => g.id.includes('col-wave-1'))
  assert(Boolean(waveGroup), 'expected wave group to exist')
  assert.equal(waveGroup?.itemCount, 2)
  assert.equal(waveGroup?.title, 'Cyber Logos')

  const soloGroup = groups.find((g) => g.id.includes('sess-solo'))
  assert(Boolean(soloGroup), 'expected solo session group to exist')
  assert.equal(soloGroup?.itemCount, 1)
})

test('filterAndSearchMedia filters by kind, search query, and sorts properly', () => {
  const refDate = new Date(2026, 8, 20, 12, 0, 0)
  const items = normalizeMediaCatalogEntries(
    [
      mockArtifact({ artifactId: 'img-1', label: 'Neon Cyber City', kind: 'image', mediaType: 'image/png', updatedAt: 100 }),
      mockArtifact({ artifactId: 'vid-1', label: 'Robotics Intro Video', kind: 'video', mediaType: 'video/mp4', filename: 'clip.mp4', updatedAt: 200 }),
      mockArtifact({ artifactId: 'aud-1', label: 'Synth Beat', kind: 'audio', mediaType: 'audio/wav', filename: 'synth.wav', updatedAt: 300 }),
    ],
    refDate,
  )

  // 1. Filter by kind
  const imageOnly = filterAndSearchMedia(items, { kind: 'image', searchQuery: '', sortOrder: 'newest' })
  assert.equal(imageOnly.length, 1)
  assert.equal(imageOnly[0].id, 'img-1')

  const videoOnly = filterAndSearchMedia(items, { kind: 'video', searchQuery: '', sortOrder: 'newest' })
  assert.equal(videoOnly.length, 1)
  assert.equal(videoOnly[0].id, 'vid-1')

  // 2. Search query
  const searchResults = filterAndSearchMedia(items, { kind: 'all', searchQuery: 'cyber', sortOrder: 'newest' })
  assert.equal(searchResults.length, 1)
  assert.equal(searchResults[0].id, 'img-1')

  // 3. Sort newest first
  const sortedNewest = filterAndSearchMedia(items, { kind: 'all', searchQuery: '', sortOrder: 'newest' })
  assert.equal(sortedNewest[0].id, 'aud-1')
  assert.equal(sortedNewest[2].id, 'img-1')

  // 4. Sort oldest first
  const sortedOldest = filterAndSearchMedia(items, { kind: 'all', searchQuery: '', sortOrder: 'oldest' })
  assert.equal(sortedOldest[0].id, 'img-1')
  assert.equal(sortedOldest[2].id, 'aud-1')
})
