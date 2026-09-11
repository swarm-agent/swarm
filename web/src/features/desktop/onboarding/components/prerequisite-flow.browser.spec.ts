// Requirement: the actual embedded setup document submits disclosed actions once,
// focuses invalid/final fields, preserves recovery and waits for readiness.
// Pending SSH setup must show an opt-in prompt, never key entry; Continue is
// navigation-only and Skip advances through the existing explicit SSH decision.
// Threat: stale status responses and double Enter replay account/password/key work.
// Chromium plus intercepted local HTTP exercises production HTML/JS without host
// accounts, real passwords, listeners, services or external provider requests.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { chromium } from 'playwright'

const html = await readFile(new URL('../../../../../../cmd/swarmsetup/onboarding_desktop.html', import.meta.url), 'utf8')
test('prerequisite keyboard progression, response failure and refresh recovery', {timeout:30000}, async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.SWARM_TEST_CHROMIUM?{executablePath:process.env.SWARM_TEST_CHROMIUM}:{})})
 try {
 const page=await browser.newPage({viewport:{width:800,height:900}});page.setDefaultTimeout(4000)
 const actions:string[]=[];let begun=false,passwordDone=false,ssh=false,install=0
 let release: (()=>void)|undefined
 const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message))
 await page.route('http://127.0.0.1/**',async route=>{
  const r=route.request(),url=new URL(r.url())
  if(url.pathname==='/identity'){await route.fulfill({contentType:'text/html',body:'<h1>Identity</h1>'});return}
  if(r.method()==='GET') {await route.fulfill({contentType:url.search?'application/json':'text/html',body:url.search?JSON.stringify({begun,created:begun,retry:passwordDone,ssh_required:ssh,message:'Account retained'}):html});return}
  const input=r.postDataJSON();actions.push(input.action)
  let body:unknown={};let status=200
  if(input.action==='begin'){begun=true;body={created:true}}
  else if(input.action==='password'){assert.equal(input.password,'fixture-only');assert.equal(input.password_confirm,input.password);passwordDone=true;ssh=true;body={ssh_required:true}}
  else if(input.action==='ssh-add'){if(input.public_key==='invalid'){status=422;body={error:'Invalid public key',ssh_required:true,retry:true}}else{assert.equal(input.public_key,'ssh-ed25519 fixture');ssh=false;body={ssh_saved:true,guidance:'SHA256:fixture\nLogin not verified.'}}}
  else if(input.action==='retry'){install++;if(install===1){status=422;body={error:'Readiness deadline reached',retry:true}}else{await new Promise<void>(resolve=>{release=resolve});body={destination:'http://127.0.0.1/identity'}}}
  else throw Error('Unexpected action '+input.action)
  await route.fulfill({status,contentType:'application/json',body:JSON.stringify(body)})
 })
 await page.goto('http://127.0.0.1/setup')
 await page.locator('#username').fill('developer');await page.keyboard.press('Enter')
 await page.getByRole('button',{name:'Set a password',exact:true}).click()
 await page.locator('#password').fill('fixture-only');await page.keyboard.press('Enter')
 assert.equal(await page.locator('#password-confirm').evaluate(el=>el===document.activeElement),true)
 await page.locator('#password-confirm').fill('mismatch');await page.keyboard.press('Enter')
 assert.equal(await page.locator('#error').textContent(),'Enter matching passwords.')
 assert.deepEqual(actions,['begin'])
 await page.locator('#password-confirm').fill('fixture-only');await page.keyboard.press('Enter')
 await page.getByRole('heading',{name:'Add an SSH key?',exact:true}).waitFor()
 assert.equal(await page.locator('#public-key').isVisible(),false)
 assert.equal(await page.locator('#password').inputValue(),'')
 assert.deepEqual(actions,['begin','password'])
 await page.reload()
 await page.getByRole('heading',{name:'Add an SSH key?',exact:true}).waitFor()
 assert.equal(await page.locator('#public-key').isVisible(),false)
 await page.getByRole('button',{name:'Continue to add an SSH key',exact:true}).click()
 await page.locator('#public-key').waitFor({state:'visible'})
 assert.deepEqual(actions,['begin','password'])
 assert.equal(await page.locator('#public-key').evaluate(el=>el===document.activeElement),true)
 await page.locator('#public-key').fill('invalid');await page.keyboard.press('Enter')
 await page.getByText('Invalid public key',{exact:true}).waitFor()
 await page.waitForFunction(()=>!document.querySelector<HTMLButtonElement>('#continue')?.disabled)
 assert.equal(await page.locator('#public-key').isVisible(),true)
 assert.equal(await page.locator('#public-key').inputValue(),'invalid')
 assert.equal(await page.locator('#ssh-choice').isVisible(),false)
 await page.locator('#public-key').fill('ssh-ed25519 fixture');await page.keyboard.press('Enter')
 await page.getByText('Readiness deadline reached',{exact:true}).waitFor()
 assert.deepEqual(actions,['begin','password','ssh-add','ssh-add','retry'])
 assert.match(await page.locator('#ssh-guidance').textContent()||'',/SHA256:fixture/)
 if(process.env.SWARM_TEST_SCREENSHOT)await page.screenshot({path:process.env.SWARM_TEST_SCREENSHOT,fullPage:true})
 await page.reload();await page.getByRole('button',{name:'Retry current stage'}).click()
 await page.waitForFunction(()=>document.querySelector<HTMLButtonElement>('#exit')?.disabled)
 await page.locator('#setup').evaluate(form=>{form.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}));form.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}))})
 assert.equal(await page.locator('h1').textContent(),'Resume Swarm setup.')
 await page.waitForTimeout(25);assert.equal(install,2);release!()
 await page.getByRole('heading',{name:'Identity',exact:true}).waitFor()
 assert.deepEqual(actions,['begin','password','ssh-add','ssh-add','retry','retry']);assert.deepEqual(errors,[])
 }finally{await browser.close()}
})

test('prerequisite skip choices advance and in-flight refresh does not replay', {timeout:20000},async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.SWARM_TEST_CHROMIUM?{executablePath:process.env.SWARM_TEST_CHROMIUM}:{})})
 try{
 const page=await browser.newPage();page.setDefaultTimeout(4000)
 const actions:string[]=[];let state={busy:false,begun:false,created:true,retry:false,ssh_required:false}
 let release:(()=>void)|undefined
 await page.route('http://127.0.0.1/**',async route=>{
  const r=route.request(),url=new URL(r.url())
  if(r.method()==='GET'){await route.fulfill({contentType:url.search?'application/json':'text/html',body:url.search?JSON.stringify(state):html});return}
  const input=r.postDataJSON();actions.push(input.action);let body:unknown={}
  if(input.action==='begin'){state={...state,busy:true};await new Promise<void>(resolve=>{release=resolve});state={...state,busy:false,begun:true};body={created:true}}
  else if(input.action==='skip'){assert.equal(input.password||'','');state={...state,retry:true,ssh_required:true};body={ssh_required:true}}
  else if(input.action==='ssh-skip'){assert.equal(input.public_key||'','');assert.equal(input.password||'','');state={...state,ssh_required:false};body={ssh_saved:true,guidance:'SSH key skipped.'}}
  else if(input.action==='retry'){body={error:'Installation needs retry',retry:true};await route.fulfill({status:422,contentType:'application/json',body:JSON.stringify(body)});return}
  else throw Error(input.action)
  await route.fulfill({contentType:'application/json',body:JSON.stringify(body)})
 })
 await page.goto('http://127.0.0.1/setup');await page.locator('#username').fill('developer');await page.keyboard.press('Enter')
 await page.waitForTimeout(25);assert.equal(state.busy,true)
 await page.reload();assert.equal(await page.locator('#username').isDisabled(),true)
 release!();await page.getByRole('button',{name:'Skip password and continue'}).click()
 await page.getByRole('heading',{name:'Add an SSH key?',exact:true}).waitFor()
 assert.equal(await page.locator('#public-key').isVisible(),false)
 assert.deepEqual(actions,['begin','skip'])
 await page.getByRole('button',{name:'Skip and continue',exact:true}).click()
 await page.getByText('Installation needs retry',{exact:true}).waitFor()
 assert.equal(await page.locator('#public-key').isVisible(),false)
 assert.deepEqual(actions,['begin','skip','ssh-skip','retry'])
 }finally{await browser.close()}
})
