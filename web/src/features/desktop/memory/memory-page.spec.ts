import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { mkdtemp, readFile } from 'node:fs/promises'
import { join } from 'node:path'

// Purpose: exercise MemoryModal against the canonical /v1/memory contract with
// fake HTTP. Prevent stale-write draft loss, metadata loss, identity changes and
// unconfirmed deletion at the narrow browser interaction boundary.
test('memory modal organizes context and preserves drafts with safe mutations', { timeout: 30000 }, async () => {
 const scratch=await mkdtemp(join(process.env.TMPDIR!, 'memory-ui-'))
 const output=join(scratch,'page.js')
 await build({stdin:{contents:`import React from 'react';import{createRoot}from'react-dom/client';import{MemoryModal}from'./src/features/desktop/memory/memory-page';createRoot(document.getElementById('root')).render(React.createElement(MemoryModal,{onClose:()=>{}}));`,resolveDir:process.cwd(),loader:'tsx'},bundle:true,outfile:output,jsx:'automatic'})
 const browser=await chromium.launch({headless:true, timeout:10000, ...(process.env.SWARM_TEST_BROWSER ? { executablePath: process.env.SWARM_TEST_BROWSER } : {})})
 try {
 const page=await browser.newPage({viewport:{width:1280,height:900}})
 page.setDefaultTimeout(5000)
 page.on('pageerror', error=>console.error('memory fixture browser error:', error.message))
 const mutations: Record<string,unknown>[]=[]
 let reject=true
 let revision=4
 let entries=[{id:'saved',kind:'rule',content:'saved content',pinned:true,workspace_id:'workspace'}]
 await page.route('https://memory.test/**',async route=>{
  const req=route.request()
  if(req.url().includes('/v1/memory')){
   if(req.method()==='POST'){
    const mutation=req.postDataJSON();mutations.push(mutation)
    if(reject){await route.fulfill({status:409,body:'memory revision conflict'});return}
    assert.equal(mutation.expected_revision,revision)
    if(mutation.action==='remember' || mutation.action==='edit')entries=[mutation.entry]
    else if(mutation.action==='forget')entries=[]
    revision++
    await route.fulfill({contentType:'application/json',body:JSON.stringify({revision,entries})});return
   }
   const body=req.url().includes('session_id=')?{revision:4,injected_tokens:20,omitted:[{id:'context',reason:'different workspace'}],payload:'explicit rule'}:{revision,stored_tokens:13,token_method:'utf8_bytes_upper_bound',entries,history:[],jobs:[],settings:{read_enabled:true,remember_enabled:true,automation_enabled:false,mode:'manual',storage_tokens:8000,injection_tokens:2000,included_sessions:[],excluded_sessions:[]}}
   await route.fulfill({contentType:'application/json',body:JSON.stringify(body)});return
  }
  await route.fulfill({contentType:'text/html',body:'<html><body><div id="root"></div></body></html>'})
 })
 await page.goto('https://memory.test/memory');await page.addStyleTag({content:await readFile(join(scratch,'page.css'),'utf8')});await page.addScriptTag({content:await readFile(output,'utf8')})
 await page.getByRole('dialog',{name:'Memory',exact:true}).waitFor()
 assert.equal(await page.locator('textarea, input, select').count(),0)
 await page.getByRole('button',{name:'Add memory',exact:true}).click()
 assert.equal(await page.locator('textarea').count(),1)
 await page.getByLabel('Purpose',{exact:true}).selectOption('project_context')
 await page.getByRole('textbox',{name:'Memory',exact:true}).fill('keep this draft')
 await page.getByRole('button',{name:'Save memory'}).click()
 await page.getByRole('alert').filter({hasText:'revision conflict'}).waitFor()
 assert.equal(mutations[0].expected_revision,4)
 assert.equal(await page.getByRole('textbox',{name:'Memory',exact:true}).inputValue(),'keep this draft')
 assert.equal(typeof (mutations[0].entry as {id:string}).id,'string')
 await page.getByRole('button',{name:'Cancel',exact:true}).click()
 // Selected objects must keep their identity, preserve metadata, refresh on save,
 // and require confirmation before a destructive request is sent.
 reject=false
 await page.getByRole('button',{name:'Edit memory: saved content',exact:true}).click()
 assert.equal(await page.getByLabel('Workspace scope ID').inputValue(),'workspace')
 await page.getByRole('textbox',{name:'Memory',exact:true}).fill('updated object')
 await page.getByRole('button',{name:'Save memory'}).click()
 await page.getByRole('button',{name:'Edit memory: updated object',exact:true}).waitFor()
 assert.equal(mutations[1].action,'edit')
 assert.equal((mutations[1].entry as {pinned:boolean}).pinned,true)
 assert.equal((mutations[1].entry as {id:string}).id,'saved')
 assert.equal((mutations[1].entry as {workspace_id:string}).workspace_id,'workspace')
 assert.equal(await page.locator('textarea').count(),0)
 await page.getByRole('button',{name:'Forget',exact:true}).click()
 await page.getByRole('button',{name:'Cancel',exact:true}).click()
 assert.equal(mutations.length,2)
 await page.getByRole('button',{name:'Forget',exact:true}).click()
 await page.getByRole('button',{name:'Forget permanently'}).click()
 await page.getByText('No memories yet',{exact:true}).waitFor()
 assert.equal(mutations[2].entry_id,'saved')
 if(process.env.SWARM_MEMORY_SCREENSHOT)await page.screenshot({path:process.env.SWARM_MEMORY_SCREENSHOT,fullPage:true})
 } finally {await browser.close()}
})
