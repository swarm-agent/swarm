import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskCardSummary, taskCardFacts } from './task-card-summary'
import { inheritedSwarmThemeStyle, projectThemePatch, resolveSwarmProjectTheme } from './swarm-section-theme'
import { setWorkspaceThemeCatalog } from '../../workspaces/launcher/services/workspace-theme'
import type { RunningTask } from './orchestrate-types'

// Purpose: task cards must display actual workspace/worktree/Git/progress and never imply
// success from absent evidence. Boundary: TaskCardSummary receives mapped RunningTask;
// the backend owns Git verification and guarded actions remain in MinimalTaskCard.
const task: RunningTask = {
  id: 'task-a', title: 'Fix session recovery', agentType: 'coder', status: 'needs_review',
  workspaceTarget: 'repository', elapsed: '2m', subtasks: [{ id: 'one', title: 'Repair', completed: true }],
  sourceWorkspacePath: '/source/project', worktreeName: 'repair', worktreeBranch: 'agent/repair',
  baseBranch: 'dev', gitStatus: 'unknown', unintegratedCommits: 3,
}

test('card summary renders real branch and target, unknown Git, and no invented validation', () => {
  const html = renderToStaticMarkup(<TaskCardSummary task={task} />)
  assert.match(html, /Fix session recovery/)
  assert.match(html, /Workspace.*project/)
  assert.match(html, /Worktree.*repair/)
  assert.match(html, /agent\/repair.*dev/)
  assert.match(html, /1\/1 steps/)
  assert.match(html, /Validation not reported/)
  assert.match(html, /Git: not inspected/)
  assert.doesNotMatch(html, /unintegrated commit\(s\)|>Integrated</)
})

test('stale Git is not presented as integrated even if cached integrated flag is true', () => {
  assert.equal(taskCardFacts({ ...task, gitStatus: 'stale', isIntegrated: true }).git, 'Git: last known state')
  assert.match(taskCardFacts({ ...task, gitStatus: 'clean', isIntegrated: true }).git, /3 unintegrated commit\(s\)/)
  assert.equal(taskCardFacts({ ...task, gitStatus: 'clean', isIntegrated: true, unintegratedCommits: 0 }).git, 'Integrated')
  assert.equal(taskCardFacts({ ...task, gitStatus: 'clean', baseBranch: undefined }).target, null)
})

test('ready output image becomes preview action; pending output never becomes a preview', () => {
  const withOutput: RunningTask = { ...task, deliverables: [{ id: 'image-a', title: 'Cover', type: 'image', status: 'ready', previewUrl: '/media/cover.png', createdAt: 1, author: 'designer' }] }
  const html = renderToStaticMarkup(<TaskCardSummary task={withOutput} onPreview={() => {}} />)
  assert.match(html, /Preview Cover/)
  assert.match(html, /src="\/media\/cover.png"/)
  assert.equal(taskCardFacts({ ...withOutput, deliverables: [{ ...withOutput.deliverables![0], status: 'pending' }] }).deliverable, undefined)
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
