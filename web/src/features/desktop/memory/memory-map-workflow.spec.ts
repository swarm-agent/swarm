import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { mkdtemp, readFile } from 'node:fs/promises'
import { join } from 'node:path'

// Purpose: exercise actual MemoryModal/WorkspaceMapEditor controls with bounded
// fake HTTP. Reject consent-free recovery saves, map revision confusion, draft
// loss after denied/conflicted writes, and accidental resume requests.
test('memory map and recovery saves require explicit review and preserve drafts', {timeout:30000}, async () => {
 const scratch=await mkdtemp(join(process.env.TMPDIR!, 'memory-map-ui-'));const output=join(scratch,'page.js')
 await build({stdin:{contents:`import React from 'react';import{createRoot}from'react-dom/client';import{MemoryModal}from'./src/features/desktop/memory/memory-page';createRoot(document.getElementById('root')).render(React.createElement(MemoryModal,{onClose:()=>{}}));`,resolveDir:process.cwd(),loader:'tsx'},bundle:true,outfile:output,jsx:'automatic'})
 const browser=await chromium.launch({headless:true,timeout:10000,...(process.env.SWARM_TEST_BROWSER?{executablePath:process.env.SWARM_TEST_BROWSER}:{})})
 try {
  const page=await browser.newPage({viewport:{width:1100,height:900}});page.setDefaultTimeout(5000)
  const writes:any[]=[];let reject=403
  const capture = async (name: string) => { if(process.env.SWARM_MEMORY_SCREENSHOT) await page.screenshot({path:process.env.SWARM_MEMORY_SCREENSHOT.replace('.png',`-${name}.png`),fullPage:true}) }
  const entries=[{id:'old',kind:'rule',content:'legacy text',pinned:false},{id:'ops',kind:'rule',content:'Use the registered endpoint',purpose:'operational_context',origin:'user',subject:'Test environment',pinned:false}]
  let record={revision:7,content:'# Workspace Map\n\nOriginal orientation\n',updated_at:1700000000000}
  await page.route('https://memory.test/**',async route=>{
   const req=route.request()
   if(req.url().endsWith('/v1/memory/workspace-map')) {
    if(req.method()==='POST') {const body=req.postDataJSON();writes.push(body);if(reject){await route.fulfill({status:reject,body:'map save rejected'});return}assert.equal(body.expected_revision,7);assert.equal(body.confirm,true);record={...record,content:body.content,revision:8};await route.fulfill({json:record});return}
    await route.fulfill({json:{found:true,workspace_map:record}});return
   }
   if(req.url().endsWith('/v1/memory')) {
    if(req.method()==='POST'){const body=req.postDataJSON();writes.push(body);if(reject){await route.fulfill({status:reject,body:'memory save denied'});return}entries.push(body.entry);await route.fulfill({json:{revision:11,entries}});return}
    await route.fulfill({json:{revision:10,entries}});return
   }
   assert.equal(req.method(),'GET','must not submit session/resume mutations')
   await route.fulfill({contentType:'text/html',body:'<html><body><div id="root"></div></body></html>'})
  })
  await page.goto('https://memory.test/memory');await page.addStyleTag({content:await readFile(join(scratch,'page.css'),'utf8')});await page.addScriptTag({content:await readFile(output,'utf8')})
  await page.getByRole('button',{name:'Edit memory: legacy text'}).waitFor()
  assert.equal(await page.getByRole('button',{name:'Edit memory: Use the registered endpoint'}).count(),0)
  await capture('requested')
  await page.getByRole('button',{name:'AI operating guidance',exact:true}).click()
  await page.getByRole('button',{name:'Edit memory: Use the registered endpoint'}).waitFor()
  await page.getByRole('button',{name:'Add memory',exact:true}).click()
  await page.getByLabel('Purpose',{exact:true}).selectOption('recovery')
  await page.getByRole('textbox',{name:'Memory',exact:true}).fill('Use an approved credential location, never a secret value.')
  assert.equal(await page.getByRole('button',{name:'Save memory',exact:true}).isDisabled(),true)
  assert.equal(writes.length,0)
  await capture('recovery')
  await page.getByRole('checkbox').check();await page.getByRole('button',{name:'Save memory',exact:true}).click()
  await page.getByRole('alert').filter({hasText:'denied'}).waitFor()
  assert.match(await page.getByRole('textbox',{name:'Memory',exact:true}).inputValue(),/approved credential location/)
  assert.equal(writes[0].entry.purpose,'recovery');assert.equal(writes[0].entry.origin,undefined)
  await page.getByRole('button',{name:'Cancel',exact:true}).click();assert.equal(writes.length,1)
  await page.getByRole('button',{name:'Workspace map',exact:true}).click()
  const editor=page.getByRole('textbox',{name:'Workspace map',exact:true});await editor.fill('# Workspace Map\n\nUpdated orientation\n')
  reject=409;await page.getByRole('button',{name:'Save workspace map'}).click();await page.getByRole('alert').filter({hasText:'map save rejected'}).waitFor()
  assert.match(await editor.inputValue(),/Updated orientation/)
  assert.equal(writes[1].expected_revision,7,'map entry revision, not memory document revision')
  reject=0;await page.getByRole('button',{name:'Save workspace map'}).click();await page.getByText('Revision 8',{exact:false}).waitFor()
  assert.equal(writes.length,3)
  await capture('map')
  await editor.fill('# Workspace Map\n\nUnsent change')
  page.once('dialog', dialog => void dialog.dismiss())
  await page.getByRole('button',{name:'Your requested memories',exact:true}).click()
  assert.match(await editor.inputValue(),/Unsent change/)
  assert.equal(writes.length,3)
  page.once('dialog', dialog => void dialog.accept())
  await page.getByRole('button',{name:'AI operating guidance',exact:true}).click()
  await page.getByRole('button',{name:'Add memory',exact:true}).click()
  await page.getByLabel('Purpose',{exact:true}).selectOption('recovery')
  await page.getByRole('textbox',{name:'Memory',exact:true}).fill('Check the approved endpoint before retrying.')
  await page.getByRole('checkbox').check()
  await page.getByRole('button',{name:'Save memory',exact:true}).click()
  await page.getByRole('button',{name:'Edit memory: Check the approved endpoint before retrying.'}).waitFor()
  assert.equal(writes.length,4)
  assert.equal(writes[3].entry.purpose,'recovery')
 } finally {await browser.close()}
})
