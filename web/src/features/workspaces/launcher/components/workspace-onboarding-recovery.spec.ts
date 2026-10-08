import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'
import { chromium } from 'playwright'

const webDir = existsSync(path.resolve(process.cwd(), 'src/features'))
  ? process.cwd()
  : existsSync(path.resolve(process.cwd(), 'web/src/features'))
    ? path.resolve(process.cwd(), 'web')
    : path.resolve(fileURLToPath(new URL('.', import.meta.url)), '../../../../..')

// Requirement: account onboarding must no longer perform workspace setup;
// Desktop onboarding follows the canonical 4-step lifecycle:
// Step 1: Identity -> Step 2: Provider -> Step 3: Project -> Step 4: Workspaces.
// WorkspaceHomePage and WorkspaceFolderTree retain selected folders on failures.
// Threat: duplicate creation, implicit Git mutation, lost retry intent or premature
// completion. Real React handlers in a hermetic browser with a fake launcher are
// the narrowest layer proving interaction ordering; repository safety is separately
// owned by workspace.Service.InspectRepositoryForPrincipal/SetupRepositoryForPrincipal.
// No daemon, credentials, provider, remote host or filesystem workspace is used.
test('workspace onboarding interaction and recovery matrix', { timeout: 60000 }, async (t) => {
  const bundle = await build({
    stdin: { contents: `
      import React from 'react'; import { createRoot } from 'react-dom/client';
      import { DesktopOnboardingGate } from './src/features/desktop/onboarding/components/desktop-onboarding-gate';
      import { WorkspaceHomePage } from './src/features/workspaces/pages/workspace-home-page';
      const w = window;
      w.calls = []; w.failSave = false; w.failSetup = false; w.failFinish = false;
      w.repository = {state:'not_repository',path:'/workspace/new',canSetup:true,message:'Initialize this empty folder',repositoryRoot:'',headCommit:'',needsReview:false};
      const status = {identity:{bootstrapped:true,username:'tester'},config:{swarmName:'Test'},auth:{providers:[],activeProviders:[],credentialCount:0},heuristics:{credentialCount:0,agentCount:0},needsOnboarding:false};
      w.status = status;
      const root = createRoot(document.getElementById('root'));
      root.render(w.surface === 'home' ? <WorkspaceHomePage/> : <DesktopOnboardingGate status={status} onReload={async()=>{if(w.failFinish){await new Promise(r=>setTimeout(r,350));throw Error('Finalization failed')}return status}} onComplete={()=>w.calls.push(['complete'])}/>);
    `, resolveDir: webDir, loader: 'tsx' },
    bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic',
    plugins: [{ name: 'isolated-workspace-api', setup(b) {
      b.onResolve({ filter: /use-workspace-launcher$|onboarding\/api$|^\.\.\/api$|query-client$|queries\/query-options$|workspace-theme$|new-session-flow$|workspace-onboarding-assistant$|list-providers$|write-api$/ }, (args) => ({ path: args.path, namespace: 'fixture' }))
      b.onLoad({ filter: /.*/, namespace: 'fixture' }, (args) => {
        if (args.path.endsWith('use-workspace-launcher')) return { contents: `
          import {WorkspaceRepositoryPrerequisiteError} from '${webDir}/src/features/workspaces/launcher/services/workspace-repository';
          export function useWorkspaceLauncher(){const w=window;return {
            workspaces:[],discovered:[],loading:false,browserLoading:false,browserError:null,
            browser:{resolvedPath:'/workspace',homePath:'/workspace',parentPath:'/',entries:[]},
            browsePath:async()=>{},refresh:async()=>{},
            createFolder:async(parent,name)=>{w.calls.push(['create',parent,name]);if(w.failCreate)throw Error('Folder creation failed');return parent+'/'+name},
            saveWorkspace:async(input)=>{w.calls.push(['save',input.path]);if(w.failSave)throw Error('Registration failed');if(w.repository.state!=='ready')throw new WorkspaceRepositoryPrerequisiteError({...w.repository,path:input.path})},
            setupWorkspaceRepository:async(path,expected)=>{w.calls.push(['setup',path,expected]);if(w.failSetup)throw Error('Setup failed');w.repository={...w.repository,state:'ready',canSetup:false,headCommit:'abc'};return {...w.repository,path}},
            openWorkspace:async(path)=>{w.calls.push(['open',path]);return {resolvedPath:path,workspaceName:'new'}},
          }}
        `, resolveDir: webDir }
        if (args.path.endsWith('workspace-theme')) return { contents: `export const WORKSPACE_THEME_OPTIONS=[];export const applyWorkspaceTheme=()=>{};export const workspaceThemeDefaultId=()=>'';export const createWorkspaceThemeStyle=()=>({});` }
        if (args.path.endsWith('query-client')) return { contents: `export const queryClient={invalidateQueries:async()=>{}};` }
        if (args.path.endsWith('query-options')) return { contents: `export const agentStateQueryOptions=()=>({});export const draftModelQueryOptions=agentStateQueryOptions,modelOptionsQueryOptions=agentStateQueryOptions,modelProfilesQueryOptions=agentStateQueryOptions;` }
        if (args.path.endsWith('new-session-flow')) return { contents: `export const applyDesktopV3RoutedStartResponse=()=>{};export const desktopV3RoutedWorkspaceAuthority=()=>({});` }
        if (args.path.endsWith('workspace-onboarding-assistant')) return { contents: `export const WorkspaceOnboardingAssistant=()=>null;` }
        if (args.path.endsWith('list-providers')) return { contents: `export const listProviders=async()=>[];` }
        if (args.path.endsWith('write-api')) return { contents: `export const postDesktopV3BackgroundRouterSessionStart=async()=>{throw Error('Unexpected assistant')};` }
        return { contents: `export const patchDesktopOnboarding=async()=>window.status;export const acceptOnboardingProviderCredential=async()=>{};export const startWorkspaceOnboardingSession=async()=>{throw Error('Unexpected assistant')};` }
      })
      b.onResolve({ filter: /^@tanstack\/react-router$/ }, () => ({ path: 'router', namespace: 'router-fixture' }))
      b.onLoad({ filter: /.*/, namespace: 'router-fixture' }, () => ({ contents: `import React from 'react';export const useNavigate=()=>async()=>{};export const Link=({children,...props})=>React.createElement('a',props,children);`, resolveDir: webDir }))
    } }],
  })
  const chromiumPath = process.env.SWARM_TEST_CHROMIUM_PATH || process.env.SWARM_TEST_CHROMIUM
  const browser = await chromium.launch({ headless: true, ...(chromiumPath ? { executablePath: chromiumPath } : { channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' }) })
  t.after(() => browser.close())
  async function mount(surface = 'onboarding', mobile = false) {
    const page = await browser.newPage({ viewport: { width: mobile ? 390 : 1280, height: 900 } })
    page.setDefaultTimeout(5000)
    await page.route('**/*', route => route.abort())
    await page.setContent('<div id="root"></div>')
    await page.evaluate(s => { (window as any).surface = s }, surface)
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    if (surface === 'home' && mobile) await page.getByRole('button', { name: 'Add from Explorer', exact: true }).click()
    return page
  }
  async function create(page: Awaited<ReturnType<typeof mount>>) {
    page.once('dialog', dialog => dialog.accept('new'))
    await page.getByRole('button', { name: 'Create and add workspace', exact: true }).click()
  }
  async function calls(page: Awaited<ReturnType<typeof mount>>) { return page.evaluate(() => (window as any).calls) }

  await t.test('account completion failure is recoverable without any workspace mutation', async () => {
    const page = await mount()
    try {
      await page.evaluate(() => { (window as any).failFinish = true })
      await page.getByRole('button', { name: 'Continue without provider', exact: true }).click()
      await page.getByRole('button', { name: 'Continue to projects', exact: true }).click()
      await page.getByText('Finalization failed', { exact: true }).waitFor()
      assert.deepEqual(await calls(page), [])
      await page.evaluate(() => { (window as any).failFinish = false })
      await page.getByRole('button', { name: 'Continue without provider', exact: true }).click()
      await page.getByRole('button', { name: 'Continue to projects', exact: true }).click()
      await page.waitForFunction(() => (window as any).calls.some((c: string[]) => c[0] === 'complete'))
      assert.deepEqual(await calls(page), [['complete']])
      assert.equal(await page.getByRole('button', { name: 'Add from Explorer', exact: true }).count(), 0)
    } finally { await page.close() }
  })
  await t.test('editor folder creation continues with the returned child path', async () => {
    const page = await mount('home')
    try {
      await page.getByRole('button', { name: 'Add folder as a new workspace', exact: true }).click()
      await page.getByRole('button', { name: 'Browse', exact: true }).click()
      const editor = page.getByRole('dialog', { name: 'Create workspace', exact: true })
      page.once('dialog', d => d.accept('new'))
      await editor.getByRole('button', { name: 'Create and add workspace', exact: true }).click()
      await editor.locator('input[value="/workspace/new"]').waitFor()
      await editor.getByRole('button', { name: 'Initialize Git repository and add workspace', exact: true }).waitFor()
      assert.deepEqual(await calls(page), [['save','/workspace'],['create','/workspace','new'],['save','/workspace/new']])
    } finally { await page.close() }
  })
  for (const mobile of [false,true]) {
    await t.test('home create/add and setup recovery '+(mobile?'mobile':'desktop'), async () => {
      const page = await mount('home', mobile)
      try {
        await create(page)
        const initialize=page.getByRole('button',{name:'Initialize Git repository and add workspace',exact:true})
        await initialize.waitFor()
        assert.equal(await page.locator('input[value="/workspace/new"]').count(),1)
        await page.evaluate(()=>{(window as any).failSave=true})
        page.once('dialog',d=>d.accept())
        await initialize.click()
        await page.getByText('Registration failed',{exact:true}).waitFor()
        assert.equal(await page.locator('input[value="/workspace/new"]').count(),1)
        await page.evaluate(()=>{(window as any).failSave=false})
        await page.getByRole('button',{name:'Create workspace',exact:true}).click()
        await page.getByRole('dialog',{name:'Create workspace',exact:true}).waitFor({state:'hidden'})
        assert.equal((await calls(page)).filter((c:string[])=>c[0]==='create').length,1)
      } finally {await page.close()}
    })
  }
})
