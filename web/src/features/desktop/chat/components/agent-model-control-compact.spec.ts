import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

assert.match(source, /const COMPACT_AGENT_NAME = 'system-compact'/, 'setup modal must identify compiled Compact')
assert.match(source, /const CODER_AGENT_NAME = 'system-coder'/, 'setup modal must identify compiled Coder compatibility ID')
assert.match(source, /const FINDER_AGENT_NAME = 'system-finder'/, 'setup modal must identify compiled Finder')
assert.match(source, /const SWARM_AGENT_NAME = 'swarm'/, 'setup modal must identify compiled Swarm')
assert.match(source, /label: 'Agents', profiles: \[\.\.\.\(swarmProfile \? \[swarmProfile\] : \[\]\), \.\.\.primaryProfiles\]/, 'setup modal must keep Swarm first and selectable in the Agents section')
assert.doesNotMatch(source, /label: 'Default agent'/, 'Swarm must not disappear into a conditional default-agent section')
assert.match(source, /label: 'System agents'[\s\S]*isCompiledSystemAgent\(agent\.name\)/, 'setup modal must list user-facing compiled agents separately from mutable profiles')
assert.match(source, /agent: profile\.name === COMPACT_AGENT_NAME \? 'compact' : profile\.name === CODER_AGENT_NAME \? 'coder' : profile\.name === DESIGNER_AGENT_NAME \? 'designer' : 'finder'[\s\S]*provider:[\s\S]*model:[\s\S]*thinking:/, 'Compact, Finder, Coder, and Designer setup must save only supported preference fields')
assert.match(source, /identity, prompt, runtime, and tool contract remain code-owned/, 'setup modal must explain immutable system-agent fields')
assert.match(source, /COMPACT_AGENT_NAME \? 'Compact'[\s\S]*independently configured single-model selection[\s\S]*inherits the active account model/, 'setup modal must explain Compact model override and fallback')
assert.doesNotMatch(source, /system-plan-sidechat|system-ai-sidechat/, 'setup modal must hide reserved sidechat agents')
assert.match(source, /Show thinking responses/, 'thinking responses preference remains available in agent setup')
assert.doesNotMatch(source, /Show compact button|onShowCompactButtonToggle|showCompactButton/, 'compact availability must not be exposed as a persisted setup toggle')
assert.doesNotMatch(source, /import \{[^\n]*\bCheck\b[^\n]*\} from 'lucide-react'/, 'active agent and profile selections must not use checkmark icons')
assert.match(source, /const displayedModelProfileId = editingProfileId[\s\S]*activeModelProfile\?\.source === 'saved'[\s\S]*modelProfiles\.find\(\(profile\) => profile\.isDefault\)\?\.profileId/, 'setup modal must display the active or account-default profile without entering edit state')
assert.match(source, /const selected = displayedModelProfileId === profile\.profileId[\s\S]*onClick=\{\(\) => chooseModelProfile\(profile\)\}/, 'displayed default profile must remain clickable to enter editing')
assert.match(source, /profile\.isDefault \? [\s\S]*>Preferred<\/span>/, 'default model profiles must use the minimal Preferred label')
assert.doesNotMatch(source, />Default<\/span>/, 'default model profiles must not show the old Default badge')
