import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: the actual collapsed MinimalTaskCard and integration controller must
// issue recovery, retain exact attempt identity, display conflicts and consume
// canonical refreshed evidence without inventing original integration. Browser
// interaction is the narrowest layer proving clickable JSX/request/result flow;
// the API/Git tests establish backend delivery, not this transport fixture.
test('rewritten card recovers through request/result and event refresh', { timeout: 60000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState,useSyncExternalStore} from 'react'; import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard} from './src/features/desktop/orchestrate/OrchestrateView';
    import {createTaskIntegrationController,taskIntegrationKey,taskIntegrationRequest} from './src/features/desktop/orchestrate/task-integration-operation';
    const controller=createTaskIntegrationController(); const project={id:'project',name:'Project'};
    const task={id:'task',title:'Recover task',status:'needs_review',agentType:'coder',outcomeType:'code_pr',subtasks:[],workspaceTarget:'local',elapsed:'0s',
      revision:1,activeAttemptId:'initial',sessionId:'source',sourceWorkspaceId:'workspace',sourceWorkspaceGeneration:1,sourceWorkspacePath:'/repo',
      baseCommit:'base',worktreeBranch:'agent/task',baseBranch:'dev',gitStatus:'diverged',unintegratedCommits:400,
      deliveryAssessment:{task_id:'task',session_id:'source',attempt_id:'initial',task_revision:1,workspace_id:'workspace',workspace_generation:1,
        base_oid:'base',source_oid:'source-tip',target_oid:'target-tip',source_branch:'agent/task',target_branch:'dev',state:'history_rewritten',freshness:'observed',candidate_commits:1,allowed_actions:['recover_integrate']}};
    const client=new QueryClient({defaultOptions:{queries:{retry:false}}});window.refreshes=0;window.calls=0;
    function App(){const [row,setRow]=useState(task);window.taskEvent=setRow;
      useSyncExternalStore(controller.subscribe,controller.getSnapshot,controller.getSnapshot);
      const operation=controller.get(taskIntegrationKey(project.id,row));
      const submit=()=>controller.run(project,row,async()=>{window.calls++;
        const request=taskIntegrationRequest(project.id,row);
        const response=await fetch(request.url,{method:'POST',body:JSON.stringify(request.body)});
        const result=await response.json();if(!response.ok)throw new Error(result.error);return result;
      },()=>{window.refreshes++});
      return <QueryClientProvider client={client}><MinimalTaskCard task={row} onSelect={()=>{}} onIntegrate={submit} integrationOperation={operation}
        integrationRecovery={operation.phase==='error'?<div role="alert">{operation.failure.error}<button onClick={()=>{window.repair=true}}>Launch repair session</button></div>:null}/></QueryClientProvider>;
    }
    window.original=task;createRoot(document.getElementById('root')).render(<App/>);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    let attempt = 0
    const bodies: any[] = []
    await page.route('**/*', async route => {
      if (!route.request().url().endsWith('/recover-integrate')) return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
      bodies.push(route.request().postDataJSON())
      attempt++
      if (attempt === 1) return route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'Task delta conflict: feature.txt; Launch repair session' }) })
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ status: 'recovered', task: { id: 'task', session_id: 'source', active_attempt_id: 'initial', is_integrated: false,
        integration: { state: 'recovered', source_head: 'source-tip', recovery_base: 'base', recovered_head: 'recovered-tip', resulting_target_head: 'recovered-tip' } } }) })
    })
    await page.goto('https://recovery.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const recover = page.getByRole('button', { name: 'Recover & integrate', exact: true })
    await recover.click()
    await page.getByRole('alert').waitFor()
    assert.match(await page.getByRole('alert').innerText(), /feature.txt/)
    assert.equal(await page.getByRole('button', { name: 'Launch repair session', exact: true }).isEnabled(), true)
    assert.equal(await page.evaluate(() => (window as any).refreshes), 1)
    assert.doesNotMatch(await page.getByTestId('task-card-summary').innerText(), /400 unintegrated/)
    await recover.click()
    await page.waitForFunction(() => (window as any).refreshes === 2)
    assert.deepEqual(bodies, [1, 2].map(() => ({ session_id: 'source', source_branch: 'agent/task', target_branch: 'dev', revision: 1, attempt_id: 'initial', source_head: 'source-tip', target_head: 'target-tip' })))
    // A canonical task-event refresh replaces the observation, not the identity.
    await page.evaluate(() => { const w = window as any; w.taskEvent({ ...w.original, revision: 4, status: 'completed', gitStatus: 'clean', unintegratedCommits: 0,
      integration: { state: 'recovered', session_id: 'source', attempt_id: 'initial' },
      deliveryAssessment: { ...w.original.deliveryAssessment, task_revision: 4, state: 'recovered', allowed_actions: [], candidate_commits: 0, target_oid: 'recovered-tip' } }) })
    const delivered = page.getByRole('button', { name: 'Task delta delivered', exact: true })
    await delivered.waitFor()
    assert.equal(await delivered.isDisabled(), true)
    assert.match(await page.getByTestId('task-card-summary').innerText(), /Task delta recovered/)
    assert.equal(await page.evaluate(() => (window as any).calls), 2)
  } finally { await browser.close() }
})
