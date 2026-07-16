import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agents-settings-page.tsx', import.meta.url), 'utf8')

assert.match(source, /normalizeCompactAgentSettings\(uiSettings\)/, 'agent settings must read Compact from canonical UI settings')
assert.match(source, /saveSystemUtilitySettings\([\s\S]*utilityProvider[\s\S]*utilityModel[\s\S]*utilityThinking/, 'Set Utility AI must persist compiled utility provider, model, and thinking')
assert.match(source, /Compact is a compiled tool-free system utility/, 'agent settings must preserve Compact system-agent immutability')
assert.match(source, /normalizeExplorerAgentSettings\(uiSettings\)/, 'agent settings must read Explorer from canonical UI settings')
assert.doesNotMatch(source, /explorer, memory/i, 'Memory must not be restored to utility-agent copy')
