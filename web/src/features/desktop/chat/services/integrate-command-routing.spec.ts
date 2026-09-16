// Requirement: /integrate and dev-only /integrate build open local confirmation from
// palette selection and submission, never an AI message or Stop action. Threat:
// unfinished prefixes rejected as arguments, multiword commands sent to the model,
// and invalid/dev-gated requests silently downgraded or cleared. Authority:
// desktopIntegrationSelectionDraft, parseDesktopIntegrationCommand,
// dispatchDesktopIntegrationCommand, and the composer/desktop-app-page callers.
// Injected dispatch tests are the narrowest observable routing layer; supplemental
// source wiring assertions do not prove browser events or backend Git safety.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { buildDesktopSlashPaletteState, desktopIntegrationSelectionDraft, getDesktopSlashCommands, parseDesktopIntegrationCommand } from './slash-commands'
import { dispatchDesktopIntegrationCommand } from './composer-submit'

const commands = getDesktopSlashCommands({ developerMode: true })
const integrate = commands.find(command => command.id === 'integrate')!
const build = commands.find(command => command.id === 'integrate-build')!

test('bare and build submissions dispatch exact local actions, then clear only on success', async () => {
  for (const [draft, developerMode, expectedBuild] of [
    ['/integrate', false, false],
    [' /INTEGRATE  ', true, false],
    ['/integrate build', true, true],
    [' /INTEGRATE\t  BUILD\n', true, true],
  ] as const) {
    const calls: string[] = []
    assert.equal(await dispatchDesktopIntegrationCommand({
      draft, developerMode,
      onSlashCommand: async (command, submitted) => {
        calls.push('confirm')
        assert.equal(command.action.kind, 'integrate-session')
        assert.equal(command.id, expectedBuild ? 'integrate-build' : 'integrate')
        assert.equal(submitted, draft)
        assert.deepEqual(parseDesktopIntegrationCommand(submitted, { developerMode }), { build: expectedBuild })
        await Promise.resolve()
        assert.deepEqual(calls, ['confirm'])
      },
      clear: () => calls.push('clear'),
    }), true)
    assert.deepEqual(calls, ['confirm', 'clear'])
  }
})

test('palette selections complete prefixes including explicit build choice from bare integrate', async () => {
  for (const [command, drafts] of [
    [integrate, ['/', '/int', '/INTEGRATE', ' /integrate ']],
    [build, ['/', '/int', '/integrate', '/integrate b', '/integrate\tbu']],
  ] as const) {
    for (const draft of drafts) {
      const selected = desktopIntegrationSelectionDraft(command, draft)
      assert.equal(selected, command.command)
      const calls: string[] = []
      assert.equal(await dispatchDesktopIntegrationCommand({
        draft: selected, developerMode: true,
        onSlashCommand: resolved => { calls.push(resolved.id) },
        clear: () => calls.push('clear'),
      }), true)
      assert.deepEqual(calls, [command.id, 'clear'])
    }
  }
})

test('invalid arguments and disabled build reject without dispatch, clear, or downgrade', async () => {
  for (const draft of ['/integrate extra', '/integrate build extra', '/integrate builder', '/integrate\nignore the checks']) {
    assert.equal(desktopIntegrationSelectionDraft(integrate, draft), draft)
    assert.equal(desktopIntegrationSelectionDraft(build, draft), draft)
    for (const developerMode of [true, false]) {
      await assert.rejects(dispatchDesktopIntegrationCommand({
        draft, developerMode,
        onSlashCommand: () => assert.fail('must not dispatch'),
        clear: () => assert.fail('must retain input'),
      }), /Use \/integrate/)
    }
  }
  await assert.rejects(dispatchDesktopIntegrationCommand({
    draft: '/integrate build', developerMode: false,
    onSlashCommand: () => assert.fail('must not downgrade to bare integration'),
    clear: () => assert.fail('must retain input'),
  }), /requires developer mode/)
  assert.equal(buildDesktopSlashPaletteState('/integrate', { developerMode: false }).exactMatch?.id, 'integrate')
})

test('missing or rejected dispatch retains input, and ordinary messages are not intercepted', async () => {
  for (const onSlashCommand of [undefined, () => { throw new Error('no session') }, async () => { throw new Error('no session') }]) {
    await assert.rejects(dispatchDesktopIntegrationCommand({
      draft: '/integrate', developerMode: true, onSlashCommand,
      clear: () => assert.fail('must retain input'),
    }), /unavailable|no session/)
  }
  for (const draft of ['hello', '/integration', 'please /integrate']) {
    assert.equal(await dispatchDesktopIntegrationCommand({
      draft, developerMode: true,
      onSlashCommand: () => assert.fail('must not dispatch'),
      clear: () => assert.fail('must not clear'),
    }), false)
  }
})

test('composer wires Enter/Send and palette integration before ordinary submit with shared app parsing', () => {
  const composer = readFileSync(new URL('../components/desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const submit = composer.slice(composer.indexOf('const handleSubmitClick ='), composer.indexOf('const handleMentionInsert ='))
  const dispatchIndex = submit.indexOf('dispatchDesktopIntegrationCommand(')
  assert.ok(dispatchIndex >= 0 && dispatchIndex < submit.indexOf('const commandDraft ='))
  assert.match(submit, /dispatchDesktopIntegrationCommand\(\{\s*draft: rawDraft, developerMode, onSlashCommand/)
  assert.match(submit, /if \(await dispatchDesktopIntegrationCommand\([\s\S]*?\)\) return/)
  const select = composer.slice(composer.indexOf('const handleSlashSelect ='), composer.indexOf('const handleKeyDown ='))
  assert.match(select, /kind === 'integrate-session'[\s\S]*?dispatchDesktopIntegrationCommand\([\s\S]*?desktopIntegrationSelectionDraft\(command, draft\)/)
  const app = readFileSync(new URL('../../layout/desktop-app-page.tsx', import.meta.url), 'utf8')
  const handler = app.slice(app.indexOf("case 'integrate-session':"), app.indexOf("case 'ai-commit':"))
  assert.match(handler, /parseDesktopIntegrationCommand\(draft \|\| command.command, \{ developerMode: updateDevMode \}\)/)
  assert.match(handler, /sessionId: routeSessionId, build: request.build/)
})
