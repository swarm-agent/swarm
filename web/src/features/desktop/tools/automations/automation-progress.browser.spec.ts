import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: both views consume the same V3 progress projection and show unknown
// actual timing honestly. Threat: paused forecasts and manual/retry history become
// scheduled successes. A real component/browser fixture proves rendered output and
// day-boundary refresh, without provider, daemon or network dependencies.
test('detailed and compact progress distinguish forecasts, pause and day rollover', { timeout: 30000 }, async () => {
  const fixture = `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {AutomationProgressView} from './src/features/desktop/tools/automations/automation-progress';
    import {desktopAutomations} from './src/features/desktop/runtime/desktop-automations';
    import {dispatchDesktopV3Cache} from './src/features/desktop/state/desktop-v3-cache-store';
    import {automationPageKey} from './src/features/desktop/state/desktop-automation-state';
    window.reads=0; const timezone=Intl.DateTimeFormat().resolvedOptions().timeZone;
    const input={workspace_id:'workspace',id:'automation',action:'progress',display_timezone:timezone};
    const key=automationPageKey(input);
    desktopAutomations.acquire=()=>({ready:Promise.resolve(),release:()=>{}});
    desktopAutomations.refresh=async()=>{window.reads++};
    window.show=(reason,partial=false,end=Date.now()+60000)=>{
      dispatchDesktopV3Cache({type:'automation.begin',key,input,requestId:'read'});
      dispatchDesktopV3Cache({type:'automation.finish',key,requestId:'read',generation:0,data:{progress:{automation_id:'automation',definition_revision:1,schedule:{kind:'cron',expression:'0 18 * * *',timezone:'UTC'},display_timezone:timezone,day_start:Date.now()-10000,day_end:end,as_of:Date.now(),history_complete:!partial,forecast_complete:true,upcoming_complete:true,planned_slots:[{scheduled_at:Date.now()+1000,definition_revision:1,forecast:true}],upcoming_slots:[],counts:{completed:0,failed:1},manual_counts:{completed:3},unknown_trigger_count:0,no_next_reason:reason,occurrences:[]}}});
    };
    window.show('paused');
    createRoot(document.getElementById('root')).render(<><AutomationProgressView workspaceId="workspace" id="automation"/><div id="compact"><AutomationProgressView workspaceId="workspace" id="automation" compact/></div></>);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://automation.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('heading', { name: 'Worker', exact: true }).waitFor()
    assert.match(await page.locator('#compact').innerText(), /0 completed.*1 failed.*paused/)
    assert.match(await page.locator('section').innerText(), /forecasts, not a run quota/)
    assert.match(await page.locator('section').innerText(), /Actual start\/completion times.*unavailable/)
    assert.equal(await page.getByText('Upcoming forecast times', { exact: true }).count(), 0)
    await page.evaluate(() => (window as any).show('expired', true))
    await page.waitForFunction(() => document.querySelector('#compact')?.textContent?.includes('at least 0 completed'))
    assert.match(await page.locator('#compact').innerText(), /expired/)
    await page.evaluate(() => (window as any).show('paused', false, Date.now()+100))
    await page.waitForFunction(() => (window as any).reads > 0)
    assert.match(await page.locator('#compact').innerText(), /stale/)
  } finally { await browser.close() }
})
