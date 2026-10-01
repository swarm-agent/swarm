// Purpose: taskReviewMessage must select stated user outcomes without promoting
// requests, process recaps, metadata or unverified validation/integration claims.
// Boundary: pure presentation selector; unit assertions are the narrowest layer
// for malformed input and warning combinations (rendering is tested separately).
import assert from 'node:assert/strict'
import test from 'node:test'
import { taskReviewMessage } from './task-review-message'

const verboseHandoff = `## Implementation handoff
I inspected the source and searched for the problem.
### What changed
- **Fixed the sidebar so project names stay visible.**
- Added a clear review button.
### Commit
Commit: abc123456789
Workspace: /worktrees/review
Changed files: web/src/sidebar.tsx
### Validation
Tests not run; parent validation required.
Integration not verified.
\`\`\`sh
pnpm test
\`\`\`
`

test('verbose handoff yields faithful outcomes, no technical or process recap, and visible limitations', () => {
  const result = taskReviewMessage({ handoffSummary: verboseHandoff })
  assert.equal(result.outcome, 'Fixed the sidebar so project names stay visible. Added a clear review button.')
  assert.deepEqual(result.warnings, ['Validation still needs to be run.', 'Integration has not been verified.'])
  assert.equal(result.raw, verboseHandoff)
  assert.doesNotMatch(result.outcome, /abc123|pnpm|worktrees|inspected|handoff/)
})

test('empty, malformed, narrative and request-only summaries do not become completion facts', () => {
  for (const handoffSummary of [undefined, '', 'I read the files and committed my implementation.',
    'Fix the sidebar so project names stay visible.', '# Fixed the sidebar\n```\nAdded a fake result.\n```',
    { outcome: 'Fixed the sidebar.' } as unknown as string]) {
    assert.equal(taskReviewMessage({ handoffSummary, whatDidDo: ['Added an older feature.'] }).outcome,
      'Review the task to see what changed.')
  }
})

test('clean concise outcome is retained, positive test and integration claims are not promoted', () => {
  assert.equal(taskReviewMessage({ handoffSummary: 'Project names now stay visible.' }).outcome, 'Project names now stay visible.')
  const result = taskReviewMessage({ handoffSummary: 'Fixed the sidebar. Tests passed. Changes were integrated.' })
  assert.equal(result.outcome, 'Fixed the sidebar.')
  assert.deepEqual(result.warnings, [])
})

test('failures and unfinished work survive even when they occur after long metadata', () => {
  const result = taskReviewMessage({ handoffSummary: `${'Commit metadata\n'.repeat(100)}Build failed. Work is incomplete.`,
    whatDidDo: ['Tests not executed.'], whatNotDone: ['Keyboard support remains.'] })
  assert.deepEqual(result.warnings, ['Validation still needs to be run.', 'Some checks failed; review the details.',
    'Some work remains; review the details.'])
  assert.equal(result.outcome, 'Review the task to see what changed.')
})

test('whole unsuitable statements are rejected rather than clipped into misleading claims', () => {
  for (const handoffSummary of ['Added a button in `sidebar.tsx`.', 'Fixed the sidebar but tests failed.',
    'Added a sidebar change that should work.', `Updated ${'very long '.repeat(40)}metadata.`,
    'I fixed the sidebar.']) {
    assert.equal(taskReviewMessage({ handoffSummary }).outcome, 'Review the task to see what changed.')
  }
})
