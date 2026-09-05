import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

import { mapWorkspaceOverviewResponse } from '../types/workspace-overview'

// Requirement: fetchWorkspaceOverview preserves catalog pagination and opts out
// of expensive details explicitly; its mapper must represent absent details as
// unknown, never a clean Git checkout or zero tasks. Static checks cover request
// wiring only; the mapper assertions exercise the actual response contract.
const source = await readFile(new URL('./fetch-workspace-overview.ts', import.meta.url), 'utf8')

test('workspace overview fetches every catalog page instead of truncating the global list', () => {
  assert.match(source, /limit: '100'/)
  assert.match(source, /include_discovered: 'false'/)
  assert.match(source, /include_details: String\(includeDetails\)/)
  assert.match(source, /includeDetails = true/)
  assert.match(source, /workspaces\.push\(\.\.\.\(response\.workspaces \?\? \[\]\)\)/)
  assert.match(source, /response\.has_more/)
  assert.match(source, /response\.next_cursor/)
  assert.match(source, /while \(cursor > 0\)/)
})

test('catalog-only response keeps omitted Git and task details unknown', () => {
  const wire = {
    path: '/workspace/project', workspace_id: 'project', workspace_name: 'Project',
    directories: ['/workspace/project'], is_git_repo: true, sort_index: 0,
    added_at: 1, updated_at: 1, last_selected_at: 1, active: true, worktree_enabled: true,
    git_has_git: true, git_clean: true,
  }
  const catalog = mapWorkspaceOverviewResponse({ ok: true, details_included: false, workspaces: [wire] }).workspaces[0]
  assert.equal(catalog.workspaceId, 'project')
  assert.equal(catalog.isGitRepo, true)
  assert.equal(catalog.worktreeEnabled, true)
  assert.equal(catalog.gitClean, undefined)
  assert.equal(catalog.gitHasGit, undefined)
  assert.equal(catalog.todoSummary, undefined)
  const full = mapWorkspaceOverviewResponse({ ok: true, workspaces: [wire] }).workspaces[0]
  assert.equal(full.gitClean, true)
  assert.equal(full.gitHasGit, true)
})
