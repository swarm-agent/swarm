import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: actual Studio controls stage selected Parts, refresh new turns,
// preview candidate-specific Parts, and move head only after explicit selection.
// Threat: helper tests miss broken hook wiring or head/candidate UI confusion.
// Bundle the production component in memory; intercept every browser request.
// This hermetic DOM proof is not a live daemon/provider or visual-quality proof.
test('native Studio Part iteration and candidate decision controls', { timeout: 30_000 }, async () => {
  const bundle = await build({
    stdin: { contents: `import React from 'react'; import {createRoot} from 'react-dom/client';
      import {DesktopV3ArtifactV3Studio} from './src/features/desktop/chat/components/desktop-v3-artifact-v3-studio';
      import {refreshOpenDesktopV3ArtifactCatalogs} from './src/features/desktop/session-v3/artifact-catalog-refresh';
      window.refreshArtifacts = refreshOpenDesktopV3ArtifactCatalogs;
      createRoot(document.getElementById('root')).render(<DesktopV3ArtifactV3Studio open artifact={{artifactId:'artifact',ownerSessionId:'parent',label:'Fixture'}} onOpenChange={()=>{}} onIterate={selection=>{window.stagedSelection=selection}} />);`, resolveDir: process.cwd(), loader: 'tsx' },
    bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent',
  })
  const browser = await chromium.launch({ headless: true, ...(process.env.SWARM_TEST_BROWSER_CHANNEL ? { channel: process.env.SWARM_TEST_BROWSER_CHANNEL } : {}) })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
    page.setDefaultTimeout(5_000)
    const part = (id: string) => ({ id, label: id, locator: { kind: 'selector', path: 'index.html', value: `#${id}` } })
    const revision = (id: string, partId: string) => ({ revision_ref: `revision-${id}`, commit_oid: id, manifest: { parts: [part(partId)] }, build: { status: 'succeeded' }, validation: { status: 'valid' } })
    const base = revision('a'.repeat(40), 'orbit')
    const candidate = revision('b'.repeat(40), 'new-part')
    let head = base
    let ready = false
    let selected = false
    const selections: Record<string, unknown>[] = []
    await page.route('**/*', async (route) => {
      const request = route.request()
      const path = new URL(request.url()).pathname
      let payload: unknown
      if (path === '/') return route.fulfill({ contentType: 'text/html', body: '<html><head><style>[data-artifact-v3-primary-preview]{height:200px;overflow:hidden} svg{width:16px;height:16px}</style></head><body><div id="root"></div></body></html>' })
      if (path.includes('/preview/access/token')) return route.fulfill({ contentType: 'text/html', body: '<html><body>Fixture preview</body></html>' })
      if (path.endsWith('/preview/access')) payload = { ok: true, preview_url: `/v3/sessions/parent/artifacts-v3/artifact/preview/access/token?revision=${request.postDataJSON().revision_ref}` }
      else if (path.endsWith('/turns/new/select')) {
        selections.push(request.postDataJSON())
        head = candidate; selected = true
        payload = { ok: true, head }
      } else if (path.endsWith('/revisions')) payload = { ok: true, revisions: [base], next_cursor: 'more' }
      else if (path.endsWith('/artifacts-v3/artifact')) payload = { ok: true, artifact: {
        id: 'artifact', owner_session_id: 'parent', label: 'Fixture', revision: 9, head, revisions: [head], parts: head.manifest.parts,
        turns: ready ? [{ turn_id: 'new', revision: 12, created_at: 20, status: selected ? 'selected' : 'awaiting_selection', selected_candidate_id: selected ? 'option' : '', target_part_ids: ['orbit'], candidates: [{ candidate_id: 'option', status: 'ready', revision: candidate }] }] : [],
      } }
      else return route.abort('blockedbyclient')
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify(payload) })
    })
    await page.goto('https://artifact.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0]!.text })
    await page.locator('[data-artifact-v3-part="orbit"]').click()
    await page.locator('[data-artifact-v3-iterate]').click()
    const staged = await page.evaluate(() => (window as unknown as { stagedSelection: Record<string, unknown> }).stagedSelection)
    assert.equal(staged.artifact_id, 'artifact')
    assert.equal(staged.revision_ref, base.revision_ref)
    assert.deepEqual(staged.target_part_ids, ['orbit'])
    assert.equal(selections.length, 0)
    ready = true
    await page.evaluate(() => (window as unknown as { refreshArtifacts(): Promise<void> }).refreshArtifacts())
    // Wait for preview readiness and exercise native keyboard activation.
    // This fixture does not load production layout CSS; pixel layout is not tested.
    await page.frameLocator('[data-artifact-v3-complete-preview]').getByText('Fixture preview').waitFor()
    await page.locator('[data-artifact-v3-candidate="option"] > button').focus()
    await page.keyboard.press('Enter')
    await page.locator('[data-artifact-v3-part="new-part"]').waitFor()
    assert.equal(await page.locator('[data-artifact-v3-part="orbit"]').count(), 0)
    assert.equal(await page.locator('[data-artifact-v3-iterate]').isDisabled(), true)
    assert.equal(selections.length, 0, 'preview must leave head unchanged')
    await page.getByRole('button', { name: 'Select head' }).click()
    // Selection completes only after the authoritative refresh clears busy;
    // the same button label is already visible while the request is pending.
    await page.locator('[data-artifact-v3-iterate]:enabled').waitFor()
    assert.equal(await page.locator('[data-artifact-v3-iterate]').isEnabled(), true)
    assert.equal(selections.length, 1)
    assert.equal(selections[0]?.expected_head_ref, base.revision_ref)
    assert.equal(selections[0]?.expected_turn_revision, 12)
    assert.equal(selections[0]?.candidate_id, 'option')
    await page.locator('[data-artifact-v3-part="new-part"]').click()
    await page.locator('[data-artifact-v3-iterate]').click()
    const continued = await page.evaluate(() => (window as unknown as { stagedSelection: Record<string, unknown> }).stagedSelection)
    assert.equal(continued.revision_ref, candidate.revision_ref)
    assert.deepEqual(continued.target_part_ids, ['new-part'])
  } finally { await browser.close() }
})
