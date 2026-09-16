import test from 'node:test'
import assert from 'node:assert/strict'
import { buildDesktopSlashPaletteState } from './slash-commands'

// Purpose: production slash registration must expose memory to ordinary users,
// resolving partial/exact input locally instead of sending a management command to AI.
test('memory command is discoverable and opens management', () => {
 const command = buildDesktopSlashPaletteState('/memory').exactMatch
 assert.equal(command?.state, 'ready')
 assert.equal(command?.action.kind, 'open-memory')
 assert(buildDesktopSlashPaletteState('/mem').matches.some(item => item.id === 'memory'))
})
