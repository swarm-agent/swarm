import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const paneSource = readFileSync(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
const markdownSource = readFileSync(new URL('./chat-markdown.tsx', import.meta.url), 'utf8')

test('live and durable plan transitions use full-width minimal cards', () => {
  assert.match(markdownSource, /className="mb-2 w-full min-w-0 py-1.5" data-plan-tool-transition/)
  assert.match(markdownSource, /w-full min-w-0 rounded-xl border border-\[var\(--app-border\)\] bg-\[var\(--app-surface-subtle\)\]/)
  assert.doesNotMatch(markdownSource, /data-plan-tool-transition[^]*border-l-2/)

  const toolWrapperStart = paneSource.indexOf('function DesktopV3ToolMessage')
  const reasoningStart = paneSource.indexOf('function DesktopV3ReasoningMessage', toolWrapperStart)
  assert.ok(toolWrapperStart >= 0 && reasoningStart > toolWrapperStart)
  const toolWrapperSource = paneSource.slice(toolWrapperStart, reasoningStart)
  assert.match(toolWrapperSource, /\["read", "list", "search", "edit", "plan-manage", "plan_manage"\]/)
  assert.match(toolWrapperSource, /usesFullWidthCard \? "w-full max-w-full" : "max-w-\[calc\(100%-2rem\)\]"/)

  const lifecycleStart = paneSource.indexOf('function DesktopV3PlanExecutionBreak')
  const handoffStart = paneSource.indexOf('function DesktopV3PlanCheckpointHandoff')
  assert.ok(lifecycleStart >= 0 && handoffStart > lifecycleStart)
  const lifecycleSource = paneSource.slice(lifecycleStart, handoffStart)
  assert.match(lifecycleSource, /flex w-full min-w-0 justify-start/)
  assert.match(lifecycleSource, /w-full min-w-0 rounded-xl border border-\[var\(--app-border\)\] bg-\[var\(--app-surface-subtle\)\]/)
  assert.doesNotMatch(lifecycleSource, /max-w-/)
  assert.doesNotMatch(lifecycleSource, /border-l-2/)

  assert.match(paneSource, /function DesktopV3PlanHandoff/)
  assert.match(paneSource, /className="flex w-full min-w-0 justify-start py-1" data-testid=\{testId\}/)
  assert.match(paneSource, /aria-label="At a glance"[^]*className="mb-3 w-full min-w-0 rounded-xl/)
  assert.match(paneSource, /<ChatMarkdown content=\{item\.summary\} \/>/)
  assert.match(paneSource, /item\.body \? <ChatMarkdown content=\{item\.body\} \/>/)
})
