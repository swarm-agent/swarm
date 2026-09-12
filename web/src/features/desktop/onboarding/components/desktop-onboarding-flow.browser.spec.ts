// Requirement: DesktopOnboardingGate advances Identity and provider/workspace
// choices once, preserves prefill and focuses invalid fields. Threat: React
// state batching admits duplicate requests before disabled props render.
// The production component bundled with fake service boundaries in Chromium is
// the narrowest keyboard/click proof; no daemon, provider or Git is contacted.
import test from 'node:test'
import assert from 'node:assert/strict'
import {build} from 'esbuild'
import {chromium} from 'playwright'

const serviceMocks:Record<string,string>={
 '@tanstack/react-router':`export const useNavigate=()=>async()=>{};`,
 '../../../../app/query-client':`export const queryClient={invalidateQueries:async()=>{}};`,
 '../api':`export const patchDesktopOnboarding=async p=>{window.calls.push([p.desktopOnboardingComplete?'finalize':'identity',p]);if(p.desktopOnboardingComplete)window.fixture.needsOnboarding=false;await new Promise(r=>setTimeout(r,80));window.fixture.identity.bootstrapped=true;window.fixture.identity.username=p.username||window.fixture.identity.username;window.fixture.config.swarmName=p.swarmName||window.fixture.config.swarmName;return window.fixture};export const acceptOnboardingProviderCredential=async p=>{window.calls.push(['provider',p]);window.fixture.auth.activeProviders=['fixture'];return {active:true,connection:{connected:true},autoDefaults:{applied:true}}};export const startWorkspaceOnboardingSession=async()=>{};`,
 '../../settings/queries/list-providers':`export const listProviders=async()=>window.fixture.auth.providers;`,
 '../../../workspaces/launcher/state/use-workspace-launcher':`const refresh=async()=>{};const browsePath=async()=>{};export const useWorkspaceLauncher=()=>({workspaces:[],discovered:[{path:'/project',name:'Project'}],refresh,browsePath,saveWorkspace:async p=>{window.calls.push(['workspace',p]);if(!window.fixture.workspaceReady)throw Error('Fixture repository needs review');return {}},openWorkspace:async()=>({resolvedPath:'/project'})});`,
 '../../../workspaces/launcher/services/workspace-theme':`export const applyWorkspaceTheme=()=>{};export const workspaceThemeDefaultId=()=>'';`,
 '../../../workspaces/launcher/services/workspace-route':`export const buildWorkspaceRouteSlugMap=()=>new Map();export const workspaceRouteSlugBase=()=>'';`,
 '../../../workspaces/launcher/services/workspace-format':`export const formatWorkspacePath=p=>p;`,
 '../../../queries/query-options':`export const agentStateQueryOptions=()=>({queryKey:[]});export const draftModelQueryOptions=agentStateQueryOptions;export const modelOptionsQueryOptions=agentStateQueryOptions;export const modelProfilesQueryOptions=agentStateQueryOptions;`,
 '../../../workspaces/launcher/services/workspace-repository':`export class WorkspaceRepositoryPrerequisiteError extends Error {};`,
 '../../session-v3/new-session-flow':`export const applyDesktopV3RoutedStartResponse=()=>{};`,
 '../../../workspaces/launcher/services/repository-review':`export const inspectRepository=async path=>{window.calls.push(['inspect',path]);return {path,state:path==='/project'?'ready':'not_repository',canSetup:true,needsReview:false}};`,
 './repository-review-panel':`export const RepositoryReviewPanel=()=>null;`,
 './workspace-onboarding-assistant':`export const WorkspaceOnboardingAssistant=()=>null;`,
 '../../../workspaces/launcher/components/workspace-folder-tree':`export const WorkspaceFolderTree=()=>null;`,
 '../../settings/auth/components/codex-device-code':`export const CodexDeviceCode=()=>null;`,
 '../../settings/auth/codex-setup-recommendation':`export const codexSetupRecommendation=()=>({});`,
}
for(const [path,name] of [['mutations/start-codex-oauth','startCodexOAuth'],['queries/get-codex-oauth-status','getCodexOAuthStatus'],['mutations/complete-codex-oauth','completeCodexOAuth'],['mutations/upsert-auth-credential','upsertAuthCredential'],['mutations/verify-auth-credential','verifyAuthCredential']])serviceMocks['../../settings/'+path]=`export const ${name}=async()=>{throw Error('Unexpected auth path')};`

test('Desktop identity final-field submit and provider save advance once', {timeout:30000},async()=>{
 const bundle=await build({stdin:{contents:`import React from 'react';import{createRoot}from'react-dom/client';import{DesktopOnboardingGate}from'./src/features/desktop/onboarding/components/desktop-onboarding-gate';window.calls=[];window.fixture={identity:{username:'developer',bootstrapped:false},config:{swarmName:''},auth:{activeProviders:[],credentialCount:0,providers:[{id:'fixture',runReason:'',authMethods:[{id:'api',credentialType:'api'}]}]},workspaceGuidance:{runtime_username:'developer',runtime_uid:'1001',home_path:'/users/developer'},heuristics:{credentialCount:0,agentCount:0},needsOnboarding:true};createRoot(document.getElementById('root')).render(<DesktopOnboardingGate status={window.fixture} onReload={async()=>structuredClone(window.fixture)} onComplete={()=>{window.calls.push(['complete'])}}/>);`,resolveDir:process.cwd(),loader:'tsx'},bundle:true,write:false,format:'iife',jsx:'automatic',plugins:[{name:'fake-service-boundaries',setup(b){b.onResolve({filter:/.*/},args=>Object.hasOwn(serviceMocks,args.path)&&args.importer.endsWith('desktop-onboarding-gate.tsx')?{path:args.path,namespace:'fixture'}:undefined);b.onLoad({filter:/.*/,namespace:'fixture'},args=>({contents:serviceMocks[args.path],loader:'js'}))}}]})
 const browser=await chromium.launch({headless:true,...(process.env.SWARM_TEST_CHROMIUM?{executablePath:process.env.SWARM_TEST_CHROMIUM}:{})})
 try{
 const page=await browser.newPage();page.setDefaultTimeout(4000)
 const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message))
 await page.route('**/*',route=>route.fulfill({contentType:'text/html',body:'<div id="root"></div>'}))
 await page.goto('http://127.0.0.1/');await page.addScriptTag({content:bundle.outputFiles[0].text})
 const name=page.locator('#desktop-onboarding-swarm-name');await name.waitFor()
 assert.equal(await page.locator('#desktop-onboarding-username').inputValue(),'developer')
 assert.equal(await name.evaluate(el=>el===document.activeElement),true)
 await page.keyboard.press('Enter');await page.getByText('Swarm name is required.',{exact:true}).waitFor()
 await page.locator('#desktop-onboarding-username').focus();await page.keyboard.press('Enter')
 assert.equal(await name.evaluate(el=>el===document.activeElement),true)
 await name.fill('Studio');await page.keyboard.press('Enter')
 await page.locator('form').evaluate(form=>{form.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}));form.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}))})
 await page.getByText('Connect your AI provider.',{exact:true}).waitFor()
 const credential=page.locator('input[type="password"]');await credential.fill('fixture-key');await credential.press('Enter')
 await page.getByText('Choose your first workspace.',{exact:true}).waitFor()
 const calls=await page.evaluate(()=> (window as any).calls)
 assert.deepEqual(calls.map((c:any)=>c[0]),['identity','provider'])
 assert.equal(calls[0][1].username,'developer');assert.equal(calls[0][1].swarmName,'Studio')
 // Requirement: home/new are explicit choices; project naming is blank and Back
 // retains its draft without any filesystem or Git request.
 await page.getByRole('button',{name:'Use home folder',exact:true}).waitFor()
 await page.getByRole('button',{name:'Create a new project folder (recommended)',exact:true}).click()
 assert.equal(await page.getByLabel('Project name',{exact:true}).inputValue(),'')
 assert.equal(await page.getByLabel('Parent location',{exact:true}).inputValue(),'/users/developer')
 await page.getByLabel('Project name',{exact:true}).fill('demo')
 await page.getByText('Destination: /users/developer/demo',{exact:true}).waitFor()
 await page.getByRole('button',{name:'Back (keep draft)',exact:true}).click()
 await page.getByRole('button',{name:'Create a new project folder (recommended)',exact:true}).click()
 assert.equal(await page.getByLabel('Project name',{exact:true}).inputValue(),'demo')
 await page.getByRole('button',{name:'Back (keep draft)',exact:true}).click()
 assert.deepEqual((await page.evaluate(()=>(window as any).calls)).map((c:any)=>c[0]),['identity','provider'])
 await page.getByRole('button',{name:/Project/}).click()
 await page.getByText('Fixture repository needs review',{exact:true}).waitFor()
 assert.equal((await page.evaluate(()=> (window as any).calls)).filter((c:any)=>c[0]==='workspace').length,1)
 // Deliberate retry now succeeds through canonical finalization, not merely
 // visibility of the workspace screen; no implicit Git initialization is mocked.
 await page.evaluate(()=>{(window as any).fixture.workspaceReady=true})
 await page.getByRole('button',{name:/Project/}).click()
 await page.waitForFunction(()=>(window as any).calls.some((c:any)=>c[0]==='complete'))
 assert.deepEqual((await page.evaluate(()=>(window as any).calls)).map((c:any)=>c[0]),['identity','provider','inspect','workspace','inspect','workspace','finalize','complete'])
 assert.deepEqual(errors,[])
 }finally{await browser.close()}
})
