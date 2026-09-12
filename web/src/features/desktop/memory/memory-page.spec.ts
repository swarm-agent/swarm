import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { mkdtemp, readFile } from 'node:fs/promises'
import { join } from 'node:path'

// Purpose: execute the real MemoryPage in a browser with a bounded fake HTTP
// authority. Prove accessible editor controls, exact CAS submission, stale-error
// draft preservation and preview rendering, rather than checking source strings.
test('memory editor preserves a stale draft and exposes scoped preview', { timeout: 30000 }, async () => {
 const scratch=await mkdtemp(join(process.env.TMPDIR!, 'memory-ui-'))
 const output=join(scratch,'page.js')
 await build({stdin:{contents:`import React from 'react';import{createRoot}from'react-dom/client';import{MemoryPage}from'./src/features/desktop/memory/memory-page';createRoot(document.getElementById('root')).render(React.createElement(MemoryPage));`,resolveDir:process.cwd(),loader:'tsx'},bundle:true,outfile:output,jsx:'automatic'})
 const browser=await chromium.launch({headless:true, timeout:10000, ...(process.env.SWARM_TEST_BROWSER ? { executablePath: process.env.SWARM_TEST_BROWSER } : {})})
 try {
 const page=await browser.newPage({viewport:{width:1280,height:900}})
 page.setDefaultTimeout(5000)
 page.on('pageerror', error=>console.error('memory fixture browser error:', error.message))
 const mutations: Record<string,unknown>[]=[]
 let reject=true
 let revision=4
 let entries=[{id:'saved',kind:'rule',content:'saved content',pinned:true,workspace_id:'workspace'}]
 await page.route('http://memory.test/**',async route=>{
  const req=route.request()
  if(req.url().includes('/v1/memory')){
   if(req.method()==='POST'){
    const mutation=req.postDataJSON();mutations.push(mutation)
    if(reject){await route.fulfill({status:409,body:'memory revision conflict'});return}
    assert.equal(mutation.expected_revision,revision)
    if(mutation.action==='remember')entries=[mutation.entry]
    else if(mutation.action==='forget')entries=[]
    revision++
    await route.fulfill({contentType:'application/json',body:JSON.stringify({revision,entries})});return
   }
   const body=req.url().includes('session_id=')?{revision:4,injected_tokens:20,omitted:[{id:'context',reason:'different workspace'}],payload:'explicit rule'}:{revision,stored_tokens:13,token_method:'utf8_bytes_upper_bound',entries,history:[],jobs:[],settings:{read_enabled:true,remember_enabled:true,automation_enabled:false,mode:'manual',storage_tokens:8000,injection_tokens:2000,included_sessions:[],excluded_sessions:[]}}
   await route.fulfill({contentType:'application/json',body:JSON.stringify(body)});return
  }
  await route.fulfill({contentType:'text/html',body:'<html><body><div id="root"></div></body></html>'})
 })
 await page.goto('http://memory.test/memory');await page.addStyleTag({content:await readFile(join(scratch,'page.css'),'utf8')});await page.addScriptTag({content:await readFile(output,'utf8')})
 await page.getByRole('heading',{name:'Explicit memory editor'}).waitFor()
 await page.getByLabel('ID',{exact:true}).fill('rule')
 await page.getByLabel('Content',{exact:true}).fill('keep this draft')
 await page.getByLabel('Reason for change').fill('explicit request')
 await page.getByRole('button',{name:'Save explicit memory'}).click()
 await page.getByRole('alert').filter({hasText:'revision conflict'}).waitFor()
 assert.equal(mutations[0].expected_revision,4)
 assert.equal(await page.getByLabel('Content',{exact:true}).inputValue(),'keep this draft')
 await page.getByRole('button',{name:'Inspect injection and omissions'}).click()
 await page.getByText('different workspace',{exact:false}).waitFor()
 await page.getByRole('button',{name:'New entry',exact:true}).focus()
 assert.equal(await page.evaluate(()=>document.activeElement?.textContent),'New entry')
 // Selected objects must keep their identity, preserve metadata, refresh on save,
 // and require confirmation before a destructive request is sent.
 reject=false
 await page.getByRole('button',{name:'Edit',exact:true}).click()
 assert.equal(await page.getByLabel('ID',{exact:true}).isDisabled(),true)
 await page.getByLabel('Content',{exact:true}).fill('updated object')
 await page.getByRole('button',{name:'Save explicit memory'}).click()
 await page.locator('article pre').filter({hasText:'updated object'}).waitFor()
 assert.equal((mutations[1].entry as {pinned:boolean}).pinned,true)
 assert.equal(await page.getByLabel('Content',{exact:true}).inputValue(),'')
 page.once('dialog',dialog=>void dialog.dismiss())
 await page.getByRole('button',{name:'Forget permanently'}).click()
 assert.equal(mutations.length,2)
 page.once('dialog',dialog=>void dialog.accept())
 await page.getByRole('button',{name:'Forget permanently'}).click()
 await page.getByText('No saved memories.',{exact:true}).waitFor()
 assert.equal(mutations[2].entry_id,'saved')
 if(process.env.SWARM_MEMORY_SCREENSHOT)await page.screenshot({path:process.env.SWARM_MEMORY_SCREENSHOT,fullPage:true})
 } finally {await browser.close()}
})
