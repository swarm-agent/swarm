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
  assert.match(pickerSource, /aria-label=\{`Open settings for \$\{profileLabel\(profile\)\}`\}/)
  assert.match(pickerSource, /onClick=\{\(\) => handleOpenSettings\(profile\.name\)\}/)
  assert.doesNotMatch(pickerSource, /onModeSelect|Session mode for|selectedAgentPlanCapable/)
  assert.match(composerSource, /<ModePicker mode=\{mode\}[\s\S]*<AgentPicker/)
  assert.match(existingPaneSource, /handleAgentSelect\(nextAgentName: string\)[\s\S]*await updateSessionV3Agent\([\s\S]*normalizedAgentName/)
  assert.match(newPaneSource, /handleAgentSelect\(nextAgentName: string\)[\s\S]*setSelectedAgent\(normalizedAgentName\)/)
})

test('agent trigger shows emoji thinking and active priority metadata to the right of its name', () => {
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
  assert.match(markup, /Agent: Swarm, codex\/gpt-5.4 · 💡 high · ⚡ priority/)
  assert.match(markup, />Swarm</)
  assert.match(markup, /data-testid="selected-agent-detail"[^>]*text-\[var\(--app-text-subtle\)\][^>]*\[font-variant-emoji:text\][^>]*>codex\/gpt-5.4 · 💡 high · ⚡ priority</)
  assert.doesNotMatch(markup, /plan mode|auto mode/)
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

  assert.match(markup, /reviewer[\s\S]*anthropic\/claude-sonnet-5 · 💡 high · ⚡ priority/)
  assert.doesNotMatch(markup, /codex\/gpt-5.4 · 💡 medium · ⚡ fast/)
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

  assert.match(markup, /codex\/gpt-5.4 · 💡 medium/)
  assert.doesNotMatch(markup, /⚡|priority standard/)
})

test('production composer keeps style 3 and smaller microphone/send controls without temporary comparison code', () => {
  const composerSource = readFileSync(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const pickerSource = readFileSync(new URL('./agent-picker.tsx', import.meta.url), 'utf8')
  const modePickerSource = readFileSync(new URL('./mode-picker.tsx', import.meta.url), 'utf8')

  assert.doesNotMatch(composerSource, /temporary-composer-style-selector|COMPOSER_BAR_STYLE_OPTIONS|composerBarStyle|barStyle=/)
  assert.match(composerSource, /border-y border-\[var\(--app-border-strong\)\] bg-transparent px-4 py-1/)
  assert.match(pickerSource, /border-b-2 border-transparent bg-transparent/)
  assert.match(modePickerSource, /border-b-2 border-transparent bg-transparent/)
  assert.match(composerSource, /<Mic size=\{15\}/)
  assert.match(composerSource, /<Send size=\{17\}/)
  assert.match(composerSource, /onModeSelect\?\.\(nextMode\)/)
  assert.match(composerSource, /onAgentSelect\?\.\(agent\)/)
  assert.match(composerSource, /onCompact\?\.\(draft\)/)
})
