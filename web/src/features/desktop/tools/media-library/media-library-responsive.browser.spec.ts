import assert from 'node:assert/strict'
import test from 'node:test'
import path from 'node:path'
import { build } from 'esbuild'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import { chromium, type Locator } from 'playwright'

// Purpose: HistoricalMediaLibrary and MediaViewerModal must keep filters, retained
// assets, metadata and generation controls reachable in constrained Swarm lanes.
// Threat: viewport breakpoints overflow a phone-width lane on a desktop; a fixed
// toolbar hides close/actions, and desktop-only details disappear on phones.
// Authority: the real library/grid/list/viewer components, production theme CSS,
// container rules and modal keyboard boundary. Browser geometry is the narrowest
// layer proving sizing, local scrolling, focus restoration and resize continuity.
// Catalog/settings are deterministic read-boundary fixtures, not live-provider
// evidence. Generation is never executed; its callback must remain untouched.
async function fits(region: Locator) {
  assert.equal(await region.evaluate(node => node.scrollWidth <= node.clientWidth + 1), true, 'no region-level horizontal overflow')
}

async function reachable(control: Locator) {
  await control.scrollIntoViewIfNeeded()
  const geometry = await control.evaluate(node => {
    const r = node.getBoundingClientRect()
    return { x: r.x, y: r.y, width: r.width, height: r.height, vw: innerWidth, vh: innerHeight }
  })
  assert.ok(geometry.width >= 44 && geometry.height >= 44, 'primary touch target is at least 44px')
  assert.ok(geometry.x >= -1 && geometry.x + geometry.width <= geometry.vw + 1 && geometry.y >= -1 && geometry.y + geometry.height <= geometry.vh + 1, 'action can be scrolled into view')
}

test('media library and viewer fit phone lanes, landscape, tablet and desktop without losing state', { timeout: 60000 }, async () => {
  const label = 'Retained_' + 'long_unbroken_label_'.repeat(12)
  const item = {
    id: 'fixture-image', title: label, filename: label + '.svg', kind: 'image', mediaType: 'image/svg+xml',
    createdAt: 1800000000000, formattedDate: 'Sep 20', formattedTime: '10:00 AM', dayKey: '2026-09-20', dayLabel: 'Today',
    sessionId: 'fixture-session', sessionTitle: label, workspaceName: 'Demo', directUrl: '/preview.svg',
    model: 'fixture-image-model', aspectRatio: '1:1', resolution: '1k',
    artifact: { artifactId: 'fixture-image', sessionId: 'fixture-session', description: label, kind: 'image', filename: 'preview.svg', mediaType: 'image/svg+xml' },
  }
  const catalog = { image_models: [{ id: 'fixture-image-model', model: 'fixture-image-model', display_name: label, ready: true, kind: 'image_generation', provider: 'fixture', generation_options: { aspect_ratios: ['1:1', '16:9'], resolutions: ['1k'], default_ratio: '1:1', default_resolution: '1k' } }] }
  const bundle = await build({
    stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
      import React from 'react'; import {createRoot} from 'react-dom/client';
      import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
      import {HistoricalMediaLibrary} from './src/features/desktop/tools/media-library/historical-media-library';
      const root=createRoot(document.getElementById('root'));
      const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
      window.catalogError=false; window.generateCalls=0; window.tagCalls=0;
      window.renderLibrary=(populated=false)=>root.render(<QueryClientProvider client={client}>
        <HistoricalMediaLibrary extraItems={populated?[${JSON.stringify(item)}]:[]} onGenerate={async()=>{window.generateCalls++}}
          onTagMedia={()=>{window.tagCalls++}} taggedMediaIds={new Set(['fixture-image'])} onOpenSession={()=>{}} />
      </QueryClientProvider>);
      window.renderLibrary();
    ` },
    bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent',
    plugins: [{ name: 'read-boundaries', setup(b) {
      b.onResolve({ filter: /(?:session-v3\/artifact-api|app\/api|get-media-settings|queries\/query-options)$/ }, args => ({ path: args.path.split('/').pop()!, namespace: 'fixture' }))
      b.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ loader: 'js', contents:
        args.path === 'artifact-api' ? `export async function fetchDesktopV3ArtifactCatalogResult(){if(window.catalogError)throw new Error('${label}');return {artifacts:[]}}` :
        args.path === 'get-media-settings' ? `export const getMediaSettingsCatalog=async()=>(${JSON.stringify(catalog)});` :
        args.path === 'query-options' ? `export const uiSettingsQueryKey=()=>['ui-settings'];export const uiSettingsQueryOptions=()=>({queryKey:uiSettingsQueryKey(),queryFn:async()=>({})});` :
        `export const requestJson=async()=>{throw new Error('Unexpected write boundary')};`,
      }))
    } }],
  })
  const styles = await buildStyles({ configFile: false, logLevel: 'silent', publicDir: false, plugins: [tailwindcss()], build: { write: false, rollupOptions: { input: path.resolve('src/theme.css') } } })
  const outputs = (Array.isArray(styles) ? styles : [styles]).flatMap(result => 'output' in result ? result.output : [])
  const css = outputs.filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css')).map(asset => asset.type === 'asset' ? String(asset.source) : '').join('\n')
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || undefined })
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({ contentType: route.request().url().endsWith('/preview.svg') ? 'image/svg+xml' : 'text/html', body: route.request().url().endsWith('/preview.svg') ? '<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="1600"><rect width="1200" height="1600" fill="blue"/></svg>' : '<div id="root" style="width:360px;height:800px"></div>' }))
    await page.goto('https://media.test/')
    await page.addStyleTag({ content: css })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByText('No media artifacts found', { exact: true }).waitFor()
    const library = page.locator('.media-library')
    await fits(library)
    await reachable(page.getByRole('button', { name: 'Refresh media catalog' }))
    await page.evaluate(() => { (window as any).catalogError = true })
    await page.getByRole('button', { name: 'Refresh media catalog' }).click()
    await page.getByText(label, { exact: true }).waitFor()
    await fits(page.locator('.media-library-content'))
    await reachable(page.getByRole('button', { name: 'Retry', exact: true }))
    await page.evaluate(() => { (window as any).catalogError = false })
    await page.getByRole('button', { name: 'Retry', exact: true }).click()
    await page.getByText('No media artifacts found', { exact: true }).waitFor()
    await page.evaluate(() => (window as any).renderLibrary(true))
    const card = page.locator('.media-library-content button').first()
    await card.waitFor()
    await page.getByRole('textbox', { name: 'Search media artifacts' }).fill('no-match')
    await page.getByText('No media artifacts found', { exact: true }).waitFor()
    await reachable(page.getByRole('button', { name: 'Reset filters', exact: true }))
    await page.getByRole('button', { name: 'Reset filters', exact: true }).click()
    await card.waitFor()
    for (const width of [360, 390, 768, 1100]) {
      await page.locator('#root').evaluate((node, width) => { node.style.width = width + 'px' }, width)
      await fits(library)
      await fits(page.locator('.media-library-content'))
      const grid = page.locator('.media-library-content .grid').first()
      const columns = await grid.evaluate(node => getComputedStyle(node).gridTemplateColumns.split(' ').length)
      assert.ok(width > 390 || columns === 1, 'narrow lane uses a readable single column even on desktop')
    }
    await page.locator('#root').evaluate(node => { node.style.width = '360px' })
    await page.getByRole('button', { name: 'List view', exact: true }).click()
    await page.locator('table').waitFor()
    await fits(page.locator('.media-library-content'))
    assert.equal(await page.locator('table').evaluate(node => getComputedStyle(node.parentElement!).overflowX), 'auto', 'wide list scrolls locally')
    await page.getByRole('button', { name: 'Thumbnails view', exact: true }).click()
    await card.focus()
    await card.click()
    const dialog = page.getByRole('dialog')
    await dialog.waitFor()
    assert.equal(await page.getByRole('button', { name: 'Close viewer', exact: true }).evaluate(node => node === document.activeElement), true)
    await page.getByRole('textbox', { name: 'Generation instructions' }).fill('Keep this draft across resize')
    for (const viewport of [{ width: 360, height: 800 }, { width: 390, height: 844 }, { width: 844, height: 390 }, { width: 768, height: 900 }, { width: 1280, height: 900 }]) {
      await page.setViewportSize(viewport)
      await fits(dialog)
      await reachable(page.getByRole('button', { name: 'Close viewer', exact: true }))
      await reachable(page.getByRole('button', { name: 'Tag media for task', exact: true }))
      await reachable(page.getByRole('button', { name: 'Download media file', exact: true }))
      await reachable(page.getByRole('button', { name: /Generate revision/ }))
      assert.equal(await page.getByRole('textbox', { name: 'Generation instructions' }).inputValue(), 'Keep this draft across resize')
      await page.locator('aside').getByText('Metadata & Details', { exact: true }).scrollIntoViewIfNeeded()
      await fits(page.locator('aside'))
      const preview = page.locator('main img').first()
      await preview.scrollIntoViewIfNeeded()
      assert.equal(await preview.evaluate(node => { const r = node.getBoundingClientRect(); const p = node.closest('main')!.getBoundingClientRect(); return r.width > 0 && r.height > 0 && r.width <= p.width && r.height <= p.height }), true, 'preview fits available canvas')
    }
    assert.equal(await page.getByRole('textbox', { name: 'Generation instructions' }).inputValue(), 'Keep this draft across resize')
    await page.getByRole('button', { name: 'Tag media for task' }).click()
    assert.equal(await page.evaluate(() => (window as any).tagCalls), 1)
    await page.keyboard.press('Escape')
    await dialog.waitFor({ state: 'detached' })
    assert.equal(await card.evaluate(node => node === document.activeElement), true, 'focus returns to retained card')
    await card.click()
    await dialog.waitFor()
    await dialog.evaluate(node => {
      const controls = Array.from(node.querySelectorAll<HTMLElement>('button:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex="0"]')).filter(control => control.getClientRects().length > 0)
      controls[controls.length - 1].focus()
    })
    await page.keyboard.press('Tab')
    assert.equal(await dialog.evaluate(node => document.activeElement === node.querySelector('button:not(:disabled)')), true, 'Tab stays within dialog')
    await page.locator('main').click({ position: { x: 2, y: 2 } })
    await dialog.waitFor({ state: 'detached' })
    assert.equal(await card.evaluate(node => node === document.activeElement), true, 'overlay close restores focus')
    assert.equal(await page.evaluate(() => (window as any).generateCalls), 0, 'layout interactions never generate media')
    assert.deepEqual(errors, [])
  } finally {
    await browser.close()
  }
})
