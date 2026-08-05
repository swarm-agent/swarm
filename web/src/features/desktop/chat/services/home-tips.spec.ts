import assert from 'node:assert/strict'
import test from 'node:test'

import {
  DESKTOP_HOME_TIPS,
  executeDesktopTipsCommand,
  parseDesktopTipsCommand,
  resolveDesktopTipsEnabled,
  selectDesktopHomeTipIndex,
} from './home-tips'

test('desktop home tip catalog contains the approved 31 tips in order', () => {
  assert.equal(DESKTOP_HOME_TIPS.length, 31)
  assert.equal(DESKTOP_HOME_TIPS[0], 'Ask Swarm for three theme variants, then apply your favorite.')
  assert.equal(DESKTOP_HOME_TIPS[27], 'TUI: press Ctrl+P to inspect the full plan and checkpoint status.')
  assert.equal(DESKTOP_HOME_TIPS[30], 'Type /tips to hide or show these tips.')
})

test('desktop home tip selection chooses a launch tip and avoids the previous tip', () => {
  assert.equal(selectDesktopHomeTipIndex(-1, () => 0), 0)
  assert.equal(selectDesktopHomeTipIndex(-1, () => 0.999), 30)
  assert.equal(selectDesktopHomeTipIndex(0, () => 0), 1)
  assert.equal(selectDesktopHomeTipIndex(30, () => 0), 0)
})

test('tips command accepts supported modes and defaults to toggle', () => {
  assert.equal(parseDesktopTipsCommand('/tips'), 'toggle')
  assert.equal(parseDesktopTipsCommand(' /TIPS on '), 'on')
  assert.equal(parseDesktopTipsCommand('/tips off'), 'off')
  assert.equal(parseDesktopTipsCommand('/tips toggle'), 'toggle')
  assert.equal(parseDesktopTipsCommand('/tips status'), 'status')
  assert.equal(parseDesktopTipsCommand('/tips sometimes'), null)
  assert.equal(parseDesktopTipsCommand('/tips on extra'), null)
  assert.equal(parseDesktopTipsCommand('/tip'), null)
})

test('tips command execution persists toggles and leaves status read-only', async () => {
  const persisted: boolean[] = []
  const toggle = await executeDesktopTipsCommand('/tips', true, async (enabled) => {
    persisted.push(enabled)
    return { chat: { show_tips: enabled } }
  })
  assert.equal(toggle?.enabled, false)
  assert.deepEqual(toggle?.saved, { chat: { show_tips: false } })
  assert.deepEqual(persisted, [false])

  const status = await executeDesktopTipsCommand('/tips status', false, async (enabled) => {
    persisted.push(enabled)
    return { chat: { show_tips: enabled } }
  })
  assert.equal(status?.enabled, false)
  assert.equal(status?.saved, null)
  assert.deepEqual(persisted, [false])
})

test('tips command modes resolve enabled state', () => {
  assert.equal(resolveDesktopTipsEnabled('toggle', true), false)
  assert.equal(resolveDesktopTipsEnabled('toggle', false), true)
  assert.equal(resolveDesktopTipsEnabled('on', false), true)
  assert.equal(resolveDesktopTipsEnabled('off', true), false)
  assert.equal(resolveDesktopTipsEnabled('status', false), false)
})
