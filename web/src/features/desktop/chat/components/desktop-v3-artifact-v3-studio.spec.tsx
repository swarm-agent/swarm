import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopV3ArtifactV3Sidebar } from './desktop-v3-artifact-v3-sidebar'
import { DesktopV3ArtifactV3Studio } from './desktop-v3-artifact-v3-studio'
import type { DesktopV3NativeArtifactSummary } from '../../session-v3/artifact-v3-api'

const artifact: DesktopV3NativeArtifactSummary = {
  artifactId: 'artifact-1',
  artifactRef: 'opaque-artifact-ref',
  ownerSessionId: 'session-1',
  label: 'Pricing workspace',
  description: 'Complete project',
  status: 'ready',
  head: { revisionRef: 'opaque-revision-ref', commitOid: '1234567890abcdef', treeOid: 'tree-1', generation: 2, selectedEventSeq: 9 },
  partCount: 96,
  turnCount: 2,
  updatedAt: 100,
}

test('Artifact V3 sidebar opens the native complete-project Studio entry', () => {
  let opened = ''
  const markup = renderToStaticMarkup(<DesktopV3ArtifactV3Sidebar artifacts={[artifact]} onOpenArtifact={(entry) => { opened = entry.artifactId }} />)
  assert.match(markup, /Artifact V3 projects/)
  assert.match(markup, /96 parts/)
  assert.match(markup, /data-artifact-v3-sidebar-id="artifact-1"/)
  assert.equal(opened, '')
})

test('Artifact V3 Studio mounts as a real interactive dialog while native data loads', () => {
  const markup = renderToStaticMarkup(<DesktopV3ArtifactV3Studio artifact={artifact} open onOpenChange={() => undefined} onIterate={() => undefined} />)
  assert.match(markup, /aria-label="Artifact V3 Studio"/)
  assert.match(markup, /One complete Git project/)
  assert.match(markup, /Artifact V3/)
  assert.match(markup, /Refresh Artifact V3 Studio/)
  assert.doesNotMatch(markup, /Storyboard|Video Studio|MP4/)
})
