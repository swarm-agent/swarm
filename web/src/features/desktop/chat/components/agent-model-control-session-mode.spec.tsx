import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const composerSource = readFileSync(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
const pickerSource = readFileSync(new URL('./agent-picker.tsx', import.meta.url), 'utf8')
const modePickerSource = readFileSync(new URL('./mode-picker.tsx', import.meta.url), 'utf8')
const settingsSource = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('Auto/Plan is a separate left-side control rather than part of the agent picker', () => {
  assert.match(composerSource, /<ModePicker mode=\{mode\}[\s\S]*<AgentPicker/)
  assert.match(composerSource, /triggerClassName="h-full shrink-0 px-2"/)
  assert.match(modePickerSource, /aria-label=\{`Session mode: \$\{mode\}\. Switch to \$\{nextMode\}`\}/)
  assert.match(modePickerSource, /onClick=\{\(\) => onSelect\(nextMode\)\}/)
  assert.doesNotMatch(modePickerSource, /createPortal|ChevronDown|setOpen/)
  assert.doesNotMatch(pickerSource, /DesktopSessionMode|onModeSelect|Session mode for/)
})

test('Agent Setup owns default mode and single/split model policy', () => {
  assert.match(settingsSource, /Default session mode/)
  assert.match(settingsSource, /Agent model policy/)
  assert.match(settingsSource, /label="Single"/)
  assert.match(settingsSource, /label="Split"/)
  assert.match(settingsSource, /Plan model/)
  assert.match(settingsSource, /Auto model/)
})
