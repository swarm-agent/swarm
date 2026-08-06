import assert from 'node:assert/strict'
import test from 'node:test'
import {
  preferenceFromAgentModelLock,
  resolveDesktopV3AgentModelLock,
  resolveDesktopV3SessionAgentModelLock,
} from './agent-model-preferences'
import type { ModelOptionRecord, SessionPreferenceRecord } from '../types/chat'

const current: SessionPreferenceRecord = {
  provider: 'fallback', model: 'fallback', thinking: 'low', serviceTier: '', contextMode: '', updatedAt: 1,
}

const options: ModelOptionRecord[] = [
  {
    key: 'openai:profile-plan:', provider: 'openai', model: 'profile-plan', contextMode: '', label: 'Plan', thinking: 'high',
    thinkingOptions: ['high'], defaultThinking: 'high', thinkingProviderParameter: '', thinkingMappings: [], favorite: true,
    contextWindow: 100000, pricing: null, serviceTiers: [], defaultServiceTier: '', serviceTierMappings: [], contextModes: [],
  },
  {
    key: 'anthropic:profile-action:', provider: 'anthropic', model: 'profile-action', contextMode: '', label: 'Action', thinking: 'medium',
    thinkingOptions: ['medium'], defaultThinking: 'medium', thinkingProviderParameter: '', thinkingMappings: [], favorite: true,
    contextWindow: 100000, pricing: null, serviceTiers: ['fast'], defaultServiceTier: 'fast', serviceTierMappings: [], contextModes: [],
  },
]

const metadata = {
  agent_profile: {
    name: 'non-default-agent',
    provider: 'anthropic',
    model: 'profile-action',
    thinking: 'medium',
    service_tier: 'fast',
  },
}

test('stored session agent profile resolves one flat model independent of lifecycle mode', () => {
  const lock = resolveDesktopV3SessionAgentModelLock(metadata)

  assert.equal(lock?.agentName, 'non-default-agent')
  assert.deepEqual(lock && preferenceFromAgentModelLock(lock, current, options), {
    provider: 'anthropic', model: 'profile-action', thinking: 'medium', serviceTier: 'fast', contextMode: '', updatedAt: 1,
  })
})

test('Swarm never claims model authority from agent profile state', () => {
  const swarmMetadata = { agent_profile: { ...metadata.agent_profile, name: 'swarm' } }
  assert.equal(resolveDesktopV3SessionAgentModelLock(swarmMetadata), null)
  assert.equal(resolveDesktopV3AgentModelLock([{ ...metadata.agent_profile, name: 'swarm' } as never], 'swarm').locked, false)
})

test('missing stored agent profile does not claim session authority', () => {
  assert.equal(resolveDesktopV3SessionAgentModelLock({}), null)
})
