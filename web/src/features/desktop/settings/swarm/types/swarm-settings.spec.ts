import assert from 'node:assert/strict'
import test from 'node:test'

import {
  localContainerUpdateWarningDismissed,
  withLocalContainerUpdateWarningDismissed,
  type UISettingsWire,
} from './swarm-settings'

test('local container update warning dismissal reads backend UI update setting only', () => {
  assert.equal(localContainerUpdateWarningDismissed(null), false)
  assert.equal(localContainerUpdateWarningDismissed({ updates: {} }), false)
  assert.equal(localContainerUpdateWarningDismissed({ updates: { local_container_warning_dismissed: true } }), true)
})

test('withLocalContainerUpdateWarningDismissed preserves existing settings while updating local warning flag', () => {
  const current: UISettingsWire = {
    theme: { active_id: 'nord' },
    chat: { thinking_tags: false, default_new_session_mode: 'plan' },
    swarm: { name: 'Desk' },
    updates: { local_container_warning_dismissed: false },
  }

  assert.deepEqual(withLocalContainerUpdateWarningDismissed(current, true), {
    ...current,
    updates: { local_container_warning_dismissed: true },
  })
})
