import test from 'node:test'
import assert from 'node:assert/strict'

import {
  desktopV3ArtifactIterationChangeDescription,
  desktopV3ArtifactIterationMessage,
  desktopV3ArtifactIterationNextSectionDescription,
  normalizeDesktopV3ArtifactIterationDescriptor,
} from './artifact-iteration-protocol'

const descriptorWire = {
  version: 'swarm.iteration/v1',
  duration_ms: 4_000,
  sections: [
    {
      id: 'opening',
      label: 'Opening',
      start_ms: 0,
      end_ms: 2_000,
      narration: [{ start_ms: 0, end_ms: 1_000, text: 'Hello', detail: 'INTRO' }],
    },
    {
      id: 'payoff',
      label: 'Payoff',
      start_ms: 2_000,
      end_ms: 4_000,
      narration: [],
    },
  ],
}

test('normalizes bounded ordered animation iteration sections and narration', () => {
  const descriptor = normalizeDesktopV3ArtifactIterationDescriptor(descriptorWire)
  assert(descriptor)
  assert.equal(descriptor.durationMs, 4_000)
  assert.deepEqual(descriptor.sections.map((section) => section.id), ['opening', 'payoff'])
  assert.equal(descriptor.sections[0]?.narration[0]?.text, 'Hello')
})

test('rejects overlapping or out-of-bounds animation sections', () => {
  assert.equal(normalizeDesktopV3ArtifactIterationDescriptor({
    ...descriptorWire,
    sections: [
      descriptorWire.sections[0],
      { ...descriptorWire.sections[1], start_ms: 1_999 },
    ],
  }), null)
  assert.equal(normalizeDesktopV3ArtifactIterationDescriptor({ ...descriptorWire, duration_ms: 0 }), null)
})

test('builds opaque player messages and an exact section-change brief', () => {
  assert.deepEqual(desktopV3ArtifactIterationMessage('request-1', 'seek', 2_200.4), {
    protocol: 'swarm-player/v1',
    id: 'request-1',
    method: 'seek',
    type: 'seek',
    time_ms: 2_200,
    params: { time_ms: 2_200 },
  })
  assert.deepEqual(desktopV3ArtifactIterationMessage('request-2', 'describe'), {
    protocol: 'swarm-player/v1',
    id: 'request-2',
    method: 'describe',
    type: 'describe',
  })
  const descriptor = normalizeDesktopV3ArtifactIterationDescriptor(descriptorWire)
  assert(descriptor)
  const prompt = desktopV3ArtifactIterationChangeDescription(descriptor.sections[0]!)
  assert.match(prompt, /Create 5 new alternatives for animation section "Opening"/)
  assert.match(prompt, /00:00\.000 to 00:02\.000/)
  assert.match(prompt, /Hello — INTRO/)
  assert.match(prompt, /preserves every other section/)
  assert.match(prompt, /attached exact current-round source artifact/)
  assert.match(prompt, /sibling alternatives from that round source/)
  assert.match(prompt, /exact section_target/)
  assert.match(prompt, /do not compose or splice content across branches/)

  const nextPrompt = desktopV3ArtifactIterationNextSectionDescription(descriptor.sections[1]!)
  assert.match(nextPrompt, /newly accepted complete artifact head/)
  assert.match(nextPrompt, /sole source artifact for this next ordered section/)
  assert.match(nextPrompt, /Do not compose or splice content from any other branch/)
})
