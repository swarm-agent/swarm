// Purpose: web/tsconfig.json must keep the Node-only Git board fixture out of
// browser compilation without dropping its production runtime. TypeScript's
// config parser and program graph are the narrowest check of this build boundary;
// checking the graph also catches production imports that bypass `exclude`.
// Native execution remains owned by Go's TestProjectGitSubscriptionsBoard.
import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

test('browser TypeScript excludes the native Git fixture but retains the board runtime', () => {
  const fixtureDir = dirname(fileURLToPath(import.meta.url))
  const webRoot = resolve(fixtureDir, '../../../..')
  const fixture = resolve(fixtureDir, 'desktop-projects-native-git.fixture.ts')
  const runtime = resolve(fixtureDir, 'desktop-projects.ts')
  const config = ts.readConfigFile(resolve(webRoot, 'tsconfig.json'), ts.sys.readFile)
  assert.equal(config.error, undefined)
  const parsed = ts.parseJsonConfigFileContent(config.config, ts.sys, webRoot)
  assert.deepEqual(parsed.errors, [])
  assert.ok(existsSync(fixture), 'native harness entrypoint must remain available')
  assert.ok(!parsed.fileNames.includes(fixture), 'fixture must not be a browser compilation root')
  assert.ok(parsed.fileNames.includes(runtime), 'production board runtime must remain typechecked')

  const program = ts.createProgram(parsed.fileNames, parsed.options)
  assert.equal(program.getSourceFile(fixture), undefined, 'production imports must not pull in the fixture')
  assert.ok(program.getSourceFile(runtime))
})
