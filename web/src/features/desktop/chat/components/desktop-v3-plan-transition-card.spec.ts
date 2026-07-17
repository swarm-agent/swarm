import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const paneSource = readFileSync(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
const markdownSource = readFileSync(new URL('./chat-markdown.tsx', import.meta.url), 'utf8')

test('live and durable plan transitions use the minimal card language', () => {
  assert.match(markdownSource, /data-plan-tool-transition data-plan-transition-tone=\{tone\}/)
  assert.match(markdownSource, /rounded-xl border border-\[var\(--app-border\)\] bg-\[var\(--app-surface-subtle\)\]/)
  assert.doesNotMatch(markdownSource, /data-plan-tool-transition[^]*border-l-2/)

  const lifecycleStart = paneSource.indexOf('function DesktopV3PlanExecutionBreak')
  const handoffStart = paneSource.indexOf('function DesktopV3PlanCheckpointHandoff')
  assert.ok(lifecycleStart >= 0 && handoffStart > lifecycleStart)
  const lifecycleSource = paneSource.slice(lifecycleStart, handoffStart)
  assert.match(lifecycleSource, /rounded-xl border border-\[var\(--app-border\)\] bg-\[var\(--app-surface-subtle\)\]/)
  assert.doesNotMatch(lifecycleSource, /border-l-2/)

  assert.match(paneSource, /function DesktopV3PlanHandoff/)
  assert.match(paneSource, /<ChatMarkdown content=\{item\.summary\} \/>/)
  assert.match(paneSource, /item\.body \? <ChatMarkdown content=\{item\.body\} \/>/)
})
