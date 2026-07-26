import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

const paneSourceUrl = new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url)

function componentBody(source: string, name: string, nextName: string): string {
  const start = source.indexOf(`export function ${name}`)
  const end = source.indexOf(`export function ${nextName}`, start + 1)
  assert.notEqual(start, -1, `expected ${name}`)
  assert.notEqual(end, -1, `expected ${nextName}`)
  return source.slice(start, end)
}

test('existing conversation draft state stays inside the composer-local controller', async () => {
  const source = await readFile(paneSourceUrl, 'utf8')
  const composer = componentBody(
    source,
    'DesktopV3ExistingConversationComposer',
    'DesktopV3ExistingConversationPane',
  )
  const pane = componentBody(
    source,
    'DesktopV3ExistingConversationPane',
    'DesktopV3RenderItemView',
  )

  assert.match(composer, /const \[draft, setDraft\] = useState\(initialDraft\)/)
  assert.match(composer, /onDraftChange=\{setDraft\}/)
  assert.match(composer, /hasStoredOperation \|\| Boolean\(draft\.trim\(\)\)/)
  assert.doesNotMatch(pane, /const \[draft, setDraft\] = useState/)
  assert.doesNotMatch(pane, /onDraftChange=\{setDraft\}/)
  assert.match(pane, /key=\{normalizedSessionId\}/)
  assert.match(pane, /controllerRef=\{composerControllerRef\}/)
})

test('existing conversation virtualizes every loaded transcript row', async () => {
  const source = await readFile(paneSourceUrl, 'utf8')
  const pane = componentBody(
    source,
    'DesktopV3ExistingConversationPane',
    'DesktopV3RenderItemView',
  )

  assert.match(source, /import \{ useVirtualizer \} from "@tanstack\/react-virtual"/)
  assert.match(pane, /const transcriptVirtualizer = useVirtualizer\(/)
  assert.match(pane, /count: renderItems\.length/)
  assert.match(pane, /measureElement: \(element\) => element\.getBoundingClientRect\(\)\.height/)
  assert.match(pane, /virtualTranscriptRows\.map/)
  assert.match(pane, /height: `\$\{transcriptVirtualizer\.getTotalSize\(\)\}px`/)
  assert.doesNotMatch(pane, /renderItems\.slice/)
  assert.doesNotMatch(pane, /visibleRenderItems\.map/)
})

test('existing conversation rows receive a stable suggested-prompt callback', async () => {
  const source = await readFile(paneSourceUrl, 'utf8')
  const pane = componentBody(
    source,
    'DesktopV3ExistingConversationPane',
    'DesktopV3RenderItemView',
  )

  assert.match(pane, /const stableSubmit = useCallback\([\s\S]*?, \[\]\)/)
  assert.match(pane, /const stableSuggestedPrompt = stableSubmit/)
  assert.match(pane, /onSuggestedPrompt=\{stableSuggestedPrompt\}/)
  assert.doesNotMatch(pane, /onSuggestedPrompt=\{\(prompt\) =>/)
})
