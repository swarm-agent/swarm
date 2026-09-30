// Purpose: soften Orchestrate actions without losing approval guards, source selection,
// or archive eligibility. Boundary: actual OrchestrateView leaf JSX and scoped CSS.
// Extracting leaf elements is the narrowest layer for callback/disabled/label contracts;
// token assertions do not prove browser contrast, layout, or backend authorization.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { Loader2, Sparkles } from 'lucide-react'
import ts from 'typescript'

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
    assert.match(html, guard === 'isApproving' ? /Approving &amp; Starting/ : /Approve &amp; Start Session/)
    if (guard === 'isApproving') assert.match(html, /animate-spin/)
  }
  for (const task of [{ agentType: 'image' }, { agentType: 'video' }, { outcomeType: 'media_bundle' }, { outcomeType: 'video_story' }, { outcomeType: 'video_clip' }]) {
    const element = button('data-testid="approve-task-btn"', { ...defaults, task, onApprove: () => {} })
    assert.match(renderToStaticMarkup(element), /Approve &amp; Generate \(1 (Clip|Variant)\)/)
  }
})

test('source filters retain mutually exclusive pressed state, counts and actions', () => {
  for (const selected of ['all', 'worker']) {
    const calls: string[] = []
    for (const target of ['all', 'worker']) {
      const element = button(`data-testid="filter-${target}-tasks"`, {
        taskSourceFilter: selected, liveTasks: [{}, {}, {}], workerTasksCount: 2,
        setTaskSourceFilter: (value: string) => calls.push(value),
      })
      assert.equal(element.props['aria-pressed'], selected === target)
      assert.match(element.props.className, /swarm-task-source-filter/)
      assert.match(renderToStaticMarkup(element), target === 'all' ? /All tasks \(3\)/ : /Worker tasks \(2\)/)
      element.props.onClick()
    }
    assert.deepEqual(calls, ['all', 'worker'])
  }
})

test('archive retains native disabled guards and archives only the selected rows', () => {
  for (const managementBusy of [false, true]) {
    for (const markedRows of [[], [{ id: 'selected-task' }]]) {
      const calls: unknown[][] = []
      const element = button('Archive selected</button>', {
        managementBusy, markedRows, manageTasks: (...args: unknown[]) => calls.push(args),
      })
      const disabled = managementBusy || markedRows.length === 0
      assert.equal(element.props.disabled, disabled)
      assert.equal(renderToStaticMarkup(element).includes('disabled=""'), disabled)
      // Native disabled buttons suppress clicks; do not invoke their handler artificially.
      if (!disabled) element.props.onClick()
      assert.deepEqual(calls, disabled ? [] : [[markedRows, 'archive']])
    }
  }
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
