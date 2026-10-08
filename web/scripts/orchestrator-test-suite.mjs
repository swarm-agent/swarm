import { realpathSync, statSync } from 'node:fs'
import path from 'node:path'

// Reviewed deterministic seed suite only. Browser/provider journeys are separate;
// adding files here does not qualify them for scripts/run-critical-tests.sh.
export const ORCHESTRATOR_TEST_FILES = Object.freeze([
  'src/features/desktop/orchestrate/orchestrate-commands.spec.ts',
  'src/features/desktop/orchestrate/orchestrate-plan-authority.spec.ts',
  'src/features/desktop/orchestrate/new-task-cta.spec.tsx',
  'src/features/desktop/orchestrate/new-task-swarm-guidance.spec.tsx',
  'src/features/desktop/orchestrate/quiet-task-actions.spec.tsx',
  'src/features/desktop/orchestrate/task-reopen-operation.spec.ts',
  'src/features/desktop/orchestrate/task-requirements.spec.tsx',
])

// Validate the entire manifest before starting Node: never silently drop a missing
// file or broaden a broken selection to a directory/glob/browser test discovery.
export function selectOrchestratorTests(webRoot, files = ORCHESTRATOR_TEST_FILES) {
  if (!Array.isArray(files) || files.length === 0 || files.length > 16) {
    throw new Error('Orchestrator selection must contain 1 to 16 explicit files')
  }
  const root = realpathSync(webRoot)
  const directory = path.join(root, 'src/features/desktop/orchestrate')
  const selected = []
  const seen = new Set()
  for (const file of files) {
    if (typeof file !== 'string'
      || !/^src\/features\/desktop\/orchestrate\/[a-z0-9-]+\.spec\.tsx?$/.test(file)) {
      throw new Error(`Not a deterministic Orchestrator test path: ${String(file)}`)
    }
    if (seen.has(file)) throw new Error(`Duplicate Orchestrator test: ${file}`)
    seen.add(file)
    const absolute = path.join(root, file)
    let resolved
    try {
      resolved = realpathSync(absolute)
      if (!statSync(resolved).isFile()) throw new Error('not a regular file')
    } catch (cause) {
      throw new Error(`Missing or invalid Orchestrator test: ${file}`, { cause })
    }
    if (resolved !== absolute || path.dirname(resolved) !== directory) {
      throw new Error(`Orchestrator test must not redirect through a symlink: ${file}`)
    }
    selected.push(`./${file}`)
  }
  return selected
}
