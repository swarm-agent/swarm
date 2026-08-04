import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import { DESKTOP_SHORTCUTS, resolveWorkspaceShortcutIndex } from './desktop-shortcuts'

test('workspace picker uses Alt+W while compact sidebar selection remains available', async () => {
  const shortcut = DESKTOP_SHORTCUTS.find((definition) => definition.id === 'workspace-picker')
  assert.deepEqual(shortcut?.keys, ['Alt', 'W'])

  const desktopSource = await readFile(new URL('../layout/desktop-app-page.tsx', import.meta.url), 'utf8')
  assert.match(desktopSource, /event\.altKey[^\n]*normalizedCode === 'keyw'/)
  assert.match(desktopSource, /handleOpenWorkspacePicker\(\)/)
  assert.match(desktopSource, /<DesktopWorkspacePicker/)
  assert.match(desktopSource, /aria-haspopup="menu"/)
  assert.match(desktopSource, /aria-label=\{`Current workspace: \$\{topWorkspaceLabel\}`\}/)
})

test('workspace picker numbers select workspaces 1 through 10', () => {
  assert.equal(resolveWorkspaceShortcutIndex('1', 10), 0)
  assert.equal(resolveWorkspaceShortcutIndex('9', 10), 8)
  assert.equal(resolveWorkspaceShortcutIndex('0', 10), 9)
})

test('workspace picker ignores numbers without a displayed workspace', () => {
  assert.equal(resolveWorkspaceShortcutIndex('3', 2), null)
  assert.equal(resolveWorkspaceShortcutIndex('0', 9), null)
  assert.equal(resolveWorkspaceShortcutIndex('x', 10), null)
})
