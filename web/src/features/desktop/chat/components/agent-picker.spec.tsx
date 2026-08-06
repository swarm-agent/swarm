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
    defaultSessionMode: 'auto',
    executionSetting: '',
    exitPlanModeEnabled: false,
    toolScope: null,
    toolContract: null,
    enabled: true,
    protected: false,
    updatedAt: 0,
    ...overrides,
  }
}

test('agent picker keeps identity switching separate from model and mode mutation', () => {
  const source = readFileSync(new URL('./agent-picker.tsx', import.meta.url), 'utf8')
  assert.match(source, /onClick=\{\(\) => handleSelect\(profile\.name\)\}/)
  assert.match(source, /pointerAnchorRef\.current = event\.detail > 0[\s\S]*clientX[\s\S]*clientY/)
  assert.match(source, /anchorX - width \/ 2/)
  assert.match(source, /viewportWidth - width - DROPDOWN_VIEWPORT_GUTTER/)
  assert.match(source, /aria-label=\{`Open settings for \$\{profileLabel\(profile\)\}`\}/)
  assert.match(source, /onClick=\{\(\) => handleOpenSettings\(profile\.name\)\}/)
  assert.match(source, /Select profile/)
  assert.match(source, /Shows thinking responses[\s\S]*role="switch"[\s\S]*onThinkingTagsToggle\(!thinkingTagsEnabled\)/)
  assert.doesNotMatch(source, /DesktopSessionMode|modelMode|planProvider|planModel|autoProvider|autoModel|onModeSelect/)
})

test('agent trigger renders only the flat provider, model, and thinking fields', () => {
  const markup = renderToStaticMarkup(
    <AgentPicker
      currentAgent="swarm"
      selectedPrimaryAgent="swarm"
      agents={[profile('swarm', { provider: 'codex', model: 'gpt-5.6', thinking: 'high' })]}
      onSelect={() => {}}
    />,
  )
  assert.match(markup, /Model: codex\/gpt-5.6 · high/)
  assert.doesNotMatch(markup, /plan mode|auto mode|priority/)
})

test('agent trigger falls back without exposing agent identity as model state', () => {
  const markup = renderToStaticMarkup(
    <AgentPicker currentAgent="system-finder" selectedPrimaryAgent="swarm" agents={[profile('system-finder')]} onSelect={() => {}} />,
  )
  assert.match(markup, /Model: Default model/)
  assert.doesNotMatch(markup, />Finder<|>system-finder</)
})
