import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

test('Desktop V3 chat centers message boundaries on the composer outline', async () => {
  const paneSource = await readFile(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
  const composerSource = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const transcriptFrameClass = paneSource.match(/ref=\{contentRef\}\s+className="([^"]+)"/)?.[1]
  const composerFrameClass = composerSource.match(/DESKTOP_V3_COMPOSER_FRAME_CLASS_NAME = "([^"]+)"/)?.[1]
  const userMessageClass = paneSource.match(/function DesktopV3UserMessage[\s\S]*?<div className="([^"]+)">/)?.[1]
  const assistantMessageClass = paneSource.match(/function DesktopV3AssistantMessage[\s\S]*?<div className="([^"]+)">/)?.[1]

  assert.ok(transcriptFrameClass, 'expected the transcript frame class')
  assert.ok(composerFrameClass, 'expected the canonical composer frame class')
  assert.match(transcriptFrameClass, /max-w-\[70rem\]/)
  assert.match(composerFrameClass, /max-w-\[70rem\]/)
  assert.match(transcriptFrameClass, /px-8/)
  assert.match(transcriptFrameClass, /sm:px-12/)
  assert.match(composerFrameClass, /px-4/)
  assert.match(composerFrameClass, /sm:px-6/)
  assert.match(paneSource, /\[scrollbar-gutter:stable_both-edges\]/)

  assert.equal(userMessageClass, 'flex justify-end')
  assert.equal(assistantMessageClass, 'flex justify-start')
  assert.doesNotMatch(userMessageClass, /translate-x|translate-y|pl-|pr-|ml-|mr-/)
  assert.doesNotMatch(assistantMessageClass, /translate-x|translate-y|pl-|pr-|ml-|mr-/)
})
