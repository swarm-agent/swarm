import assert from 'node:assert/strict'
import test from 'node:test'
import { desktopEffectiveThemeId } from './desktop-theme-controller'

test('uses workspace theme from the URL-selected workspace over global settings', () => {
  const themeId = desktopEffectiveThemeId('/repo', [
    { path: '/repo', themeId: 'midnight' },
  ], { theme: { active_id: 'crimson' } })

  assert.equal(themeId, 'midnight')
})

test('returns null while a URL-selected workspace has not resolved', () => {
  const themeId = desktopEffectiveThemeId('/repo', [], { theme: { active_id: 'crimson' } })

  assert.equal(themeId, null)
})

test('returns null while a URL-selected workspace path has not resolved', () => {
  const themeId = desktopEffectiveThemeId(null, [], { theme: { active_id: 'crimson' } })

  assert.equal(themeId, null)
})

test('can fall back to global settings when there is no URL-selected workspace to wait for', () => {
  const themeId = desktopEffectiveThemeId(null, [], { theme: { active_id: 'crimson' } }, false)

  assert.equal(themeId, 'crimson')
})

test('falls back to global settings only when the selected workspace inherits', () => {
  const themeId = desktopEffectiveThemeId('/repo', [
    { path: '/repo', themeId: '' },
  ], { theme: { active_id: 'ember' } })

  assert.equal(themeId, 'ember')
})
