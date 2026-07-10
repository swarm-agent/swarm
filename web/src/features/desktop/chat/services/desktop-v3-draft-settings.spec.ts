import assert from 'node:assert/strict'
import test from 'node:test'
import type { AgentProfileRecord, ModelOptionRecord, SessionPreferenceRecord } from '../types/chat'
import { getDesktopV3DraftSettings, initializeDesktopV3DraftSettings, selectDesktopV3DraftMode } from './desktop-v3-draft-settings'

const preference: SessionPreferenceRecord = { provider: 'p', model: 'default', thinking: 'medium', serviceTier: '', contextMode: '', updatedAt: 1 }
const models: ModelOptionRecord[] = []
const agent = {
  name: 'primary', runtimeMode: 'plan_auto', exitPlanModeEnabled: true, modelMode: 'split',
  planProvider: 'p', planModel: 'plan-model', planThinking: 'high', planServiceTier: '',
  autoProvider: 'p', autoModel: 'auto-model', autoThinking: 'medium', autoServiceTier: '',
  provider: '', model: '', thinking: '',
} as AgentProfileRecord

test('draft settings stay unresolved until the complete source arrives', () => {
  const workspace = 'draft-ordering'
  assert.equal(getDesktopV3DraftSettings(workspace), null)
  initializeDesktopV3DraftSettings(workspace, { mode: 'plan', selectedAgentName: '', agents: [agent], preference, modelOptions: models })
  assert.equal(getDesktopV3DraftSettings(workspace), null)
  initializeDesktopV3DraftSettings(workspace, { mode: 'plan', selectedAgentName: 'primary', agents: [agent], preference, modelOptions: models })
  assert.deepEqual(
    [getDesktopV3DraftSettings(workspace)?.mode, getDesktopV3DraftSettings(workspace)?.effectivePreference.model],
    ['plan', 'plan-model'],
  )
})

test('mode selection installs the matching split model atomically', () => {
  const workspace = 'draft-mode-transition'
  const observe = () => {
    const settings = getDesktopV3DraftSettings(workspace)
    return settings ? `${settings.mode}:${settings.agentName}:${settings.effectivePreference.model}` : 'loading'
  }
  const observed = [observe()]
  initializeDesktopV3DraftSettings(workspace, { mode: 'auto', selectedAgentName: 'primary', agents: [agent], preference, modelOptions: models })
  observed.push(observe())
  selectDesktopV3DraftMode(workspace, 'plan')
  observed.push(observe())
  assert.deepEqual(observed, ['loading', 'auto:primary:auto-model', 'plan:primary:plan-model'])
})
