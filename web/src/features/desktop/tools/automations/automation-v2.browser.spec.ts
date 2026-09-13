import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { createRequire } from 'node:module'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
const requireCSS = createRequire(import.meta.resolve('@tailwindcss/vite'))

// Requirement: the real pending card edits a complete exact V2 review, never
// ordinary plan approval or V1 save/approve/enable. Threat: stale/invalid edits,
// double acceptance and rejected proposals creating records. The actual React
// card, runtime reducer and API run in Chromium against a deterministic HTTP
// boundary; this is UI interaction proof, not server authorization/live proof.
test('V2 card edits exact snapshots and reconciles accepted discovery without reload', { timeout: 45000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {DesktopInlinePlanReviewCard} from './src/features/desktop/chat/components/desktop-inline-plan-review-card';
    import {AutomationV2Workspace} from './src/features/desktop/tools/automations/automation-v2-workspace';
    import {desktopAutomationV2} from './src/features/desktop/runtime/desktop-automation-v2';
    import {useDesktopV3CacheSelector} from './src/features/desktop/state/desktop-v3-cache-store';
    import {selectAutomationV2Identity} from './src/features/desktop/state/desktop-automation-v2-state';
    window.base={proposal_id:'proposal',revision:1,digest:'a'.repeat(64),account_id:'account',workspace_id:'workspace',session_id:'author',document:{title:'Harmless recurring check',info:{goal:'Inspect the fixture only'},checkpoints:[{id:'check',title:'Inspect fixture',status:'pending',order:1,tasks:['Read fixture state'],acceptance_criteria:['No production changes']}],automation_v2:{schema_version:2,schedule:{kind:'interval',interval_seconds:3600},expiration:{kind:'indefinite'},missed:'skip',overlap:'serialize',activate_on_accept:true}}};
    window.proposal=structuredClone(window.base); window.accepted=null; window.writes=[]; window.rejects=0; window.deny=false;
    function Identity(){const value=useDesktopV3CacheSelector(s=>selectAutomationV2Identity(s,'author'));return <aside aria-label='Identity'>{value||'No accepted automation'}</aside>}
    const root=createRoot(document.getElementById('root')); window.render=()=>root.render(<><DesktopInlinePlanReviewCard permission={{id:'permission_'+window.proposal.proposal_id,sessionId:'author',toolName:'plan_manage',requirement:'automation_v2_acceptance',status:'pending',toolArguments:JSON.stringify({review_kind:'automation_v2',document:window.proposal.document,automation_review:{proposal_id:window.proposal.proposal_id,revision:window.proposal.revision,digest:window.proposal.digest},scope:{workspace_id:'workspace',account_id:'account'}})}} parentSessionId='author' pendingPosition={1} pendingCount={1} onResolve={async(p,action)=>{if(action!=='deny')throw Error('ordinary approval forbidden');window.rejects++}}/><Identity/><AutomationV2Workspace workspaceId='workspace' workspacePath='/fixture' workspaceName='Fixture'/></>);window.render();window.runtime=desktopAutomationV2;
  ` }, bundle: true, write: false, outdir: 'out', platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent', plugins: [{ name: 'isolated-chat', setup(b) {
    b.onResolve({ filter: /automation-conversations$/ }, args => ({ path: args.path, namespace: 'fixture' }))
    b.onLoad({ filter: /.*/, namespace: 'fixture' }, () => ({ loader: 'tsx', contents: 'export function AutomationConversations(){return null}' }))
  } }] })
  const { compile } = requireCSS('@tailwindcss/node')
  const { Scanner } = requireCSS('@tailwindcss/oxide')
  const sources = ['src/features/desktop/tools/automations/automation-v2-plan-review.tsx', 'src/features/desktop/tools/automations/automation-v2-workspace.tsx', 'src/features/desktop/chat/components/structured-plan-document.tsx', 'src/components/ui/button.tsx']
  const scanner = new Scanner({ sources: sources.map(pattern => ({ base: process.cwd(), pattern, negated: false })) })
  const compiler = await compile(await readFile('src/theme.css', 'utf8'), { base: path.resolve('src'), onDependency() {} })
  const css = compiler.build(scanner.scan())
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 1100, height: 1000 } })
    page.setDefaultTimeout(7000)
    const errors: string[] = []; page.on('pageerror', e => { errors.push(e.message); console.error(e.message) })
    await page.route('**/*', async route => {
      const url = new URL(route.request().url()), body = route.request().postDataJSON()
      if (url.pathname === '/') { await route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }); return }
      const state = await page.evaluate(() => ({ proposal: (window as any).proposal, accepted: (window as any).accepted, deny: (window as any).deny })).catch(() => ({} as any))
      let data: any = {}
      if (url.pathname === '/v1/auth/desktop/session') data = { ok: true, user_id: 'user', account_scope_id: 'account' }
      else if (url.pathname.endsWith('/v3/automations/v2/proposal')) {
        await page.evaluate(b => (window as any).writes.push(b), body)
        if (state.deny) { await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'Review conflict; refresh current proposal' }) }); return }
        const proposal = { ...state.proposal, ...body.review, revision: body.review.revision + 1, digest: 'b'.repeat(64), document: body.document }
        await page.evaluate(p => { (window as any).proposal = p }, proposal); data = { proposal }
      } else if (url.pathname.endsWith('/v3/automations/v2/accept')) {
        assert.deepEqual(body.review, { proposal_id: state.proposal.proposal_id, revision: state.proposal.revision, digest: state.proposal.digest })
        const record = { ...state.proposal, automation_id: 'automation', generation: 1, enabled: true, cancelled: false, accepted_at: Date.now(), authorization: state.proposal.document.automation_v2.expiration }
        await page.evaluate(({ b, r }) => { (window as any).writes.push(b); (window as any).accepted = r }, { b: body, r: record }); data = { record }
      } else if (url.pathname.endsWith('/v3/automations/v2/control')) {
        assert.equal(body.generation, state.accepted.generation)
        const record = { ...state.accepted, generation: state.accepted.generation + 1, enabled: body.action === 'resume' }
        await page.evaluate(({b,r}) => { (window as any).writes.push(b); (window as any).accepted=r }, {b:body,r:record}); data={record}
      } else if (url.pathname.endsWith('/v3/automations/v2/progress')) data = { record: state.accepted, observed_at: Date.now(), timezone: url.searchParams.get('timezone'), forecast: [], forecast_is_admission: false, no_next_reason: 'paused', complete: true, occurrences: [{ id: 'slot', state: 'admitted', accepted: state.accepted, detail: 'Waiting for dispatch', due_at: Date.now(), session_id: 'occurrence' }] }
      else if (url.pathname.endsWith('/v3/automations/v2/review')) data = { proposal: state.proposal }
      else if (url.pathname.endsWith('/v3/automations/v2')) data = { records: state.accepted ? [state.accepted] : [] }
      else if (url.pathname.endsWith('/v3/sync/hydrate')) data = { selector: { kind: 'session_ids', session_ids: ['author'] }, scope_id: 'fixture', sync_scope: 'fixture', snapshot_endpoint_cursor: 'opaque', session_order: [], sessions_by_id: {}, projections_by_session: {} }
      else { await route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }); return }
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify(data) })
    })
    await page.goto('https://automation.test/')
    // The production shell owns scrolling; this component fixture has no shell.
    // Give its root document flow so full-page captures cannot clip tall cards.
    await page.addStyleTag({ content: css + '\nhtml, body, #root { position: static; height: auto; min-height: 100%; overflow: visible; }' })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('button', { name: 'Accept automation', exact: true }).waitFor()
    assert.equal(await page.getByLabel('Expiration', { exact: true }).inputValue(), 'indefinite')
    // Unsupported cadence cannot be silently approximated or submitted.
    await page.getByLabel('Timing', { exact: true }).selectOption('cron')
    await page.getByLabel('Frequency / times (cron)').fill('0,30 * * * *')
    assert.equal(await page.getByRole('button', { name: 'Update reviewed snapshot' }).isDisabled(), true)
    assert.equal(await page.evaluate(() => (window as any).writes.length), 0)
    await page.getByLabel('Timing', { exact: true }).selectOption('interval')
    await page.getByLabel('Every (seconds)').fill('900')
    assert.equal(await page.getByRole('button', { name: 'Accept automation', exact: true }).count(), 0)
    await page.getByLabel('Expiration', { exact: true }).selectOption('at')
    await page.getByLabel('Expiration (UTC)').fill('2030-04-03T12:30:00.123')
    await page.evaluate(() => { (window as any).deny = true })
    await page.getByRole('button', { name: 'Update reviewed snapshot' }).click()
    await page.getByRole('alert').filter({ hasText: 'Review conflict' }).waitFor()
    assert.equal(await page.getByLabel('Every (seconds)').inputValue(), '900')
    assert.equal(await page.evaluate(() => (window as any).accepted), null)
    await page.evaluate(() => { (window as any).deny = false })
    await page.getByRole('button', { name: 'Update reviewed snapshot' }).click()
    await page.getByRole('button', { name: 'Accept automation', exact: true }).waitFor().catch(async error => { console.error((await page.locator('body').innerText()).slice(-2500)); throw error })
    const revised = await page.evaluate(() => (window as any).proposal.document)
    assert.equal(revised.automation_v2.expiration.expires_at, Date.parse('2030-04-03T12:30:00.123Z'))
    assert.equal(revised.automation_v2.schedule.interval_seconds, 900)
    assert.deepEqual(revised.checkpoints[0].tasks, ['Read fixture state'])
    if (process.env.SWARM_AUTOMATION_SCREENSHOT) await page.screenshot({ path: process.env.SWARM_AUTOMATION_SCREENSHOT, fullPage: true })
    await page.getByRole('button', { name: 'Accept automation', exact: true }).dblclick()
    await page.getByRole('button', { name: 'Automation accepted' }).waitFor()
    // Requirement: acceptance discloses scheduling, never an immediate run.
    await page.getByRole('status', { name: 'Automation handoff' }).getByText('Acceptance did not start a run.', { exact: false }).waitFor()
    await page.getByRole('button', { name: /Harmless recurring check.*Enabled/ }).waitFor()
    assert.equal(await page.evaluate(() => (window as any).writes.filter((w: any) => w.action === 'accept_automation').length), 1)
    await page.evaluate(() => (window as any).runtime.acceptFrame({ kind: 'rehydrate.required' }))
    await page.getByRole('button', { name: /Harmless recurring check.*Enabled/ }).waitFor()
    assert.equal(await page.getByRole('button', { name: /Harmless recurring check.*Enabled/ }).count(), 1)
    await page.getByRole('button', { name: /Harmless recurring check.*Enabled/ }).click()
    await page.getByText('Waiting for dispatch', { exact: true }).waitFor()
    await page.getByRole('button', { name: 'Talk to Swarm to help optimize this automation', exact: true }).waitFor()
    await page.getByRole('button', { name: 'Pause future occurrences' }).click()
    await page.getByRole('button', { name: 'Resume schedule' }).waitFor()
    assert.equal(await page.evaluate(() => (window as any).writes.at(-1).action),'pause')
    await page.getByRole('button', { name: 'Change instructions or times' }).click()
    await page.getByRole('region', {name:'Edit recurring plan'}).getByLabel('Every (seconds)').fill('1800')
    assert.equal(await page.evaluate(() => (window as any).accepted.document.automation_v2.schedule.interval_seconds),900)
    await page.getByRole('button', { name: 'Change instructions or times' }).click()
    assert.deepEqual(errors, [])
    // A separately rejected pending proposal creates no additional record.
    await page.evaluate(() => { (window as any).proposal={...structuredClone((window as any).base),proposal_id:'rejected'};(window as any).render() })
    await page.getByRole('button', { name: 'Reject', exact: true }).click()
    assert.equal(await page.evaluate(() => (window as any).rejects), 1)
    assert.equal(await page.evaluate(() => (window as any).writes.filter((w:any)=>w.action==='accept_automation').length), 1)
    // Concurrent AI revision preserves the local draft but fences acceptance.
    await page.evaluate(() => { (window as any).proposal={...structuredClone((window as any).base),proposal_id:'concurrent'};(window as any).render() })
    await page.getByLabel('Every (seconds)').fill('1200')
    await page.evaluate(() => { (window as any).proposal={...(window as any).proposal,revision:2,digest:'c'.repeat(64)};(window as any).render() })
    await page.getByRole('alert').filter({hasText:'newer proposal'}).waitFor()
    assert.equal(await page.getByLabel('Every (seconds)').inputValue(),'1200')
    assert.equal(await page.getByRole('button',{name:'Update reviewed snapshot'}).isDisabled(),true)
    await page.getByRole('button',{name:'Discard draft and load current review'}).click()
    assert.equal(await page.getByLabel('Every (seconds)').inputValue(),'3600')
    await page.setViewportSize({ width: 390, height: 844 })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
    if (process.env.SWARM_AUTOMATION_MOBILE_SCREENSHOT) {
      // Capture from the document origin after viewport reflow, not the scroll
      // offset left by the preceding controls. Evidence must include the title.
      await page.evaluate(async () => { scrollTo(0, 0); await new Promise<void>(resolve => requestAnimationFrame(() => resolve())) })
      await page.screenshot({ path: process.env.SWARM_AUTOMATION_MOBILE_SCREENSHOT, fullPage: true })
    }
  } finally { await browser.close() }
})
