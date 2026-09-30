// Purpose: TaskCardSummary shows a compact outcome with visible limitations and
// preserves the exact handoff behind native, keyboard-accessible collapsed details.
// Threat: raw recaps overwhelm cards, hidden warnings imply success, or expansion
// bubbles into card actions and stale attempt text persists. The real-browser
// component fixture is the narrowest boundary proving visibility, focus and updates;
// no provider/daemon calls or pixel/aesthetic verification are claimed.
import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import type { RunningTask } from './orchestrate-types'

const raw = `## Implementation handoff
I inspected source and authored local tests.
- **Fixed the sidebar so project names stay visible.**
Commit: abc123456789
Workspace: /worktrees/review
\`\`\`sh
pnpm test
\`\`\`
Tests not run; parent validation required.
Integration not verified.`
const task: RunningTask = {
  id: 'review-task', activeAttemptId: 'attempt-one', title: 'Sidebar', agentType: 'coder',
  status: 'needs_review', workspaceTarget: 'repository', elapsed: '', subtasks: [],
  handoffSummary: raw, gitStatus: 'clean', isIntegrated: false,
}

test('review copy, native details, warning visibility and attempt replacement', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client'; import {flushSync} from 'react-dom';
    import {TaskCardSummary} from './src/features/desktop/orchestrate/task-card-summary';
    let task=${JSON.stringify(task)};
    window.cardActions=0; window.cardKeys=0;
    const root=createRoot(document.getElementById('root'));
    function render(){flushSync(()=>root.render(<article onClick={()=>window.cardActions++} onKeyDown={()=>window.cardKeys++}>
      <TaskCardSummary task={task}/></article>))}
    window.updateTask=patch=>{task={...task,...patch};render()};render();
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://review.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const review = page.getByRole('region', { name: 'Ready for review' })
    const details = review.locator('details')
    const summary = review.locator('summary')
    assert.match(await review.innerText(), /Fixed the sidebar so project names stay visible\. Review the changes\./)
    assert.match(await review.innerText(), /Validation still needs to be run\./)
    assert.match(await review.innerText(), /Integration has not been verified\./)
    assert.doesNotMatch(await review.innerText(), /abc123|pnpm|\/worktrees|I inspected|##/)
    assert.equal(await details.getAttribute('open'), null)
    assert.equal(await review.locator('pre').isVisible(), false)
    assert.match(await page.getByTestId('task-card-summary').innerText(), /Integration not verified/)
    assert.doesNotMatch(await page.getByTestId('task-card-summary').innerText(), /Tests passed|Integrated/)
    await summary.focus()
    await page.keyboard.press('Enter')
    assert.equal(await review.locator('pre').isVisible(), true)
    assert.equal(await review.locator('pre').textContent(), raw)
    assert.equal(await page.evaluate(() => (window as any).cardActions), 0)
    assert.equal(await page.evaluate(() => (window as any).cardKeys), 0)
    await summary.click()
    assert.equal(await details.getAttribute('open'), null)
    assert.equal(await page.evaluate(() => (window as any).cardActions), 0)
    await summary.click()
    await page.evaluate(() => (window as any).updateTask({ activeAttemptId: 'attempt-two', handoffSummary: 'Added keyboard navigation.' }))
    assert.equal(await details.getAttribute('open'), null)
    assert.match(await review.innerText(), /Added keyboard navigation\. Review the changes\./)
    assert.doesNotMatch(await review.innerText(), /project names|Validation still/)
    assert.equal(await review.locator('pre').textContent(), 'Added keyboard navigation.')
    await page.evaluate(() => (window as any).updateTask({ id: 'new-task', handoffSummary: '' }))
    assert.equal(await details.count(), 0)
    assert.equal(await review.innerText(), 'Review the task to see what changed.')
    await page.evaluate(() => (window as any).updateTask({ status: 'running', handoffSummary: 'Fixed another feature.' }))
    assert.equal(await review.count(), 0)
    // Media preview remains an independent action, not a review/Accept mutation.
    await page.evaluate(() => (window as any).updateTask({ agentType: 'image', status: 'needs_review',
      deliverables: [{ id: 'image-result', type: 'image', title: 'Preview result', status: 'ready', previewUrl: 'data:image/png;base64,' }] }))
    assert.equal(await page.getByRole('button', { name: 'Preview Preview result' }).count(), 1)
  } finally { await browser.close() }
})
