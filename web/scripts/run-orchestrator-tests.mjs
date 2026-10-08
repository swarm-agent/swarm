import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { selectOrchestratorTests } from './orchestrator-test-suite.mjs'

const webRoot = fileURLToPath(new URL('../', import.meta.url))
try {
  if (process.argv.length !== 2) throw new Error('Orchestrator suite accepts no extra selectors or test-runner options')
  const files = selectOrchestratorTests(webRoot)
  const result = spawnSync(process.execPath, [
    '--import', 'tsx', '--test', '--test-concurrency=1', '--test-timeout=15000', ...files,
  ], { cwd: webRoot, stdio: 'inherit', timeout: 120_000 })
  if (result.error) throw result.error
  if (result.signal || result.status === null) throw new Error(`Orchestrator runner terminated: ${result.signal ?? 'no exit status'}`)
  process.exitCode = result.status
} catch (error) {
  console.error(`test:orchestrator: ${error.message}`)
  process.exitCode = 1
}
