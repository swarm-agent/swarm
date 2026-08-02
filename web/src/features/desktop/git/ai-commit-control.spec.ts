import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const controlURL = new URL('./ai-commit-control.tsx', import.meta.url)
const pageURL = new URL('../layout/desktop-app-page.tsx', import.meta.url)

test('AI Commit split control exposes generation and an accessible post-commit Action menu', async () => {
  const source = await readFile(controlURL, 'utf8')

  assert.match(source, /data-ai-commit-control/)
  assert.match(source, /onClick=\{onGenerate\}/)
  assert.match(source, /phase === 'generating' \? 'Generating…' : phase === 'committing' \? 'Committing…' :/)
  assert.match(source, /aria-haspopup="menu"/)
  assert.match(source, /aria-expanded=\{open\}/)
  assert.match(source, /role="menu" aria-label=\{actionsOnly \? 'Workspace Actions' : 'Post-commit Actions'\}/)
  assert.match(source, /createPortal\(/)
  assert.match(source, /className="fixed z-50[^\n]*style=\{\{ top: menuPosition\.top, right: menuPosition\.right \}\}/)
  assert.match(source, /top: bounds\.bottom \+ 8/)
  assert.doesNotMatch(source, /bottom-full/)
  assert.match(source, /compact \? 'w-9 px-0'/)
  assert.match(source, /!compact \? <span className="truncate">/)
  assert.match(source, /fetchWorkspaceActions\(workspacePath, controller\.signal\)/)
  assert.match(source, /No post-commit Action/)
  assert.match(source, /data-actions-only=\{actionsOnly \|\| undefined\}/)
  assert.match(source, /actionsOnly \? 'Open workspace Actions' : 'Choose post-commit Action'/)
  assert.match(source, /if \(action\) onActionRun\?\.\(action\)/)
  assert.match(source, /onActionSelect\?\.\(action\)/)
})

test('plan sidebar uses compact Save and AI controls while the details overlay preserves its commit entry points', async () => {
  const source = await readFile(pageURL, 'utf8')
  const sidebarStart = source.indexOf('data-plan-git-commit')
  const sidebarEnd = source.indexOf('data-plan-git-integrate', sidebarStart)
  const sidebar = source.slice(sidebarStart, sidebarEnd)
  const panelStart = source.indexOf('const planSidebarGitPanel =')
  const panelEnd = source.indexOf('const focusedSidebarContent =', panelStart)
  const overlayStart = source.lastIndexOf('<GitDetailsOverlay')
  const overlayEnd = source.indexOf('/>', overlayStart)

  assert.ok(sidebarStart >= 0 && sidebarEnd > sidebarStart)
  assert.match(sidebar, /aria-label="Commit changes"[^\n]*<Save size=\{14\}/)
  assert.match(sidebar, /<AICommitControl compact/)
  assert.match(sidebar, /gitSnapshot\.files\.length > 0 \? \([\s\S]*<AICommitControl actionsOnly/)
  assert.match(sidebar, /onActionRun=\{openWorkspaceAction\}/)
  assert.doesNotMatch(sidebar, />Commit…<\/button>/)
  assert.ok(panelStart >= 0 && panelEnd > panelStart)
  assert.doesNotMatch(source.slice(panelStart, panelEnd), /aria-label="Refresh Git status"/)
  assert.ok(overlayStart >= 0 && overlayEnd > overlayStart)
  assert.match(source.slice(overlayStart, overlayEnd), /aiCommitControl=\{[\s\S]*<AICommitControl/)
})

test('AI Commit runs suggestion and commit in the background without opening the review modal', async () => {
  const source = await readFile(pageURL, 'utf8')
  const workflowStart = source.indexOf('const handleAICommit = async')
  const workflowEnd = source.indexOf('const openGitCommitReview', workflowStart)
  const workflow = source.slice(workflowStart, workflowEnd)

  assert.match(workflow, /await suggestWorkspaceCommitMessage/)
  assert.match(workflow, /setGitAICommitPhase\('committing'\)/)
  assert.match(workflow, /await commitWorkspaceChanges/)
  assert.ok(workflow.indexOf('await suggestWorkspaceCommitMessage') < workflow.indexOf('await commitWorkspaceChanges'))
  assert.doesNotMatch(workflow, /setGitCommitModal/)
  assert.doesNotMatch(workflow, /setGitCommitMessage/)
  assert.doesNotMatch(source, /openGitCommitReview\([^\n]*, true\)/)
})

test('AI Commit blocks duplicate clicks and reports progress, success, and failure', async () => {
  const source = await readFile(pageURL, 'utf8')
  const workflowStart = source.indexOf('const handleAICommit = async')
  const workflowEnd = source.indexOf('const openGitCommitReview', workflowStart)
  const workflow = source.slice(workflowStart, workflowEnd)

  assert.match(workflow, /gitAICommitRunningRef\.current/)
  assert.match(workflow, /setGitAICommitPhase\('generating'\)/)
  assert.match(workflow, /setDesktopToast\(\{ message: 'AI Commit is generating a commit message\. Please wait…'/)
  assert.match(workflow, /setDesktopToast\(\{ message: `AI Commit is committing/)
  assert.match(workflow, /Changes committed with/)
  assert.match(workflow, /AI Commit failed:/)
  assert.match(workflow, /finally \{[\s\S]*gitAICommitRunningRef\.current = false[\s\S]*setGitAICommitPhase\(null\)/)
  assert.match(source, /phase=\{gitAICommitPhase\}/)
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
  assert.match(source, /setGitCommitActionPresentation\(\{ workspacePath: action\.workspacePath, action, run: null, committedMessage: '' \}\)/)
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
