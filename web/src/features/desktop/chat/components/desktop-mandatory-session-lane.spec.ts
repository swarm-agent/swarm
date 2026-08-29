import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

const paneURL = new URL('./desktop-v3-new-session-pane.tsx', import.meta.url)
const composerURL = new URL('./desktop-v3-agentic-composer.tsx', import.meta.url)
const appURL = new URL('../../layout/desktop-app-page.tsx', import.meta.url)
const slashURL = new URL('../services/slash-commands.ts', import.meta.url)
const cardURL = new URL('../../../workspaces/launcher/components/workspace-card.tsx', import.meta.url)

test('new Desktop sessions always request a server-owned worktree lane', async () => {
  const [pane, composer] = await Promise.all([
    readFile(paneURL, 'utf8'),
    readFile(composerURL, 'utf8'),
  ])

  assert.match(pane, /desktopRoutedSessionMetadata\(\{ source: 'desktop-v3' \}\)/)
  assert.doesNotMatch(pane, /worktreePrimed|managed_worktree_requested/)
  assert.doesNotMatch(composer, /worktreePrimed|managed_worktree_requested/)
  assert.doesNotMatch(pane, /initialWorktreeRequested|setWorktreeIntent|onRoutedWorktreeRequestedChange/)
  assert.doesNotMatch(composer, /DesktopRoutedWorktreePrime|routedWorktreeRequested|onRoutedWorktreeRequestedChange/)
})

test('Desktop exposes Plan as the only selectable new-session execution mode', async () => {
  const [app, slashCommands, card] = await Promise.all([
    readFile(appURL, 'utf8'),
    readFile(slashURL, 'utf8'),
    readFile(cardURL, 'utf8'),
  ])

  assert.match(app, /planModeRequested/)
  assert.doesNotMatch(app, /newWorktree|initialWorktreeRequested|worktreeRequested|workspaceWorktreeMatch/)
  assert.doesNotMatch(slashCommands, /enable-new-session-worktree|\/wt on|\/new worktree|\/new wp/)
  assert.doesNotMatch(card, /WorkspaceWorktreeToggle|onToggleWorktree|worktreeEnabled/)
})

test('Desktop sends no branch naming authority', async () => {
  const [pane, app] = await Promise.all([
    readFile(paneURL, 'utf8'),
    readFile(appURL, 'utf8'),
  ])

  assert.doesNotMatch(`${pane}\n${app}`, /worktree_branch_name|branchName:|titleToWorktreeBranchSlug|composeWorktreeBranchName/)
})
