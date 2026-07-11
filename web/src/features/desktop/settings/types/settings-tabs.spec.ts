import assert from 'node:assert/strict'
import test from 'node:test'

import { SETTINGS_TABS, normalizeSettingsTabID } from './settings-tabs'

test('account settings is the first/default settings tab', () => {
  assert.equal(SETTINGS_TABS[0], 'account')
  assert.equal(normalizeSettingsTabID(undefined), 'account')
  assert.equal(normalizeSettingsTabID('not-a-tab'), 'account')
  assert.equal(normalizeSettingsTabID('notifications'), 'notifications')
})
