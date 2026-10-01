import test from 'node:test'
import assert from 'node:assert/strict'
import { ORCHESTRATE_COMMANDS, filterOrchestrateCommands, parseOrchestrateCommand } from './orchestrate-commands'

// Purpose: Orchestrate owns a navigation-only allowlist. The parser is the
// narrowest boundary preventing legacy command dispatch or accidental AI runs
// while preserving ordinary prose, URLs, paths and code containing slash text.
test('Orchestrate command boundaries normalize case and reject arguments/legacy commands', () => {
  assert.deepEqual(ORCHESTRATE_COMMANDS.map((entry) => entry.name), ['help', 'projects', 'tasks', 'workers', 'deliverables', 'agents', 'settings'])
  for (const command of ORCHESTRATE_COMMANDS) {
    assert.deepEqual(parseOrchestrateCommand(` \t/${command.name.toUpperCase()} \n`), { kind: 'command', command })
    assert.equal(parseOrchestrateCommand(`/${command.name} please`).kind, 'unsupported')
  }
  for (const banned of ['actions', 'artifact', 'commit', 'commit ai', 'integrate', 'plan', 'task plan', 'task', 'settings extra', 'unknown']) {
    for (const form of [`/${banned}`, ` \t/${banned.toUpperCase().replaceAll(' ', '\t')}\n`]) {
      assert.equal(parseOrchestrateCommand(form).kind, 'unsupported', form)
    }
  }
  for (const prose of ['Discuss /commit ai', 'https://example.test/path', '/src/commit', '/settings.json', '/commit/ai', '//comment', '`/plan`', 'const x = /plan/i', 'Use /settings tomorrow']) {
    assert.equal(parseOrchestrateCommand(prose).kind, 'message', prose)
  }
})

// Purpose: filtering must remain within the same allowlist, with no aliases.
test('Orchestrate palette filters without admitting old chat aliases', () => {
  assert.equal(filterOrchestrateCommands('/AGENT')[0]?.name, 'agents')
  assert.equal(filterOrchestrateCommands('/setting')[0]?.name, 'settings')
  assert.equal(filterOrchestrateCommands('/').length, 7)
  for (const banned of ['actions', 'artifact', 'commit', 'commit ai', 'integrate', 'plan', 'task plan']) assert.equal(filterOrchestrateCommands(banned).length, 0)
})
