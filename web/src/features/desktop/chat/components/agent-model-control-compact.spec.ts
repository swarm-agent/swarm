import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

assert.doesNotMatch(source, /system-compact/, 'setup modal must hide the internal Compact utility')
assert.match(source, /const CLONE_AGENT_NAME = 'system-clone'/, 'setup modal must identify compiled Clone')
assert.match(source, /const EXPLORER_AGENT_NAME = 'system-explorer'/, 'setup modal must identify compiled Explorer')
assert.match(source, /const SWARM_AGENT_NAME = 'swarm'/, 'setup modal must identify compiled Swarm')
assert.match(source, /label: 'System agents'[\s\S]*isCompiledSystemAgent\(agent\.name\)/, 'setup modal must list user-facing compiled agents separately from mutable profiles')
assert.match(source, /saveSystemAgentSettings\([\s\S]*provider:[\s\S]*model:[\s\S]*thinking:/, 'Explorer setup must save only its supported preference fields')
assert.match(source, /identity, prompt, runtime, and tool contract remain code-owned/, 'setup modal must explain immutable system-agent fields')
assert.match(source, /Clone has no independent model controls[\s\S]*inherits its parent session[\s\S]*service tier/, 'setup modal must explain Clone model and priority inheritance')
assert.doesNotMatch(source, /system-plan-sidechat|system-ai-sidechat/, 'setup modal must hide reserved sidechat agents')
