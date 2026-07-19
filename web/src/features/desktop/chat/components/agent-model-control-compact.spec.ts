import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

assert.doesNotMatch(source, /system-compact/, 'setup modal must hide the internal Compact utility')
assert.match(source, /const CLONE_AGENT_NAME = 'system-clone'/, 'setup modal must identify compiled Coder compatibility ID')
assert.match(source, /const EXPLORER_AGENT_NAME = 'system-explorer'/, 'setup modal must identify compiled Explorer')
assert.match(source, /const SWARM_AGENT_NAME = 'swarm'/, 'setup modal must identify compiled Swarm')
assert.match(source, /label: 'Agents', profiles: \[\.\.\.\(swarmProfile \? \[swarmProfile\] : \[\]\), \.\.\.primaryProfiles\]/, 'setup modal must keep Swarm first and selectable in the Agents section')
assert.doesNotMatch(source, /label: 'Default agent'/, 'Swarm must not disappear into a conditional default-agent section')
assert.match(source, /label: 'System agents'[\s\S]*isCompiledSystemAgent\(agent\.name\)/, 'setup modal must list user-facing compiled agents separately from mutable profiles')
assert.match(source, /agent: profile\.name === CLONE_AGENT_NAME \? 'coder' : 'explorer'[\s\S]*provider:[\s\S]*model:[\s\S]*thinking:/, 'Explorer and Coder setup must save only supported preference fields')
assert.match(source, /identity, prompt, runtime, and tool contract remain code-owned/, 'setup modal must explain immutable system-agent fields')
assert.match(source, /Coder'[\s\S]*independently configured single-model selection[\s\S]*inherits the parent session model/, 'setup modal must explain Coder model override and fallback')
assert.doesNotMatch(source, /system-plan-sidechat|system-ai-sidechat/, 'setup modal must hide reserved sidechat agents')
assert.match(source, /Shows thinking responses[\s\S]*Show compact button/, 'compact button switch must appear directly below thinking tags')
assert.match(source, /aria-label="Show compact button"[\s\S]*onShowCompactButtonToggle\(!showCompactButton\)/, 'compact button switch must toggle the persisted preference')
assert.doesNotMatch(source, /import \{[^\n]*\bCheck\b[^\n]*\} from 'lucide-react'/, 'active agent and profile selections must not use checkmark icons')
assert.match(source, /const displayedModelProfileId = editingProfileId[\s\S]*activeModelProfile\?\.source === 'saved'[\s\S]*modelProfiles\.find\(\(profile\) => profile\.isDefault\)\?\.profileId/, 'setup modal must display the active or account-default profile without entering edit state')
assert.match(source, /const selected = displayedModelProfileId === profile\.profileId[\s\S]*onClick=\{\(\) => chooseModelProfile\(profile\)\}/, 'displayed default profile must remain clickable to enter editing')
assert.match(source, /profile\.isDefault \? [\s\S]*>Preferred<\/span>/, 'default model profiles must use the minimal Preferred label')
assert.doesNotMatch(source, />Default<\/span>/, 'default model profiles must not show the old Default badge')
