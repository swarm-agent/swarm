import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskAttemptHistory } from './task-attempt-history'

// Requirement: retained history is explicitly loaded, not a second polling or
// transcript authority. SSR is the narrow initial presentation layer; interactive
// pagination/navigation requires parent browser validation and is not claimed here.
test('history initially offers explicit bounded retrieval without invented outcomes', () => {
  const html = renderToStaticMarkup(<TaskAttemptHistory projectId="project" taskId="task" onOpen={() => { throw new Error('initial render must not navigate') }} />)
  assert.match(html, /Task session history/)
  assert.match(html, /View previous runs/)
  assert.doesNotMatch(html, /No ready summary|Open swarm session/)
})
