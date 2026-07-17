import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const paneSource = readFileSync(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
const markdownSource = readFileSync(new URL('./chat-markdown.tsx', import.meta.url), 'utf8')

test('file action cards span the transcript and grouped actions stack vertically', () => {
  assert.match(paneSource, /max-w-\[70rem\] flex-col gap-5/)
  assert.doesNotMatch(paneSource, /md:grid-cols-2/)
  assert.match(paneSource, /toolName === "bash" \|\| toolName === "task" \|\| \["read", "list", "search", "edit"\]\.includes/)
  assert.match(paneSource, /usesFullWidthCard \? "w-full max-w-full" : "max-w-\[calc\(100%-2rem\)\]"/)
  assert.match(markdownSource, /<div className="flex min-w-0 flex-col gap-2">/)
  assert.doesNotMatch(markdownSource, /grid min-w-0 grid-cols-1 gap-2 md:grid-cols-2/)
})
