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
// Step titles must remain visible by default; one expander reveals all step details.
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
  const task = {
    id: 'proposal', title: 'Tier 3 proposal', tier: 'complex', status: 'pending_approval', agentType: 'plan', outcomeType: 'plan_spec',
    planSummary: fields.overview, agents: [], worktreeBranch: 'agent/proposal',
    planDocument: { title: fields.title, info: { goal: fields.goal }, checkpoints: [{ id: 'cp-1', title: fields.checkpoint, objective: fields.objective, tasks: [fields.task], acceptance_criteria: [fields.criterion], notes: fields.notes }, { id: 'cp-2', title: 'Verify changes', tasks: ['Run focused checks'], acceptance_criteria: ['No regressions'] }] },
    taskProgram: { id: 'program', stages: [{ id: fields.stage, depends_on: [fields.dependency], dependency_evidence: fields.evidence }], jobs: [{ id: 'job', stage_id: fields.stage, agent_type: 'coder', title: fields.job, owned_scope: [fields.scope], deliverable: fields.deliverable, acceptance_criteria: [fields.first, fields.second, fields.third] }] },
  }
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard} from './src/features/desktop/orchestrate/OrchestrateView';
    const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
    const root=createRoot(document.getElementById('root'));
    const task=${JSON.stringify(task)};
    window.calls=[];
    window.renderProposal=(fallback=false, rejected=false)=>root.render(<QueryClientProvider client={client}>
      <MinimalTaskCard key={String(fallback)+String(rejected)} isExpanded task={fallback ? {...task,agentType:'coder',outcomeType:'code_pr',planDocument:null,taskProgram:null,fullPlanMarkdown:${JSON.stringify(fields.markdown)}} : {...task,planDocument:{...task.planDocument,status:rejected?'rejected':'pending'}}}
        onSelect={()=>window.calls.push('select')} onApprove={()=>window.calls.push('approve')} onRefine={feedback=>window.calls.push(feedback)}/>
    </QueryClientProvider>);
    window.renderProposal();
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
    await reader.waitFor()
    const toggle = page.getByTestId('toggle-plan-spec-btn')
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    assert.equal(await toggle.textContent(), 'Review structured plan and acceptance criteria')
    assert.equal(await page.getByText('Tier 3 plan · review before launch', { exact: true }).count(), 1)
    const cardText = await page.getByTestId('orchestrate-task-card').textContent()
    assert.equal(cardText?.split('agent/proposal').length, 2, 'branch appears only in metadata')
    assert.doesNotMatch(cardText!, /Worktree:|AI Mission Proposal|Execution Mode:|plan spec/i)
    assert.match(cardText!, /Expected: 1 branch PR \+ test suite/)
    assert.match(await page.getByTestId('plan-checkpoint-cp-2').textContent() || '', /2\.Verify changes/)
    assert.equal(await reader.getByText('Run focused checks', { exact: true }).count(), 0)
    assert.equal(await reader.getByText(fields.task, { exact: true }).count(), 0)
    assert.equal(await reader.locator('.swarm-plan-step-title-collapsed').first().evaluate(node => getComputedStyle(node).whiteSpace), 'nowrap')
    assert.notEqual(await page.getByTestId('plan-checkpoint-cp-2').evaluate(node => getComputedStyle(node).borderTopStyle), 'none')
    await toggle.click()
    await reader.getByText('Run focused checks', { exact: true }).waitFor()
    await reader.getByText('No regressions', { exact: true }).waitFor()
    for (const [viewport, width] of [[390, 320], [1100, 320], [1100, 760]]) {
      await page.setViewportSize({ width: viewport, height: 900 })
      await page.locator('#root').evaluate((root, width) => { root.style.width = `${width}px` }, width)
      await assertReadable(reader)
      const paneBox = await reader.boundingBox()
      const cardBox = await page.locator('#root').boundingBox()
      assert.ok(paneBox && cardBox && paneBox.x >= cardBox.x && paneBox.x + paneBox.width <= cardBox.x + cardBox.width + 1, 'reader fits the constrained card')
      await assertReadable(page.getByTestId('task-execution-overview'))
      for (const key of ['title', 'goal', 'checkpoint', 'objective', 'task', 'criterion', 'notes', 'stage', 'dependency', 'evidence', 'job', 'scope', 'deliverable', 'first', 'second', 'third']) {
        assert.ok((await reader.textContent())?.includes(fields[key]), `missing ${key}`)
      }
      await reader.evaluate(node => { node.scrollTop = node.scrollHeight })
      const tailVisible = await reader.getByText(fields.third, { exact: true }).evaluate(node => {
        const text = node.firstChild!
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
    await page.getByTestId('toggle-plan-spec-btn').click()
    assert.equal(await reader.count(), 1, 'step titles remain visible when details close')
    assert.equal(await reader.getByText(fields.task, { exact: true }).count(), 0)
    assert.equal(await reader.getByText('Verify changes', { exact: true }).count(), 1)
    await page.getByTestId('toggle-plan-spec-btn').click()
    await reader.waitFor()
    await page.getByRole('button', { name: 'Refine Plan', exact: true }).click()
    await page.getByPlaceholder("e.g. Keep in web workspace only, don't touch daemon API...").fill('Keep every criterion')
    await page.getByPlaceholder("e.g. Keep in web workspace only, don't touch daemon API...").press('Enter')
    await page.getByTestId('approve-task-btn').click()
    assert.deepEqual(await page.evaluate(() => (window as any).calls), ['Keep every criterion', 'approve'])
    await page.evaluate(() => (window as any).renderProposal(false, true))
    await page.getByTestId('task-plan-rejected-banner').waitFor()
    assert.equal(await page.getByTestId('approve-task-btn').isDisabled(), true)
    await page.evaluate(() => (window as any).renderProposal(true))
    await page.getByRole('button', { name: 'Read Full Plan Spec & Criteria' }).click()
    await reader.getByText(fields.markdown, { exact: true }).waitFor()
    await page.locator('#root').evaluate(root => { root.style.width = '320px' })
    await assertReadable(reader)
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
