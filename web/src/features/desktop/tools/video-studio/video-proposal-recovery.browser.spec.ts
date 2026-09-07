import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: proposal selection on reload must stay revision-bound; stale
// recovery only stages feedback and never accepts/rejects/renders the old cut.
// Threat: hidden automatic selection causes a permanent render disable, or a
// recovery control replaces the confirmed cut. Bundle the actual hook component
// against intercepted synthetic API responses, the narrowest DOM authority test.
test('proposal reload selects current review and stale recovery does not mutate', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { contents: `import React from 'react'; import {createRoot} from 'react-dom/client'; import {VideoIterationSidebar} from './src/features/desktop/tools/video-studio/video-studio-surface';
    createRoot(document.getElementById('root')).render(<VideoIterationSidebar sessionId="session" projectId="project" currentRevisionId="r4" revisions={[]} onAccepted={()=>{}} onFeedback={text=>{window.feedback=text}} onPreviewProposal={p=>{window.selected=p?.id??null}} onPreviewRevision={id=>{window.preview=id}} onFocusChange={()=>{}} onAttachChange={()=>{}}/>);`, resolveDir: process.cwd(), loader: 'tsx' }, bundle:true,write:false,platform:'browser',format:'iife',jsx:'automatic',logLevel:'silent' })
  const browser = await chromium.launch({headless:true,channel:process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome'})
  try {
    const page=await browser.newPage(); page.setDefaultTimeout(5000)
    const older={id:'older',project_id:'project',status:'pending',working_revision_id:'r2',base_revision_id:'r1',base_revision_number:1,created_at:1,updated_at:1,title:'Older initial proposal',operations:[],plan:{kind:'initial',parts:[]}}
    let current=false; const mutations:string[]=[]
    await page.route('**/*',route=>{
      const request=route.request(); const pathname=new URL(request.url()).pathname
      if(request.method()!=='GET') {mutations.push(pathname); return route.abort()}
      if(pathname==='/') return route.fulfill({contentType:'text/html',body:'<div id="root"></div>'})
      if(pathname.endsWith('/edit-proposals')) return route.fulfill({json:{proposals:current?[older,{...older,id:'current',working_revision_id:'r4',title:'Current proposal',created_at:2}]:[older]}})
      return route.abort()
    })
    await page.goto('https://studio.test/'); await page.addScriptTag({content:bundle.outputFiles[0].text})
    await page.getByText('Older pending proposal.',{exact:false}).waitFor()
    assert.equal(await page.evaluate(()=>(window as any).selected),null)
    await page.getByRole('button',{name:'Ask AI to rework from current cut'}).click()
    assert.match(await page.evaluate(()=>(window as any).feedback),/older.*current cut/)
    await page.getByRole('button',{name:'Preview older working cut'}).click()
    assert.equal(await page.evaluate(()=>(window as any).preview),'r2')
    current=true; await page.getByRole('button',{name:'Refresh',exact:true}).click()
    await page.waitForFunction(()=>(window as any).selected==='current')
    assert.equal(await page.getByRole('button',{name:'Confirm enabled changes'}).count(),1)
    await page.reload(); await page.addScriptTag({content:bundle.outputFiles[0].text})
    await page.waitForFunction(()=>(window as any).selected==='current')
    assert.deepEqual(mutations,[],'recovery and reload never mutate the project')
  } finally {await browser.close()}
})
