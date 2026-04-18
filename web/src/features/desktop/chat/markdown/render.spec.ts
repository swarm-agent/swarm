import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MarkdownRenderer } from './render'

test('MarkdownRenderer preserves nested list content for desktop session-origin steps', () => {
  const content = [
    '1. Add Playwright to `web/`',
    '2. Add one spec:',
    '   - `desktop-normal-session-origin.spec.ts`',
    '3. Have it:',
    '   - start backend on temp state',
    '   - open desktop',
    '   - create/send in a normal session',
    '   - query `/v1/sessions`',
    '   - assert newest session metadata is not background-targeted',
    '4. Add a second spec later for `/commit`:',
    '   - that one should become background and should label as `bg:commit`',
  ].join('\n')

  const html = renderToStaticMarkup(createElement(MarkdownRenderer, { content }))

  assert.match(html, /<ol[\s\S]*<ul[\s\S]*desktop-normal-session-origin\.spec\.ts[\s\S]*<\/ul>[\s\S]*<\/ol>/)
  assert.match(html, />Have it:</, `expected nested ordered-list heading text, got ${html}`)
  assert.match(html, />Add a second spec later for <[\s\S]*\/commit/, `expected final ordered-list heading text, got ${html}`)
  assert.doesNotMatch(html, />ve it:</, 'nested markdown text lost its first characters')
  assert.doesNotMatch(html, />d a second spec later/, 'nested markdown text lost its first characters')
})
