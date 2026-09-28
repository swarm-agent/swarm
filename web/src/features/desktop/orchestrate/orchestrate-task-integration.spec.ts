import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'

// Purpose: A selected Swarm task must send its explicit session/source/target
// lineage to the mutation boundary, rather than POST an empty request or silently
// substitute main. The OrchestrateView card/handler is the narrowest UI boundary;
// this guards the historically broken request wiring, not backend Git execution.
const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')

test('task integration sends selected lineage and renders errors without a branch fallback', () => {
  const handler = source.slice(source.indexOf('const handleIntegrateTask ='), source.indexOf('// Refine task with router'))
  assert.match(handler, /tasks\.find\(row => row\.id === taskId\)/)
  assert.match(handler, /session_id: task\.sessionId, source_branch: task\.worktreeBranch, target_branch: task\.baseBranch/)
  assert.match(handler, /setTaskActionErrors\(prev => \(\{ \.\.\.prev, \[taskId\]: err instanceof Error/)
  assert.match(handler, /integratingTaskFlights\.current\.has\(taskId\)/)
  assert.match(source, /disabled=\{isIntegrating \|\| !task\.baseBranch\}/)
  assert.doesNotMatch(source, /Integrate into \{task\.baseBranch \|\| 'main'\}/)
})
