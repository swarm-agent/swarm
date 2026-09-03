import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const pane = readFileSync(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
const sidebar = readFileSync(new URL('./desktop-v3-artifact-v2-sidebar.tsx', import.meta.url), 'utf8')
const studio = readFileSync(new URL('./desktop-v3-artifact-v2-studio.tsx', import.meta.url), 'utf8')
const api = readFileSync(new URL('../../session-v3/artifact-v2-api.ts', import.meta.url), 'utf8')
const model = readFileSync(new URL('../../session-v3/artifact-v2-studio-model.ts', import.meta.url), 'utf8')

test('new-work Desktop path consumes the dedicated Artifact V2 catalog and Studio', () => {
  assert.match(pane, /fetchDesktopV3ArtifactV2Catalog\(normalizedSessionId\)/)
  assert.match(pane, /<DesktopV3ArtifactV2Sidebar/)
  assert.match(pane, /<DesktopV3ArtifactV2Studio/)
  assert.match(api, /\/artifact-v2/)
  for (const source of [api, model, sidebar, studio]) {
    assert.doesNotMatch(source, /acceptedPartHeads|collectionId|variantId|git_projection|ProjectionReservation|official composition head/)
  }
})

test('Artifact V2 Desktop surface is historical and exposes no write affordance', () => {
  assert.match(studio, /Historical Artifact V2 · read-only/)
  assert.doesNotMatch(studio, />Iterate</)
  assert.doesNotMatch(studio, />Lock</)
  assert.doesNotMatch(studio, />Unlock</)
  assert.doesNotMatch(studio, />Select head</)
  assert.doesNotMatch(studio, /selectDesktopV3ArtifactV2Candidate/)
  assert.doesNotMatch(studio, /updateDesktopV3ArtifactV2Composition/)
})
