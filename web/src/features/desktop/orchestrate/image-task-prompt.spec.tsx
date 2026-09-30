// Purpose: image prompt consent is opt-in, reset-safe, and drives the actual New Task
// POST without rewriting the original prompt or borrowing repository authority.
// Threat: count/intent/dialog reuse silently invokes Router or reuses stale consent.
// Boundaries: ImagePromptControls/reducer and OrchestrateView.handleDeployModalSubmit.
// Narrow layer: invoke leaf controls and the production submit closure with a recording
// transport; no unrelated Desktop runtime mounting or source-string-only payload proof.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import ts from 'typescript'
import { ImagePromptControls, imagePromptReducer, initialImagePromptState, imagePromptEnhancement, imageExecutionLabel } from './image-task-prompt'
import { mapBackendTask } from '../state/desktop-projects-state'
import { resolveTaskImpendingAgents } from './orchestrate-task-helpers'

const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
const file = ts.createSourceFile('OrchestrateView.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
let submit: ts.Expression | undefined
let resetEffect: ts.CallExpression | undefined
function visit(node: ts.Node) {
  if (ts.isVariableDeclaration(node) && node.name.getText(file) === 'handleDeployModalSubmit') submit = node.initializer
  if (ts.isCallExpression(node) && node.expression.getText(file) === 'useEffect'
    && node.arguments[1]?.getText(file) === '[taskIntent, isDeployModalOpen]') resetEffect = node
  ts.forEachChild(node, visit)
}
visit(file)

function compile(expression: string, context: Record<string, unknown>) {
  const js = ts.transpileModule(`return (${expression})`, { compilerOptions: { target: ts.ScriptTarget.ES2020 } }).outputText
  return new Function(...Object.keys(context), js)(...Object.values(context))
}

function inputs(element: React.ReactNode): React.ReactElement<any>[] {
  if (!React.isValidElement(element)) return []
  const props = element.props as { children?: React.ReactNode }
  return [ ...(element.type === 'input' ? [element] : []), ...React.Children.toArray(props.children).flatMap(inputs) ]
}

test('accessible explicit choices never enable Router by count and clear consent at one image', () => {
  let state = imagePromptReducer(initialImagePromptState, { type: 'count', count: 4 })
  assert.equal(state.aiVariants, false)
  const render = () => ImagePromptControls({ count: state.count, aiVariants: state.aiVariants,
    onChange: aiVariants => { state = imagePromptReducer(state, { type: 'choice', aiVariants }) } })
  assert.match(renderToStaticMarkup(render()), /Image prompt mode/)
  assert.match(renderToStaticMarkup(render()), /Same prompt/)
  assert.match(renderToStaticMarkup(render()), /AI variants/)
  assert.doesNotMatch(renderToStaticMarkup(render()), /Router prompt-generation step/)
  let radios = inputs(render())
  assert.equal(radios[0].props.checked, true)
  assert.equal(radios[1].props.disabled, false)
  radios[1].props.onChange()
  assert.equal(state.aiVariants, true)
  assert.equal(imageExecutionLabel(state.count, state.aiVariants), 'Router → image model')
  inputs(render())[0].props.onChange()
  assert.equal(state.aiVariants, false)
  inputs(render())[1].props.onChange()
  state = imagePromptReducer(state, { type: 'count', count: 1 })
  radios = inputs(render())
  assert.equal(radios[1].props.disabled, true)
  assert.equal(radios[0].props.checked, true)
  assert.equal(state.aiVariants, false)
  radios[1].props.onChange() // Defensive reducer also rejects stale/programmatic consent.
  assert.equal(state.aiVariants, false)
  state = imagePromptReducer(state, { type: 'count', count: 25 })
  assert.equal(state.aiVariants, false)
  assert.equal(imageExecutionLabel(state.count, state.aiVariants), 'Direct image generation')
})

test('image task cards identify model generation without a Designer model fallback', () => {
  const task = { agentType: 'image' as const, model: 'selected-image-model' }
  const selected = resolveTaskImpendingAgents(task)
  assert.equal(selected[0].label, 'Image model generation')
  assert.equal(selected[0].model, 'selected-image-model')
  const configured = resolveTaskImpendingAgents({ agentType: 'image' }, [], undefined, undefined, { image: 'configured-image-model' })
  assert.equal(configured[0].model, 'configured-image-model')
  const missing = resolveTaskImpendingAgents({ agentType: 'image' })
  assert.equal(missing[0].model, 'Account default')
})

test('production intent/dialog reset effect removes prior consent', () => {
  assert.ok(resetEffect)
  let state = { count: 5, aiVariants: true }
  const effect = compile(resetEffect.arguments[0].getText(file), {
    dispatchImagePrompt: (action: Parameters<typeof imagePromptReducer>[1]) => { state = imagePromptReducer(state, action) },
  })
  effect()
  assert.deepEqual(state, { count: 5, aiVariants: false })
})

test('actual submit POST preserves image prompt/settings/attachments and suppresses stale opt-in', async () => {
  assert.ok(submit)
  for (const [count, consent, expected] of [[1, true, false], [2, false, false], [2, true, true], [25, false, false]] as const) {
    const requests: { url: string; body: any }[] = []
    const actions: any[] = []
    const noop = () => {}
    const media = [{ id: 'asset', kind: 'image', filename: 'reference.png' }]
    const context = {
      isDeployingTaskRef: { current: false }, newTaskPrompt: '  café\noriginal brief  ', selectedProject: { id: 'project', workspaces: [{ path: 'a' }, { path: 'b' }] },
      taskIntent: 'image', selectedImageModel: 'configured-image-model', imageAspectRatio: '9:16', imageResolution: '2k',
      imageVariants: count, imagePromptState: { count, aiVariants: consent }, imagePromptEnhancement,
      taggedMedia: media, autoApproveTask: false, pendingDeployRequestRef: { current: null },
      taskDeployRequestIdentity: () => ({ clientTaskId: 'request' }),
      taskWorkspaceSelection: () => { throw new Error('image must not resolve repository') },
      requestJson: async (url: string, options: { body: string }) => { requests.push({ url, body: JSON.parse(options.body) }); return { task: { id: 'request' } } },
      mapBackendTask,
      desktopProjects: { invalidate: noop, setOptimisticTasks: (_project: string, update: (tasks: any[]) => any[]) => { assert.equal(update([])[0].id, 'request') } }, dispatchImagePrompt: (action: unknown) => actions.push(action),
      setIsDeployingTask: noop, setDeployError: noop, setIsDeployModalOpen: noop, setNewTaskPrompt: noop, setNewTaskModelOverride: noop, setTaggedMedia: noop,
    }
    await compile(submit.getText(file), context)()
    assert.equal(requests.length, 1)
    assert.equal(requests[0].url, '/v3/projects/project/tasks')
    assert.deepEqual(requests[0].body, {
      id: 'request', prompt: context.newTaskPrompt, intent: 'image', agent: 'image', enhance_prompt: expected,
      aspect_ratio: '9:16', resolution: '2k', variant_count: count, model: 'configured-image-model',
      auto_approve: false, deploy_session: true, attached_media: media,
    })
    assert.deepEqual(actions, [{ type: 'reset' }])
    assert.equal(context.isDeployingTaskRef.current, false)
  }
})
