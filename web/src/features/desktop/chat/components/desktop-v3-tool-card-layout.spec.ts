import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const source = readFileSync(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')

test('bash and task tool cards use the full transcript width', () => {
  assert.match(source, /const usesFullWidthCard = toolName === "bash" \|\| toolName === "task";/)
  assert.match(source, /usesFullWidthCard \? "w-full max-w-full" : "max-w-\[calc\(100%-2rem\)\]"/)
})
