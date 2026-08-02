import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const controlURL = new URL('./ai-commit-control.tsx', import.meta.url)
const pageURL = new URL('../layout/desktop-app-page.tsx', import.meta.url)

test('AI Commit split control exposes generation and an accessible post-commit Action menu', async () => {
  const source = await readFile(controlURL, 'utf8')

  assert.match(source, /data-ai-commit-control/)
  assert.match(source, /onClick=\{onGenerate\}/)
  assert.match(source, /generating \? 'Generating…' :/)
  assert.match(source, /aria-haspopup="menu"/)
  assert.match(source, /aria-expanded=\{open\}/)
  assert.match(source, /role="menu" aria-label="Post-commit Actions"/)
  assert.match(source, /fetchWorkspaceActions\(workspacePath, controller\.signal\)/)
  assert.match(source, /No post-commit Action/)
  assert.match(source, /onActionSelect\(action\)/)
})

test('both Desktop commit entry points place AI Commit beside manual Commit', async () => {
  const source = await readFile(pageURL, 'utf8')
  const sidebarStart = source.indexOf('data-plan-git-commit')
  const sidebarEnd = source.indexOf('data-plan-git-integrate', sidebarStart)
  const overlayStart = source.lastIndexOf('<GitDetailsOverlay')
  const overlayEnd = source.indexOf('/>', overlayStart)

  assert.ok(sidebarStart >= 0 && sidebarEnd > sidebarStart)
  assert.match(source.slice(sidebarStart, sidebarEnd), />Commit…<\/button>[\s\S]*<AICommitControl/)
  assert.ok(overlayStart >= 0 && overlayEnd > overlayStart)
  assert.match(source.slice(overlayStart, overlayEnd), /aiCommitControl=\{[\s\S]*<AICommitControl/)
})

test('AI generation populates and focuses the editable message without committing', async () => {
  const source = await readFile(pageURL, 'utf8')
  const suggestionStart = source.indexOf('const handleGitCommitSuggestion = async')
  const suggestionEnd = source.indexOf('const openGitCommitReview', suggestionStart)
  const suggestion = source.slice(suggestionStart, suggestionEnd)

  assert.match(suggestion, /await suggestWorkspaceCommitMessage/)
  assert.match(suggestion, /setGitCommitMessage\(response\.message\)/)
  assert.match(suggestion, /gitCommitMessageInputRef\.current\?\.focus\(\)/)
  assert.doesNotMatch(suggestion, /commitWorkspaceChanges/)
  assert.match(source, /disabled=\{gitCommitBusy \|\| gitCommitGenerating \|\| !gitCommitMessage\.trim\(\) \|\| gitCommitActionMissingInputs\}/)
})

test('selected Action inputs render in commit review and required values block commit', async () => {
  const source = await readFile(pageURL, 'utf8')

  assert.match(source, /setGitCommitActionInputs\(action \? Object\.fromEntries\(action\.inputs\.map/)
  assert.match(source, /After commit: \{gitCommitAction\.name\}/)
  assert.match(source, /input\.kind === 'secret' \? 'password' : 'text'/)
  assert.match(source, /required=\{input\.required\}/)
  assert.match(source, /gitCommitActionMissingInputs/)
  assert.match(source, /Fill every required Action input before committing\./)
  assert.match(source, />Clear<\/button>/)
})


test('selected Action launches exactly once after commit success and reuses the foreground run panel', async () => {
  const source = await readFile(pageURL, 'utf8')
  const commitStart = source.indexOf('const handleGitCommit = async')
  const commitEnd = source.indexOf('const handleGitIntegrate', commitStart)
  const handler = source.slice(commitStart, commitEnd)
  const commitCall = handler.indexOf('await commitWorkspaceChanges')
  const actionCall = handler.indexOf('await startWorkspaceAction')

  assert.ok(commitCall >= 0 && actionCall > commitCall, 'Action must launch only after commit resolves')
  assert.equal(handler.match(/await startWorkspaceAction/g)?.length, 1)
  assert.match(handler, /startWorkspaceAction\(selectedAction\.workspacePath, selectedAction\.id, selectedActionInputs\)/)
  assert.match(handler, /setGitCommitActionPresentation\(\{ workspacePath: selectedAction\.workspacePath, action: selectedAction, run, committedMessage: message \}\)/)
  assert.match(source, /<DesktopWorkspaceActionPanel[\s\S]*initialRun=\{gitCommitActionPresentation\.run\}/)
  assert.doesNotMatch(handler.slice(0, commitCall), /startWorkspaceAction/)
})

test('post-commit Action failure remains an explicit partial success', async () => {
  const source = await readFile(pageURL, 'utf8')

  assert.match(source, /The post-commit Action could not start:/)
  assert.match(source, /Commit succeeded, but \$\{run\.actionName\} failed:/)
  assert.match(source, /contextNotice=\{`Commit succeeded with message/)
  assert.match(source, /invalidateQueries\(\{ queryKey: \['workspace-git-status'\] \}\)/)
  assert.match(source, /invalidateQueries\(\{ queryKey: \['session-worktree-review'\] \}\)/)
})
