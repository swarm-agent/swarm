// Purpose: creativeThreads/orderCreativeTurns are presentation-only lineage boundaries.
// Exact output ownership must connect request turns, never titles, input order or
// ambiguous references. Pure graph tests are the narrowest proof of grouping;
// they do not establish backend ownership, authorization or live generation.
import assert from 'node:assert/strict'
import test from 'node:test'
import { creativeThreads, orderCreativeTurns, type CreativeLineageNode } from './creative-thread'

const node = (id: string, outputs: string[] = [id], parents: string[] = []): CreativeLineageNode<string> => ({ id, value: id, outputs, parents })

test('reverse hydration order and duplicate requests retain one root and sibling candidates in one turn', () => {
  const root = node('request', ['a', 'b'])
  const edit = node('edit', ['c', 'd'], ['b'])
  const child = node('child', [], ['c'])
  assert.deepEqual(creativeThreads([child, edit, root, root]), [{ id: 'request', turns: ['request', 'edit', 'child'] }])
  assert.deepEqual(orderCreativeTurns([child, edit, root]), ['request', 'edit', 'child'])
  assert.deepEqual(creativeThreads([root, node('unrelated')]), [
    { id: 'request', turns: ['request'] }, { id: 'unrelated', turns: ['unrelated'] },
  ])
})

test('missing parents, conflicting ownership and multi-root requests never fuse creations', () => {
  const first = node('first', ['a', 'shared'])
  const second = node('second', ['b', 'shared'])
  for (const child of [node('missing', [], ['absent']), node('ambiguous', [], ['shared']), node('multi-root', [], ['a', 'b'])]) {
    assert.deepEqual(creativeThreads([first, second, child]), [
      { id: 'first', turns: ['first'] }, { id: 'second', turns: ['second'] }, { id: child.id, turns: [child.id] },
    ])
  }
  // Two candidate bases from the same root still represent one continuation.
  assert.deepEqual(creativeThreads([node('root', ['a', 'b']), node('edit', [], ['a', 'b'])]), [{ id: 'root', turns: ['root', 'edit'] }])
})

test('self references and cycles terminate without combining cyclic requests', () => {
  assert.deepEqual(creativeThreads([node('self', ['self'], ['self'])]), [{ id: 'self', turns: ['self'] }])
  const a = node('a', ['out-a'], ['out-b'])
  const b = node('b', ['out-b'], ['out-a'])
  for (const input of [[a, b], [b, a]]) {
    const result = creativeThreads(input)
    assert.equal(result.length, 2)
    for (const thread of result) assert.deepEqual(thread.turns, [thread.id])
  }
  assert.deepEqual(creativeThreads([]), [])
})
