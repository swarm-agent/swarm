// Purpose: compact media controls expose all supplied capability values, preserve scenes,
// and save defaults only on an explicit gesture. Threat: compacting drops values/data,
// invents unsupported options or silently saves settings. Authority: MediaTaskSelect,
// MediaTaskScenes, MediaTaskDefault and OrchestrateView model-change closures.
// Narrow layer: leaf callbacks and compiled production closures, not live generation.
import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { readFileSync } from 'node:fs'
import ts from 'typescript'
import { MediaTaskSelect, MediaTaskScenes, MediaTaskDefault, MediaTaskCost } from './media-task-controls'
import { resolveAllowedVideoDurations } from './videoTaskSettings'

function elements(node: React.ReactNode, type: string): React.ReactElement<any>[] {
  if (!React.isValidElement(node)) return []
  return [...(node.type === type ? [node] : []), ...React.Children.toArray((node.props as any).children).flatMap(child => elements(child, type))]
}

test('capability values remain directly selectable without unsupported fallbacks', () => {
  const capabilities = { resolution_durations: { '720p': [4, 8], '1080p': [8] } }
  for (const resolution of ['720p', '1080p']) {
    const values = resolveAllowedVideoDurations(capabilities, resolution)
    let selected = ''
    const tree = MediaTaskSelect({ label: 'Duration', value: 8, values, suffix: 's', onChange: value => { selected = value } })
    const select = elements(tree, 'select')[0]
    assert.equal(select.props['aria-label'], 'Duration')
    assert.equal(select.props.value, 8)
    assert.deepEqual(elements(tree, 'option').map(option => option.props.value), values)
    select.props.onChange({ target: { value: String(values[0]) } })
    assert.equal(selected, String(values[0]))
  }
  const absent = MediaTaskSelect({ label: 'Size', value: '', values: [], onChange: () => assert.fail('no invented option') })
  assert.equal(elements(absent, 'select').length, 0)
  assert.match(renderToStaticMarkup(absent), /Not configurable/)
})

test('scene disclosure is collapsed empty and opens populated without changing stored text', () => {
  let value = ''
  const render = () => MediaTaskScenes({ value, onChange: next => { value = next } })
  assert.equal(render().props.open, undefined)
  elements(render(), 'textarea')[0].props.onChange({ target: { value: '  Dawn\nNight  ' } })
  assert.equal(render().props.open, true)
  assert.equal(elements(render(), 'textarea')[0].props.value, '  Dawn\nNight  ')
  elements(render(), 'textarea')[0].props.onChange({ target: { value: '' } })
  assert.equal(render().props.open, undefined)
})

test('default control has one explicit save gesture and disabled saving state', () => {
  let saves = 0
  const tree = MediaTaskDefault({ isDefault: false, onSave: () => { saves++ } })
  assert.equal(saves, 0)
  const buttons = elements(tree, 'button')
  assert.equal(buttons.length, 1)
  assert.equal(buttons[0].props.type, 'button')
  buttons[0].props.onClick()
  assert.equal(saves, 1)
  assert.equal(elements(MediaTaskDefault({ isDefault: true, onSave: () => assert.fail() }), 'button').length, 0)
  assert.equal(elements(MediaTaskDefault({ isDefault: false, disabled: true, saving: true, onSave: () => {} }), 'button')[0].props.disabled, true)
})

test('production image and video model selection never saves defaults', () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const file = ts.createSourceFile('view.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const closures = new Map<string, ts.Expression>()
  function visit(node: ts.Node) {
    if (ts.isVariableDeclaration(node) && node.initializer) closures.set(node.name.getText(file), node.initializer)
    ts.forEachChild(node, visit)
  }
  visit(file)
  const selected: string[] = []
  for (const name of ['handleImageModelChange', 'handleVideoModelChange']) {
    const closure = closures.get(name)
    assert.ok(closure)
    const context = { setSelectedImageModel: (value: string) => selected.push(value), videoDefaults: { select: (value: string) => selected.push(value) }, handleSetImageAsDefault: () => assert.fail('implicit image save'), handleSetVideoAsDefault: () => assert.fail('implicit video save') }
    const js = ts.transpileModule(`return (${closure.getText(file)})`, { compilerOptions: { target: ts.ScriptTarget.ES2020 } }).outputText
    new Function(...Object.keys(context), js)(...Object.values(context))('chosen-model')
  }
  assert.deepEqual(selected, ['chosen-model', 'chosen-model'])
})

test('one estimate retains approximate, unknown and per-scene semantics without persistent breakdown', () => {
  const details = 'Catalog estimate; input charges additional'
  assert.match(renderToStaticMarkup(MediaTaskCost({ total: 1.2, approximate: true, details, scenes: true })), /≈\$1.20.*\/ scene/)
  const unknown = renderToStaticMarkup(MediaTaskCost({ details }))
  assert.match(unknown, /Unknown/)
  assert.doesNotMatch(unknown, /Catalog estimate; input charges/)
})

// Purpose: failed explicit image-default saves must not falsely display a saved default.
// Boundary: actual handleSetImageAsDefault closure; recording transport isolates settings.
test('image default publishes only a successful save and surfaces failures', async () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const file = ts.createSourceFile('view.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  let closure: ts.Expression | undefined
  function visit(node: ts.Node) {
    if (ts.isVariableDeclaration(node) && node.name.getText(file) === 'handleSetImageAsDefault') closure = node.initializer
    ts.forEachChild(node, visit)
  }
  visit(file)
  assert.ok(closure)
  for (const fails of [true, false]) {
    let saved = 'prior-model'
    let error: string | null = null
    let saving = false
    const requests: any[] = []
    const context = {
      setDefaultImageModel: (value: string) => { saved = value },
      setImageDefaultError: (value: string | null) => { error = value },
      setIsSavingModelChoice: (value: boolean) => { saving = value },
      requestJson: async (url: string, options: any) => {
        requests.push({ url, body: JSON.parse(options.body) })
        assert.equal(saved, 'prior-model')
        assert.equal(saving, true)
        if (fails) throw new Error('Save failed')
      },
    }
    const js = ts.transpileModule(`return (${closure.getText(file)})`, { compilerOptions: { target: ts.ScriptTarget.ES2020 } }).outputText
    await new Function(...Object.keys(context), js)(...Object.values(context))('chosen-model')
    assert.equal(saved, fails ? 'prior-model' : 'chosen-model')
    assert.equal(error, fails ? 'Save failed' : null)
    assert.equal(saving, false)
    assert.deepEqual(requests, [{ url: '/v1/ui/settings', body: { tools: { image: { default_model: 'chosen-model' } } } }])
  }
})

// Purpose: actual New Task select wiring must forward unchanged settings to the draft,
// with numeric counts/durations and catalog-supported video values, not test-only props.
test('production compact selects preserve image and video draft values', () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const file = ts.createSourceFile('view.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const selects: ts.JsxSelfClosingElement[] = []
  function visit(node: ts.Node) {
    if (ts.isJsxSelfClosingElement(node) && node.tagName.getText(file) === 'MediaTaskSelect') selects.push(node)
    ts.forEachChild(node, visit)
  }
  visit(file)
  assert.equal(selects.length, 7)
  const draft: Record<string, string | number> = {}
  const context = {
    React, MediaTaskSelect, imageResolution: '2k', imageAspectRatio: '9:16', imageVariants: 25,
    IMAGE_ASPECT_RATIOS: [{ ratio: '1:1' }, { ratio: '16:9' }, { ratio: '9:16' }, { ratio: '4:3' }],
    videoResolution: '1080p', supportedVideoResolutions: ['720p', '1080p'],
    videoAspectRatio: '9:16', supportedVideoAspectRatios: ['16:9', '9:16'],
    videoDuration: 8, supportedVideoDurations: [8], videoClipCount: 4,
    setImageResolution: (value: string) => { draft.imageResolution = value },
    setImageAspectRatio: (value: string) => { draft.imageAspectRatio = value },
    setImageVariants: (value: number) => { draft.imageVariants = value },
    setVideoResolution: (value: string) => { draft.videoResolution = value },
    setVideoAspectRatio: (value: string) => { draft.videoAspectRatio = value },
    setVideoDuration: (value: number) => { draft.videoDuration = value },
    setVideoClipCount: (value: number) => { draft.videoClipCount = value },
  }
  const expected = [['1k', '2k', '4k'], ['1:1', '16:9', '9:16', '4:3'], [1, 2, 4, 5, 10, 25], ['720p', '1080p'], ['16:9', '9:16'], [8], [1, 2, 4, 8]]
  for (const [index, node] of selects.entries()) {
    const js = ts.transpileModule(`return (${node.getText(file)})`, { fileName: 'select.tsx', compilerOptions: { jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ES2020 } }).outputText
    const element = new Function(...Object.keys(context), js)(...Object.values(context)) as React.ReactElement<any>
    assert.deepEqual(element.props.values, expected[index])
    const tree = MediaTaskSelect(element.props)
    elements(tree, 'select')[0].props.onChange({ target: { value: String(element.props.value) } })
  }
  assert.deepEqual(draft, { imageResolution: '2k', imageAspectRatio: '9:16', imageVariants: 25, videoResolution: '1080p', videoAspectRatio: '9:16', videoDuration: 8, videoClipCount: 4 })
})

// Purpose: the image default control must render from its declared saving state and
// disable repeat saves while pending. Threat: a discarded useState value leaves JSX
// referencing an undeclared name, breaking compilation and rendering. Authority:
// OrchestrateView's saving-state declaration and image MediaTaskDefault wiring.
// Narrow layer: execute those production expressions together in idle/pending states.
test('image default control binds its saving state and disables pending saves', () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const file = ts.createSourceFile('view.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  let state: ts.VariableDeclaration | undefined
  let control: ts.JsxSelfClosingElement | undefined
  function visit(node: ts.Node) {
    if (ts.isVariableDeclaration(node) && ts.isArrayBindingPattern(node.name)
      && node.name.elements.some(element => ts.isBindingElement(element) && element.name.getText(file) === 'setIsSavingModelChoice')) {
      state = node
    }
    if (ts.isJsxSelfClosingElement(node) && node.tagName.getText(file) === 'MediaTaskDefault'
      && node.getText(file).includes('handleSetImageAsDefault')) {
      control = node
    }
    ts.forEachChild(node, visit)
  }
  visit(file)
  assert.ok(state)
  assert.ok(control)
  const js = ts.transpileModule(`const ${state.getText(file)}; return (${control.getText(file)})`, {
    fileName: 'image-default.tsx',
    compilerOptions: { jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ES2020 },
  }).outputText
  for (const saving of [false, true]) {
    let saves = 0
    const context = {
      React, MediaTaskDefault,
      useState: (initial: boolean) => {
        assert.equal(initial, false)
        return [saving, () => {}]
      },
      selectedImageModel: 'chosen-model', defaultImageModel: 'prior-model',
      selectedImageOption: { ready: true },
      handleSetImageAsDefault: () => { saves++ },
    }
    const element = new Function(...Object.keys(context), js)(...Object.values(context)) as React.ReactElement<any>
    assert.equal(element.props.saving, saving)
    const button = elements(MediaTaskDefault(element.props), 'button')[0]
    assert.equal(button.props.disabled, saving)
    assert.equal(button.props.children, saving ? 'Saving…' : 'Set default')
    assert.equal(saves, 0, 'rendering must not persist settings')
  }
})
