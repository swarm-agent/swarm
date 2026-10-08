// Purpose: preserve quiet approval/archive actions and the distinct segmented Task scope
// control without losing approval guards, source selection, counts or keyboard access.
// Boundary: OrchestrateView approval JSX, TaskListToolbar and swarm-section.css.
// Leaf elements and CSS rules are the narrowest proof of callbacks, native button semantics
// and styling tokens; they do not prove browser contrast, actual focus or backend authorization.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { Loader2, Sparkles } from 'lucide-react'
import ts from 'typescript'
import { TaskListToolbar } from './task-list-toolbar'

const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
const css = readFileSync(new URL('./swarm-section.css', import.meta.url), 'utf8')
const file = ts.createSourceFile('OrchestrateView.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
const buttons: ts.JsxElement[] = []
function visit(node: ts.Node) {
  if (ts.isJsxElement(node) && node.openingElement.tagName.getText(file) === 'button') buttons.push(node)
  ts.forEachChild(node, visit)
}
visit(file)
function button(marker: string, scope: Record<string, unknown>) {
  const matches = buttons.filter(node => node.getText(file).includes(marker))
  assert.equal(matches.length, 1, marker)
  const compiled = ts.transpileModule(`return (${matches[0].getText(file)})`, {
    fileName: 'button.tsx', compilerOptions: { jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ES2020 },
  }).outputText
  const element = new Function('React', ...Object.keys(scope), compiled)(React, ...Object.values(scope)) as React.ReactElement<{
    className: string; disabled?: boolean; onClick: (event?: unknown) => void; 'aria-pressed'?: boolean
  }>
  assert.match(element.props.className, /\bswarm-outline-action\b/)
  assert.doesNotMatch(element.props.className, /bg-|text-white|shadow|scale-|font-bold/)
  return element
}

test('approval retains each disabled guard, propagation, busy state and media labels', () => {
  const defaults = {
    isApproving: false, isPlanRejected: false, isPlanTaskWithoutStructuredPlan: false,
    isPlanBindingMissingRevision: false, task: { agentType: 'coder' }, variantSlots: [{}], Loader2, Sparkles,
  }
  for (const guard of [null, 'isApproving', 'isPlanRejected', 'isPlanTaskWithoutStructuredPlan', 'isPlanBindingMissingRevision']) {
    let calls = 0
    let stopped = 0
    const element = button('data-testid="approve-task-btn"', {
      ...defaults, ...(guard ? { [guard]: true } : {}), onApprove: () => calls++,
    })
    assert.equal(element.props.disabled, guard !== null)
    element.props.onClick({ stopPropagation: () => stopped++ })
    assert.equal(stopped, 1)
    assert.equal(calls, guard ? 0 : 1)
    const html = renderToStaticMarkup(element)
    assert.match(html, guard === 'isApproving' ? /Approving &amp; Starting/ : /Approve and start session/)
    if (guard === 'isApproving') assert.match(html, /animate-spin/)
  }
  for (const task of [{ agentType: 'image' }, { agentType: 'video' }, { outcomeType: 'media_bundle' }, { outcomeType: 'video_story' }, { outcomeType: 'video_clip' }]) {
    const element = button('data-testid="approve-task-btn"', { ...defaults, task, onApprove: () => {} })
    assert.match(renderToStaticMarkup(element), /Approve &amp; Generate \(1 (Clip|Variant)\)/)
  }
})

const toolbarDefaults = {
  search: '', onSearch: () => {}, source: 'all' as const, onSource: (_value: string) => {},
  status: 'all' as const, onStatus: () => {}, counts: { all: 3, running: 0, needs_review: 2, queued: 0, completed: 1 },
  total: 3, selected: 0, busy: false, onSelectAll: () => {}, onClear: () => {},
  onArchive: () => {}, onDelete: () => {}, onArchived: () => {},
}
function elements(node: React.ReactNode): React.ReactElement<any>[] {
  if (Array.isArray(node)) return node.flatMap(elements)
  if (!React.isValidElement<{ children?: React.ReactNode }>(node)) return []
  return [node, ...elements(node.props.children)]
}
function assertQuiet(element: React.ReactElement<any>) {
  assert.match(element.props.className, /\bswarm-outline-action\b/)
  assert.doesNotMatch(element.props.className, /bg-|text-white|shadow|scale-|font-bold/)
}
// Requirement: TaskListToolbar source buttons must remain a labelled, keyboard-accessible
// segmented pair, not bulk outline actions. Prevent coupled source/status callbacks,
// ambiguous pressed states and counts migrating out of the status tabs at this leaf boundary.
test('source filters retain mutually exclusive pressed state and actions; counts belong to status chips', () => {
  const scopes = [{ value: 'all', label: 'All' }, { value: 'worker', label: 'Workers' }] as const
  for (const selected of ['all', 'worker'] as const) {
    const calls: string[] = []
    const statusCalls: string[] = []
    const tree = TaskListToolbar({ ...toolbarDefaults, source: selected,
      onSource: value => calls.push(value), onStatus: value => statusCalls.push(value),
    })
    const group = elements(tree).find(node => node.props.role === 'group' && node.props['aria-label'] === 'Task scope')
    assert.ok(group, 'source controls retain their accessible group label')
    assert.match(group.props.className, /\bswarm-task-source-control\b/)
    const filters = elements(group).filter(node => node.type === 'button')
    assert.equal(filters.length, 2)
    assert.equal(filters.filter(node => node.props['aria-pressed'] === true).length, 1)
    assert.equal(filters.filter(node => node.props['aria-pressed'] === false).length, 1)
    for (const [index, target] of scopes.entries()) {
      const element = filters[index]
      assert.equal(element.props['data-testid'], `filter-${target.value}-tasks`)
      assert.equal(element.props.type, 'button', 'source selection must not submit a form')
      assert.equal(element.props['aria-pressed'], selected === target.value)
      assert.match(element.props.className, /\bswarm-task-source-filter\b/)
      assert.doesNotMatch(element.props.className, /swarm-outline-action|bg-|text-white|shadow|scale-|font-bold/)
      assert.equal(element.props.children, target.label, 'source labels have no status counts')
      assert.ok(!element.props.disabled, 'both source scopes remain actionable')
      assert.ok(element.props.tabIndex === undefined || element.props.tabIndex === 0, 'both native buttons remain in the tab order')
      assert.match(renderToStaticMarkup(element), new RegExp(`aria-pressed="${selected === target.value}"`))
      element.props.onClick()
    }
    const tabs = elements(tree).filter(node => node.props.role === 'tab')
    assert.equal(tabs.length, 6)
    const expectedCounts = [3, 0, 2, 0, 1, 0]
    for (const [index, tab] of tabs.entries()) {
      const chips = elements(tab).filter(node => node.props.className === 'swarm-task-count')
      assert.equal(chips.length, 1, 'each status tab retains its count chip, including zero')
      assert.equal(React.Children.toArray(chips[0].props.children).join(''), String(expectedCounts[index]))
      assert.equal(chips[0].props['data-zero'], expectedCounts[index] === 0)
    }
    assert.match(renderToStaticMarkup(tree), /Review<span[^>]*>2<\/span>/)
    assert.deepEqual(calls, ['all', 'worker'])
    assert.deepEqual(statusCalls, [], 'source selection must not change the status filter')
  }
})

test('archive retains native disabled guards and archives only the selected rows', () => {
  for (const managementBusy of [false, true]) {
    for (const markedRows of [[], [{ id: 'selected-task' }]]) {
      const calls: unknown[][] = []
      const tree = TaskListToolbar({ ...toolbarDefaults, busy: managementBusy, selected: markedRows.length,
        onArchive: () => calls.push([markedRows, 'archive']),
      })
      const element = elements(tree).find(node => node.type === 'button' && node.props.children === 'Archive')
      if (!markedRows.length) {
        assert.equal(element, undefined, 'bulk archive is absent without selected rows')
        continue
      }
      assert.ok(element)
      assertQuiet(element)
      const disabled = managementBusy
      assert.equal(element.props.disabled, disabled)
      assert.equal(renderToStaticMarkup(element).includes('disabled=""'), disabled)
      // Native disabled buttons suppress clicks; do not invoke their handler artificially.
      if (!disabled) element.props.onClick()
      assert.deepEqual(calls, disabled ? [] : [[markedRows, 'archive']])
    }
  }
  assert.match(source, /onArchive=\{\(\) => void manageTasks\(markedRows, 'archive'\)\}/, 'view forwards only selected rows')
})

function rule(selector: string) {
  const start = css.indexOf(selector)
  assert.notEqual(start, -1, selector)
  return css.slice(start, css.indexOf('}', start) + 1)
}

test('quiet actions share semantic New Task outline, focus and enabled-only interactions', () => {
  const base = rule('.swarm-section .swarm-outline-action,')
  assert.match(base, /\.swarm-section \.swarm-new-task-cta/)
  assert.match(base, /border: 1px solid currentColor/)
  assert.match(base, /color: var\(--swarm-text-accent\)/)
  assert.match(base, /background-color: transparent/)
  assert.match(base, /box-shadow: none/)
  assert.match(rule('.swarm-section .swarm-outline-action:not(:disabled):hover,'), /var\(--swarm-surface-hover\)/)
  assert.match(rule('.swarm-section .swarm-outline-action:not(:disabled):active,'), /var\(--swarm-surface-subtle\)/)
  assert.match(rule('.swarm-section .swarm-outline-action:focus-visible,'), /outline: 2px solid currentColor; outline-offset: 2px/)
  assert.match(rule('.swarm-section .swarm-outline-action:disabled {'), /opacity: 0.4; cursor: not-allowed/)
})

// Requirement: swarm-section.css gives Task scope its own inset segmented surface and
// uses the shared section focus ring for both native buttons. Prevent accidental outline
// restyling, lost pressed-state distinction or state-dependent geometry; rule inspection
// is the narrowest deterministic layer for these tokens, not a browser focus/contrast test.
test('source filters use segmented base and pressed styling with the shared focus ring', () => {
  const control = rule('.swarm-task-source-control {')
  assert.match(control, /display: flex/)
  assert.match(control, /background: var\(--swarm-background-inset\)/)
  const base = rule('.swarm-task-source-filter {')
  assert.match(base, /color: var\(--swarm-text-muted\)/)
  assert.match(base, /padding: 0 12px/)
  assert.match(base, /border-radius: 5px/)
  assert.doesNotMatch(base, /border:|background:|box-shadow:|font-weight:/)
  const pressed = rule('.swarm-task-source-filter[aria-pressed="true"] {')
  assert.match(pressed, /background: var\(--swarm-surface-hover\)/)
  assert.match(pressed, /color: var\(--swarm-text\)/)
  assert.match(pressed, /box-shadow: 0 1px 3px #0003/)
  assert.doesNotMatch(pressed, /border:|border-width:|padding:|font-size:|font-weight:/)
  const focus = rule('.swarm-section :focus-visible {')
  assert.match(focus, /outline: 2px solid var\(--swarm-accent\)/)
  assert.match(focus, /outline-offset: 2px/)
})
