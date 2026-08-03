import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import { DESKTOP_SHORTCUTS } from './desktop-shortcuts'

test('desktop omits workspace quick switching', async () => {
  assert.equal(DESKTOP_SHORTCUTS.some((definition) => definition.id === 'workspace-picker'), false)

  const desktopSource = await readFile(new URL('../layout/desktop-app-page.tsx', import.meta.url), 'utf8')
  assert.doesNotMatch(desktopSource, /normalizedCode === 'keyw'/)
  assert.doesNotMatch(desktopSource, /handleOpenWorkspacePicker/)
  assert.doesNotMatch(desktopSource, /<DesktopWorkspacePicker/)
  assert.doesNotMatch(desktopSource, /aria-haspopup="menu"/)
  assert.match(desktopSource, /aria-label="Current workspace"/)
})
