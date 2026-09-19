import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const galleryURL = new URL('./desktop-v3-artifact-gallery.tsx', import.meta.url)
const thumbnailURL = new URL('./desktop-v3-artifact-preview-thumbnail.tsx', import.meta.url)
const sidebarURL = new URL('./desktop-v3-artifact-sidebar.tsx', import.meta.url)
const markdownURL = new URL('./chat-markdown.tsx', import.meta.url)

test('gallery renders native HTML5 audio controls for audio/* media types', async () => {
  const gallery = await readFile(galleryURL, 'utf8')

  // Verifies Lucide Music icon is imported
  assert.match(gallery, /import\s*\{[\s\S]*?\bMusic\b[\s\S]*?\}\s*from\s*'lucide-react'/)

  // Verifies audio media type or kind checks
  assert.match(gallery, /selected\.mediaType\.startsWith\('audio\/'\)\s*\|\|\s*selected\.kind\s*===\s*'audio'/)

  // Verifies HTML5 <audio controls> with key, src, preload, and data attribute
  assert.match(gallery, /<audio\s+key=\{`\$\{previewURL\}:\$\{previewRetry\}`\}\s+src=\{previewURL\}\s+controls\s+autoPlay=\{false\}\s+preload="metadata"\s+className="w-full"\s+data-artifact-audio-player/)

  // Verifies error handling
  assert.match(gallery, /onError=\{\(\)\s*=>\s*setPreviewError\('The browser could not decode or load this audio\.'\)\}/)
})

test('preview thumbnail renders audio controls and interactive preview for audio/* media types', async () => {
  const thumbnail = await readFile(thumbnailURL, 'utf8')

  // Verifies Lucide Music icon is imported
  assert.match(thumbnail, /import\s*\{[\s\S]*?\bMusic\b[\s\S]*?\}\s*from\s*'lucide-react'/)

  // Verifies interactivePreview marks audio as interactive
  assert.match(thumbnail, /artifact\.mediaType\.startsWith\('audio\/'\)\s*\|\|\s*artifact\.kind\s*===\s*'audio'/)

  // Verifies hasAudioPreview checks previewActive and previewURL
  assert.match(thumbnail, /const hasAudioPreview\s*=\s*previewActive\s*&&\s*\(artifact\.mediaType\.startsWith\('audio\/'\)\s*\|\|\s*artifact\.kind\s*===\s*'audio'\)\s*&&\s*Boolean\(previewURL\)/)

  // Verifies HTML5 <audio controls> with preload and data attribute
  assert.match(thumbnail, /<audio\s+src=\{previewURL\}\s+controls\s+preload="metadata"\s+className="w-full max-w-full"\s+data-artifact-audio-preview/)

  // Verifies Music icon is shown in audio container and fallback
  assert.match(thumbnail, /data-artifact-audio-container/)
  assert.match(thumbnail, /<Music\s+className="size-4 shrink-0"\s+aria-hidden="true"\s*\/>/)
})

test('sidebar renders Music icon indicators for audio/* media types', async () => {
  const sidebar = await readFile(sidebarURL, 'utf8')

  // Verifies Lucide Music icon is imported
  assert.match(sidebar, /import\s*\{[\s\S]*?\bMusic\b[\s\S]*?\}\s*from\s*'lucide-react'/)

  // Verifies thumbnail shows Music icon indicator
  assert.match(sidebar, /const isAudio\s*=\s*artifact\.mediaType\.startsWith\('audio\/'\)\s*\|\|\s*artifact\.kind\s*===\s*'audio'/)
  assert.match(sidebar, /<Music\s+className="size-5 text-\[var\(--app-text-muted\)\]"\s+aria-label="Audio artifact"\s+data-artifact-audio-indicator\s*\/>/)

  // Verifies compact document rows show Music icon indicator for audio
  assert.match(sidebar, /<Music\s+className="size-3\.5 shrink-0 text-\[var\(--app-text-muted\)\]"\s+aria-label="Audio artifact"\s+data-artifact-audio-indicator\s*\/>/)

  // Verifies attach tooltip distinguishes audio
  assert.match(sidebar, /Attach audio to chat/)
})

test('chat markdown ManageArtifactCard renders variations selector for multi-variant audio/artifacts', async () => {
  const markdown = await readFile(markdownURL, 'utf8')
  assert.match(markdown, /data-testid="artifact-variants-selector"/)
  assert.match(markdown, /rawVariants\.length > 1/)
  assert.match(markdown, /setSelectedVariantIndex/)
  assert.match(markdown, /isAudioGeneration \? `Sound Clips \(\$\{rawVariants\.length\}\)`/)
  assert.match(markdown, /isAudioGeneration \? <Music size=\{14\} \/> : <Sparkles size=\{14\} \/>/)
})

test('artifact gallery renders clean titles on left generation group and full readable prompt in center', async () => {
  const gallery = await readFile(galleryURL, 'utf8')
  assert.match(gallery, /data-artifact-generation-group/)
  assert.match(gallery, /truncate font-semibold text-\[11px\]/)
  assert.match(gallery, /selected\.description \|\| selected\.collectionDescription/)
  assert.match(gallery, /data-artifact-audio-player/)
})

test('chat markdown ManageArtifactCard renders interactive sound bars with play controls', async () => {
  const markdown = await readFile(markdownURL, 'utf8')
  assert.match(markdown, /function ChatAudioSoundBar/)
  assert.match(markdown, /data-testid="audio-sound-bar"/)
  assert.match(markdown, /data-artifact-soundbar-index/)
  assert.match(markdown, /<Pause size=\{13\} fill="currentColor" \/>/)
  assert.match(markdown, /<Play size=\{13\} fill="currentColor"/)
  assert.match(markdown, /document\.querySelectorAll\("audio"\)/)
})

test('sidebar renders interactive sound bars for audio entries with direct play button', async () => {
  const sidebar = await readFile(sidebarURL, 'utf8')
  assert.match(sidebar, /function SidebarAudioSoundBar/)
  assert.match(sidebar, /data-artifact-sidebar-sound-bar/)
  assert.match(sidebar, /isAudioGroup && 'grid-cols-1 gap-1\.5'/)
  assert.match(sidebar, /<Pause size=\{11\} fill="currentColor" \/>/)
  assert.match(sidebar, /<Play size=\{11\} fill="currentColor"/)
  assert.match(sidebar, /document\.querySelectorAll\('audio'\)/)
})

test('preview thumbnail renders audio with aspect-auto sound bar layout', async () => {
  const thumbnail = await readFile(thumbnailURL, 'utf8')
  assert.match(thumbnail, /isAudio && '!aspect-auto'/)
})

test('chat markdown ManageArtifactCard renders select action on individual sound bars', async () => {
  const markdown = await readFile(markdownURL, 'utf8')
  assert.match(markdown, /data-testid="select-audio-clip-button"/)
  assert.match(markdown, /handleSelectClip/)
  assert.match(markdown, /soundBarArtifactSelection/)
  assert.match(markdown, /onArtifactSelections=\{onArtifactSelections\}/)
})

test('normalizeDesktopV3ArtifactCatalogEntry populates label, description, kind, and previewable from presentation', async () => {
  const { normalizeDesktopV3ArtifactCatalogEntry, desktopV3ArtifactMessageSelection } = await import('../../session-v3/artifact-api')
  const entry = normalizeDesktopV3ArtifactCatalogEntry({
    id: 'variant-audio-1',
    collection_id: 'col-audio',
    session_id: 'sess-audio',
    event_seq: 12,
    status: 'ready',
    filename: 'clip.mp3',
    media_type: 'audio/mp3',
    presentation: {
      kind: 'audio',
      label: 'Neurofunk DnB 174 BPM',
      description: 'Heavy rolling reese bass with cybernetic accents',
      previewable: true,
    },
  })
  assert.ok(entry)
  assert.equal(entry.label, 'Neurofunk DnB 174 BPM')
  assert.equal(entry.description, 'Heavy rolling reese bass with cybernetic accents')
  assert.equal(entry.kind, 'audio')
  assert.equal(entry.previewable, true)

  const selection = desktopV3ArtifactMessageSelection(entry, 'select')
  assert.equal(selection.variant_id, 'variant-audio-1')
  assert.equal(selection.session_id, 'sess-audio')
  assert.equal(selection.collection_id, 'col-audio')
  assert.equal(selection.event_seq, 12)
  assert.equal(selection.action, 'select')
  assert.match(selection.label, /Neurofunk DnB 174 BPM/)
  assert.match(selection.description || '', /Heavy rolling reese bass with cybernetic accents/)
})



