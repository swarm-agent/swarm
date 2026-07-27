import assert from 'node:assert/strict'
import test from 'node:test'

import {
  DESKTOP_MAIN_SIDEBAR_MODE_STORAGE_KEY,
  loadDesktopMainSidebarMode,
  normalizeDesktopMainSidebarMode,
  saveDesktopMainSidebarMode,
} from './main-sidebar-focus-state'

test('main sidebar mode replaces legacy thin mode with focus mode', () => {
  assert.equal(normalizeDesktopMainSidebarMode('full'), 'full')
  assert.equal(normalizeDesktopMainSidebarMode('compact'), 'full')
  assert.equal(normalizeDesktopMainSidebarMode('focus'), 'focus')
  assert.equal(normalizeDesktopMainSidebarMode('thin'), 'focus')
  assert.equal(normalizeDesktopMainSidebarMode('collapsed'), 'full')
})

test('focus mode persists through best-effort local storage', () => {
  const previousWindow = globalThis.window
  const values = new Map<string, string>()
  globalThis.window = {
    localStorage: {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => { values.set(key, value) },
    },
  } as unknown as Window & typeof globalThis
  try {
    saveDesktopMainSidebarMode('focus')
    assert.equal(values.get(DESKTOP_MAIN_SIDEBAR_MODE_STORAGE_KEY), 'focus')
    assert.equal(loadDesktopMainSidebarMode(), 'focus')
  } finally {
    globalThis.window = previousWindow
  }
})

test('legacy thin preference restores as focus mode without changing plan sidebar storage', () => {
  const previousWindow = globalThis.window
  const values = new Map<string, string>([['swarm.web.desktop.sidebar.display-mode', 'thin']])
  globalThis.window = {
    localStorage: {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => { values.set(key, value) },
    },
  } as unknown as Window & typeof globalThis
  try {
    assert.equal(loadDesktopMainSidebarMode(), 'focus')
    saveDesktopMainSidebarMode('full')
    assert.equal(values.get('swarm.web.desktop.sidebar.display-mode'), 'thin')
  } finally {
    globalThis.window = previousWindow
  }
})
