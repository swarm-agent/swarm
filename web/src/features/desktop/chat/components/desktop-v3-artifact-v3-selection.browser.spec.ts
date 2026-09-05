import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: code-owned preview clicks resolve the innermost declared Part,
// retain single/multiple exact narration IDs through composer operations and the
// portable transport envelope, and never select head or submit on preview intent.
// Threat: foreign/stale/malformed messages, scene-only selection, inaccessible list
// controls, or revision navigation reusing old intent. Production authorities:
// injected artifact_v3_preview_selection.js, NativeArtifactStudio and its parser.
// This bounded headless DOM test executes the actual script/component, intercepts
// all requests, and proves behavior (not installed runtime or styled visual quality).
test('native narration preview selection is exact, sandboxed and intent-only', { timeout: 30_000 }, async () => {
  const script = await readFile('../swarmd/internal/runtime/artifact_v3_preview_selection.js', 'utf8')
  const bundle = await build({
    stdin: { contents: `import React from 'react'; import {createRoot} from 'react-dom/client';
      import {DesktopV3ArtifactV3Studio} from './src/features/desktop/chat/components/desktop-v3-artifact-v3-studio';
      import {appendDesktopV3ArtifactMessageSelections} from './src/features/desktop/session-v3/artifact-api';
      import {createDesktopV3ExistingMessageOperation} from './src/features/desktop/session-v3/existing-session-flow';
      import {portableDesktopV3ArtifactMessageSelection} from './src/features/desktop/session-v3/write-api';
      createRoot(document.getElementById('root')).render(<DesktopV3ArtifactV3Studio open artifact={{artifactId:'artifact',ownerSessionId:'parent',label:'Narration'}} onOpenChange={()=>{}} onIterate={selection=>{
        window.stagedSelection=selection;window.stageCount=(window.stageCount||0)+1;
        const chips=appendDesktopV3ArtifactMessageSelections([], [selection]);
        const operation=createDesktopV3ExistingMessageOperation({sessionId:'parent',prompt:'Shorten the selected narration.',artifactSelections:chips});
        window.composerRequest={...operation.request,artifact_selections:operation.request.artifact_selections.map(portableDesktopV3ArtifactMessageSelection)};
      }} />);`, resolveDir: process.cwd(), loader: 'tsx' },
    bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent',
  })
  const part = (id: string) => ({ id, label: id, locator: { kind: 'selector', path: 'index.html', value: `#${id}` } })
  const parts = ['scene', 'narration-one', 'narration-two', 'visual-one'].map(part)
  const head = { revision_ref: `revision-${'a'.repeat(40)}`, commit_oid: 'a'.repeat(40), manifest: { parts } }
  const prior = { ...head, revision_ref: `revision-${'b'.repeat(40)}`, commit_oid: 'b'.repeat(40) }
  const mutations: string[] = []
  const browser = await chromium.launch({ headless: true, ...(process.env.SWARM_TEST_BROWSER_CHANNEL ? { channel: process.env.SWARM_TEST_BROWSER_CHANNEL } : {}) })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
    page.setDefaultTimeout(5_000)
    await page.route('**/*', async (route) => {
      const request = route.request()
      const url = new URL(request.url())
      if (url.pathname === '/') return route.fulfill({ contentType: 'text/html', body: '<html><body><div id="root"></div></body></html>' })
      if (url.pathname.includes('/preview/access/token')) {
        const config = { revision_ref: url.searchParams.get('revision'), parts: parts.map((part) => ({ id: part.id, selector: part.locator.value })), part_ids: parts.map((part) => part.id) }
        return route.fulfill({ contentType: 'text/html', body: `<html><head><script>${script.replace('__SWARM_ARTIFACT_V3_SELECTION_CONFIG__', JSON.stringify(config))}</script></head><body><section id="scene"><p id="narration-one"><strong>First narration</strong></p><p id="visual-one">Visual only</p><div style="height:1100px"></div><p id="narration-two"><span>Second narration</span></p></section></body></html>` })
      }
      if (url.pathname.endsWith('/preview/access')) return route.fulfill({ json: { ok: true, preview_url: `/v3/sessions/parent/artifacts-v3/artifact/preview/access/token?revision=${request.postDataJSON().revision_ref}` } })
      if (request.method() !== 'GET') mutations.push(url.pathname)
      if (url.pathname.endsWith('/revisions')) return route.fulfill({ json: { ok: true, revisions: [prior, head] } })
      if (url.pathname.endsWith('/artifacts-v3/artifact')) return route.fulfill({ json: { ok: true, artifact: { id: 'artifact', owner_session_id: 'parent', head, parts, revisions: [prior, head], turns: [] } } })
      return route.abort('blockedbyclient')
    })
    await page.goto('https://artifact.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0]!.text })
    const frame = page.frameLocator('[data-artifact-v3-complete-preview]')
    const button = (id: string) => page.locator(`[data-artifact-v3-part="${id}"]`)
    await frame.locator('#narration-one strong').click()
    await page.waitForFunction(() => document.querySelector('[data-artifact-v3-part="narration-one"]')?.getAttribute('aria-pressed') === 'true')
    assert.equal(await button('scene').getAttribute('aria-pressed'), 'false')
    await frame.locator('#narration-one[data-swarm-v3-selected]').waitFor()
    assert.equal(await frame.locator('#narration-one').evaluate((el) => getComputedStyle(el).outlineWidth), '3px')
    await page.locator('[data-artifact-v3-iterate]').click()
    const single = await page.evaluate(() => (window as unknown as { composerRequest: Record<string, any> }).composerRequest)
    assert.equal(single.content, 'Shorten the selected narration.')
    assert.deepEqual(single.artifact_selections.map((item: Record<string, unknown>) => ({ session: item.session_id, artifact: item.artifact_id, revision: item.revision_ref, targets: item.target_part_ids })), [{ session: 'parent', artifact: 'artifact', revision: head.revision_ref, targets: ['narration-one'] }])
    assert.deepEqual(mutations, [])
    await frame.locator('#narration-two span').click()
    await page.waitForFunction(() => document.querySelector('[role="status"]')?.textContent === '2 selected')
    await frame.locator('#narration-two span').click()
    await page.waitForFunction(() => document.querySelector('[role="status"]')?.textContent === '1 selected')
    // Keyboard list control focuses/scrolls the actual region without clipping it.
    await button('narration-two').focus()
    await page.keyboard.press('Space')
    await frame.locator('#narration-two[data-swarm-v3-selected]').waitFor()
    assert.equal(await frame.locator('#narration-two').evaluate((el) => document.activeElement === el && el.getBoundingClientRect().bottom <= innerHeight), true)
    assert.equal(await button('narration-one').getAttribute('aria-pressed'), 'true')
    assert.equal(await page.locator('iframe').getAttribute('sandbox'), 'allow-scripts')
    assert.equal(await page.locator('iframe').evaluate((el) => (el as HTMLIFrameElement).contentDocument === null), true)
    const invalid = [null, [], { protocol: 'wrong' },
      { protocol: 'swarm.artifact/v3', type: 'toggle-part', revision_ref: prior.revision_ref, part_id: 'narration-one' },
      { protocol: 'swarm.artifact/v3', type: 'toggle-part', revision_ref: head.revision_ref, part_id: 'unknown' },
      { protocol: 'swarm.artifact/v3', type: 'toggle-part', revision_ref: head.revision_ref, part_id: ['narration-one'] },
      { protocol: 'swarm.artifact/v3', type: 'submit-edit', revision_ref: head.revision_ref, part_id: 'narration-one' }]
    await frame.locator('body').evaluate((_el, messages) => { for (const message of messages) parent.postMessage(message, '*') }, invalid)
    // Correct protocol/revision/ID but foreign source window must do nothing.
    await page.evaluate((revision) => window.postMessage({ protocol: 'swarm.artifact/v3', type: 'toggle-part', revision_ref: revision, part_id: 'narration-one' }, '*'), head.revision_ref)
    // A valid toggle sent after the invalid messages is the delivery barrier.
    await frame.locator('#visual-one').click()
    await page.waitForFunction(() => document.querySelector('[data-artifact-v3-part="visual-one"]')?.getAttribute('aria-pressed') === 'true')
    assert.equal(await button('narration-one').getAttribute('aria-pressed'), 'true')
    assert.equal(await button('narration-two').getAttribute('aria-pressed'), 'true')
    assert.equal(await page.evaluate(() => (window as unknown as { stageCount?: number }).stageCount || 0), 1)
    await button('visual-one').click()
    await page.locator('[data-artifact-v3-iterate]').click()
    const staged = await page.evaluate(() => (window as unknown as { stagedSelection: Record<string, unknown> }).stagedSelection)
    assert.deepEqual(staged.target_part_ids, ['narration-one', 'narration-two'])
    assert.equal(staged.revision_ref, head.revision_ref)
    assert.equal(staged.artifact_id, 'artifact')
    assert.equal(staged.session_id, 'parent')
    const multiple = await page.evaluate(() => (window as unknown as { composerRequest: Record<string, any> }).composerRequest)
    assert.equal(multiple.content, 'Shorten the selected narration.')
    assert.deepEqual(multiple.artifact_selections.map((item: Record<string, unknown>) => ({ session: item.session_id, artifact: item.artifact_id, revision: item.revision_ref, targets: item.target_part_ids })), [{ session: 'parent', artifact: 'artifact', revision: head.revision_ref, targets: ['narration-one', 'narration-two'] }])
    assert.doesNotMatch(JSON.stringify(multiple), /collection_id|variant_id|pending_request|projection_seq/)
    assert.deepEqual(mutations, [], 'selection only stages composer intent')
    await page.locator(`[data-artifact-v3-revision="${prior.commit_oid}"]`).click()
    await page.waitForFunction(() => document.querySelector('[role="status"]')?.textContent === '0 selected')
    assert.equal(await page.locator('[data-artifact-v3-iterate]').isDisabled(), true)
    await frame.locator('#narration-one strong').click()
    await page.waitForFunction(() => document.querySelector('[role="status"]')?.textContent === '1 selected')
    assert.equal(await page.locator('[data-artifact-v3-iterate]').isDisabled(), true)
    assert.deepEqual(mutations, [])
    await page.locator(`[data-artifact-v3-revision="${head.commit_oid}"]`).click()
    await page.waitForFunction(() => document.querySelector('[role="status"]')?.textContent === '0 selected')
    await page.setViewportSize({ width: 600, height: 900 })
    await page.getByRole('button', { name: 'Parts', exact: true }).click()
    assert.equal(await page.getByRole('button', { name: 'Parts', exact: true }).getAttribute('aria-expanded'), 'true')
  } finally { await browser.close() }
})
