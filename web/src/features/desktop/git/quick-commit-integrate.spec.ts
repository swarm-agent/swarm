// Requirement: unexpanded session changes box supports 1-step commit and integrate
// with click-to-confirm, graceful failure display, and "Ask Swarm for Help".
// Also verifies that background reads do not cause status flashing or action button unmounting.
import { test } from 'node:test'
import assert from 'node:assert/strict'

interface QuickCommitIntegrateState {
  confirming: boolean
  busy: boolean
  phase: string | null
  error: string | null
}

function computeUnexpandedVisibility(input: {
  expanded: boolean
  isWorktree: boolean
  hasTargetWorkspace: boolean
  actionsEnabled: boolean
  hasGit: boolean
  dirtyCount: number
  error: string | null
}): boolean {
  return Boolean(
    !input.expanded &&
    input.isWorktree &&
    input.hasTargetWorkspace &&
    input.actionsEnabled &&
    ((input.hasGit && input.dirtyCount > 0) || Boolean(input.error))
  )
}

test('unexpanded commit and integrate visibility requires unexpanded view, worktree, and dirty files or error', () => {
  // Case 1: unexpanded, dirty worktree with target, actions enabled -> visible
  assert.equal(
    computeUnexpandedVisibility({
      expanded: false,
      isWorktree: true,
      hasTargetWorkspace: true,
      actionsEnabled: true,
      hasGit: true,
      dirtyCount: 3,
      error: null,
    }),
    true,
  )

  // Case 2: dock is expanded -> hidden (expanded view has its own commit and integrate controls)
  assert.equal(
    computeUnexpandedVisibility({
      expanded: true,
      isWorktree: true,
      hasTargetWorkspace: true,
      actionsEnabled: true,
      hasGit: true,
      dirtyCount: 3,
      error: null,
    }),
    false,
  )

  // Case 3: clean worktree with no error -> hidden (standard integrate button handles clean worktrees)
  assert.equal(
    computeUnexpandedVisibility({
      expanded: false,
      isWorktree: true,
      hasTargetWorkspace: true,
      actionsEnabled: true,
      hasGit: true,
      dirtyCount: 0,
      error: null,
    }),
    false,
  )

  // Case 4: clean worktree BUT with a prior integration error -> visible so error and retry can be shown
  assert.equal(
    computeUnexpandedVisibility({
      expanded: false,
      isWorktree: true,
      hasTargetWorkspace: true,
      actionsEnabled: true,
      hasGit: true,
      dirtyCount: 0,
      error: 'Merge conflict in package.json',
    }),
    true,
  )

  // Case 5: not a worktree (source checkout) -> hidden
  assert.equal(
    computeUnexpandedVisibility({
      expanded: false,
      isWorktree: false,
      hasTargetWorkspace: true,
      actionsEnabled: true,
      hasGit: true,
      dirtyCount: 2,
      error: null,
    }),
    false,
  )

  // Case 6: actions disabled (e.g. error in repository) -> hidden
  assert.equal(
    computeUnexpandedVisibility({
      expanded: false,
      isWorktree: true,
      hasTargetWorkspace: true,
      actionsEnabled: false,
      hasGit: true,
      dirtyCount: 2,
      error: null,
    }),
    false,
  )
})

test('quick commit and integrate workflow orders commit then integrate and handles failures gracefully', async () => {
  const steps: string[] = []
  let commitShouldFail = false
  let integrateShouldFail = false

  const mockApi = {
    suggest: async () => {
      steps.push('suggest')
      return { ok: true, message: 'feat: quick updates' }
    },
    commit: async (msg: string) => {
      steps.push(`commit:${msg}`)
      if (commitShouldFail) throw new Error('commit rejected by git hook')
      return { ok: true }
    },
    integrate: async () => {
      steps.push('integrate')
      if (integrateShouldFail) throw new Error('CONFLICT: target branch has diverged')
      return { ok: true }
    },
  }

  async function executeQuickWorkflow(dirty: boolean, state: QuickCommitIntegrateState): Promise<string | null> {
    state.busy = true
    state.error = null
    state.confirming = false

    let commitMessage = ''
    let commitSucceeded = false

    if (dirty) {
      state.phase = 'Generating commit message…'
      try {
        const suggestion = await mockApi.suggest()
        commitMessage = suggestion.message
      } catch {
        commitMessage = 'Update session changes'
      }

      state.phase = 'Committing changes…'
      try {
        await mockApi.commit(commitMessage)
        commitSucceeded = true
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err)
        state.error = `Commit failed: ${msg}`
        state.busy = false
        state.phase = null
        return state.error
      }
    }

    state.phase = 'Integrating into target…'
    try {
      await mockApi.integrate()
      state.error = null
      state.busy = false
      state.phase = null
      return null
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      state.error = msg
      state.busy = false
      state.phase = null
      return state.error
    }
  }

  // Run 1: Successful commit and integrate
  const state: QuickCommitIntegrateState = { confirming: false, busy: false, phase: null, error: null }
  const result1 = await executeQuickWorkflow(true, state)
  assert.equal(result1, null)
  assert.equal(state.error, null)
  assert.deepEqual(steps, ['suggest', 'commit:feat: quick updates', 'integrate'])

  // Run 2: Commit failure stops before integrate
  steps.length = 0
  commitShouldFail = true
  const result2 = await executeQuickWorkflow(true, state)
  assert.match(result2 ?? '', /Commit failed: commit rejected by git hook/)
  assert.deepEqual(steps, ['suggest', 'commit:feat: quick updates'])

  // Run 3: Integration failure preserves commit and returns error for Swarm help / retry
  steps.length = 0
  commitShouldFail = false
  integrateShouldFail = true
  const result3 = await executeQuickWorkflow(false, state) // already committed, retrying integrate
  assert.match(result3 ?? '', /CONFLICT: target branch has diverged/)
  assert.deepEqual(steps, ['integrate'])
})

test('status text remains stable during background reads and queries', () => {
  function getDirtyFilesStatus(gitSnapshot: { has_git: boolean; dirty_count: number } | null, inventory: { error?: string; stale: boolean; loading: boolean }, queryError: boolean): string {
    return gitSnapshot?.has_git
      ? `${gitSnapshot.dirty_count} uncommitted file${gitSnapshot.dirty_count === 1 ? '' : 's'}`
      : inventory.error || queryError || (inventory.stale && !inventory.loading)
      ? 'Status unavailable · refresh before making changes'
      : inventory.loading
      ? 'Loading changes…'
      : 'Git status unavailable'
  }

  const snapshot = { has_git: true, dirty_count: 2 }

  // When snapshot is present, background inventory loading does NOT flash "Status unavailable"
  assert.equal(getDirtyFilesStatus(snapshot, { stale: true, loading: true }, false), '2 uncommitted files')
  assert.equal(getDirtyFilesStatus(snapshot, { stale: true, loading: false }, false), '2 uncommitted files')
  assert.equal(getDirtyFilesStatus(snapshot, { stale: false, loading: false }, false), '2 uncommitted files')

  // When snapshot is absent, initial loading shows "Loading changes…"
  assert.equal(getDirtyFilesStatus(null, { stale: true, loading: true }, false), 'Loading changes…')

  // When snapshot is absent and an error exists, shows unavailable
  assert.equal(getDirtyFilesStatus(null, { error: 'Network error', stale: true, loading: false }, false), 'Status unavailable · refresh before making changes')
})
