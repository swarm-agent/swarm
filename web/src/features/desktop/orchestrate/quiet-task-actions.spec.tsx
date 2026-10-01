// Purpose: soften Orchestrate actions without losing approval guards, source selection,
// or archive eligibility. Boundary: OrchestrateView approval JSX, TaskListToolbar and scoped CSS.
// Extracting leaf elements is the narrowest layer for callback/disabled/label contracts;
// token assertions do not prove browser contrast, layout, or backend authorization.
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
test('source filters retain mutually exclusive pressed state and actions; counts belong to status chips', () => {
  for (const selected of ['all', 'worker'] as const) {
    const calls: string[] = []
    const tree = TaskListToolbar({ ...toolbarDefaults, source: selected, onSource: value => calls.push(value) })
    for (const target of ['all', 'worker']) {
      const element = elements(tree).find(node => node.props['data-testid'] === `filter-${target}-tasks`)!
      assertQuiet(element)
      assert.equal(element.props['aria-pressed'], selected === target)
      assert.match(element.props.className, /swarm-task-source-filter/)
      assert.equal(element.props.children, target === 'all' ? 'All' : 'Workers')
      element.props.onClick()
    }
    assert.match(renderToStaticMarkup(tree), /Review<span[^>]*>2<\/span>/)
    assert.deepEqual(calls, ['all', 'worker'])
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

test('quiet actions share semantic New Task outline, focus and enabled-only interactions', () => {
  function rule(selector: string) {
    const start = css.indexOf(selector)
    assert.notEqual(start, -1, selector)
    return css.slice(start, css.indexOf('}', start) + 1)
  }
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
  const unselected = rule('.swarm-section .swarm-task-source-filter[aria-pressed="false"]')
  assert.match(unselected, /border-color: transparent/)
  assert.match(unselected, /color: var\(--swarm-text-muted\)/)
  assert.doesNotMatch(unselected, /border-width|padding|font-weight/)
})
