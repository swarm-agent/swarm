import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { readFileSync } from 'node:fs'
import { renderToStaticMarkup } from 'react-dom/server'
import { AgentPicker } from './agent-picker'
import type { AgentProfileRecord } from '../types/chat'

function profile(name: string, overrides: Partial<AgentProfileRecord> = {}): AgentProfileRecord {
  return {
    name,
    mode: 'primary',
    description: '',
    provider: '',
    model: '',
    thinking: '',
    modelMode: 'single',
    planProvider: '',
    planModel: '',
    planThinking: '',
    planServiceTier: '',
    autoProvider: '',
    autoModel: '',
    autoThinking: '',
    autoServiceTier: '',
    prompt: '',
    runtimeMode: '',
    defaultSessionMode: 'plan',
    executionSetting: '',
    exitPlanModeEnabled: false,
    enabled: true,
    ...overrides,
  }
}

test('agent picker is separate from mode and keeps canonical switching and settings callbacks', () => {
  const pickerSource = readFileSync(new URL('./agent-picker.tsx', import.meta.url), 'utf8')
  const composerSource = readFileSync(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const existingPaneSource = readFileSync(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
  const newPaneSource = readFileSync(new URL('./desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')

  assert.match(pickerSource, /onClick=\{\(\) => handleSelect\(profile\.name\)\}/)
  assert.match(pickerSource, /pointerAnchorRef\.current = event\.detail > 0[\s\S]*clientX[\s\S]*clientY/)
  assert.match(pickerSource, /anchorX - width \/ 2/)
  assert.match(pickerSource, /viewportWidth - width - DROPDOWN_VIEWPORT_GUTTER/)
  assert.match(pickerSource, /viewportHeight - anchorY \+ DROPDOWN_VIEWPORT_GUTTER/)
  assert.match(pickerSource, /aria-label=\{`Open settings for \$\{profileLabel\(profile\)\}`\}/)
  assert.match(pickerSource, /onClick=\{\(\) => handleOpenSettings\(profile\.name\)\}/)
  assert.match(pickerSource, /aria-label=\{section\.ariaLabel\}/)
  assert.match(pickerSource, /Select profile/)
  assert.match(pickerSource, /label: '', ariaLabel: 'Profiles', profiles: primaryAgents/)
  assert.doesNotMatch(pickerSource, /label: 'Primary'/)
  assert.match(pickerSource, /\{section\.label \? \([\s\S]*section\.profiles\.map\(\(profile, profileIndex\)/)
  assert.match(pickerSource, /Shows thinking responses[\s\S]*role="switch"[\s\S]*onThinkingTagsToggle\(!thinkingTagsEnabled\)/)
  assert.match(pickerSource, /absolute left-1 top-1[\s\S]*thinkingTagsEnabled \? 'translate-x-5[\s\S]*: 'translate-x-0/)
  assert.match(pickerSource, /w-full overflow-hidden rounded-lg border/)
  assert.match(pickerSource, /const DESKTOP_DROPDOWN_WIDTH = 480/)
  assert.match(pickerSource, /left: DROPDOWN_VIEWPORT_GUTTER,[\s\S]*width: maxWidth,[\s\S]*maxWidth/)
  assert.match(pickerSource, /`Plan \$\{settingLabel[\s\S]*`Auto \$\{settingLabel/)
  assert.match(pickerSource, /profileModelLabels\(profile\)\.map\(\(label\) => \([\s\S]*className="block"/)
  assert.doesNotMatch(pickerSource, /💡|⚡|font-variant-emoji/)
  assert.doesNotMatch(pickerSource, /grid-cols-\[72px_minmax\(0,1fr\)\]/)
  assert.doesNotMatch(pickerSource, /\{profileModeLabel\(profile\)\}/)
  assert.doesNotMatch(pickerSource, /max-w-\[20rem\] truncate/)
  assert.doesNotMatch(pickerSource, /onModeSelect|Session mode for|selectedAgentPlanCapable/)
  assert.match(composerSource, /<ModePicker mode=\{mode\}[\s\S]*<AgentPicker/)
  assert.match(composerSource, /onOpenSettings=\{openAgentSetup\}/)
  assert.match(composerSource, /<AgentPicker[\s\S]*thinkingTagsEnabled=\{thinkingTagsEnabled\}[\s\S]*onThinkingTagsToggle=\{onThinkingTagsToggle\}/)
  assert.doesNotMatch(composerSource, /tags \{thinkingTagsEnabled \? 'on' : 'off'\}/)
  assert.match(composerSource, /<AgentModelControl[\s\S]*openSignal=\{effectiveAgentSetupOpenSignal\}[\s\S]*showTrigger=\{false\}/)
  assert.match(composerSource, /initialAgentName=\{effectiveAgentSetupInitialAgent\}/)
  assert.match(composerSource, /agentSettingsInitialAgent/)
  assert.match(existingPaneSource, /handleAgentSelect\(nextAgentName: string\)[\s\S]*await updateSessionV3Agent\([\s\S]*normalizedAgentName/)
  assert.match(newPaneSource, /handleAgentSelect\(nextAgentName: string\)[\s\S]*setSelectedAgent\(normalizedAgentName\)/)
})

test('agent trigger shows text-only thinking and active priority metadata to the right of its name', () => {
  const markup = renderToStaticMarkup(
    <AgentPicker
      currentAgent="swarm"
      selectedPrimaryAgent="swarm"
      agents={[profile('swarm', { provider: 'codex', model: 'gpt-5.4', thinking: 'high', autoServiceTier: 'priority' })]}
      mode="auto"
      onSelect={() => {}}
      onOpenSettings={() => {}}
    />,
  )
  assert.match(markup, /Agent: Swarm, codex\/gpt-5.4 · high · priority/)
  assert.match(markup, />Swarm</)
  assert.match(markup, /data-testid="selected-agent-detail"[^>]*text-\[var\(--app-text-subtle\)\][^>]*>codex\/gpt-5.4 · high · priority</)
  assert.doesNotMatch(markup, /💡|⚡|font-variant-emoji|plan mode|auto mode/)
})

test('compiled system-agent identities keep their canonical values but render without the redundant prefix', () => {
  const markup = renderToStaticMarkup(
    <AgentPicker
      currentAgent="system-explorer"
      selectedPrimaryAgent="swarm"
      agents={[profile('system-explorer', { mode: 'subagent', protected: true })]}
      onSelect={() => {}}
    />,
  )

  assert.match(markup, /Agent: Explorer/)
  assert.match(markup, />Explorer</)
  assert.doesNotMatch(markup, />system-explorer</)
})

test('agent trigger uses the active mode details for a split profile', () => {
  const split = profile('reviewer', {
    modelMode: 'split',
    planProvider: 'anthropic',
    planModel: 'claude-sonnet-5',
    planThinking: 'high',
    planServiceTier: 'priority',
    autoProvider: 'codex',
    autoModel: 'gpt-5.4',
    autoThinking: 'medium',
    autoServiceTier: 'fast',
  })
  const markup = renderToStaticMarkup(
    <AgentPicker currentAgent="reviewer" selectedPrimaryAgent="reviewer" agents={[split]} mode="plan" onSelect={() => {}} />,
  )

  assert.match(markup, /reviewer[\s\S]*anthropic\/claude-sonnet-5 · high · priority/)
  assert.doesNotMatch(markup, /codex\/gpt-5.4 · medium · fast|💡|⚡/)
})

test('agent trigger omits priority metadata when the service tier is inactive', () => {
  const markup = renderToStaticMarkup(
    <AgentPicker
      currentAgent="swarm"
      selectedPrimaryAgent="swarm"
      agents={[profile('swarm', { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', autoServiceTier: 'standard' })]}
      onSelect={() => {}}
    />,
  )

  assert.match(markup, /codex\/gpt-5.4 · medium/)
  assert.doesNotMatch(markup, /💡|⚡|priority standard/)
})

test('production composer keeps style 3 with comfortably sized controls and no temporary comparison code', () => {
  const composerSource = readFileSync(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const pickerSource = readFileSync(new URL('./agent-picker.tsx', import.meta.url), 'utf8')
  const modePickerSource = readFileSync(new URL('./mode-picker.tsx', import.meta.url), 'utf8')

  assert.doesNotMatch(composerSource, /temporary-composer-style-selector|COMPOSER_BAR_STYLE_OPTIONS|composerBarStyle|barStyle=/)
  assert.match(composerSource, /border-y border-\[var\(--app-border-strong\)\] bg-transparent px-4 py-2/)
  assert.match(pickerSource, /border-b-2 border-transparent bg-transparent/)
  assert.match(modePickerSource, /border-b-2 border-transparent bg-transparent/)
  assert.match(composerSource, /<Mic size=\{15\}/)
  assert.match(composerSource, /<Send size=\{17\}/)
  assert.match(composerSource, /h-9 w-9/)
  assert.match(pickerSource, /min-h-9[\s\S]*px-3 py-2 text-xs/)
  assert.match(modePickerSource, /min-h-9[\s\S]*px-3 py-2 text-xs/)
  assert.match(modePickerSource, /onClick=\{\(\) => onSelect\(nextMode\)\}/)
  assert.doesNotMatch(modePickerSource, /createPortal|ChevronDown|setOpen/)
  assert.match(composerSource, /onModeSelect\?\.\(nextMode\)/)
  assert.match(composerSource, /onAgentSelect\?\.\(agent\)/)
  assert.match(composerSource, /onCompact\?\.\(draft\)/)
})
