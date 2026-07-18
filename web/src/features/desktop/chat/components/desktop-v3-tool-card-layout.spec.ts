import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const paneSource = readFileSync(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
const markdownSource = readFileSync(new URL('./chat-markdown.tsx', import.meta.url), 'utf8')

test('bash and task tool cards use the full transcript width', () => {
  assert.match(paneSource, /const usesFullWidthCard = toolName === "bash" \|\| toolName === "task";/)
  assert.match(paneSource, /usesFullWidthCard \? "w-full max-w-full" : "max-w-\[calc\(100%-2rem\)\]"/)
})

test('expanded tool result bodies stay within half the viewport and scroll internally', () => {
  assert.match(markdownSource, /BASH_EXPANDED_MAX_HEIGHT = "50vh"/)
  assert.match(markdownSource, /TOOL_RESULT_BODY_CLASS = "max-h-\[50vh\] min-w-0 overflow-y-auto overflow-x-hidden overscroll-contain"/)
  assert.match(markdownSource, /<div className=\{TOOL_RESULT_BODY_CLASS\}>/)
  assert.match(markdownSource, /cn\(TOOL_RESULT_BODY_CLASS, "mt-2 grid gap-2 font-mono pr-1"\)/)
})
