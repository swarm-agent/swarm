import assert from 'node:assert/strict'
import test from 'node:test'

import {
  DESKTOP_SIDEBAR_DISPLAY_STORAGE_KEY,
  effectiveDesktopSidebarDisplayMode,
  loadDesktopSidebarDisplayMode,
  normalizeDesktopSidebarDisplayMode,
  saveDesktopSidebarDisplayMode,
} from './desktop-sidebar-display'

test('sidebar display normalization accepts full compact and thin only', () => {
  assert.equal(normalizeDesktopSidebarDisplayMode('full'), 'full')
  assert.equal(normalizeDesktopSidebarDisplayMode('compact'), 'compact')
  assert.equal(normalizeDesktopSidebarDisplayMode('thin'), 'thin')
  assert.equal(normalizeDesktopSidebarDisplayMode('collapsed'), 'full')
})

test('responsive sidebar mode downgrades without changing the preference', () => {
  const preferred = 'full' as const
  assert.equal(effectiveDesktopSidebarDisplayMode(preferred, 1440), 'full')
  assert.equal(effectiveDesktopSidebarDisplayMode(preferred, 900), 'compact')
  assert.equal(effectiveDesktopSidebarDisplayMode(preferred, 680), 'thin')
  assert.equal(preferred, 'full')
  assert.equal(effectiveDesktopSidebarDisplayMode('compact', 1440), 'compact')
  assert.equal(effectiveDesktopSidebarDisplayMode('thin', 1440), 'thin')
})

test('sidebar display mode persists through client-local storage', () => {
  const previousWindow = globalThis.window
  const values = new Map<string, string>()
  globalThis.window = {
    localStorage: {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => { values.set(key, value) },
    },
  } as unknown as Window & typeof globalThis
  try {
    saveDesktopSidebarDisplayMode('compact')
    assert.equal(values.get(DESKTOP_SIDEBAR_DISPLAY_STORAGE_KEY), 'compact')
    assert.equal(loadDesktopSidebarDisplayMode(), 'compact')
    saveDesktopSidebarDisplayMode('thin')
    assert.equal(loadDesktopSidebarDisplayMode(), 'thin')
  } finally {
    globalThis.window = previousWindow
  }
})
