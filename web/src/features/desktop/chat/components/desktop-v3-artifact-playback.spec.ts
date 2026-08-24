import assert from 'node:assert/strict'
import test from 'node:test'

import type { DesktopV3ArtifactIterationDescriptor } from '../../session-v3/artifact-iteration-protocol'
import {
  DesktopV3ArtifactSeekAcknowledger,
  resolveDesktopV3ArtifactAutoplaySection,
} from './desktop-v3-artifact-playback'

const descriptor: DesktopV3ArtifactIterationDescriptor = {
  version: 'swarm.iteration/v1',
  durationMs: 12_000,
  sections: [
    { id: 'part-1', label: 'Part 1', startMs: 0, endMs: 4_000, narration: [] },
    { id: 'part-2', label: 'Part 2', startMs: 4_000, endMs: 8_000, narration: [] },
    { id: 'part-3', label: 'Part 3', startMs: 8_000, endMs: 12_000, narration: [] },
  ],
}

test('section playback keeps the exact authored boundary instead of widening to the full artifact', () => {
  const section = descriptor.sections[1]!
  assert.deepEqual(section, { id: 'part-2', label: 'Part 2', startMs: 4_000, endMs: 8_000, narration: [] })
})

test('a stale preview cannot consume a clicked iteration autoplay request', () => {
  const request = { artifactKey: 'clicked-artifact', sectionId: 'part-3' }

  assert.equal(resolveDesktopV3ArtifactAutoplaySection(
    request,
    'clicked-artifact',
    'previous-artifact',
    descriptor,
  ), null)

  assert.equal(resolveDesktopV3ArtifactAutoplaySection(
    request,
    'clicked-artifact',
    'clicked-artifact',
    descriptor,
  )?.startMs, 8_000)
})

test('playback starts only after the clicked section seek is acknowledged', () => {
  const sent: Array<{ id: string; timeMs: number }> = []
  const acknowledger = new DesktopV3ArtifactSeekAcknowledger((timeMs) => {
    const id = `seek-${sent.length + 1}`
    sent.push({ id, timeMs })
    return id
  })
  let playbackStarts = 0

  acknowledger.setOnSettled(() => { playbackStarts += 1 })
  acknowledger.queue(8_000)

  assert.deepEqual(sent, [{ id: 'seek-1', timeMs: 8_000 }])
  assert.equal(playbackStarts, 0, 'sending the section seek must not start playback')
  assert.equal(acknowledger.acknowledge('stale-preview-seek'), false)
  assert.equal(playbackStarts, 0, 'a stale preview acknowledgement must not start playback')
  assert.equal(acknowledger.acknowledge('seek-1'), true)
  assert.equal(playbackStarts, 1, 'the exact clicked section acknowledgement starts playback')
})

test('loading the clicked artifact clears an unacknowledged seek sent to the replaced iframe', () => {
  const sent: Array<{ id: string; timeMs: number }> = []
  const acknowledger = new DesktopV3ArtifactSeekAcknowledger((timeMs) => {
    const id = `seek-${sent.length + 1}`
    sent.push({ id, timeMs })
    return id
  })

  acknowledger.queue(8_000)
  acknowledger.reset()
  acknowledger.queue(8_000)

  assert.deepEqual(sent, [
    { id: 'seek-1', timeMs: 8_000 },
    { id: 'seek-2', timeMs: 8_000 },
  ])
  assert.equal(acknowledger.acknowledge('seek-1'), false, 'the replaced iframe seek cannot block or settle the new frame')
  assert.equal(acknowledger.acknowledge('seek-2'), true)
})

test('a queued playback tick cannot overtake the initial clicked section seek', () => {
  const sent: Array<{ id: string; timeMs: number }> = []
  const acknowledger = new DesktopV3ArtifactSeekAcknowledger((timeMs) => {
    const id = `seek-${sent.length + 1}`
    sent.push({ id, timeMs })
    return id
  })
  let playbackStarts = 0

  acknowledger.setOnSettled(() => { playbackStarts += 1 })
  acknowledger.queue(8_000)
  acknowledger.queue(8_025)

  acknowledger.acknowledge('seek-1')
  assert.equal(playbackStarts, 0)
  assert.deepEqual(sent, [
    { id: 'seek-1', timeMs: 8_000 },
    { id: 'seek-2', timeMs: 8_025 },
  ])

  acknowledger.acknowledge('seek-2')
  assert.equal(playbackStarts, 1)
})
