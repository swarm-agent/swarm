import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: the production toolbar must keep Select all scoped to visible rows,
// make selection non-mutating, disable a zero-eligible/pending batch, and invoke
// the real serial controller once. Browser interaction is the narrowest layer
// proving the accessible control contract; transport fixtures never touch Git.
test('Select all then Integrate selected dispatches only eligible visible rows', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState,useSyncExternalStore} from 'react'; import {createRoot} from 'react-dom/client';
    import {TaskListToolbar} from './src/features/desktop/orchestrate/task-list-toolbar';
    import {createTaskIntegrationBatchController,integrationSkipReason} from './src/features/desktop/orchestrate/task-integration-batch';
    import {createTaskIntegrationController} from './src/features/desktop/orchestrate/task-integration-operation';
    const batch=createTaskIntegrationBatchController(), operations=createTaskIntegrationController();
    const project={id:'browser-project',name:'Project'};
    const make=(id,agentType='coder')=>({id,title:id,agentType,status:'needs_review',subtasks:[],workspaceTarget:'local',elapsed:'',sessionId:'session-'+id,sourceWorkspacePath:'/fixture/repo',worktreeBranch:'agent/'+id,baseBranch:'dev'});
    const visible=[make('code'),make('media','image')], hidden=make('hidden'); window.calls=[];
    function App(){const [selected,setSelected]=useState([]); const snapshot=useSyncExternalStore(batch.subscribe,batch.getSnapshot,batch.getSnapshot);const state=snapshot.get(project.id);
      return <><TaskListToolbar search="" onSearch={()=>{}} source="all" onSource={()=>{}} status="all" onStatus={()=>{}} counts={{all:2,running:0,needs_review:2,queued:0,completed:0}} total={2} selected={selected.length} busy={!!state?.pending}
        integrationEligible={selected.filter(row=>!integrationSkipReason(project.id,row)).length}
        onSelectAll={()=>setSelected(visible)} onClear={()=>setSelected([])} onArchive={()=>{}} onDelete={()=>{}} onArchived={()=>{}}
        onIntegrate={()=>batch.run(project,selected,id=>visible.find(row=>row.id===id),(row,token)=>operations.run(project,row,()=>{window.calls.push(row.id);return new Promise(resolve=>{window.finish=()=>resolve({status:'integrated',task:{id:row.id,session_id:row.sessionId,is_integrated:true}})})},()=>{},token),()=>{})}/>
        <button onClick={()=>setSelected([visible[1]])}>Select media only</button>
        <div role="status">{state?.entries.map(entry=>entry.id+':'+entry.status).join(',')}</div></>;
    }createRoot(document.getElementById('root')).render(<App/>);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    await page.setContent('<div id="root"></div>')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('button', { name: 'Select all', exact: true }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).calls), [])
    const integrate = page.getByRole('button', { name: /^Integrate selected/ })
    assert.equal(await integrate.isEnabled(), true)
    await integrate.click()
    assert.equal(await integrate.isDisabled(), true)
    assert.equal(await page.getByRole('button', { name: 'Archive', exact: true }).isDisabled(), true)
    assert.deepEqual(await page.evaluate(() => (window as any).calls), ['code'])
    await page.evaluate(() => (window as any).finish())
    await page.getByText('code:integrated,media:skipped', { exact: true }).waitFor()
    await page.getByRole('button', { name: 'Select media only' }).click()
    assert.equal(await page.getByRole('button', { name: 'Integrate selected (0)', exact: true }).isDisabled(), true)
    assert.deepEqual(await page.evaluate(() => (window as any).calls), ['code'])
  } finally { await browser.close() }
})
