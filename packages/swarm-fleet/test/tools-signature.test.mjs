// A machine update that changes a tool's schema (not the number of tools)
// must still tell the client to re-read the list, or the client keeps the old
// schema and refuses new arguments (including swarm_set_workflow's on).
// Purpose: toolsSignature is the fleet cache boundary; changing trigger fields
// must invalidate the cached tools even when tool count and names stay fixed.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { toolsSignature } from '../server.mjs';

test('a changed tool schema changes what the client is told', () => {
  const before = [{ name: 'swarm_set_workflow', description: 'Set a workflow', inputSchema: { type: 'object', properties: { name: { type: 'string' } } } }];
  const after = [{ ...before[0], inputSchema: { type: 'object', properties: { name: { type: 'string' }, on: { type: 'object', properties: { event: { type: 'object', properties: { source: { type: 'string', enum: ['pull', 'webhook'] }, url: { type: 'string' }, every_seconds: { type: 'integer' }, secret: { type: 'string' }, items: { type: 'string' }, id: { type: 'string' } } }, start: { type: 'object', properties: { worker: { type: 'string' }, brief: { type: 'string' } } } } }, rotate_key: { type: 'boolean' } } } }];
  assert.notEqual(toolsSignature(before), toolsSignature(after));
  // Reusable inputs must invalidate the same schema cache; unchanged copies must not.
  const reusable = structuredClone(after);
  reusable[0].inputSchema.properties.inputs = { type: 'object' };
  reusable[0].inputSchema.properties.with = { type: 'object', additionalProperties: { type: 'string' } };
  assert.notEqual(toolsSignature(after), toolsSignature(reusable));
  assert.equal(toolsSignature(reusable), toolsSignature(structuredClone(reusable)));
  assert.equal(toolsSignature(before), toolsSignature(structuredClone(before)));
  assert.notEqual(toolsSignature(before), toolsSignature([{ ...before[0], description: 'Set or update a workflow' }]));
});
