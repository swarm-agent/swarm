import test from 'node:test'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskCardSummary, taskCardFacts } from './task-card-summary'
import { projectThemePatch, resolveSwarmProjectTheme } from './swarm-section-theme'
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
  assert.equal(taskCardFacts({ ...task, gitStatus: 'clean', isIntegrated: true }).git, 'Integrated')
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
    const missing = resolveSwarmProjectTheme('deleted')
    assert.equal(missing.state, 'missing')
    assert.deepEqual(missing.style, {})
    assert.equal(resolveSwarmProjectTheme('').state, 'inherited')
    assert.deepEqual(projectThemePatch(''), { theme_id: '' })
    assert.deepEqual(projectThemePatch('nord'), { theme_id: 'nord' })
  } finally { setWorkspaceThemeCatalog(null) }
})
