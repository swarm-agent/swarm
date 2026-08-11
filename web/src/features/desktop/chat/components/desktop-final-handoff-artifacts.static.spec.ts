import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const paneURL = new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url)
const apiURL = new URL('../../session-v3/artifact-api.ts', import.meta.url)

test('final handoff gallery uses authenticated opaque artifact routes and a sandboxed HTML preview', async () => {
  const [pane, api] = await Promise.all([readFile(paneURL, 'utf8'), readFile(apiURL, 'utf8')])
  assert.match(pane, /handoff\.artifacts\.length > 0/)
  assert.match(pane, /fetchDesktopV3Artifact\(sessionId, selected\.artifactId/)
  assert.match(pane, /selected\.mediaType === "text\/html"/)
  assert.match(pane, /sandbox=""/)
  assert.match(pane, /referrerPolicy="no-referrer"/)
  assert.doesNotMatch(pane, /changedFiles.*DesktopV3ArtifactGallery/)
  assert.match(api, /apiFetch\(desktopV3ArtifactEndpoint/)
  assert.match(api, /encodeURIComponent\(normalizedArtifactId\)/)
})
