#!/usr/bin/env node

import { spawnSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const webRoot = path.resolve(scriptDir, '..')
const localNode = path.join(webRoot, 'node_modules', 'node', 'bin', 'node')

const [script, ...args] = process.argv.slice(2)
if (!script) {
  console.error('usage: node ./scripts/use-local-node.mjs <script> [args...]')
  process.exit(2)
}
if (!existsSync(localNode)) {
  console.error(`missing local Node runtime at ${localNode}; run npm install from ${webRoot}`)
  process.exit(1)
}

const scriptPath = path.isAbsolute(script) ? script : path.resolve(webRoot, script)
const result = spawnSync(localNode, [scriptPath, ...args], {
  cwd: webRoot,
  env: process.env,
  stdio: 'inherit',
})

if (result.error) {
  console.error(result.error.message)
  process.exit(1)
}
process.exit(result.status ?? 1)
