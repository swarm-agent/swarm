import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const galleryURL = new URL('./desktop-v3-artifact-gallery.tsx', import.meta.url)
const composerURL = new URL('./desktop-v3-agentic-composer.tsx', import.meta.url)
const paneURL = new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url)
const routedPaneURL = new URL('./desktop-v3-new-session-pane.tsx', import.meta.url)
const apiURL = new URL('../../session-v3/artifact-api.ts', import.meta.url)

test('artifact gallery selection actions attach opaque references without artifact bytes', async () => {
  const [gallery, composer, api] = await Promise.all([
    readFile(galleryURL, 'utf8'),
    readFile(composerURL, 'utf8'),
    readFile(apiURL, 'utf8'),
  ])

  assert.match(gallery, /onAddToChat\(pendingChatArtifacts\.map\(\(artifact\) => \(\{/)
  assert.match(gallery, /onUseThisDesign\(\{ label: selected\.label, selection: canonicalSelection \}\)/)
  assert.match(composer, /appendDesktopV3ArtifactMessageSelections\(current, artifacts\.map/)
  assert.match(composer, /appendDesktopV3ArtifactMessageSelections\(current, artifactSelectionRequests\)/)
  assert.match(composer, /artifacts\.map\(\(\{ label, selection \}\) => \(\{ \.\.\.selection, label, action: 'select' \}\)\)/)
  assert.match(composer, /selections: artifactSelections/)
  assert.match(api, /session_id: entry\.sessionId/)
  assert.match(api, /collection_id: entry\.collectionId/)
  assert.match(api, /variant_id: entry\.artifactId/)
  assert.match(api, /event_seq: entry\.eventSeq/)
  assert.doesNotMatch(api, /desktopV3ArtifactSelection\([^)]*\)[\s\S]{0,300}content:/)
  assert.doesNotMatch(gallery, /onAddToChat\(\{[^}]*content:/)
})

test('artifact selections survive retained-operation retry and routed-session handoff', async () => {
  const [pane, routedPane, composer] = await Promise.all([
    readFile(paneURL, 'utf8'),
    readFile(routedPaneURL, 'utf8'),
    readFile(composerURL, 'utf8'),
  ])

  assert.match(pane, /initialArtifactSelections=\{storedOperation\?\.request\.artifact_selections \?\? \[\]\}/)
  assert.match(pane, /JSON\.stringify\(retainedArtifacts\) === JSON\.stringify\(artifactSelections\)/)
  assert.match(pane, /Retry the retained message without changing its text or attachments/)
  assert.match(routedPane, /snapshot\.artifactSelections/)
  assert.match(routedPane, /artifactSelectionRequest=\{artifactSelectionRequest\}/)
  assert.match(composer, /routedComposerSnapshot\?\.artifactSelections \?\? initialArtifactSelections/)
  assert.match(composer, /setArtifactSelections\(routedComposerSnapshot\.artifactSelections \?\? \[\]\)/)
})
