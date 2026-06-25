import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopV3RunStatusPill, formatDesktopV3RunTimer } from './desktop-v3-run-status'

test('DesktopV3RunStatusPill renders compacting label with live timer', () => {
  const startedAt = 10_000
  const model = { kind: 'active' as const, label: 'Compacting', startedAt, active: true }

  assert.equal(formatDesktopV3RunTimer(model, 72_345), '1:02')
  const markup = renderToStaticMarkup(<DesktopV3RunStatusPill model={model} now={72_345} />)

  assert.match(markup, /Compacting/)
  assert.match(markup, /1:02/)
})
