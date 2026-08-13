import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

function source(name: string): string {
  return readFileSync(fileURLToPath(new URL(name, import.meta.url)), 'utf8')
}

test('artifact composer renders removable labeled chips and submits structured refs', () => {
  const composer = source('./desktop-v3-agentic-composer.tsx')
  assert.match(composer, /data-testid="desktop-composer-artifact-chip"/)
  assert.match(composer, /selection\.label/)
  assert.match(composer, /removeDesktopV3ArtifactMessageSelection/)
  assert.match(composer, /selections: artifactSelections/)
  assert.match(composer, /artifactSelections,/)
  assert.match(composer, /onAddToChat=\{\(\{ label, selection \}\)/)
  assert.match(composer, /onUseThisDesign=\{\(\{ label, selection \}\)/)
})

test('existing and routed panes preserve artifact refs across retry boundaries', () => {
  const existing = source('./desktop-v3-existing-conversation-pane.tsx')
  const routed = source('./desktop-v3-new-session-pane.tsx')
  assert.match(existing, /initialArtifactSelections=\{storedOperation\?\.request\.artifact_selections \?\? \[\]\}/)
  assert.match(existing, /artifactSelections,/)
  assert.match(existing, /JSON\.stringify\(retainedArtifacts\) === JSON\.stringify\(artifactSelections\)/)
  assert.match(existing, /Retry the retained message without changing its text or attachments/)
  assert.match(routed, /snapshot\.artifactSelections/)
  assert.match(routed, /artifactSelectionRequest=\{artifactSelectionRequest\}/)
})
