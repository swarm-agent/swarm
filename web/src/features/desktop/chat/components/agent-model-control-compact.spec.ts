import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

assert.match(source, /const COMPACT_AGENT_NAME = 'system-compact'/, 'setup modal must identify the compiled Compact utility')
assert.match(source, /label: 'System utilities'[\s\S]*isSystemUtility\(agent\.name\)/, 'setup modal must list compiled utilities separately from mutable profiles')
assert.match(source, /saveSystemAgentSettings\([\s\S]*provider:[\s\S]*model:[\s\S]*thinking:/, 'system utility setup must save only its supported preference fields')
assert.match(source, /identity, prompt, runtime, and tool contract remain code-owned/, 'setup modal must explain immutable system-agent fields')
assert.doesNotMatch(source, /updateAgentProfile\([^)]*COMPACT_AGENT_NAME/, 'Compact must not be persisted through the mutable agent-profile path')
