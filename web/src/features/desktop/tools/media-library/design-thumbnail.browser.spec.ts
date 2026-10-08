import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: DesignThumbnail/fetchDesignView must identify immutable previews,
// not readyDesignRevision's freshly allocated snapshots. Regressions cause POSTs,
// iframe reloads, and stale cross-session output. The real MediaTaskCard, React DOM,
// design API and apiFetch run in Chromium: only the fetch transport is local and
// deterministic. Delayed body completion deliberately ignores abort to prove the
// component's stale-result guard as well as signal propagation. This is the
// narrowest layer proving DOM identity, API counts, and ready-output accessibility.
test('design thumbnail preserves equal snapshots and isolates changed identities', { timeout: 30_000 }, async () => {
  const bundle = await build({
    stdin: {
      contents: `import React from 'react';
        import {createRoot} from 'react-dom/client';
        import {flushSync} from 'react-dom';
        import {MediaTaskCard} from './src/features/desktop/orchestrate/media-task-card';
        const root=createRoot(document.getElementById('root'));
        window.calls=[];window.opened=[];
        window.fetch=async (url,init)=>{
          const body=JSON.parse(init.body);
          const call={url:String(url),body,signal:init.signal,credentials:init.credentials,method:init.method};
          window.calls.push(call);
          const response=new Response('missing preview',{status:window.failNext?404:200});
          window.failNext=false;
          if(response.ok) response.text=()=>new Promise(resolve=>{call.complete=resolve});
          return response;
        };
        let session='session-a',ref={artifact_id:'artifact-a',revision:1,sha256:'a'.repeat(64)},kind='design';
        window.render=(patch={})=>{
          session=patch.session??session;ref={...ref,...patch.ref};kind=patch.kind??kind;
          const row={project_id:'project-fixture',title:patch.title??'Fixture',request:{id:'request-fixture',parent_session_id:session,state:'succeeded',candidates:[{
            spec:{artifact_id:ref.artifact_id,kind},state:'succeeded',attempts:[{number:1,state:'succeeded',result:{...ref},router_alert:patch.progress}]
          }]}};
          flushSync(()=>root.render(<MediaTaskCard source="independent-design" design={row} onDesignPreview={item=>window.opened.push(item)}/>));
        };
        window.unmount=()=>flushSync(()=>root.unmount());
        window.render();`,
      resolveDir: process.cwd(), loader: 'tsx',
    },
    bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent',
  })
  const browser = await chromium.launch({ headless: true, ...(process.env.SWARM_TEST_BROWSER_CHANNEL ? { channel: process.env.SWARM_TEST_BROWSER_CHANNEL } : {}) })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.abort())
    await page.setContent('<div id="root"></div>')
    await page.addScriptTag({ content: bundle.outputFiles[0]!.text })
    const settle = () => page.evaluate(async () => {
      for (let i = 0; i < 3; i++) await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
    })
    const count = () => page.evaluate(() => (window as any).calls.length)
    const render = async (patch: object = {}) => {
      await page.evaluate(value => (window as any).render(value), patch)
      await settle()
    }
    const complete = async (index: number, marker: string) => {
      await page.waitForFunction(i => typeof (window as any).calls[i]?.complete === 'function', index)
      await page.evaluate(({ index, marker }) => (window as any).calls[index].complete(`<html><body>${marker}</body></html>`), { index, marker })
      await settle()
    }
    await settle()
    assert.equal(await count(), 1)
    assert.deepEqual(await page.evaluate(() => {
      const c = (window as any).calls[0]
      return { url: c.url, body: c.body, method: c.method, credentials: c.credentials }
    }), {
      url: '/v3/sessions/session-a/designs/artifacts/artifact-a',
      body: { action: 'preview_html', ref: { artifact_id: 'artifact-a', revision: 1, sha256: 'a'.repeat(64) } },
      method: 'POST', credentials: 'same-origin',
    })
    await render({ progress: 'still loading' })
    assert.equal(await count(), 1, 'equal snapshots must also preserve an in-flight request')
    assert.equal(await page.evaluate(() => (window as any).calls[0].signal.aborted), false)
    await complete(0, 'initial')
    const frame = page.locator('iframe')
    assert.match((await frame.getAttribute('srcdoc'))!, /initial/)
    assert.equal(await frame.getAttribute('sandbox'), '')
    assert.equal(await frame.getAttribute('referrerpolicy'), 'no-referrer')
    assert.equal(await frame.getAttribute('tabindex'), '-1')
    await page.evaluate(() => {
      const w = window as any
      w.originalFrame = document.querySelector('iframe')
      w.originalContent = w.originalFrame.srcdoc
      w.mutations = []
      w.observer = new MutationObserver(records => w.mutations.push(...records))
      w.observer.observe(document.getElementById('root')!, { subtree: true, childList: true, attributes: true, attributeFilter: ['srcdoc'] })
    })
    for (let i = 0; i < 5; i++) await render({ title: `Progress ${i}`, progress: `Git update ${i}` })
    assert.equal(await count(), 1, 'equal refs and unrelated parent progress must not refetch')
    assert.equal(await page.evaluate(() => {
      const w = window as any
      return document.querySelector('iframe') === w.originalFrame && w.originalFrame.srcdoc === w.originalContent &&
        !w.mutations.some((r: MutationRecord) => r.target === w.originalFrame || [...r.removedNodes].includes(w.originalFrame))
    }), true, 'the iframe must not be replaced, cleared, or have srcDoc rewritten')

    // Every API identity field changes independently; old output disappears before
    // the new response, including same-number revisions with a different hash.
    const changes = [
      { ref: { revision: 2 } }, { ref: { sha256: 'b'.repeat(64) } },
      { session: 'session-b' }, { ref: { artifact_id: 'artifact-b' } }, { kind: 'animation' },
    ]
    for (const [i, patch] of changes.entries()) {
      await render(patch)
      assert.equal(await count(), i + 2)
      assert.equal(await frame.count(), 0, 'changed identity must not show the previous preview')
      assert.equal(await page.evaluate(index => (window as any).calls[index].signal.aborted, i), true)
      await complete(i + 1, `changed-${i}`)
      assert.match((await frame.getAttribute('srcdoc'))!, new RegExp(`changed-${i}`))
    }
    assert.deepEqual(await page.evaluate(() => (window as any).calls.slice(1).map((c: any) => [c.url, c.body.ref.revision, c.body.ref.sha256])), [
      ['/v3/sessions/session-a/designs/artifacts/artifact-a', 2, 'a'.repeat(64)],
      ['/v3/sessions/session-a/designs/artifacts/artifact-a', 2, 'b'.repeat(64)],
      ['/v3/sessions/session-b/designs/artifacts/artifact-a', 2, 'b'.repeat(64)],
      ['/v3/sessions/session-b/designs/artifacts/artifact-b', 2, 'b'.repeat(64)],
      ['/v3/sessions/session-b/designs/artifacts/artifact-b', 2, 'b'.repeat(64)],
    ])

    await render({ ref: { revision: 3 } }) // delayed body, superseded below
    await render({ ref: { revision: 4 } })
    assert.equal(await count(), 8)
    await complete(7, 'current')
    await complete(6, 'obsolete')
    assert.match((await frame.getAttribute('srcdoc'))!, /current/)
    assert.equal(await page.evaluate(() => (window as any).calls[6].signal.aborted), true)

    await render({ kind: 'plan' })
    assert.equal(await count(), 8, 'plans must not request thumbnails')
    assert.equal(await frame.count(), 0)
    await page.getByText('Design plan', { exact: true }).waitFor()
    await render({ title: 'Plan progress' })
    assert.equal(await count(), 8)
    await render({ kind: 'design' })
    assert.equal(await count(), 9)
    await complete(8, 'after-plan')

    await page.evaluate(() => { (window as any).failNext = true })
    await render({ ref: { revision: 5 } })
    assert.equal(await frame.count(), 0)
    for (let i = 0; i < 3; i++) await render()
    assert.equal(await count(), 10, 'missing preview must not retry on equivalent snapshots')
    await page.getByRole('button', { name: 'Open selected output', exact: true }).click()
    assert.deepEqual(await page.evaluate(() => {
      const item = (window as any).opened[0]
      return { session: item.sessionId, ref: item.design.revision.ref }
    }), { session: 'session-b', ref: { artifact_id: 'artifact-b', revision: 5, sha256: 'b'.repeat(64) } })
    await render({ ref: { revision: 6 } })
    await complete(10, 'recovered')
    await render()
    assert.equal(await count(), 11)
    assert.match((await frame.getAttribute('srcdoc'))!, /recovered/)

    await render({ ref: { revision: 7 } })
    await page.waitForFunction(() => typeof (window as any).calls[11]?.complete === 'function')
    await page.evaluate(() => (window as any).unmount())
    assert.equal(await page.evaluate(() => (window as any).calls[11].signal.aborted), true)
    await complete(11, 'after-unmount')
    assert.equal(await frame.count(), 0)
    assert.equal(await count(), 12)
    assert.deepEqual(errors, [])
  } finally {
    await browser.close()
  }
})
