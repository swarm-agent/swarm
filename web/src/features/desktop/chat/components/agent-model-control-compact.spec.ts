import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

assert.match(source, /const COMPACT_AGENT_NAME = 'system-compact'/, 'setup modal must identify the compiled Compact utility')
assert.match(source, /label: 'System utilities'[\s\S]*agent\.name === COMPACT_AGENT_NAME/, 'setup modal must list Compact separately from mutable profiles')
assert.match(source, /saveCompactAgentSettings\([\s\S]*provider:[\s\S]*model:[\s\S]*thinking:/, 'Compact setup must save only its supported preference fields')
assert.match(source, /identity, prompt, runtime, and empty tool contract remain code-owned/, 'setup modal must explain immutable Compact fields')
assert.doesNotMatch(source, /updateAgentProfile\([^)]*COMPACT_AGENT_NAME/, 'Compact must not be persisted through the mutable agent-profile path')
