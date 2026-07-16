import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const composerSource = readFileSync(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
const pickerSource = readFileSync(new URL('./agent-picker.tsx', import.meta.url), 'utf8')
const modePickerSource = readFileSync(new URL('./mode-picker.tsx', import.meta.url), 'utf8')
const settingsSource = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('plan is a toggle control rather than a desktop mode picker', () => {
  assert.match(composerSource, /<ModePicker mode=\{mode\}[\s\S]*<AgentPicker/)
  assert.match(composerSource, /triggerClassName="h-full shrink-0 px-2"/)
  assert.match(modePickerSource, /aria-label=\{`\$\{planEnabled \? 'Disable' : 'Enable'\} plan mode`\}/)
  assert.match(modePickerSource, /aria-pressed=\{planEnabled\}/)
  assert.match(modePickerSource, /hover:-translate-y-0\.5 hover:shadow-sm/)
  assert.match(pickerSource, /hover:-translate-y-0\.5[\s\S]*hover:shadow-sm/)
  assert.match(composerSource, /hover:-translate-y-0\.5/)
  assert.doesNotMatch(modePickerSource, /border-b-2|hover:border-\[var\(--app-border-accent\)\]/)
  assert.doesNotMatch(pickerSource, /border-b-2|hover:border-\[var\(--app-border-accent\)\]/)
  assert.doesNotMatch(composerSource, /border-b-2|hover:border-\[var\(--app-border-accent\)\]/)
  assert.match(modePickerSource, />plan<\/span>/)
  assert.match(modePickerSource, /onClick=\{\(\) => onSelect\(nextMode\)\}/)
  assert.doesNotMatch(modePickerSource, /createPortal|ChevronDown|ChevronsUp|setOpen|>\{mode\}<\/span>|PlanHoverPreview|hoverPreview/)
  assert.doesNotMatch(pickerSource, /DesktopSessionMode|onModeSelect|Session mode for/)
})

test('Agent Setup presents the default auto value as Action and owns model policy', () => {
  assert.match(settingsSource, /Default session mode/)
  assert.match(settingsSource, /Agent model policy/)
  assert.match(settingsSource, /label="Single"/)
  assert.match(settingsSource, /label="Split"/)
  assert.match(settingsSource, /label="Action" onClick=\{\(\) => setDraftSessionMode\('auto'\)\}/)
  assert.match(settingsSource, /Plan model/)
  assert.match(settingsSource, /Action model/)
  assert.doesNotMatch(settingsSource, /label="Auto"|Auto model|Plan\/Auto/)
})
