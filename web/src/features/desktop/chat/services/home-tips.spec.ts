import assert from 'node:assert/strict'
import test from 'node:test'

import {
  DESKTOP_HOME_TIPS,
  executeDesktopTipsCommand,
  parseDesktopTipsCommand,
  resolveDesktopTipsEnabled,
  selectDesktopHomeTipIndex,
} from './home-tips'

test('desktop home tip catalog contains 27 concise supported tips in order', () => {
  assert.equal(DESKTOP_HOME_TIPS.length, 27)
  assert.equal(DESKTOP_HOME_TIPS[0], 'Ask Swarm for three theme variants, then pick one.')
  assert.equal(DESKTOP_HOME_TIPS[24], 'TUI: Ctrl+P opens plan and checkpoint status.')
  assert.equal(DESKTOP_HOME_TIPS[26], 'Type /tips to hide or show these tips.')
  assert.equal(DESKTOP_HOME_TIPS.includes('Drop a Todo into chat to start working on it.'), false)
  assert.equal(DESKTOP_HOME_TIPS.includes('Ask Swarm to update several workspace Todos.'), false)
  assert.equal(DESKTOP_HOME_TIPS.includes('Integrate selected Coder branches as one batch.'), false)
  assert.equal(DESKTOP_HOME_TIPS.includes('TUI: Ctrl+W switches the workspace directory.'), false)
  assert.equal(DESKTOP_HOME_TIPS.every((tip) => tip.length <= 56), true)
})

test('desktop home tip selection chooses a launch tip and avoids the previous tip', () => {
  assert.equal(selectDesktopHomeTipIndex(-1, () => 0), 0)
  assert.equal(selectDesktopHomeTipIndex(-1, () => 0.999), 26)
  assert.equal(selectDesktopHomeTipIndex(0, () => 0), 1)
  assert.equal(selectDesktopHomeTipIndex(26, () => 0), 0)
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
