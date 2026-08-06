import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { moveFavoriteIds, validateFlatModelFavorite, type FlatModelFavorite } from './model-favorites-settings'

const source = readFileSync(new URL('./model-favorites-settings.tsx', import.meta.url), 'utf8')

const favorites: FlatModelFavorite[] = [
  { id: 'alpha', name: 'Fast', provider: 'codex', model: 'gpt-fast', thinking: 'medium', serviceTier: 'fast', contextMode: '', isDefault: true },
  { id: 'beta', name: 'Deep', provider: 'codex', model: 'gpt-deep', thinking: 'high', serviceTier: '', contextMode: 'large', isDefault: false },
  { id: 'gamma', name: 'Local', provider: 'local', model: 'small', thinking: '', serviceTier: '', contextMode: '', isDefault: false },
]

test('flat favorite validation requires a name and one provider/model pair', () => {
  assert.deepEqual(validateFlatModelFavorite({ name: '', provider: '', model: '', thinking: '', serviceTier: '', contextMode: '' }), [
    'Enter a favorite name.',
    'Choose a model.',
  ])
  assert.deepEqual(validateFlatModelFavorite({ name: ' Daily ', provider: 'codex', model: 'gpt-fast', thinking: '', serviceTier: '', contextMode: '' }), [])
})

test('ordering helper moves one favorite without changing boundary order', () => {
  assert.deepEqual(moveFavoriteIds(favorites, 'beta', -1), ['beta', 'alpha', 'gamma'])
  assert.deepEqual(moveFavoriteIds(favorites, 'beta', 1), ['alpha', 'gamma', 'beta'])
  assert.deepEqual(moveFavoriteIds(favorites, 'alpha', -1), ['alpha', 'beta', 'gamma'])
  assert.deepEqual(moveFavoriteIds(favorites, 'missing', 1), ['alpha', 'beta', 'gamma'])
})

test('settings component exposes pure callbacks for flat favorite management', () => {
  assert.match(source, /favorites: FlatModelFavorite\[\]/)
  assert.match(source, /modelOptions: FlatModelOption\[\]/)
  assert.match(source, /onCreate:/)
  assert.match(source, /onUpdate:/)
  assert.match(source, /onDelete:/)
  assert.match(source, /onReorder:/)
  assert.match(source, /onSetDefault:/)
  assert.doesNotMatch(source, /useQuery|useMutation|fetch\(|apiRequest/)
})

test('favorite UI includes explicit errors, CRUD, default, and accessible ordering controls', () => {
  assert.match(source, /role="alert"/)
  assert.match(source, /Add favorite/)
  assert.match(source, /Create favorite/)
  assert.match(source, /Save favorite/)
  assert.match(source, /Delete favorite/)
  assert.match(source, /Set default/)
  assert.match(source, /aria-label=\{`Move \$\{favorite\.name\} up`\}/)
  assert.match(source, /aria-label=\{`Move \$\{favorite\.name\} down`\}/)
})

test('favorite language describes one flat model choice', () => {
  assert.match(source, /Model favorites/)
  assert.match(source, /one provider and model/)
  assert.match(source, /Each favorite contains one provider and model/)
})
