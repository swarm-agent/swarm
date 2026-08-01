import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { buildSwarmModelAssignmentSaveInput } from './swarm-model-assignment-settings'

const source = readFileSync(new URL('./swarm-model-assignment-settings.tsx', import.meta.url), 'utf8')
const favoriteIds = ['favorite-action', 'favorite-plan']

test('Action assignment is always required and must reference an available flat favorite', () => {
  assert.deepEqual(buildSwarmModelAssignmentSaveInput({
    favoriteIds,
    actionFavoriteId: '',
    planEnabled: false,
  }), { value: null, error: 'Choose an Action favorite.' })

  assert.deepEqual(buildSwarmModelAssignmentSaveInput({
    favoriteIds,
    actionFavoriteId: 'missing',
    planEnabled: false,
  }), { value: null, error: 'Choose an Action favorite.' })
})

test('disabled Plan omits its assignment from the save contract', () => {
  assert.deepEqual(buildSwarmModelAssignmentSaveInput({
    favoriteIds,
    actionFavoriteId: ' favorite-action ',
    planEnabled: false,
    planFavoriteId: 'favorite-plan',
  }), {
    value: { actionFavoriteId: 'favorite-action', planEnabled: false },
    error: null,
  })
})

test('enabled Plan requires a distinct available favorite', () => {
  assert.deepEqual(buildSwarmModelAssignmentSaveInput({
    favoriteIds,
    actionFavoriteId: 'favorite-action',
    planEnabled: true,
  }), { value: null, error: 'Choose a Plan favorite.' })

  assert.deepEqual(buildSwarmModelAssignmentSaveInput({
    favoriteIds,
    actionFavoriteId: 'favorite-action',
    planEnabled: true,
    planFavoriteId: 'favorite-action',
  }), { value: null, error: 'Plan must use a different favorite from Action.' })

  assert.deepEqual(buildSwarmModelAssignmentSaveInput({
    favoriteIds,
    actionFavoriteId: 'favorite-action',
    planEnabled: true,
    planFavoriteId: 'favorite-plan',
  }), {
    value: {
      actionFavoriteId: 'favorite-action',
      planEnabled: true,
      planFavoriteId: 'favorite-plan',
    },
    error: null,
  })
})

test('component exposes one pure-prop save seam and explicit Action and Plan controls', () => {
  assert.match(source, /onSave: \(input: SwarmModelAssignmentSaveInput\) => void/)
  assert.match(source, />\s*Action <span/)
  assert.match(source, />Enable Plan assignment</)
  assert.match(source, />\s*Plan <span/)
  assert.match(source, /Plan must use a different favorite from Action/)
  assert.doesNotMatch(source, /requestJson|useQuery|useMutation/)
})
