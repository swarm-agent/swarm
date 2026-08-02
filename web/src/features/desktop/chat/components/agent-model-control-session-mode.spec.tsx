import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const composerSource = readFileSync(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
const modePickerSource = readFileSync(new URL('./mode-picker.tsx', import.meta.url), 'utf8')
const settingsSource = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('plan remains a composer lifecycle toggle rather than a favorite bundle mode', () => {
  assert.match(modePickerSource, /aria-label=\{`\$\{planEnabled \? 'Disable' : 'Enable'\} plan mode`\}/)
  assert.match(modePickerSource, /aria-pressed=\{planEnabled\}/)
  assert.match(composerSource, /const planToggle = \(\) => onModeSelect\?\.\(mode === 'plan' \? 'auto' : 'plan'\)/)
  assert.doesNotMatch(settingsSource, /Default session mode|defaultSessionMode: draftSessionMode|SessionModeChoices|DesktopSessionMode/)
})

test('agent setup exposes direct Swarm mode models without session-mode profile controls', () => {
  assert.match(settingsSource, /title="Default Model"/)
  assert.match(settingsSource, /title="Plan Model"/)
  assert.match(settingsSource, /saveSystemAgentSettings/)
  assert.doesNotMatch(settingsSource, /Saved profiles|Profile settings|Make account default/)
  assert.doesNotMatch(settingsSource, /mode\s*===\s*['"]plan['"]|mode\s*===\s*['"]auto['"]/)
})
