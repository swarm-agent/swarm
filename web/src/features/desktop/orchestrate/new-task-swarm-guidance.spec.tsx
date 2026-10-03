// Purpose: New Task's actual JSX must describe permission-dependent Swarm tools,
// not manual Plan mode, and navigate to the canonical permissions settings pane.
// Extracted leaf rendering/clicks prove the dialog link without mounting unrelated
// agent runtimes; this does not claim browser layout or live permission changes.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import ts from 'typescript'

const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
const file = ts.createSourceFile('view.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
test('big-feature guidance links to permissions and closes only the dialog', () => {
  let guidance: ts.JsxElement | undefined
  function visit(node: ts.Node) {
    if (ts.isJsxElement(node) && node.openingElement.tagName.getText(file) === 'p' && node.getText(file).includes('Swarm handles big features directly')) guidance = node
    ts.forEachChild(node, visit)
  }
  visit(file)
  assert.ok(guidance)
  const calls: unknown[] = []
  let linkProps: any
  const Link = (props: any) => { linkProps = props; return <a href={`${props.to}#${props.hash}`}>{props.children}</a> }
  const compiled = ts.transpileModule(`return (${guidance.getText(file)})`, { compilerOptions: { jsx: ts.JsxEmit.React } }).outputText
  const render = new Function('React', 'Link', 'projectPageLink', 'setIsDeployModalOpen', 'taskIntent', 'featureSize', compiled)
  const element = render(React, Link, (section: string) => { calls.push(section); return { to: '/swarm/settings' } }, (open: boolean) => calls.push(open), 'code', 'big')
  const html = renderToStaticMarkup(element)
  assert.match(html, /Bash and test execution subject to configured tools and permissions/)
  assert.match(html, /href="\/swarm\/settings#permissions"/)
  assert.match(html, />\/permissions<\/a>/)
  assert.match(html, /no permissions are changed automatically/)
  assert.doesNotMatch(html, /Plan.mode|@plan/)
  assert.deepEqual(calls, ['settings'])
  linkProps.onClick()
  assert.deepEqual(calls, ['settings', false])
  const small = renderToStaticMarkup(render(React, Link, () => { throw Error('small task navigated') }, () => { throw Error('changed state') }, 'code', 'small'))
  assert.match(small, /@coder/)
  assert.doesNotMatch(small, /Bash|href=/)
  const settings = readFileSync(new URL('./orchestrate-settings.tsx', import.meta.url), 'utf8')
  assert.match(settings, /selected === 'permissions' && <PermissionsSettingsPage/)
  assert.doesNotMatch(source, /featureSize === 'big' \? 'plan'|@plan \(Orchestrator Plan Mode\)|Plan-mode orchestration/)
})
