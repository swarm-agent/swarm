import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MarkdownRenderer } from './render'

test('MarkdownRenderer auto-links bare assistant URLs', () => {
  const content = [
    'Open https://example.com/path?q=1.',
    'Email mailto:support@example.com for help.',
    'Keep [explicit links](https://example.com/docs) clickable.',
  ].join('\n')

  const html = renderToStaticMarkup(createElement(MarkdownRenderer, { content }))

  assert.match(html, /<a[^>]*href="https:\/\/example\.com\/path\?q=1"[^>]*target="_blank"/)
  assert.match(html, /<a[^>]*href="https:\/\/example\.com\/path\?q=1"[\s\S]*>\s*<span>https:\/\/example\.com\/path\?q=1<\/span>\s*<\/a><span>\.<\/span>/)
  assert.match(html, /<a[^>]*href="mailto:support@example\.com"[^>]*target="_blank"/)
  assert.match(html, /<a[^>]*href="https:\/\/example\.com\/docs"[^>]*target="_blank"[\s\S]*>\s*<span>explicit links<\/span>\s*<\/a>/)
})

test('MarkdownRenderer renders tagged copy blocks without swallowing markdown', () => {
  const content = [
    '## Before',
    '',
    '<copy label="command">',
    'swarm status',
    '</copy>',
    '',
    '- after item',
  ].join('\n')

  const html = renderToStaticMarkup(createElement(MarkdownRenderer, { content }))

  assert.match(html, />Before</)
  assert.match(html, /\/copy · command/)
  assert.match(html, /swarm status/)
  assert.match(html, /<ul[\s\S]*after item[\s\S]*<\/ul>/)
})

test('MarkdownRenderer leaves copy tags inside code fences literal', () => {
  const content = ['```html', '<copy label="literal">not actionable</copy>', '```'].join('\n')

  const html = renderToStaticMarkup(createElement(MarkdownRenderer, { content }))

  assert.doesNotMatch(html, /\/copy · literal/)
  assert.match(html, /&lt;copy label=&quot;literal&quot;&gt;not actionable&lt;\/copy&gt;/)
})

test('MarkdownRenderer leaves unclosed copy tags as markdown while streaming', () => {
  const content = 'Intro **bold**\n\n<copy label="partial">still streaming\n\n- visible item'

  const html = renderToStaticMarkup(createElement(MarkdownRenderer, { content }))

  assert.doesNotMatch(html, /\/copy · partial/)
  assert.match(html, /<strong>\s*<span>bold<\/span>\s*<\/strong>/)
  assert.match(html, /&lt;copy label=&quot;partial&quot;&gt;still streaming/)
  assert.match(html, /<ul[\s\S]*visible item[\s\S]*<\/ul>/)
})

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

test('MarkdownRenderer preserves underscores inside identifiers and inline code', () => {
  const content = [
    'Keep snake_case, SOME_VALUE, and path/to/my_file_name.ts visible.',
    'Inline code keeps `some_value_name` literal.',
    'Do not italicize foo_bar_baz or pre_middle_post.',
  ].join('\n')

  const html = renderToStaticMarkup(createElement(MarkdownRenderer, { content }))

  assert.match(html, /snake_case/)
  assert.match(html, /SOME_VALUE/)
  assert.match(html, /my_file_name\.ts/)
  assert.match(html, /<code[^>]*>\s*some_value_name\s*<\/code>/)
  assert.match(html, /foo_bar_baz/)
  assert.match(html, /pre_middle_post/)
  assert.doesNotMatch(html, /<em>/)
})

test('MarkdownRenderer still supports underscore emphasis outside identifiers', () => {
  const content = 'This keeps _intentional emphasis_ while leaving words_with_underscores alone.'

  const html = renderToStaticMarkup(createElement(MarkdownRenderer, { content }))

  assert.match(html, /<em>\s*<span>intentional emphasis<\/span>\s*<\/em>/)
  assert.match(html, /words_with_underscores/)
})
