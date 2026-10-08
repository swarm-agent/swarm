import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium, type Locator } from 'playwright'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'
import { readFile } from 'node:fs/promises'

// Requirement: pending Tier 3 proposals expose every plan detail before approval,
// including all job criteria, at narrow card widths and with unbroken paths/URLs.
// Threat: text exists in the DOM but is ellipsized, clipped, or omitted after the
// first criterion. Boundary: MinimalTaskCard with production Tailwind CSS in a
// real browser; geometry proves wrapping without inner scrolling, callbacks prove controls.
// Pending plans expose the outcome checklist without opening details or selecting
// a session. Task details retain the complete plan and optional execution reader.
// Revision changes reset plan disclosure; unrelated updates preserve user choice.
// Duplicate branch/spec banners must not compete with the summary metadata.
// HTTP settings are fixtures, not evidence of daemon/provider execution.
async function assertReadable(region: Locator) {
  const failures = await region.evaluate(root => {
    const bounds = root.getBoundingClientRect()
    const failures: string[] = []
    for (const element of [root, ...root.querySelectorAll('*')]) {
      const style = getComputedStyle(element)
      if (style.textOverflow === 'ellipsis') failures.push(`ellipsis: ${element.textContent}`)
      if (element.clientWidth && element.scrollWidth > element.clientWidth + 1) failures.push(`overflow: ${element.tagName}`)
    }
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
    while (walker.nextNode()) {
      if (!walker.currentNode.textContent?.trim()) continue
      const range = document.createRange()
      range.selectNodeContents(walker.currentNode)
      for (const rect of range.getClientRects()) {
        if (rect.left < bounds.left - 1 || rect.right > bounds.right + 1) failures.push(`clipped text: ${walker.currentNode.textContent}`)
      }
    }
    return failures
  })
  assert.deepEqual(failures, [])
}

test('mission proposal wraps full structured and fallback plans without changing review controls', { timeout: 60000 }, async () => {
  const long = (label: string) => `${label}: Read every requirement before approving. ${'Detailed implementation and verification instructions. '.repeat(4)}https://example.invalid/${'unbroken'.repeat(35)}`
  const fields = Object.fromEntries(['title', 'goal', 'checkpoint', 'objective', 'task', 'criterion', 'notes', 'stage', 'dependency', 'evidence', 'job', 'scope', 'deliverable', 'first', 'second', 'third', 'overview', 'markdown'].map(key => [key, long(key)]))
  fields.notes += '\n\nPreserve this second paragraph in full.\nKeep this final line visible.'
  const task = {
    id: 'proposal', title: 'Tier 3 proposal', tier: 'complex', status: 'pending_approval', agentType: 'plan', outcomeType: 'plan_spec',
    planSummary: fields.overview, agents: [], worktreeBranch: 'agent/proposal',
    planDocument: { title: fields.title, info: { goal: fields.goal }, requirements: [{ id: 'r1', text: fields.criterion, checkpoint_id: 'cp-1' }, { id: 'r2', text: 'No regressions', checkpoint_id: 'cp-2' }], checkpoints: [{ id: 'cp-1', title: fields.checkpoint, objective: fields.objective, tasks: [fields.task], acceptance_criteria: [fields.criterion], notes: fields.notes }, { id: 'cp-2', title: 'Verify changes', tasks: ['Run focused checks'], acceptance_criteria: ['No regressions'] }] },
    taskProgram: { id: 'program', stages: [{ id: fields.stage, depends_on: [fields.dependency], dependency_evidence: fields.evidence }], jobs: [{ id: 'job', stage_id: fields.stage, agent_type: 'coder', title: fields.job, owned_scope: [fields.scope], deliverable: fields.deliverable, acceptance_criteria: [fields.first, fields.second, fields.third] }] },
  }
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard} from './src/features/desktop/orchestrate/OrchestrateView';
    import {mapBackendTask} from './src/features/desktop/state/desktop-projects-state';
    const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
    const root=createRoot(document.getElementById('root'));
    const original=${JSON.stringify(task)};
    const task={...original,...mapBackendTask({id:original.id,title:original.title,tier:original.tier,status:original.status,agent:original.agentType,outcome_type:original.outcomeType,plan_summary:original.planSummary,worktree_branch:original.worktreeBranch,session_id:'session',plan_binding:{plan_id:'plan',session_id:'session',definition_revision:1},plan_document:original.planDocument,task_program:original.taskProgram,auto_approve:true})};
    window.calls=[];
    window.renderProposal=(fallback=false, rejected=false, busy=false, error='', revision=1, id=task.id, status='pending_approval', programOnly=false)=>root.render(<QueryClientProvider client={client}>
      <MinimalTaskCard key={String(fallback)+String(rejected)} task={fallback ? {...task,agentType:'coder',outcomeType:'code_pr',planDocument:null,plan_document:null,activePlanCheckpoints:[],taskProgram:null,task_program:null,fullPlanMarkdown:${JSON.stringify(fields.markdown)}} : {...task,id,status,planBinding:{...task.planBinding,definitionRevision:revision},activePlanCheckpoints:programOnly?[]:task.activePlanCheckpoints,plan_document:programOnly?null:task.plan_document,planDocument:programOnly?null:{...task.planDocument,status:rejected?'rejected':'pending'}}}
        isApproving={busy} taskError={error} onSelect={()=>window.calls.push('select')} onApprove={()=>window.calls.push('approve')} onRefine={feedback=>window.calls.push(feedback)}/>
    </QueryClientProvider>);
    window.renderProposal(false, false, false, '', 1, task.id, 'planning');
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const styles = await buildStyles({ configFile: false, logLevel: 'silent', publicDir: false, plugins: [tailwindcss()], build: { write: false, rollupOptions: { input: path.resolve('src/theme.css') } } })
  const outputs = (Array.isArray(styles) ? styles : [styles]).flatMap(result => 'output' in result ? result.output : [])
  const css = outputs.filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css')).map(asset => asset.type === 'asset' ? String(asset.source) : '').join('\n')
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 1100, height: 900 } })
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({ contentType: new URL(route.request().url()).pathname === '/' ? 'text/html' : 'application/json', body: new URL(route.request().url()).pathname === '/' ? '<div class="swarm-section"><div id="root" style="width:320px"></div></div>' : '{}' }))
    await page.goto('https://proposal.test/')
    await page.addStyleTag({ content: css + await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8') })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const reader = page.getByTestId('task-plan-reader')
    await page.getByTestId('toggle-task-details-btn').waitFor()
    assert.equal(await reader.count(), 0, 'investigation is not a ready pending proposal')
    await page.evaluate(() => (window as any).renderProposal())
    const checklist = page.getByRole('region', { name: 'Plan checklist', exact: true })
    await checklist.waitFor()
    await assertReadable(checklist)
    assert.equal(await reader.count(), 0)
    assert.equal(await page.getByText('Auto-Approved', { exact: true }).count(), 0)
    const toggle = page.getByTestId('toggle-plan-spec-btn')
    assert.equal(await page.getByTestId('toggle-task-details-btn').getAttribute('aria-expanded'), 'false')
    assert.equal(await page.getByTestId('task-impending-agents').count(), 0)
    assert.equal(await toggle.count(), 0, 'technical disclosure is inside task details')
    assert.equal(await page.getByText('Tier 3 plan · review before launch', { exact: true }).count(), 1)
    const cardText = await page.getByTestId('orchestrate-task-card').textContent()
    assert.equal(cardText?.split('agent/proposal').length, 2, 'branch appears only in metadata')
    assert.doesNotMatch(cardText!, /Worktree:|AI Mission Proposal|Execution Mode:|plan spec/i)
    assert.match(cardText!, /Expected: 1 branch PR \+ test suite/)
    assert.deepEqual(await page.evaluate(() => (window as any).calls), [], 'surfacing the plan must not select or approve the task')
    assert.deepEqual(await checklist.locator('li').allTextContents(), [`□${fields.criterion}`, '□No regressions'])
    assert.equal(await reader.getByText('Run focused checks', { exact: true }).count(), 0)
    assert.equal(await reader.getByText(fields.task, { exact: true }).count(), 0)
    await page.getByTestId('toggle-task-details-btn').click()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    assert.equal(await toggle.textContent(), 'Show execution details')
    await toggle.click()
    await reader.getByText('Run focused checks', { exact: true }).waitFor()
    await reader.getByText('No regressions', { exact: true }).first().waitFor()
    assert.equal(await page.getByTestId('toggle-task-details-btn').getAttribute('aria-expanded'), 'true')
    const controlledPlan = await toggle.getAttribute('aria-controls')
    assert.ok(controlledPlan && await reader.evaluate((node, id) => node.parentElement?.id === id, controlledPlan))
    for (const [viewport, width] of [[390, 320], [1100, 320], [1100, 760]]) {
      await page.setViewportSize({ width: viewport, height: 900 })
      await page.locator('#root').evaluate((root, width) => { root.style.width = `${width}px` }, width)
      await assertReadable(checklist)
      await assertReadable(reader)
      const full = page.getByRole('region', { name: 'Full current plan', exact: true })
      await assertReadable(full)
      for (const key of ['goal', 'task', 'criterion', 'job', 'deliverable', 'third']) assert.ok((await full.innerText()).includes(fields[key]))
      assert.doesNotMatch(await full.innerText(), /"checkpoints"|"acceptance_criteria"|"stage_id"/)
      assert.match(await full.innerText(), /Preserve this second paragraph in full/)
      assert.match(await full.innerText(), /Keep this final line visible/)
      const paneBox = await reader.boundingBox()
      const cardBox = await page.locator('#root').boundingBox()
      assert.ok(paneBox && cardBox && paneBox.x >= cardBox.x && paneBox.x + paneBox.width <= cardBox.x + cardBox.width + 1, 'reader fits the constrained card')
      await assertReadable(page.getByTestId('task-execution-overview'))
      for (const key of ['title', 'goal', 'checkpoint', 'objective', 'task', 'criterion', 'notes', 'stage', 'dependency', 'evidence', 'job', 'scope', 'deliverable', 'first', 'second', 'third']) {
        for (const paragraph of fields[key].split(/\n+/)) assert.ok((await reader.textContent())?.includes(paragraph), `missing ${key}`)
      }
      await reader.evaluate(node => { node.scrollTop = node.scrollHeight })
      const tailVisible = await reader.getByText(fields.third, { exact: true }).evaluate(node => {
        const walker = document.createTreeWalker(node, NodeFilter.SHOW_TEXT)
        let text: Node = node
        while (walker.nextNode()) text = walker.currentNode
        const range = document.createRange()
        range.setStart(text, text.textContent!.length - 1)
        range.setEnd(text, text.textContent!.length)
        const tail = range.getBoundingClientRect()
        const pane = node.closest('[data-testid="task-plan-reader"]')!.getBoundingClientRect()
        return tail.top >= pane.top && tail.bottom <= pane.bottom
      })
      assert.ok(tailVisible, 'the end of the last criterion can be scrolled into view')
      assert.ok(await reader.evaluate(node => node.scrollHeight <= node.clientHeight + 1), 'long plan grows without an inner scroll')
    }
    await page.getByTestId('toggle-task-details-btn').click()
    assert.equal(await toggle.count(), 0)
    assert.equal(await reader.count(), 0, 'closing task details restores checklist-only presentation')
    assert.equal(await checklist.isVisible(), true)
    await page.getByTestId('toggle-task-details-btn').click()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true', 'reopening details retains execution disclosure choice')
    await toggle.click()
    assert.equal(await reader.count(), 0)
    await toggle.click()
    await reader.waitFor()
    // Same-card definition/task changes close details; status changes are not approval.
    await page.evaluate(() => (window as any).renderProposal(false, false, false, '', 2))
    await page.waitForFunction(() => document.querySelector('[data-testid="toggle-plan-spec-btn"]')?.getAttribute('aria-expanded') === 'false')
    assert.equal(await reader.getByText(fields.task, { exact: true }).count(), 0)
    await toggle.click()
    await page.evaluate(() => (window as any).renderProposal(false, false, false, '', 2, 'another-proposal'))
    await page.waitForFunction(() => document.querySelector('[data-testid="toggle-plan-spec-btn"]')?.getAttribute('aria-expanded') === 'false')
    await page.evaluate(() => (window as any).renderProposal(false, false, false, '', 2, 'another-proposal', 'queued'))
    await page.waitForFunction(() => !document.querySelector('[data-testid="approve-task-btn"]'))
    assert.equal(await page.getByTestId('approve-task-btn').count(), 0, 'queued is not pending approval')
    await page.evaluate(() => (window as any).renderProposal())
    await page.getByTestId('approve-task-btn').waitFor()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    await toggle.click()
    await page.evaluate(() => (window as any).renderProposal(false, false, true))
    await page.waitForFunction(() => (document.querySelector('[data-testid="approve-task-btn"]') as HTMLButtonElement)?.disabled)
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true', 'same-definition busy updates retain the user disclosure')
    await page.evaluate(() => (window as any).renderProposal())
    await page.waitForFunction(() => !(document.querySelector('[data-testid="approve-task-btn"]') as HTMLButtonElement)?.disabled)
    await page.getByRole('button', { name: 'Request changes', exact: true }).click()
    await page.getByRole('textbox', { name: 'Requested requirement changes' }).fill('Keep every criterion')
    await page.getByRole('textbox', { name: 'Requested requirement changes' }).press('Enter')
    await page.getByTestId('approve-task-btn').click()
    assert.deepEqual(await page.evaluate(() => (window as any).calls), ['Keep every criterion', 'approve'])
    await page.evaluate(() => (window as any).renderProposal(false, false, true))
    await page.waitForFunction(() => (document.querySelector('[data-testid="approve-task-btn"]') as HTMLButtonElement)?.disabled)
    assert.equal(await page.getByTestId('approve-task-btn').isDisabled(), true)
    assert.match(await page.getByTestId('approve-task-btn').textContent() || '', /Approving & Starting/)
    await page.evaluate(() => (window as any).renderProposal(false, false, false, 'Scheduling unavailable'))
    await page.getByTestId('task-error-banner').waitFor()
    assert.ok((await page.getByTestId('task-error-banner').textContent())?.includes('Scheduling unavailable'))
    assert.equal(await page.getByTestId('retry-approve-btn').isDisabled(), false)
    await page.getByTestId('retry-approve-btn').click()
    assert.deepEqual(await page.evaluate(() => (window as any).calls), ['Keep every criterion', 'approve', 'approve'])
    await page.evaluate(() => (window as any).renderProposal(false, true))
    await page.getByTestId('task-plan-rejected-banner').waitFor()
    assert.equal(await page.getByTestId('approve-task-btn').isDisabled(), true)
    await page.evaluate(() => (window as any).renderProposal(true))
    await checklist.waitFor()
    assert.match(await checklist.innerText(), /No bound requirements or acceptance criteria/)
    assert.equal(await page.getByTestId('toggle-task-details-btn').getAttribute('aria-expanded'), 'false')
    await page.getByTestId('toggle-task-details-btn').click()
    await page.getByRole('button', { name: 'Read Full Plan Spec & Criteria' }).click()
    // Legacy Markdown is readable content, never executable approval authority.
    await reader.getByText(fields.markdown, { exact: true }).waitFor()
    assert.ok((await reader.innerText()).includes(fields.markdown))
    assert.equal(await page.getByTestId('approve-task-btn').isDisabled(), true)
    await page.locator('#root').evaluate(root => { root.style.width = '320px' })
    await assertReadable(reader)
    await page.evaluate(() => (window as any).renderProposal(false, false, false, '', 2, 'program-only', 'pending_approval', true))
    await checklist.waitFor()
    assert.match(await checklist.innerText(), /No bound requirements or acceptance criteria/)
    assert.equal(await page.getByTestId('task-program-spec').count(), 0)
    assert.equal(await toggle.count(), 0, 'program-only proposal does not dump execution jobs while collapsed')
    await page.getByTestId('toggle-task-details-btn').click()
    await toggle.click()
    await reader.getByText(fields.third, { exact: true }).waitFor()
    assert.ok((await reader.textContent())?.includes(fields.third))
    assert.equal(await page.getByTestId('toggle-task-details-btn').getAttribute('aria-expanded'), 'true')
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
