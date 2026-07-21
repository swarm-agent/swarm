import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

test('review worktree recovery creates a canonical auto session bound to the failed worktree', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const handler = source.slice(
    source.indexOf('const handleAskSwarmToFixReviewIntegration'),
    source.indexOf('useEffect(() => {', source.indexOf('const handleAskSwarmToFixReviewIntegration')),
  )

  assert.match(handler, /resolveDesktopWorktreeSessionDefaults\(\{[\s\S]*?explicitMode: 'auto',[\s\S]*?globalDefaultMode: 'auto'/)
  assert.match(handler, /modelProfileChoice: defaults\.modelProfileChoice/)
  assert.match(handler, /worktree: \{ mode: 'on', branchName: candidateWorktreeBranch, existingPath: candidateWorktreePath \}/)
  assert.match(handler, /repair_source_session_id: failure\.candidate\.session_id/)
  assert.match(handler, /repair_source_worktree_path: candidateWorktreePath/)
  assert.doesNotMatch(handler, /resolveDesktopV3AgentModelLock/)
})
