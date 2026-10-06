#!/usr/bin/env node
// Receipt adapter ONLY: actual UI assertions, bootstrap and owned process disposal
// remain in the existing isolated smoke runner. No API-only substitute or paid work.
import { openSync, writeFileSync, closeSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { runWrapper, parseWrapperArgs } from '../run-new-task-smoke-isolated.mjs'
import { createReceipt, validateReceipt, validateOutput, verifyCandidate } from './orchestrator-pr.mjs'

export function parseBrowserOptions(argv, env = process.env) {
  const o = {}, wrapperArgs = []
  for (let i = 0; i < argv.length;) {
    const key = argv[i++]
    if (['--output', '--candidate-revision', '--run-id'].includes(key)) {
      const value = argv[i++]
      if (key in o || !value || value.startsWith('--')) throw new Error('invalid_adapter_option')
      o[key] = value
    } else {
      wrapperArgs.push(key)
      if (key !== '--isolated-no-provider-egress') wrapperArgs.push(argv[i++])
    }
  }
  if (!/^[a-f0-9]{40}$/.test(o['--candidate-revision'] ?? '') || !/^[a-zA-Z0-9_.:-]{1,200}$/.test(o['--run-id'] ?? '')) throw new Error('candidate_and_run_identity_required')
  validateOutput(o['--output'] ?? '', env.TMPDIR)
  parseWrapperArgs(wrapperArgs) // Full maintained CLI admission, before receipt reservation.
  return { output: o['--output'], candidate: o['--candidate-revision'], runID: o['--run-id'], scenario: 'new-task-browser', wrapperArgs }
}

export async function runBrowserAdapter(o, deps = {}) {
  const fd = openSync(o.output, 'wx', 0o600)
  const r = createReceipt(o)
  try {
    ;(deps.verifyCandidate ?? verifyCandidate)(o.candidate)
    r.assertions[0].passed = true
    r.status = 'FAIL'
    // runWrapper resolves only after nonzero real browser assertions AND disposal.
    await (deps.runWrapper ?? runWrapper)(o.wrapperArgs)
    r.assertions[1].passed = true; r.assertions[2].passed = true
    r.status = 'PASS'; r.native_exit = 0; r.assertion_count = r.assertions.length
    validateReceipt(r, o, 0)
  } catch (error) {
    if (r.status === 'PASS') r.status = 'FAIL'
    r.native_exit = 2
    r.failures.push(/^[a-z_]{1,80}$/.test(error.message) ? error.message : 'browser_or_cleanup_failed')
  } finally {
    r.assertion_count = r.assertions.filter(a => a.passed).length
    try { writeFileSync(fd, JSON.stringify(r, null, 2) + '\n') } finally { closeSync(fd) }
  }
  return r.native_exit
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const o = parseBrowserOptions(process.argv.slice(2))
    runBrowserAdapter(o).then(code => { process.exitCode = code }).catch(() => { console.error('browser adapter: receipt unavailable'); process.exitCode = 2 })
  } catch { console.error('browser adapter: invalid preflight'); process.exitCode = 2 }
}
