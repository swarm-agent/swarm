import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const paneSource = readFileSync(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
const markdownSource = readFileSync(new URL('./chat-markdown.tsx', import.meta.url), 'utf8')

test('file action cards span the transcript and grouped actions stack vertically', () => {
  assert.match(paneSource, /max-w-\[70rem\] flex-col gap-5/)
  assert.doesNotMatch(paneSource, /md:grid-cols-2/)
  assert.match(paneSource, /"flex w-full min-w-0 justify-start"/)
  assert.match(paneSource, /className="w-full min-w-0 max-w-full"/)
  assert.doesNotMatch(paneSource, /usesFullWidthCard/)
  assert.doesNotMatch(paneSource, /max-w-\[calc\(100%-2rem\)\].*toolMessage/)
  assert.match(markdownSource, /<div className="flex min-w-0 flex-col gap-2">/)
  assert.doesNotMatch(markdownSource, /grid min-w-0 grid-cols-1 gap-2 md:grid-cols-2/)
})

test('expanded tool result bodies stay within half the viewport and scroll internally', () => {
  assert.match(markdownSource, /BASH_EXPANDED_HEIGHT = "50vh"/)
  assert.match(markdownSource, /TOOL_RESULT_BODY_CLASS = "max-h-\[50vh\] min-w-0 overflow-y-auto overflow-x-hidden overscroll-contain"/)
  assert.match(markdownSource, /className=\{TOOL_RESULT_BODY_CLASS\}/)
  assert.match(markdownSource, /cn\(TOOL_RESULT_BODY_CLASS, "mt-2 grid gap-2 font-mono pr-1"\)/)
})
