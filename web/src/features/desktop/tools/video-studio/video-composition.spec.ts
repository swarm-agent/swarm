import assert from 'node:assert/strict'
import test from 'node:test'

import { detachVideoComposition, resolveVideoComposition, updateVideoCompositionGeometry, videoCompositionViewport, type VideoCompositionCatalogWire, type VideoCompositionLinkWire, type VideoCompositionSourceWire } from './video-composition'

const portraitSlot = (id: string, x: number) => ({
  id, requirement: `Portrait ${id}`, geometry: { x, y: .1, width: .25, height: .8 }, z_index: 1,
  fit: 'cover' as const, alignment_x: .5, alignment_y: .5, mask: { kind: 'rounded_rect' as const, radius: .04 }, aspect_lock: 9 / 16,
})
const catalog: VideoCompositionCatalogWire = { schema_version: 1, layouts: [{ id: 'phones', slots: [portraitSlot('a', .05), portraitSlot('b', .375), portraitSlot('c', .7)] }] }
const link: VideoCompositionLinkWire = { layout_id: 'phones' }

test('composition viewport letterboxes exact project aspect across window sizes', () => {
  assert.deepEqual(videoCompositionViewport(1000, 1000, 1920, 1080), { x: 0, y: 218.75, width: 1000, height: 562.5, scale: 1000 / 1920 })
  assert.deepEqual(videoCompositionViewport(540, 960, 1080, 1920), { x: 0, y: 0, width: 540, height: 960, scale: .5 })
})

test('composition resolves two and three portrait slots into even bounded pixels', () => {
  const three = resolveVideoComposition(catalog, link, 1920, 1080)
  assert.equal(three.length, 3)
  assert.ok(three.every((slot) => slot.pixels.width % 2 === 0 && slot.pixels.height % 2 === 0))
  assert.ok(three.every((slot) => slot.pixels.x >= 0 && slot.pixels.x + slot.pixels.width <= 1920))
  assert.equal(resolveVideoComposition({ ...catalog, layouts: [{ ...catalog.layouts[0], slots: catalog.layouts[0].slots.slice(0, 2) }] }, link, 1920, 1080).length, 2)
})

test('linked geometry edit updates catalog while shot edit creates an override', () => {
  const geometry = { x: .1, y: .2, width: .3, height: .6 }
  const linked = updateVideoCompositionGeometry({ catalog, link, slotId: 'a', geometry, scope: 'linked' })
  assert.deepEqual(linked.catalog.layouts[0].slots[0].geometry, geometry)
  assert.equal(linked.link.overrides, undefined)
  const shot = updateVideoCompositionGeometry({ catalog, link, slotId: 'a', geometry, scope: 'shot' })
  assert.deepEqual(shot.link.overrides, [{ slot_id: 'a', geometry }])
  assert.deepEqual(catalog.layouts[0].slots[0].geometry, { x: .05, y: .1, width: .25, height: .8 })
})

test('shot override wins and detach materializes independent slots', () => {
  const overridden = resolveVideoComposition(catalog, { ...link, overrides: [{ slot_id: 'b', fit: 'contain', geometry: { x: .4, y: .2, width: .2, height: .6 } }] }, 1920, 1080)
  assert.equal(overridden[1].fit, 'contain')
  assert.equal(overridden[1].geometry.y, .2)
  const detached = detachVideoComposition(catalog, { ...link, overrides: [{ slot_id: 'b', clear_source: true }] })
  assert.equal(detached.detached, true)
  assert.equal(detached.layout_id, undefined)
  assert.equal(detached.detached_slots?.length, 3)
})

test('composition preserves independent source and timeline timing plus audio policy', () => {
  const source: VideoCompositionSourceWire = { source_ref: 'videosrc_phone', media_type: 'video/mp4', source_start_ms: 1000, source_end_ms: 5000, timeline_start_ms: 500, timeline_end_ms: 3500, audio_policy: 'include', gain: .6 }
  const [resolved] = resolveVideoComposition(catalog, { ...link, overrides: [{ slot_id: 'a', source }] }, 1920, 1080)
  assert.deepEqual(resolved.source, source)
  assert.ok(resolved.source)
  const resolvedSource = resolved.source as VideoCompositionSourceWire
  assert.equal(resolvedSource.source_end_ms - resolvedSource.source_start_ms, 4000)
  assert.equal(resolvedSource.timeline_end_ms - resolvedSource.timeline_start_ms, 3000)
})

test('disabled and pending unfilled links resolve without inventing media', () => {
  assert.deepEqual(resolveVideoComposition(catalog, { disabled: true }, 1920, 1080), [])
  const pending = resolveVideoComposition(catalog, link, 1920, 1080)
  assert.ok(pending.every((slot) => slot.source === undefined))
})
