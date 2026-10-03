import assert from 'node:assert/strict'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import ts from 'typescript'

// Purpose: the app tsconfig must keep the Node-only projectTestStyles helper out
// of its root files without excluding production OrchestrateView. Querying the
// TypeScript config parser proves file selection without a browser or build.
test('app typecheck excludes the Node-only browser-test style helper', () => {
  const root = fileURLToPath(new URL('../../../../', import.meta.url))
  const configPath = path.join(root, 'tsconfig.json')
  const config = ts.readConfigFile(configPath, ts.sys.readFile)
  assert.equal(config.error, undefined)
  const parsed = ts.parseJsonConfigFileContent(config.config, ts.sys, root)
  assert.deepEqual(parsed.errors, [])
  const files = new Set(parsed.fileNames.map(file => path.resolve(file)))
  assert.equal(files.has(path.join(root, 'src/features/desktop/orchestrate/project-test-styles.ts')), false)
  assert.equal(files.has(path.join(root, 'src/features/desktop/orchestrate/OrchestrateView.tsx')), true)
})
