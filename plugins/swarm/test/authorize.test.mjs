// Requirement: approved local onboarding hands a scoped credential directly to
// the adapter without disclosure, duplicate issuance or clobbering settings.
// Authority: authorizeConnection/ownerCommand and swarmctl setup sdk-token.
// Real private files plus an injected owner-command boundary narrowly test
// failure postconditions; this is not evidence of a live daemon or agent run.
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, readFile, stat, readdir, chmod, symlink, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { authorizeConnection, ownerCommand } from '../bin/lib/authorize.mjs';

const token = 'swk_fixture-only-not-a-real-key';
const baseline = JSON.stringify({ mode: 'http', url: 'https://swarm.example/mcp' });
async function fixture(t) {
  const dir = await mkdtemp(join(tmpdir(), 'swarm-mcp-authorize-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const path = join(dir, 'connection.json');
  await writeFile(path, baseline, { mode: 0o600 });
  return { dir, path, binary: join(dir, 'swarmctl') };
}
const response = () => JSON.stringify({ token, id: 'fixture-grant-id' });

test('private handoff uses write scope, preserves revocation ID and refuses duplicate issuance', async t => {
  const input = await fixture(t), calls = [];
  const run = async (...args) => { calls.push(args); return args[2][1] === 'sdk-token' ? response() : '{}'; };
  const result = await authorizeConnection({ ...input, sudo: true }, run);
  assert.equal(JSON.stringify(result).includes(token), false);
  assert.equal(calls.length, 2);
  assert.deepEqual(calls[1][2].slice(4), ['--ai-access', 'write', '--expires-in-seconds', '3600']);
  assert.equal(calls.every(c => c[0] === input.binary && c[1] === true), true);
  const config = JSON.parse(await readFile(input.path, 'utf8'));
  assert.equal(config.url, 'https://swarm.example/mcp');
  assert.equal(await readFile(config.tokenFile, 'utf8'), token);
  assert.equal((await stat(config.tokenFile)).mode & 0o777, 0o600);
  assert.equal((await stat(input.path)).mode & 0o777, 0o600);
  const grant = JSON.parse(await readFile(result.grantFile, 'utf8'));
  assert.equal(grant.id, 'fixture-grant-id');
  assert.equal(JSON.stringify(grant).includes(token), false);
  await assert.rejects(authorizeConnection(input, run), /existing tokenFile/);
  assert.equal(calls.length, 2);
});

test('unsafe files and an existing authorization lock stop before owner commands', async t => {
  const input = await fixture(t);
  let calls = 0; const run = async () => { calls++; return response(); };
  await chmod(input.path, 0o644);
  await assert.rejects(authorizeConnection(input, run), /private connection/);
  await chmod(input.path, 0o600);
  const link = join(input.dir, 'link.json'); await symlink(input.path, link);
  await assert.rejects(authorizeConnection({ ...input, path: link }, run), /private connection/);
  await writeFile(input.path + '.authorize-lock', 'existing recovery marker', { mode: 0o600 });
  await assert.rejects(authorizeConnection(input, run), /requires recovery/);
  assert.equal(calls, 0);
  assert.equal(await readFile(input.path + '.authorize-lock', 'utf8'), 'existing recovery marker');
  assert.equal(await readFile(input.path, 'utf8'), baseline);
});

test('owner access failure never mints a key, leaks output or blocks a later attempt', async t => {
  const input = await fixture(t), calls = [];
  await assert.rejects(authorizeConnection({ ...input, sudo: true }, async (...args) => {
    calls.push(args); throw Error(token);
  }), error => error.message.includes('sudo -v') && !error.message.includes(token));
  assert.equal(calls.length, 1);
  assert.equal(calls[0][2][1], 'status');
  assert.deepEqual(await readdir(input.dir), ['connection.json']);
  assert.equal(await readFile(input.path, 'utf8'), baseline);
});

test('sandbox denial directs the user to a normal terminal without creating a key', async t => {
  const input = await fixture(t);
  let calls = 0;
  await assert.rejects(authorizeConnection({ ...input, sudo: true }, async () => {
    calls++;
    throw Object.assign(Error('private diagnostics'), { code: 'SUDO_SANDBOX_BLOCKED' });
  }), /sandbox prevents sudo/);
  assert.equal(calls, 1);
  assert.deepEqual(await readdir(input.dir), ['connection.json']);
});

test('a concurrent config edit revokes only the new credential and preserves the edit', async t => {
  const input = await fixture(t), calls = [];
  const changed = JSON.stringify({ mode: 'native' });
  await assert.rejects(authorizeConnection(input, async (_binary, _sudo, args) => {
    calls.push(args);
    if (args[1] === 'sdk-token') { await writeFile(input.path, changed); return response(); }
    return '{}';
  }), /new key was revoked/);
  assert.deepEqual(calls[2], ['setup', 'revoke-sdk-token', '--id', 'fixture-grant-id']);
  assert.equal(await readFile(input.path, 'utf8'), changed);
  const credential = (await readdir(input.dir)).find(name => name.startsWith('credential-'));
  assert.equal(JSON.parse(await readFile(join(input.dir, credential, 'grant.json'), 'utf8')).state, 'revoked');
  await assert.rejects(stat(input.path + '.authorize-lock'), { code: 'ENOENT' });
});

test('uncertain issuance or revocation retains recovery state and prevents retries', async t => {
  for (const failRevocation of [false, true]) {
    const input = await fixture(t), calls = [];
    const run = async (_binary, _sudo, args) => {
      calls.push(args);
      if (args[1] === 'sdk-token') {
        if (!failRevocation) throw Error(token);
        // A valid ID with a malformed token must still be revoked.
        return JSON.stringify({ id: 'fixture-grant-id', token: 'invalid' });
      }
      if (args[1] === 'revoke-sdk-token') throw Error(token);
      return '{}';
    };
    await assert.rejects(authorizeConnection(input, run), error => !error.message.includes(token));
    const count = calls.length;
    await assert.rejects(authorizeConnection(input, run), /requires recovery/);
    assert.equal(calls.length, count);
    assert.equal(await readFile(input.path, 'utf8'), baseline);
    assert.equal((await stat(input.path + '.authorize-lock')).isFile(), true);
    if (failRevocation) {
      const { directory } = JSON.parse(await readFile(input.path + '.authorize-lock', 'utf8'));
      assert.equal(JSON.parse(await readFile(join(directory, 'grant.json'), 'utf8')).id, 'fixture-grant-id');
    }
  }
});

test('owner command suppresses failed command output', async t => {
  const input = await fixture(t);
  await writeFile(input.binary, '#!/bin/sh\nprintf "swk_fixture-secret-on-stdout"\nprintf "swk_fixture-secret-on-stderr" >&2\nexit 1\n', { mode: 0o700 });
  await assert.rejects(ownerCommand(input.binary, false, []), error => error.message === 'Owner command failed.' && !Object.hasOwn(error, 'stdout'));
});
