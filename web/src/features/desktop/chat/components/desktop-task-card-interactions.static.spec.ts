import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const sourceURL = new URL('./chat-markdown.tsx', import.meta.url)
const planURL = new URL('./desktop-plan-execution-sidebar.tsx', import.meta.url)
const sidecarURL = new URL('./desktop-plan-subagent-list.tsx', import.meta.url)

test('task cards bind visible/focus/debounced-hover leases and clean observer timer and demand', async () => {
  const source = await readFile(sourceURL, 'utf8')
  assert.match(source, /new IntersectionObserver/)
  assert.match(source, /observer\.disconnect\(\)/)
  assert.match(source, /TASK_CARD_HOVER_DEBOUNCE_MS/)
  assert.match(source, /clearTimeout\(hoverTimerRef\.current\)/)
  assert.match(source, /controller\.acquireSessionDemand\(ownerKey, row\.childSessionId\)/)
  assert.match(source, /demandRef\.current\?\.release\(\)/)
  assert.match(source, /closest\('\[data-task-stop\]'\)/)
})

test('task card navigation, stop isolation, pending/error state, and keyboard/touch controls remain explicit', async () => {
  const source = await readFile(sourceURL, 'utf8')
  assert.match(source, /role=\{canNavigate \? 'link'/)
  assert.match(source, /event\.key === 'Enter' \|\| event\.key === ' '/)
  assert.match(source, /event\.stopPropagation\(\)/)
  assert.match(source, /setStopPending\(true\)/)
  assert.match(source, /setStopMessage\('Stopping…'\)/)
  assert.match(source, /error instanceof Error \? error\.message : 'Stop failed'/)
  assert.match(source, /sm:hidden">Stop/)
})

test('task cards switch to a titleless vertical subagent list from their own width', async () => {
  const [source, theme] = await Promise.all([
    readFile(sourceURL, 'utf8'),
    readFile(new URL('../../../../theme.css', import.meta.url), 'utf8'),
  ])
  assert.match(source, /data-task-card/)
  assert.match(source, /task-card-narrow-only/)
  assert.match(source, /Subagent \{rowNumber\}/)
  assert.match(theme, /container-name: task-card/)
  assert.match(theme, /@container task-card \(max-width: 23rem\)/)
  assert.match(theme, /\.task-card-container > \[data-task-card-header\]/)
  assert.match(theme, /\.task-card-swarm-grid[\s\S]*grid-template-columns: minmax\(0, 1fr\)/)
  assert.match(theme, /@container task-card \(max-width: 12rem\)[\s\S]*\.task-card-narrow-detail/)
})

test('the full plan sidebar keeps explicit subagent navigation and stop actions', async () => {
  const [plan, subagents] = await Promise.all([readFile(planURL, 'utf8'), readFile(sidecarURL, 'utf8')])
  assert.doesNotMatch(plan, /data-display-mode|data-plan-thin-rail/)
  assert.match(plan, /mode="full"/)
  assert.match(subagents, /min-h-11/)
  assert.match(subagents, /aria-label=\{`Stop \$\{title\}`\}/)
  assert.match(subagents, /aria-label=\{`Open \$\{details\}`\}/)
})
