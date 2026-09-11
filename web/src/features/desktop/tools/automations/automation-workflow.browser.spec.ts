import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: AutomationConversations creates only on a user gesture, preserves
// idempotency across uncertain failures, transfers editable drafts without sending,
// and ignores completion after a workspace unmount. AutomationProposalCard must
// keep acceptance tied to exact request bytes and require a separate enable click.
// Threat: duplicate chats, cross-workspace selection and stale/implicit grants.
// Real components with controlled boundary promises are the narrowest browser
// layer proving these interactions; not daemon, websocket or visual-quality proof.
test('first-use chat, workspace isolation and exact proposal acceptance', { timeout: 30000 }, async () => {
  const fixture = `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {AutomationConversations} from './src/features/desktop/tools/automations/automation-conversations';
    import {AutomationProposalCard} from './src/features/desktop/tools/automations/automation-proposal-card';
    window.creates=[]; window.selections=[]; window.writes=[]; window.chat=null;
    const root=createRoot(document.getElementById('root'));
    window.conversations=(workspace='one',selected='')=>root.render(<AutomationConversations key={workspace} workspaceId={workspace} workspacePath={workspace} selected={selected} onSelect={id=>window.selections.push(id)}/>);
    window.proposal=(action='approve',revision=1)=>root.render(<AutomationProposalCard payload={{result:{status:'requires_user_approval',applied:false,proposal:{method:'POST',path:action==='approve'?'/v3/automations/approve':'/v3/automations',body:{action,workspace_id:'one',id:'automation',mutation_id:action+revision,expected_revision:revision,...(action==='approve'?{policy_sha256:'a'.repeat(64)}:{})}}}}}/>);
    window.conversations();`
  const bundle = await build({
    stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false,
    format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent',
    plugins: [{ name: 'controlled-boundaries', setup(b) {
      b.onResolve({ filter: /(?:desktop-automation-conversations|desktop-automations|automation-chat|automation-session)$/ }, args => ({ path: args.path.split('/').pop()!, namespace: 'fixture' }))
      b.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ loader: 'tsx', resolveDir: process.cwd(), contents: args.path === 'desktop-automation-conversations' ? `
        export const isAutomationManagementSession=()=>true;
        export const loadAutomationConversations=async()=>({sessions_by_id:{},session_order:[],pagination:{}});
        export const createAutomationConversation=(workspace,id)=>new Promise((resolve,reject)=>{window.creates.push({workspace,id});window.finishCreate=resolve;window.failCreate=()=>reject(new Error('uncertain create'));});
      ` : args.path === 'desktop-automations' ? `
        export const desktopAutomations={mutate:input=>new Promise((resolve,reject)=>{window.writes.push(input);window.finishWrite=resolve;window.failWrite=()=>reject(new Error('stale proposal'));})};
      ` : args.path === 'automation-chat' ? `
        export function AutomationChat(props){window.chat=props;return <div>Canonical chat fixture</div>}
      ` : 'export function AutomationSessionPanel(){return null}' }))
    } }],
  })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://automation.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByLabel('What should this automation do?').fill('Read-only review')
    assert.deepEqual(await page.evaluate(() => (window as any).creates), [])
    await page.getByRole('button', { name: 'Start chat with this draft' }).click()
    assert.equal(await page.getByRole('button', { name: 'Starting…' }).isDisabled(), true)
    await page.evaluate(() => (window as any).failCreate())
    await page.getByRole('alert').filter({ hasText: 'uncertain create' }).waitFor()
    await page.getByRole('button', { name: 'Start chat with this draft' }).click()
    const creates = await page.evaluate(() => (window as any).creates)
    assert.equal(creates.length, 2)
    assert.equal(creates[0].id, creates[1].id)
    await page.evaluate(() => (window as any).finishCreate({ id: 'chat-one' }))
    await page.waitForFunction(() => (window as any).selections.length === 1)
    await page.evaluate(() => (window as any).conversations('one', 'chat-one'))
    await page.getByText('Canonical chat fixture').waitFor()
    assert.deepEqual(await page.evaluate(() => (window as any).chat.composerDraftRequest), { sessionId: 'chat-one', id: 1, draft: 'Read-only review', append: true })
    await page.getByRole('button', { name: 'New automation chat' }).click()
    await page.evaluate(() => (window as any).conversations('two'))
    await page.getByLabel('What should this automation do?').waitFor()
    await page.evaluate(() => (window as any).finishCreate({ id: 'obsolete-chat' }))
    assert.deepEqual(await page.evaluate(() => (window as any).selections), ['chat-one'])
    assert.equal(await page.getByLabel('What should this automation do?').inputValue(), '')

    await page.evaluate(() => (window as any).proposal())
    await page.getByRole('button', { name: 'Accept reviewed request', exact: true }).click()
    assert.equal(await page.getByRole('button', { name: 'Submitting…' }).isDisabled(), true)
    await page.evaluate(() => (window as any).failWrite())
    await page.getByRole('status').filter({ hasText: 'stale proposal' }).waitFor()
    assert.equal(await page.getByRole('button', { name: 'Accept reviewed request', exact: true }).isEnabled(), true)
    await page.getByRole('button', { name: 'Accept reviewed request', exact: true }).click()
    await page.evaluate(() => (window as any).finishWrite({ enable_proposal: { method: 'POST', path: '/v3/automations', body: { action: 'enable', workspace_id: 'one', id: 'automation', mutation_id: 'enable', expected_revision: 2 } } }))
    await page.getByRole('heading', { name: 'Review automation enable' }).waitFor()
    assert.equal(await page.evaluate(() => (window as any).writes.length), 2)
    await page.getByRole('button', { name: 'Accept reviewed request', exact: true }).click()
    assert.equal(await page.evaluate(() => (window as any).writes[2].action), 'enable')
    await page.evaluate(() => (window as any).finishWrite({ fresh: true }))
    await page.waitForFunction(() => !document.body.textContent?.includes('Submitting…'))
    await page.evaluate(() => (window as any).proposal('pause', 3))
    await page.getByRole('heading', { name: 'Review automation pause' }).waitFor()
    assert.equal(await page.getByRole('button', { name: 'Accept reviewed request', exact: true }).isEnabled(), true)
  } finally { await browser.close() }
})
