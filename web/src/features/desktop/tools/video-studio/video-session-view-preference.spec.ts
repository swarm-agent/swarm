import assert from 'node:assert/strict'
import test from 'node:test'

import {
  loadVideoSessionViewPreference,
  preferredVideoSessionView,
  saveVideoSessionViewPreference,
} from './video-session-view-preference'

function withLocalStorage(run: (storage: Map<string, string>) => void): void {
  const storage = new Map<string, string>()
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      localStorage: {
        getItem: (key: string) => storage.get(key) ?? null,
        setItem: (key: string, value: string) => storage.set(key, value),
        removeItem: (key: string) => storage.delete(key),
      },
    },
  })
  try {
    run(storage)
  } finally {
    Reflect.deleteProperty(globalThis, 'window')
  }
}

test('video sessions default to Studio and remember Chat independently per session', () => {
  withLocalStorage(() => {
    assert.equal(preferredVideoSessionView('video-a'), 'studio')

    saveVideoSessionViewPreference(' video-a ', 'chat')

    assert.equal(loadVideoSessionViewPreference('video-a'), 'chat')
    assert.equal(preferredVideoSessionView('video-a'), 'chat')
    assert.equal(preferredVideoSessionView('video-b'), 'studio')
  })
})

test('returning to Studio replaces the remembered Chat preference', () => {
  withLocalStorage(() => {
    saveVideoSessionViewPreference('video-a', 'chat')
    saveVideoSessionViewPreference('video-a', 'studio')

    assert.equal(preferredVideoSessionView('video-a'), 'studio')
  })
})
