import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskCardSummary, taskCardFacts, formatElapsedString, formatElapsedSeconds } from './task-card-summary'
import { inheritedSwarmThemeStyle, projectThemePatch, resolveSwarmProjectTheme } from './swarm-section-theme'
import { setWorkspaceThemeCatalog } from '../../workspaces/launcher/services/workspace-theme'
import type { RunningTask } from './orchestrate-types'

// Purpose: task cards must display actual workspace/worktree/Git/progress and never imply
// success from absent evidence. Boundary: TaskCardSummary receives mapped RunningTask;
// the backend owns Git verification and guarded actions remain in MinimalTaskCard.
// SSR is the narrowest boundary for separate metadata, tooltip-only full paths and
// truthful unknown/stale Git labels; no cached flag or requested model implies success.
const visibleText = (html: string) => html.replace(/<[^>]*>/g, '')
const task: RunningTask = {
  id: 'task-a', title: 'Fix session recovery', agentType: 'coder', status: 'needs_review',
  workspaceTarget: 'repository', elapsed: '2m', subtasks: [{ id: 'one', title: 'Repair', completed: true }],
  sourceWorkspacePath: '/source/project', worktreeName: 'repair', worktreeBranch: 'agent/repair',
  baseBranch: 'dev', gitStatus: 'unknown', unintegratedCommits: 3,
}

test('compact card separates identity and lineage and reports uninspected Git truthfully', () => {
  const html = renderToStaticMarkup(<TaskCardSummary task={task} />)
  assert.match(html, /Fix session recovery/)
  assert.match(html, /class="swarm-task-meta-agent" title="Agent: coder">coder<\/span>/)
  assert.match(html, /title="\/source\/project">project<\/span>/)
  assert.match(html, /title="Worktree: agent\/repair">agent\/repair<\/span>/)
  const visible = visibleText(html)
  assert.equal((visible.match(/agent\/repair/g) || []).length, 1)
  assert.match(visible, /1\/1 steps/)
  assert.doesNotMatch(visible, /\/source\/project|Validation not reported|agent\/repair.*dev/)
  assert.match(visible, /Git: not inspected/)
  assert.doesNotMatch(visible, /Git: last known state|unintegrated commit|Integrated/)
})

test('stale Git is not presented as integrated even if cached integrated flag is true', () => {
  assert.equal(taskCardFacts({ ...task, gitStatus: 'stale', isIntegrated: true }).git, 'Git: last known state')
  assert.equal(taskCardFacts({ ...task, gitStatus: 'clean', isIntegrated: true }).git, '3 unintegrated commits')
  assert.equal(taskCardFacts({ ...task, gitStatus: 'clean', unintegratedCommits: 1 }).git, '1 unintegrated commit')
  const stale = visibleText(renderToStaticMarkup(<TaskCardSummary task={{ ...task, gitStatus: 'stale', isIntegrated: true }} />))
  assert.match(stale, /Git: last known state/)
  assert.doesNotMatch(stale, /Integrated|unintegrated commit/)
  assert.equal(taskCardFacts({ ...task, gitStatus: 'clean', isIntegrated: true, unintegratedCommits: 0 }).git, 'Integrated')
})

test('ready output image becomes preview action; pending output never becomes a preview', () => {
  const withOutput: RunningTask = { ...task, deliverables: [{ id: 'image-a', title: 'Cover', type: 'image', status: 'ready', previewUrl: '/media/cover.png', createdAt: 1, author: 'designer' }] }
  const html = renderToStaticMarkup(<TaskCardSummary task={withOutput} onPreview={() => {}} />)
  assert.match(html, /Preview Cover/)
  assert.match(html, /src="\/media\/cover.png"/)
  assert.equal(taskCardFacts({ ...withOutput, deliverables: [{ ...withOutput.deliverables![0], status: 'pending' }] }).deliverable, undefined)
})

// Purpose: the compact card must show resolved session identity/model and real live tool
// activity, not a requested agent/model or orchestrator prompt. Boundary: aggregateTaskLiveState
// supplies active session fields; TaskCardSummary hides stale activity after termination.
test('card uses active session identity and activity but omits orchestration prompt noise', () => {
  const running: RunningTask = { ...task, status: 'running', description: 'Execution Pipeline Stages: internal prompt',
    planSummary: 'Assigned Agent @coder', activeAgent: 'finder', activeProvider: 'anthropic', activeModel: 'model-x',
    model: 'requested-model', toolActivitySummary: 'Searching source', currentFocus: 'Internal mission plan' }
  const html = renderToStaticMarkup(<TaskCardSummary task={running} />)
  assert.match(html, /finder.*running/)
  assert.match(html, /anthropic \/ model-x/)
  assert.doesNotMatch(html, /Searching source/) // Dedicated focus slot owns live tools.
  assert.doesNotMatch(html, /Execution Pipeline|Assigned Agent|requested-model|Internal mission plan/)
  assert.equal(taskCardFacts({ ...running, status: 'completed' }).activity, null)
  const unavailable = renderToStaticMarkup(<TaskCardSummary task={{ ...task, model: 'requested-model' }} />)
  assert.match(unavailable, /title="Agent: coder">coder<\/span>/)
  assert.match(unavailable, /title="\/source\/project">project<\/span>/)
  assert.match(unavailable, /title="Worktree: agent\/repair">agent\/repair<\/span>/)
  assert.doesNotMatch(unavailable, /requested-model|Active session model/)
})

// Purpose: selection persists only a canonical project reference, never a new global theme
// authority; deleted references are surfaced instead of silently falling back as if selected.
// Boundaries: account theme catalog palette resolver and project PATCH body.
test('project theme selects canonical palette, resets to inherited, and exposes missing references', () => {
  setWorkspaceThemeCatalog({ default_theme_id: 'nord', builtin_themes: [{ id: 'nord', name: 'Nord', palette: {
    background: '#202532', panel: '#303847', border: '#586174', text: '#f2f4f7', text_muted: '#b0b8c6',
    primary: '#7ab9d9', success: '#a3be8c', warning: '#ebcb8b', error: '#bf616a',
  } }] })
  try {
    const selected = resolveSwarmProjectTheme('nord')
    assert.equal(selected.state, 'selected')
    assert.equal((selected.style as Record<string, string>)['--swarm-background'], '#202532')
    assert.equal((selected.style as Record<string, string>)['--swarm-warning'], '#ebcb8b')
    assert.equal((selected.style as Record<string, string>)['--app-bg'], undefined)
    setWorkspaceThemeCatalog({ custom_themes: [{ id: 'paper', name: 'Paper', palette: {
      background: '#f7f7f3', panel: '#ffffff', border: '#c2c2ba', text: '#202423', text_muted: '#515b58',
      primary: '#285c99', success: '#236d49', warning: '#84600b', error: '#9d3045',
    } }] })
    const light = resolveSwarmProjectTheme('paper')
    assert.equal(light.colorScheme, 'light')
    assert.equal((light.style as Record<string, string>)['--swarm-text'], '#202423')
    assert.equal((light.style as Record<string, string>)['--swarm-surface'], '#ffffff')
    const missing = resolveSwarmProjectTheme('deleted')
    assert.equal(missing.state, 'missing')
    assert.deepEqual(missing.style, {})
    assert.equal(resolveSwarmProjectTheme('').state, 'inherited')
    const inherited = inheritedSwarmThemeStyle('deep_indigo')
    assert.equal(inherited['--swarm-background'], '#070914')
    assert.equal(inherited['--swarm-accent'], '#6366f1')
    assert.notEqual(inherited['--swarm-accent'], inheritedSwarmThemeStyle('modern_navy')['--swarm-accent'])
    assert.deepEqual(projectThemePatch(''), { theme_id: '' })
    assert.deepEqual(projectThemePatch('nord'), { theme_id: 'nord' })
  } finally { setWorkspaceThemeCatalog(null) }
})

// Requirement: all retained orchestration presets, including aliases, provide a
// complete scoped palette. Missing optional colors must not break CSS or TS builds.
test('every inherited preset provides nonempty semantic colors', async () => {
  const { inheritedSwarmThemeStyle } = await import('./swarm-section-theme')
  const { ORCHESTRATE_THEMES } = await import('./orchestrate-themes')
  for (const id of Object.keys(ORCHESTRATE_THEMES) as Array<keyof typeof ORCHESTRATE_THEMES>) {
    const style = inheritedSwarmThemeStyle(id)
    for (const [name, value] of Object.entries(style)) {
      assert.equal(typeof value, 'string', `${id}: ${name}`)
      assert.ok(value.length > 0, `${id}: ${name}`)
    }
    assert.equal(style['--swarm-accent'], ORCHESTRATE_THEMES[id].accentColor)
  }
})

test('formatElapsedString and formatElapsedSeconds turn minutes into hours or days when >= 60m', () => {
  // Static string durations
  assert.equal(formatElapsedString('5m'), '5m')
  assert.equal(formatElapsedString('45m'), '45m')
  assert.equal(formatElapsedString('60m'), '1h')
  assert.equal(formatElapsedString('90m'), '1h 30m')
  assert.equal(formatElapsedString('120m'), '2h')
  assert.equal(formatElapsedString('150m'), '2h 30m')
  assert.equal(formatElapsedString('1440m'), '1d')
  assert.equal(formatElapsedString('1500m'), '1d 1h')
  assert.equal(formatElapsedString('2880m'), '2d')
  assert.equal(formatElapsedString('3000m'), '2d 2h')
  assert.equal(formatElapsedString('10s'), '10s')

  // Dynamic seconds duration
  assert.equal(formatElapsedSeconds(30), '0:30')
  assert.equal(formatElapsedSeconds(125), '2:05')
  assert.equal(formatElapsedSeconds(3600), '1h')
  assert.equal(formatElapsedSeconds(5400), '1h 30m')
  assert.equal(formatElapsedSeconds(86400), '1d')
  assert.equal(formatElapsedSeconds(90000), '1d 1h')
})

test('card formats coder agent identity cleanly without system prefix', () => {
  const taskWithSystemCoder: RunningTask = {
    ...task,
    activeAgent: 'system-coder',
  }
  const html = renderToStaticMarkup(<TaskCardSummary task={taskWithSystemCoder} />)
  assert.match(html, /title="Agent: coder">coder<\/span>/)
  assert.match(html, /title="\/source\/project">project<\/span>/)
  assert.match(html, /title="Worktree: agent\/repair">agent\/repair<\/span>/)
  assert.doesNotMatch(html, />system-coder<|>system coder<|@system-coder/)
})

test('user account HUD omits raw acct_ ID and MoreHorizontal dots and provides inline name change', async () => {
  const fs = await import('node:fs')
  const path = await import('node:path')
  const { fileURLToPath } = await import('node:url')
  const dir = path.dirname(fileURLToPath(import.meta.url))
  const source = fs.readFileSync(path.join(dir, 'OrchestrateView.tsx'), 'utf8')

  // Verifies MoreHorizontal dots menu is removed from sidebar footer
  assert.ok(!source.includes('<MoreHorizontal'), 'MoreHorizontal dots menu must be removed from the sidebar footer')

  // Verifies raw email/acct_ display is omitted from user HUD
  assert.ok(!source.includes('{userProfile.email}'), 'userProfile.email / acct_ ID must not be displayed under the account name')

  // Verifies account name is an interactive trigger to change name
  assert.ok(source.includes('data-testid="account-name-button"'), 'Must render account-name-button to allow clicking name to change it')
  assert.ok(source.includes('data-testid="account-name-input"'), 'Must render account-name-input for editing username')
  assert.ok(source.includes('/v1/account/username'), 'Must call /v1/account/username API to save updated account username')
  assert.ok(source.includes("method: 'PUT'"), 'Must call /v1/account/username with PUT method')
  assert.ok(source.includes('updateDesktopSessionUsername'), 'Must sync session username via updateDesktopSessionUsername')
})
