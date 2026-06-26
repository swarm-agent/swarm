#!/usr/bin/env node
import { spawn } from 'node:child_process'
import { existsSync } from 'node:fs'
import { resolve } from 'node:path'

function usage() {
  console.error(`Usage: node ./scripts/run-desktop-subagent-task-e2e.mjs <desktop-url> [--headed] [--evidence-dir <dir>] [--timeout-ms <ms>]

Example:
  node ./scripts/run-desktop-subagent-task-e2e.mjs https://example.invalid/swarm-go

Environment overrides:
  SWARM_SUBAGENT_E2E_PROMPT       Prompt sent to the Desktop UI
  SWARM_E2E_EVIDENCE_DIR          Directory for logs/screenshots/summary
  SWARM_SUBAGENT_E2E_TIMEOUT_MS   Wait timeout in milliseconds
  SWARM_E2E_HEADFUL=1             Run headed browser
`)
}

const args = process.argv.slice(2)
let url = ''
let evidenceDir = ''
let timeoutMs = ''
let headed = false

for (let index = 0; index < args.length; index += 1) {
  const arg = args[index]
  if (arg === '--help' || arg === '-h') {
    usage()
    process.exit(0)
  }
  if (arg === '--headed' || arg === '--headful') {
    headed = true
    continue
  }
  if (arg === '--evidence-dir') {
    evidenceDir = args[++index] || ''
    continue
  }
  if (arg === '--timeout-ms') {
    timeoutMs = args[++index] || ''
    continue
  }
  if (!url) {
    url = arg
    continue
  }
  console.error(`Unexpected argument: ${arg}`)
  usage()
  process.exit(2)
}

if (!url) {
  usage()
  process.exit(2)
}

if (!/^https?:\/\//i.test(url)) {
  console.error(`Desktop URL must start with http:// or https://: ${url}`)
  process.exit(2)
}

const localNode = resolve(process.cwd(), 'node_modules/node/bin/node')
const nodeBin = existsSync(localNode) ? localNode : process.execPath
const spec = './src/features/desktop/chat/components/desktop-subagent-task-live.e2e.spec.ts'
const env = {
  ...process.env,
  SWARM_DESKTOP_SUBAGENT_E2E: '1',
  SWARM_DESKTOP_URL: url,
}
if (evidenceDir) env.SWARM_E2E_EVIDENCE_DIR = evidenceDir
if (timeoutMs) env.SWARM_SUBAGENT_E2E_TIMEOUT_MS = timeoutMs
if (headed) env.SWARM_E2E_HEADFUL = '1'

const child = spawn(nodeBin, ['--import', 'tsx', '--test', '--test-timeout=300000', spec], {
  cwd: process.cwd(),
  env,
  stdio: 'inherit',
})

child.on('exit', (code, signal) => {
  if (signal) {
    console.error(`subagent task e2e terminated by ${signal}`)
    process.exit(1)
  }
  process.exit(code ?? 1)
})
