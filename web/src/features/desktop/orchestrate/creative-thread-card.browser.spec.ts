// Purpose: MediaTaskThreads -> CreativeThreadCard -> MediaViewerModal must keep
// request/candidate identity through updates, retain preview DOM/resources and route
// real media elements to exact outputs. Chromium is the narrowest layer for DOM
// identity, roving keyboard focus, overflow and source attributes. It also proves
// pending spinners are centered with real CSS, honor reduced motion, and disappear
// on failure/completion without replacing ready sibling previews. HTTP responses
// are hermetic fixtures, not daemon/provider or end-to-end generation evidence.
import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { chromium } from 'playwright'

test('creative cards retain previews, isolate updates and navigate exact image video and audio outputs', { timeout: 60_000 }, async () => {
  const result = await build({ configFile: false, logLevel: 'error', plugins: [react(), tailwindcss(), {
    name: 'creative-thread-fixture',
    resolveId(id) { if (id === 'virtual:creative-thread') return '\0creative-thread.tsx' },
    load(id) { if (id === '\0creative-thread.tsx') return `
      import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
      import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
      import {MediaTaskThreads} from '${process.cwd()}/src/features/desktop/orchestrate/media-task-card.tsx';
      import {mapBackendTask} from '${process.cwd()}/src/features/desktop/state/desktop-projects-state.ts';
      import {MediaViewerModal} from '${process.cwd()}/src/features/desktop/tools/media-library/media-viewer-modal.tsx';
      import '${process.cwd()}/src/theme.css';
      const output=(id,kind,parent)=>({id,kind,title:id,status:'ready',media_url:'/media/'+id,parent_deliverable_id:parent});
      const imageA=output('image-a','image'),imageB=output('image-b','image');
      const video=output('video','video','image-b'),audio=output('audio','audio','video');
      const root=mapBackendTask({id:'root',title:'Creative request',agent:'image',status:'completed',deliverables:[imageA,imageB]});
      const unrelated=mapBackendTask({id:'unrelated',title:'Unrelated request',agent:'image',status:'completed',deliverables:[output('other','image')]});
      const item=(d)=>({id:d.id,title:d.id,filename:d.id,kind:d.kind,mediaType:d.kind==='image'?'image/png':d.kind==='video'?'video/mp4':'audio/wav',directUrl:d.media_url,parentId:d.parent_deliverable_id,createdAt:0,formattedDate:'',formattedTime:'',dayKey:'fixture',dayLabel:'Fixture',sessionId:'',sessionTitle:'',workspacePath:'',workspaceName:''});
      const items=[imageA,imageB,video,audio].map(item);
      window.opened=[];window.archived=[];window.deleted=[];
      function App(){const[phase,setPhase]=useState('initial');const[item,setItem]=useState(null);
        window.setPhase=setPhase;
        const child=mapBackendTask({id:'child',title:'Continue image',agent:'video',status:phase==='ready'?'completed':phase==='failed'?'failed':'in_progress',last_error:phase==='failed'?'Generation failed':undefined,attached_media:[{id:'image-b',kind:'image',title:'image-b'}],deliverables:phase==='ready'?[video]:[{id:'pending-video',kind:'video',title:'Pending video',status:phase==='failed'?'failed':'generating'}]});
        const final=mapBackendTask({id:'audio-turn',title:'Add audio',agent:'audio',status:'completed',deliverables:[audio]});
        const tasks=[root,...(phase==='initial'?[]:[child]),...(phase==='ready'?[final]:[]),{...unrelated,status:phase==='unrelated'?'in_progress':'completed'}];
        const jobs=[{id:'root',sourceId:'image-a',outputIds:['image-a','image-b'],title:'Original',status:'completed',count:2},{id:'child',sourceId:'image-b',outputIds:['video'],title:'Continue image',status:'completed',count:1},{id:'audio-turn',sourceId:'video',outputIds:['audio'],title:'Add audio',status:'completed',count:1}];
        const select=next=>{window.opened.push(next.id);setItem(next)};
        return <><MediaTaskThreads tasks={tasks} visibleTaskIds={new Set(tasks.map(task=>task.id))} actions={task=>({onPreview:d=>select(items.find(item=>item.id===d.id)),onArchive:()=>window.archived.push(task.id),onDelete:()=>window.deleted.push(task.id)})}/>
          {item&&<MediaViewerModal item={item} items={items} threadItems={items} generationJobs={jobs} onClose={()=>setItem(null)} onSelect={select}/>}</>;
      }
      createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><App/></QueryClientProvider>);
    ` },
  }], build: { write: false, minify: false, rolldownOptions: { input: 'virtual:creative-thread', output: { inlineDynamicImports: true } } } })
  assert.ok(!Array.isArray(result) && 'output' in result)
  const js = result.output.filter(entry => entry.type === 'chunk').map(entry => entry.code).join('\n')
  const css = result.output.filter(entry => entry.type === 'asset' && entry.fileName.endsWith('.css')).map(entry => String(entry.source)).join('\n')
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || undefined })
  try {
    const page = await browser.newPage({ viewport: { width: 375, height: 850 }, reducedMotion: 'reduce' })
    page.setDefaultTimeout(7000)
    const reads: string[] = []; const unexpected: string[] = []; const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => {
      const path = new URL(route.request().url()).pathname
      if (path === '/fixture') return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
      if (path.startsWith('/media/')) {
        // Source-routing proof only: deliberately no claim of video/audio decoding.
        reads.push(path)
        return route.fulfill({ contentType: 'image/png', body: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6H7sAAAAASUVORK5CYII=', 'base64') })
      }
      if (path === '/v1/auth/desktop/session') return route.fulfill({ json: { user_id: 'fixture', account_scope_id: 'fixture' } })
      if (path.includes('catalog')) return route.fulfill({ json: { image_models: [], video_models: [] } })
      if (path.includes('settings')) return route.fulfill({ json: {} })
      unexpected.push(`${route.request().method()} ${path}`)
      return route.fulfill({ status: 501, json: { error: 'Unexpected fixture request' } })
    })
    await page.goto('http://localhost/fixture')
    await page.addStyleTag({ content: css }); await page.addScriptTag({ content: js, type: 'module' })
    const card = page.locator('[data-task-id="root"]')
    await card.getByRole('tab').waitFor()
    const image = card.locator('img[alt="image-a"]')
    await image.evaluate(async element => { await (element as HTMLImageElement).decode(); (window as any).retainedImage = element })
    await card.getByRole('tab').click() // Explicit earlier selection must survive later completions.
    const originalReads = reads.filter(path => path === '/media/image-a').length
    await card.evaluate(element => { (window as any).retainedCard = element })
    for (const phase of ['running', 'unrelated', 'failed']) {
      await page.evaluate(async phase => {
        (window as any).setPhase(phase)
        await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      }, phase)
      await card.getByRole('tab').nth(1).waitFor()
      if (phase === 'failed') await card.getByRole('alert').filter({ hasText: 'Generation failed' }).waitFor()
      else await card.getByRole('status').filter({ hasText: 'running' }).waitFor()
      const pendingTurn = card.getByRole('tab').nth(1)
      const spinner = pendingTurn.getByRole('status', { name: 'Turn 2: running' })
      if (phase === 'failed') {
        assert.equal(await spinner.count(), 0)
      } else {
        await spinner.waitFor()
        assert.equal(await pendingTurn.locator('.creative-turn-heading').innerText(), 'Turn 2')
        for (const width of [320, 375, 1440]) {
          await page.setViewportSize({ width, height: 850 })
          await pendingTurn.scrollIntoViewIfNeeded()
          const previewBox = await pendingTurn.locator('.creative-turn-preview').boundingBox()
          const spinnerBox = await spinner.locator('svg').boundingBox()
          assert.ok(previewBox && spinnerBox)
          assert.ok(Math.abs(previewBox.x + previewBox.width / 2 - spinnerBox.x - spinnerBox.width / 2) < 1)
          assert.ok(Math.abs(previewBox.y + previewBox.height / 2 - spinnerBox.y - spinnerBox.height / 2) < 1)
          assert.equal(await spinner.locator('svg').evaluate(el => getComputedStyle(el).animationName), 'none')
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
        }
        await page.emulateMedia({ reducedMotion: 'no-preference' })
        assert.equal(await spinner.locator('svg').evaluate(el => getComputedStyle(el).animationName), 'spin')
        await page.emulateMedia({ reducedMotion: 'reduce' })
        if (phase === 'running' && process.env.SWARM_MEDIA_REVIEW_SCREENSHOT) {
          await card.screenshot({ path: process.env.SWARM_MEDIA_REVIEW_SCREENSHOT })
        }
      }
      assert.equal(await card.getByRole('tab').count(), 2)
      assert.equal(await card.getByRole('tab').first().getAttribute('aria-selected'), 'true')
      assert.equal(await card.evaluate(element => element === (window as any).retainedCard), true)
      assert.equal(await image.evaluate(element => element === (window as any).retainedImage), true)
      assert.equal(reads.filter(path => path === '/media/image-a').length, originalReads)
      assert.equal(await page.getByTestId('media-task-card').count(), 2)
    }
    await card.getByRole('tab').nth(1).click()
    assert.equal(await card.getByRole('button', { name: 'Open selected output' }).isDisabled(), true)
    assert.deepEqual(await page.evaluate(() => (window as any).opened), [])
    await card.getByRole('tab').first().click()
    await page.evaluate(() => (window as any).setPhase('ready'))
    await card.getByRole('tab').nth(2).waitFor()
    assert.equal(await card.locator('.creative-turn-preview [role="status"]').count(), 0)
    assert.equal(await image.evaluate(element => element === (window as any).retainedImage), true)
    assert.equal(reads.filter(path => path === '/media/image-a').length, originalReads)
    assert.equal(await card.getByRole('tab').first().getAttribute('aria-selected'), 'true')
    for (const width of [320, 375, 1440]) {
      await page.setViewportSize({ width, height: 850 })
      const tabs = card.getByRole('tab')
      await tabs.first().focus(); await tabs.first().press('End')
      assert.equal(await tabs.last().evaluate(element => element === document.activeElement), true)
      assert.equal(await tabs.last().getAttribute('aria-selected'), 'true')
      await tabs.last().press('ArrowRight')
      assert.equal(await tabs.first().evaluate(element => element === document.activeElement), true)
      await tabs.first().press('ArrowLeft')
      assert.equal(await tabs.last().evaluate(element => element === document.activeElement), true)
      await tabs.last().press('Home')
      assert.equal(await tabs.first().getAttribute('tabindex'), '0')
      assert.equal(await card.locator('[role="tab"][tabindex="0"]').count(), 1)
      assert.equal(await tabs.first().evaluate(element => getComputedStyle(element).transitionDuration), '0s')
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
      if (width === 320) assert.equal(await card.getByRole('tablist').evaluate(element => element.scrollWidth > element.clientWidth), true)
    }
    await card.getByRole('button', { name: 'Candidate 2 · ready', exact: true }).click()
    await card.getByRole('button', { name: 'Open selected output', exact: true }).click()
    const dialog = page.getByRole('dialog')
    await dialog.locator('main img[src="/media/image-b"]').waitFor()
    const nav = dialog.getByRole('navigation', { name: 'Viewer turn navigation' })
    assert.equal(await nav.getByRole('button', { name: '← Prev Turn' }).isDisabled(), true)
    await nav.getByRole('button', { name: 'Next Turn →' }).click()
    await dialog.locator('main video[src="/media/video"]').waitFor()
    await nav.getByRole('button', { name: 'Next Turn →' }).click()
    await dialog.locator('main audio[src="/media/audio"]').waitFor()
    assert.equal(await nav.getByRole('button', { name: 'Next Turn →' }).isDisabled(), true)
    await nav.getByRole('button', { name: '← Prev Turn' }).click()
    await dialog.locator('main video[src="/media/video"]').waitFor()
    assert.deepEqual(await page.evaluate(() => (window as any).opened), ['image-b', 'video', 'audio', 'video'])
    await dialog.getByRole('button', { name: 'Close viewer' }).click()
    await card.getByRole('button', { name: 'Archive', exact: true }).click()
    await card.getByRole('button', { name: 'Delete', exact: true }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).archived), ['root'])
    assert.deepEqual(await page.evaluate(() => (window as any).deleted), ['root'])
    assert.deepEqual(unexpected, [])
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
