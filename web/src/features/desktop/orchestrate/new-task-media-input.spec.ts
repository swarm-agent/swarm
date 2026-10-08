// Purpose: New Task must not lose pasted images or submit text-only while media
// is still uploading. Authority: OrchestrateView's actual paste and submit
// handlers. Executing those handlers with controlled clipboard/upload state is
// the narrowest deterministic reproduction; this is not browser or provider E2E.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import ts from 'typescript'
import { taskDeployRequestIdentity } from './orchestrate-task-helpers'

const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
const file = ts.createSourceFile('OrchestrateView.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)

function expression(predicate: (node: ts.Node) => boolean): string {
  let found: ts.Node | undefined
  function visit(node: ts.Node) {
    if (predicate(node)) found = node
    ts.forEachChild(node, visit)
  }
  visit(file)
  assert.ok(found, 'production handler exists')
  return found.getText(file)
}

function compileHandler(text: string, scope: Record<string, unknown>) {
  const compiled = ts.transpileModule(`return (${text})`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
  }).outputText
  return new Function('scope', `with (scope) { ${compiled} }`)(scope)
}

const paste = expression(node => ts.isArrowFunction(node)
  && ts.isJsxExpression(node.parent) && ts.isJsxAttribute(node.parent.parent)
  && node.parent.parent.name.getText(file) === 'onPaste'
  && node.getText(file).includes('handleFileUpload'))
const submit = expression(node => ts.isArrowFunction(node)
  && ts.isVariableDeclaration(node.parent) && node.parent.name.getText(file) === 'handleDeployModalSubmit')

test('New Task admits clipboard item images even when the files list is empty', () => {
  const image = { name: 'clipboard.png', type: 'image/png', size: 32 }
  const uploads: unknown[][] = []
  const handler = compileHandler(paste, {
    handleFileUpload: (files: Iterable<unknown>) => uploads.push(Array.from(files)),
  })
  handler({ clipboardData: { getData: () => '', files: [], items: [{ kind: 'file', type: image.type, getAsFile: () => image }] }, preventDefault() {} })
  assert.deepEqual(uploads, [[image]])
})

test('New Task preserves ordinary clipboard-file image intake', () => {
  const image = { name: 'clipboard.png', type: 'image/png', size: 32 }
  const uploads: unknown[][] = []
  const handler = compileHandler(paste, {
    handleFileUpload: (files: Iterable<unknown>) => uploads.push(Array.from(files)),
  })
  handler({ clipboardData: { getData: () => '', files: [image], items: [] }, preventDefault() {} })
  assert.deepEqual(uploads, [[image]])
})

test('New Task must not submit before its attachment upload resolves', async () => {
  const requests: unknown[] = []
  const noop = () => {}
  const handler = compileHandler(submit, {
    isDeployingTaskRef: { current: false }, isUploadingMedia: true,
    mediaUploadScopeRef: { current: { pending: 0, failedFiles: [] } },
    taskIntent: 'code', newTaskPrompt: 'Implement the attached screenshot',
    selectedProject: { id: 'project-fixture' }, newTaskModelOverride: '',
    featureSize: 'small', taggedMedia: [], newTaskWorkspace: '', autoApproveTask: false,
    setIsDeployingTask: noop, setDeployError: noop,
    taskWorkspaceSelection: () => ({}), pendingDeployRequestRef: { current: null },
    taskDeployRequestIdentity: () => ({ clientTaskId: 'task-fixture' }),
    requestJson: async (_url: string, options: { body: string }) => {
      requests.push(JSON.parse(options.body))
      return { task: { id: 'task-fixture' } }
    },
    desktopProjects: { setOptimisticTasks: noop, invalidate: noop },
    setIsDeployModalOpen: noop, setNewTaskPrompt: noop, dispatchImagePrompt: noop,
    setNewTaskModelOverride: noop, setTaggedMedia: noop,
  })
  await handler()
  assert.deepEqual(requests, [], 'pending media must block task creation, not send attached_media: []')
})

// Purpose: prevent duplicate clipboard imports and preserve accompanying text.
// The production paste handler is the narrowest intake boundary for this contract.
test('New Task imports overlapping clipboard representations once and preserves text paste', () => {
  const image = { name: 'clipboard.png', type: 'image/png' }
  const uploads: unknown[][] = []
  let prevented = false
  const handler = compileHandler(paste, {
    handleFileUpload: (files: Iterable<unknown>) => uploads.push(Array.from(files)),
  })
  handler({ clipboardData: { files: [image], items: [{ kind: 'file', getAsFile: () => image }], getData: () => 'Explain this image' }, preventDefault() { prevented = true } })
  assert.deepEqual(uploads, [[image]])
  assert.equal(prevented, false)
  handler({ clipboardData: { files: [], items: [{ kind: 'string' }, { kind: 'file', getAsFile: () => null }], getData: () => 'Plain text' }, preventDefault() { prevented = true } })
  assert.deepEqual(uploads, [[image]])
  assert.equal(prevented, false)
})

const upload = expression(node => ts.isArrowFunction(node)
  && ts.isVariableDeclaration(node.parent) && node.parent.name.getText(file) === 'handleFileUpload')

function uploadFixture() {
  const state = { uploading: false, failed: [] as unknown[], tagged: [] as any[], errors: [] as unknown[] }
  const pending: { body: any; resolve: (value: unknown) => void; reject: (error: Error) => void }[] = []
  const scope = {
    selectedProject: { id: 'project-fixture' }, selectedProjectRef: { current: 'project-fixture' },
    isDeployingTaskRef: { current: false }, mediaUploadScopeRef: { current: { pending: 0, failedFiles: [] as unknown[], inFlight: new Set<unknown>() } },
    taskIntent: 'code',
    setIsUploadingMedia: (value: boolean) => { state.uploading = value },
    setShelfUploadError: (value: unknown) => { state.errors.push(value) },
    setShelfFailedFiles: (value: unknown[]) => { state.failed = value },
    setTaggedMedia: (value: any) => { state.tagged = typeof value === 'function' ? value(state.tagged) : value },
    FileReader: class {
      result = ''; onload = () => {}; onerror = () => {}; onabort = () => {}
      readAsDataURL(file: { type: string; name: string }) { this.result = `data:${file.type};base64,ZmFrZQ==`; this.onload() }
    },
    requestJson: (_url: string, options: { body: string }) => new Promise((resolve, reject) => pending.push({ body: JSON.parse(options.body), resolve, reject })),
    desktopProjects: { setOptimisticMedia() {}, invalidate() {} },
  }
  return { state, pending, scope, handler: compileHandler(upload, scope) }
}

// Purpose: unfinished or failed attachments must never become a text-only task.
// Executes handleFileUpload/handleDeployModalSubmit with controlled retention IO;
// checks synchronous admission, overlapping batches, failures and retry postconditions.
test('concurrent image/video uploads block same-tick submit and retain both references', async () => {
  const f = uploadFixture()
  const image = { name: 'shot.png', type: 'image/png', size: 32 }
  const video = { name: 'clip.mp4', type: 'video/mp4', size: 64 }
  const first = f.handler([image])
  const second = f.handler([video])
  await f.handler([image]) // Double intake of the same in-flight file is a no-op.
  assert.equal(f.scope.mediaUploadScopeRef.current.pending, 2)
  const requests: unknown[] = []
  const submitScope = {
    ...f.scope, isUploadingMedia: false, // React has not committed the upload render yet.
    setDeployError: (value: unknown) => f.state.errors.push(value),
    requestJson: (...args: unknown[]) => requests.push(args),
  }
  await compileHandler(submit, submitScope)()
  assert.deepEqual(requests, [])
  await Promise.resolve()
  const savedImage = { id: 'retained-image', kind: 'image', url: '/media/image', digestSha256: 'image-digest' }
  const savedVideo = { id: 'retained-video', kind: 'video', url: '/media/video', digestSha256: 'video-digest' }
  f.pending[0].resolve({ media: savedImage })
  await first
  assert.equal(f.state.uploading, true)
  assert.equal(f.scope.mediaUploadScopeRef.current.pending, 1)
  f.pending[1].resolve({ media: savedVideo })
  await second
  assert.equal(f.state.uploading, false)
  assert.deepEqual(f.state.tagged, [savedImage, savedVideo])
  assert.equal(f.pending[1].body.kind, 'video')
  assert.equal(f.pending[1].body.filename, video.name)
})

test('failed uploads remain actionable and only the failed file is retried', async () => {
  const f = uploadFixture()
  const image = { name: 'shot.png', type: 'image/png', size: 32 }
  const video = { name: 'clip.mp4', type: 'video/mp4', size: 64 }
  const first = f.handler([image])
  const second = f.handler([video])
  await Promise.resolve()
  const savedImage = { id: 'retained-image', kind: 'image', url: '/media/image' }
  f.pending[0].resolve({ media: savedImage })
  f.pending[1].reject(new Error('Retention unavailable'))
  await Promise.all([first, second])
  assert.deepEqual(f.state.tagged, [savedImage])
  assert.deepEqual(f.state.failed, [video])
  assert.ok(f.state.errors.includes('Retention unavailable'))
  const errors: unknown[] = []
  await compileHandler(submit, { ...f.scope, isUploadingMedia: false, setDeployError: (value: unknown) => errors.push(value) })()
  assert.match(String(errors[0]), /Retry failed attachments/)
  const retry = f.handler(f.state.failed)
  await Promise.resolve()
  const savedVideo = { id: 'retained-video', kind: 'video', url: '/media/video' }
  f.pending[2].resolve({ media: savedVideo })
  await retry
  assert.deepEqual(f.state.failed, [])
  assert.deepEqual(f.state.tagged, [savedImage, savedVideo])
  assert.equal(f.pending.length, 3)
})

// Purpose: project-switch completion must not leak media into another draft,
// including a switch away and back. Scope identity owns UI writes, not project ID alone.
test('obsolete upload completion cannot alter a new project upload scope', async () => {
  const f = uploadFixture()
  const first = f.handler([{ name: 'shot.png', type: 'image/png', size: 32 }])
  await Promise.resolve()
  f.scope.mediaUploadScopeRef.current = { pending: 1, failedFiles: [], inFlight: new Set<unknown>() }
  f.pending[0].resolve({ media: { id: 'obsolete', kind: 'image', url: '/media/old' } })
  await first
  assert.deepEqual(f.state.tagged, [])
  assert.equal(f.scope.mediaUploadScopeRef.current.pending, 1)
  assert.equal(f.state.uploading, true)
})

// Purpose: a rejected creation must preserve prompt/media and retry identity,
// not clear the draft, change the configured model or create a second task.
// Exercises the production submit handler plus its real idempotency helper.
test('submission preserves retained image/video references and prompt across rejection and retry', async () => {
  const media = [
    { id: 'image', kind: 'image', url: '/retained/image', digestSha256: 'image-digest' },
    { id: 'video', kind: 'video', url: '/retained/video', digestSha256: 'video-digest' },
  ]
  const requests: any[] = []
  const errors: unknown[] = []
  const cleared: string[] = []
  const noop = () => {}
  const scope = {
    isDeployingTaskRef: { current: false }, isUploadingMedia: false,
    mediaUploadScopeRef: { current: { pending: 0, failedFiles: [] } },
    taskIntent: 'code', newTaskPrompt: 'Explain these attachments',
    selectedProject: { id: 'project-fixture' }, newTaskModelOverride: '',
    featureSize: 'small', taggedMedia: media, newTaskWorkspace: '', autoApproveTask: false,
    setIsDeployingTask: noop, setDeployError: (value: unknown) => errors.push(value),
    taskWorkspaceSelection: () => ({}), pendingDeployRequestRef: { current: null },
    taskDeployRequestIdentity,
    requestJson: async (_url: string, options: { body: string; headers: unknown }) => {
      requests.push({ body: JSON.parse(options.body), headers: options.headers })
      throw new Error('Unsupported attachment: choose compatible input')
    },
    console: { warn: noop },
    desktopProjects: { setOptimisticTasks: noop, invalidate: noop },
    setIsDeployModalOpen: () => cleared.push('modal'), setNewTaskPrompt: () => cleared.push('prompt'),
    dispatchImagePrompt: noop, setNewTaskModelOverride: () => cleared.push('model'), setTaggedMedia: () => cleared.push('media'),
  }
  const handler = compileHandler(submit, scope)
  await handler()
  await handler()
  assert.equal(requests.length, 2)
  assert.deepEqual(requests[0], requests[1])
  assert.deepEqual(requests[0].body.attached_media, media)
  assert.equal(requests[0].body.prompt, scope.newTaskPrompt)
  assert.equal(requests[0].body.auto_approve, false)
  assert.equal(requests[0].body.model, undefined)
  assert.deepEqual(cleared, [])
  assert.ok(errors.includes('Unsupported attachment: choose compatible input'))
  assert.equal(scope.isDeployingTaskRef.current, false)
})
