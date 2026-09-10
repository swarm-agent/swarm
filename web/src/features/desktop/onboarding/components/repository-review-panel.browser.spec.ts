// Requirement: RepositoryReviewPanel must accept real keyboard selection/consent,
// preserve exact retries after response loss, and await save acknowledgement.
// Threat: DOM-only snapshots miss focus dispatch and duplicate/changed mutations.
// A bundled production component in Chromium is the narrowest browser boundary;
// real API/store and PTY integration tests independently prove durable state.
import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

test('repository review keyboard consent and exact response-loss retry', { timeout: 30000 }, async () => {
 const bundle = await build({ stdin: { contents: `import React from 'react'; import {createRoot} from 'react-dom/client'; import {RepositoryReviewPanel} from './src/features/desktop/onboarding/components/repository-review-panel'; createRoot(document.getElementById('root')).render(<RepositoryReviewPanel path="/project" onCancel={()=>{}} onReady={async(path, consent)=>{ const r=await fetch('/saved',{method:'POST',body:JSON.stringify({path,consent})}); if(!r.ok) throw new Error('Save not acknowledged'); document.getElementById('done').textContent='Acknowledged'; }}/>);`, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format:'iife', jsx:'automatic' })
 const browser = await chromium.launch({headless:true, ...(process.env.SWARM_TEST_CHROMIUM ? {executablePath:process.env.SWARM_TEST_CHROMIUM} : {})})
 try {
 const page = await browser.newPage()
 const bodies: unknown[]=[]; let saves=0
 await page.route('http://127.0.0.1/**', async route => {
  const url=new URL(route.request().url())
  let body: unknown={ok:true}; let status=200
  if(url.pathname==='/') { await route.fulfill({contentType:'text/html',body:'<div id="root"></div><div id="done"></div>'}); return }
  if(url.pathname==='/v1/auth/desktop/session') body={ok:true,user_id:'fixture',account_scope_id:'fixture-account'}
  else if(url.pathname.endsWith('/review')) body={ok:true,review:{digest:'exact-review',repository:{path:'/project',state:'needs_assisted_setup'},files:[{path:'README.md',size:12,selectable:true},{path:'.env',size:10,selectable:true}],warning:'Omitted files are not copied.'}}
  else if(url.pathname.endsWith('/baseline')) { bodies.push(route.request().postDataJSON()); if(bodies.length===1) { await route.abort('failed'); return }; body={ok:true,repository:{path:'/project',state:'ready',head_commit:'acknowledged'}} }
  else if(url.pathname==='/saved') { saves++; status=saves===1?409:200 }
  else { throw new Error('Unexpected request '+url.pathname) }
  await route.fulfill({status,contentType:'application/json',body:JSON.stringify(body)})
 })
 await page.goto('http://127.0.0.1/')
 await page.addScriptTag({content:bundle.outputFiles[0].text})
 await page.getByRole('button',{name:'Load content review'}).focus(); await page.keyboard.press('Enter')
 await page.getByRole('checkbox').first().waitFor()
 const create=page.getByRole('button',{name:'Create selected baseline and save'})
 assert.equal(await create.isDisabled(),true)
 await page.getByRole('checkbox').first().focus(); await page.keyboard.press('Space')
 await page.keyboard.press('Tab'); await page.keyboard.press('Tab'); await page.keyboard.press('Space')
 await page.keyboard.press('Tab'); await page.keyboard.press('Enter')
 await page.getByRole('button',{name:'Retry exact baseline request'}).waitFor()
 assert.equal(saves,0); assert.equal(await page.getByRole('checkbox').first().isDisabled(),true)
 await page.getByRole('button',{name:'Retry exact baseline request'}).focus(); await page.keyboard.press('Enter')
 await page.getByText('Save not acknowledged',{exact:true}).waitFor()
 assert.equal(await page.locator('#done').textContent(),'')
 await page.getByRole('button',{name:'Retry saving / opening workspace'}).focus(); await page.keyboard.press('Enter')
 await page.getByText('Acknowledged',{exact:true}).waitFor()
 assert.equal(bodies.length,2); assert.deepEqual(bodies[0],bodies[1])
 assert.deepEqual(bodies[0],{path:'/project',expected_resolved_path:'/project',review_digest:'exact-review',selected_paths:['README.md'],confirm_baseline:true,confirm_omissions:true})
 assert.equal(saves,2)
 if (process.env.SWARM_TEST_SCREENSHOT) await page.screenshot({path:process.env.SWARM_TEST_SCREENSHOT,fullPage:true})
 } finally { await browser.close() }
})
