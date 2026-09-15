import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('agent setup modal includes dedicated Favorites section in navigation sidebar', () => {
  assert.match(source, /key="favorites-nav-section"/)
  assert.match(source, /setSetupSection\('favorites'\)/)
  assert.match(source, /Model favorites/)
  assert.match(source, /modelProfiles\.length/)
})

test('star button to create favorite is removed from Default Model draft card header', () => {
  assert.doesNotMatch(source, /aria-label="Save Default Model as favorite"/)
  assert.match(source, /title="Default Model"/)
  assert.match(source, /title="Plan Model"/)
})

test('favorites management view provides offer banner when default model is not already saved', () => {
  assert.match(source, /defaultMatchesFavorite/)
  assert.match(source, /Make Default Model a favorite\?/)
  assert.match(source, /Accept & Name Favorite/)
  assert.match(source, /createFavoriteFromDefaultOffer/)
  assert.match(source, /Current default model is saved as a favorite/)
})

test('favorites management view supports edit, delete, and multi-delete with selection', () => {
  // Multi-delete selection state and handlers
  assert.match(source, /selectedFavoriteIds/)
  assert.match(source, /toggleSelectFavorite/)
  assert.match(source, /toggleSelectAllFavorites/)
  assert.match(source, /deleteSelectedFavorites/)
  assert.match(source, /Delete selected/)
  assert.match(source, /Confirm multi-delete/)

  // Edit favorite
  assert.match(source, /startEditingFavorite/)
  assert.match(source, /saveEditingFavorite/)
  assert.match(source, /updateModelProfile\(editingFavoriteId/)
  assert.match(source, /Saving updates this favorite shortcut only/)

  // Single delete
  assert.match(source, /deleteFavorite\(profile\)/)
  assert.match(source, /Delete\?/)
})

test('quick favorites popover connects edit to favorites management section and provides direct manage button', () => {
  // Ellipsis edit opens setup on favorites section
  assert.match(source, /function editFavorite\(profile: ModelProfileRecord\)/)
  assert.match(source, /setSetupSection\('favorites'\)/)
  // Manage favorites button in popover footer
  assert.match(source, /Manage favorites/)
})
