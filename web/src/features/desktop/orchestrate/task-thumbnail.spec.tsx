import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskThumbnail } from './task-thumbnail'

// Purpose: absent/unsupported derivatives must render a lightweight explicit
// placeholder; known derivatives render only a lazy image, never a video or an
// original fallback. SSR proves transfer-triggering markup, not browser latency.
test('thumbnail placeholder emits no automatic media request', () => {
  const html = renderToStaticMarkup(<TaskThumbnail title="Output" />)
  assert.match(html, /Preview unavailable/)
  assert.match(html, /open original/)
  assert.doesNotMatch(html, /<img|<video|src=|<link/)
})

test('thumbnail renders only its lazy derivative', () => {
  const src = '/v3/projects/p/tasks/t/deliverables/d?field=media&sha256=abc&preview=1'
  const html = renderToStaticMarkup(<TaskThumbnail title="Output" src={src} />)
  assert.match(html, /loading="lazy"/)
  assert.match(html, /preview=1/)
  assert.doesNotMatch(html, /<video|<link|data:image/)
})
