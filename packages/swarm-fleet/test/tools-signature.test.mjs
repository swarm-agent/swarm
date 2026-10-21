// A machine update that changes a tool's schema (not the number of tools)
// must still tell the client to re-read the list, or the client keeps the old
// schema and refuses new arguments (seen with swarm_set_workflow's result).
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { toolsSignature } from '../server.mjs';

test('a changed tool schema changes what the client is told', () => {
  const before = [{ name: 'swarm_set_workflow', description: 'Set a workflow', inputSchema: { type: 'object', properties: { name: { type: 'string' } } } }];
  const after = [{ ...before[0], inputSchema: { type: 'object', properties: { name: { type: 'string' }, result: { type: 'object' } } } }];
  assert.notEqual(toolsSignature(before), toolsSignature(after));
  assert.equal(toolsSignature(before), toolsSignature(structuredClone(before)));
  assert.notEqual(toolsSignature(before), toolsSignature([{ ...before[0], description: 'Set or update a workflow' }]));
});
