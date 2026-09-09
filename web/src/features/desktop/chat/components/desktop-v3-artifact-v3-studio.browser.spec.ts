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
      createRoot(document.getElementById('root')).render(<DesktopV3ArtifactV3Studio open artifact={{artifactId:'artifact',ownerSessionId:'parent',label:'Fixture'}} onOpenChange={()=>{}} onRepairDraft={artifact=>{window.repairArtifact=artifact}} onIterate={selection=>{window.stagedSelection=selection}} />);`, resolveDir: process.cwd(), loader: 'tsx' },
    bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent',
  })
  const browser = await chromium.launch({ headless: true, ...(process.env.SWARM_TEST_BROWSER_CHANNEL ? { channel: process.env.SWARM_TEST_BROWSER_CHANNEL } : {}) })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
    page.setDefaultTimeout(5_000)
    const part = (id: string) => ({ id, label: id, locator: { kind: 'selector', path: 'index.html', value: `#${id}` } })
    const revision = (id: string, partId: string) => ({ revision_ref: `revision-${id}`, commit_oid: id, manifest: { parts: [part(partId)] }, build: { status: 'succeeded' }, validation: { status: 'valid' } })
    const base = { ...revision('a'.repeat(40), 'orbit'), diagnostics: [{ code: 'prior-warning', message: 'Prior revision warning', severity: 'warning' }] }
    const candidate = revision('b'.repeat(40), 'new-part')
    let head = base
    let ready = false
    let unavailable = false
    let selected = false
    let draftError = true
    const draftDiagnostic = { stage: 'validation', code: 'draft_validation_failed', message: 'The artifact needs a preview or validation repair.' }
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
        id: 'artifact', owner_session_id: 'parent', label: 'Fixture', revision: 9, head, revisions: unavailable ? [base] : [head], parts: head.manifest.parts,
        current_draft: { status: draftError ? 'error' : 'ready', sequence: 3, diagnostics: draftError ? [draftDiagnostic] : [], history: [{ ready: false, diagnostics: [draftDiagnostic] }] },
        turns: ready && !unavailable ? [{ turn_id: 'new', revision: 12, created_at: 20, status: selected ? 'selected' : 'awaiting_selection', selected_candidate_id: selected ? 'option' : '', target_part_ids: ['orbit'], candidates: [{ candidate_id: 'one', status: 'ready', revision: base }, { candidate_id: 'two', status: 'failed' }, { candidate_id: 'option', status: 'ready', revision: candidate }] }] : [],
      } }
      else return route.abort('blockedbyclient')
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify(payload) })
    })
    await page.goto('https://artifact.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0]!.text })
    await page.locator('[data-artifact-v3-part="orbit"]').waitFor()
    await page.getByRole('heading', { name: 'Current errors', exact: true }).waitFor()
    assert.equal(await page.locator('[data-artifact-repair-history]').count(), 1)
    await page.getByRole('button', { name: 'Ask Swarm to fix errors' }).click()
    assert.equal(await page.evaluate(() => (window as unknown as { repairArtifact: { artifactId: string } }).repairArtifact.artifactId), 'artifact')
    assert.equal(selections.length, 0, 'repair navigation never selects a candidate')
    draftError = false
    await page.evaluate(() => (window as unknown as { refreshArtifacts(): Promise<void> }).refreshArtifacts())
    await page.getByRole('heading', { name: 'Current errors', exact: true }).waitFor({ state: 'detached' })
    assert.equal(await page.locator('[data-artifact-repair-history]').count(), 1, 'repair history remains after current errors clear')
    await page.locator('[data-artifact-v3-iterate]').click()
    const whole = await page.evaluate(() => (window as unknown as { stagedSelection: Record<string, unknown> }).stagedSelection)
    assert.equal(whole.revision_ref, base.revision_ref)
    assert.deepEqual(whole.target_part_ids, [])
    await page.locator('[data-artifact-v3-part="orbit"]').click()
    await page.locator('[data-artifact-v3-iterate]').click()
    const staged = await page.evaluate(() => (window as unknown as { stagedSelection: Record<string, unknown> }).stagedSelection)
    assert.equal(staged.artifact_id, 'artifact')
    assert.equal(staged.revision_ref, base.revision_ref)
    assert.deepEqual(staged.target_part_ids, ['orbit'])
    await page.getByRole('button', { name: 'Remix whole project', exact: true }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).stagedSelection.target_part_ids), [])
    assert.equal(await page.locator('[data-artifact-v3-part="orbit"]').getAttribute('aria-pressed'), 'true')
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
    assert.equal(await page.locator('[data-artifact-v3-iterate]').isEnabled(), true)
    await page.locator('[data-artifact-v3-iterate]').click()
    assert.equal(await page.evaluate(() => (window as any).stagedSelection.revision_ref), candidate.revision_ref)
    await page.locator('[data-artifact-v3-part="new-part"]').click()
    await page.getByRole('button', { name: 'Use as style/example reference' }).click()
    const reference = await page.evaluate(() => (window as any).stagedSelection)
    assert.equal(reference.action, 'select')
    assert.equal(reference.revision_ref, candidate.revision_ref)
    assert.deepEqual(reference.target_part_ids, ['new-part'])
    head = { ...base, ...revision('c'.repeat(40), 'concurrent-head-part') }
    await page.evaluate(() => (window as any).refreshArtifacts())
    assert.equal(await page.locator('[data-artifact-v3-part="new-part"]').getAttribute('aria-pressed'), 'true')
    assert.equal(await page.locator('[data-artifact-v3-part="concurrent-head-part"]').count(), 0)
    unavailable = true
    await page.evaluate(() => (window as any).refreshArtifacts())
    await page.getByRole('alert').filter({ hasText: 'exact viewed revision' }).waitFor()
    assert.equal(await page.locator('[data-artifact-v3-iterate]').isDisabled(), true)
    assert.equal(await page.evaluate(() => (window as any).stagedSelection.revision_ref), candidate.revision_ref)
    assert.equal(await page.locator('[data-artifact-v3-part="orbit"]').count(), 0)
    unavailable = false; head = base
    await page.evaluate(() => (window as any).refreshArtifacts())
    assert.equal(await page.locator('[data-artifact-v3-part="new-part"]').getAttribute('aria-pressed'), 'true')
    await page.locator('[data-artifact-v3-part="new-part"]').click()
    assert.equal(selections.length, 0, 'preview must leave head unchanged')
    await page.locator('[data-artifact-v3-candidate="option"]').getByRole('button', { name: 'Select head' }).focus()
    await page.keyboard.press('Enter')
    // Selection completes only after the authoritative refresh clears busy;
    // the same button label is already visible while the request is pending.
    await page.waitForFunction(() => document.querySelector('[data-artifact-v3-candidate="option"] > button')?.textContent?.includes('Selected')).catch(async (error) => { throw new Error(`${error}\n${await page.locator('[role="alert"]').allTextContents()}`) })
    await page.locator('[data-artifact-v3-iterate]:enabled').waitFor()
    assert.equal(await page.locator('[data-artifact-v3-iterate]').isEnabled(), true)
    assert.equal(await page.locator('[data-artifact-v3-diagnostics]').count(), 0, 'old revision diagnostics clear after successful selection')
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

// Requirement: the actual ephemeral bridge owns serialized seek playback without
// new authored methods. Threat: overlapping async seeks, stale completion after
// scene navigation, paused playback that cannot resume, and silent seek failure.
// Execute the embedded production script in an opaque iframe with a fake runtime.
test('native playback bridge serializes scene seek, resume, end and failure', { timeout: 30_000 }, async () => {
  const { readFile } = await import('node:fs/promises')
  const script = (await readFile('../swarmd/internal/runtime/artifact_v3_preview_selection.js', 'utf8')).replace('__SWARM_ARTIFACT_V3_SELECTION_CONFIG__', JSON.stringify({ revision_ref: 'revision-test', parts: [], part_ids: [] }))
  const browser = await chromium.launch({ headless: true, ...(process.env.SWARM_TEST_BROWSER_CHANNEL ? { channel: process.env.SWARM_TEST_BROWSER_CHANNEL } : {}) })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    await page.setContent('<iframe sandbox="allow-scripts"></iframe>')
    await page.evaluate(({ script }) => {
      ;(window as any).states = []
      window.addEventListener('message', event => (window as any).states.push(event.data))
      document.querySelector('iframe')!.srcdoc = `<script>${script}</script><script>
        let active=0;
        window.__SWARM_ANIMATION_V1__={version:'swarm.animation/v1',ready:()=>({duration_ms:1000}),seek:async(time_ms)=>{
          if (++active>1) throw Error('overlap');
          await new Promise(r=>setTimeout(r,30)); active--;
          if(time_ms===777) throw Error('injected');
          return {time_ms};
        }};
      </script>`
    }, { script })
    const waitState = async (id: number, time?: number) => {
      await page.waitForFunction(({ id, time }) => (window as any).states.some((s: any) => s.type === 'playback-state' && s.command_id === id && (time === undefined || s.time_ms === time)), { id, time })
    }
    const command = async (id: number, action: string, time_ms?: number) => page.evaluate(({ id, action, time_ms }) => document.querySelector('iframe')!.contentWindow!.postMessage({ protocol: 'swarm.artifact/v3', revision_ref: 'revision-test', type: 'playback-command', command_id: id, action, time_ms }, '*'), { id, action, time_ms })
    await waitState(0, 0)
    await command(1, 'seek', 500)
    await command(2, 'seek', 600)
    await waitState(2, 600)
    assert.equal(await page.evaluate(() => (window as any).states.some((s: any) => s.command_id === 2 && s.time_ms === 500)), false)
    await command(3, 'play')
    await waitState(3, 1000)
    assert.equal(await page.evaluate(() => (window as any).states.at(-1).playing), false)
    await command(4, 'play')
    await waitState(4, 0)
    await command(5, 'pause')
    await waitState(5)
    await command(6, 'seek', 777)
    await page.waitForFunction(() => (window as any).states.some((s: any) => s.command_id === 6 && s.error))
    assert.equal(await page.evaluate(() => (window as any).states.at(-1).playing), false)
  } finally { await browser.close() }
})
