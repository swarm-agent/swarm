#!/usr/bin/env node
// Stable external filename, deliberately no legacy UI/Plan/model-setting aliases.
// Supported session-api coverage lives in orchestrator-pr; delegation in task-program-worktrees.
import { main } from './orchestrator-pr.mjs'
main().then(code => { process.exitCode = code }).catch(() => {
  console.error('task-routing: legacy new-router/existing-session selectors retired; use orchestrator-pr explicit scenario/identity/output inputs')
  process.exitCode = 2
})
