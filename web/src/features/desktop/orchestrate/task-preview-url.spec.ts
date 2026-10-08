import assert from 'node:assert/strict'
import { test } from 'node:test'
import { taskPreviewURL } from './task-preview-url'

// Purpose: initial cards must never request original media. This URL boundary
// accepts only authenticated derivative-capable task references; explicit open
// retains the separate original URL. No browser timing claim is made here.
test('card previews accept only authenticated small derivative references', () => {
  const original = `/v3/projects/p/tasks/t/deliverables/d?field=media&sha256=${'a'.repeat(64)}`
  assert.equal(taskPreviewURL(original), `${original}&preview=1`)
  for (const value of ['data:image/png;base64,AAAA', 'https://example.invalid/image.png', '/v3/sessions/s/media/a', '/v3/projects/p/tasks/t/deliverables/d?field=media', undefined]) {
    assert.equal(taskPreviewURL(value), undefined)
  }
})
