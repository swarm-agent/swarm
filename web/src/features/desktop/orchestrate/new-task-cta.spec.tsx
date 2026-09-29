// Purpose: all three OrchestrateView New Task entry points must retain one decorative
// plus, a clean accessible label, and the deploy-dialog action without a filled CTA.
// Boundary: the actual button JSX and scoped swarm-section.css. Extracting these
// leaf elements avoids mounting unrelated orchestration runtimes; CSS assertions
// guard semantic token wiring, not browser contrast or visual verification.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { Plus } from 'lucide-react'
import ts from 'typescript'

const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
const css = readFileSync(new URL('./swarm-section.css', import.meta.url), 'utf8')
const file = ts.createSourceFile('OrchestrateView.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
const buttons: ts.JsxElement[] = []
function visit(node: ts.Node) {
  if (ts.isJsxElement(node) && node.openingElement.tagName.getText(file) === 'button'
    && node.getText(file).includes('New Task</span>')) buttons.push(node)
  ts.forEachChild(node, visit)
}
visit(file)

test('all New Task entry points render one decorative plus and open the deploy dialog', () => {
  assert.equal(buttons.length, 3)
  for (const button of buttons) {
    const calls: boolean[] = []
    const compiled = ts.transpileModule(`return (${button.getText(file)})`, {
      fileName: 'new-task-button.tsx',
      compilerOptions: { jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ES2020 },
    }).outputText
    const element = new Function('React', 'Plus', 'setIsDeployModalOpen', compiled)(
      React, Plus, (open: boolean) => calls.push(open),
    ) as React.ReactElement<{ onClick: () => void; className: string }>
    const html = renderToStaticMarkup(element)
    assert.equal((html.match(/<svg\b/g) || []).length, 1)
    assert.match(html, /<svg[^>]*aria-hidden="true"/)
    assert.match(html, /<span>New Task<\/span>/)
    assert.doesNotMatch(html, /\+\s*New Task/)
    assert.match(element.props.className, /\bswarm-new-task-cta\b/)
    assert.doesNotMatch(element.props.className, /bg-|text-white|shadow|active:scale/)
    assert.deepEqual(calls, [])
    element.props.onClick()
    assert.deepEqual(calls, [true])
  }
})

test('shared CTA style uses scoped semantic colors for outline and interaction states', () => {
  const rule = (suffix = '') => {
    const selector = `.swarm-section .swarm-new-task-cta${suffix}`
    const start = css.indexOf(`${selector} {`)
    assert.notEqual(start, -1, selector)
    return css.slice(start, css.indexOf('}', start) + 1)
  }
  assert.match(rule(), /border: 1px solid currentColor/)
  assert.match(rule(), /color: var\(--swarm-text-accent\)/)
  assert.match(rule(), /background-color: transparent/)
  assert.match(rule(), /box-shadow: none/)
  assert.match(rule(':hover'), /background-color: var\(--swarm-surface-hover\)/)
  assert.match(rule(':active'), /background-color: var\(--swarm-surface-subtle\)/)
  assert.match(rule(':focus-visible'), /outline: 2px solid currentColor; outline-offset: 2px/)
})
