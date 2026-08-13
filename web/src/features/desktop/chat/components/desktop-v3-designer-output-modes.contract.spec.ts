import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const promptURL = new URL('../../../../../../swarmd/internal/run/service_prompt.go', import.meta.url)
const runtimeURL = new URL('../../../../../../swarmd/internal/tool/runtime.go', import.meta.url)
const launchURL = new URL('../../../../../../swarmd/internal/run/service_task_launch.go', import.meta.url)

test('managed Designer output is the explicit zero-repository-diff default', async () => {
  const [prompt, runtime, launch] = await Promise.all([
    readFile(promptURL, 'utf8'),
    readFile(runtimeURL, 'utf8'),
    readFile(launchURL, 'utf8'),
  ])

  assert.match(prompt, /Designer output defaults to managed artifacts/)
  assert.match(prompt, /Managed Designers must use manage_artifact/)
  assert.match(prompt, /must not write\/edit the checkout/)
  assert.match(prompt, /Managed Designer variants omit owned_scope/)
  assert.match(runtime, /Designer alternatives and Designer Iteration Swarms default to output_mode=managed/)
  assert.match(runtime, /workspace targets must be omitted/)
  assert.match(runtime, /model may choose only managed versus workspace and cannot name session, collection, or variant destinations/)
  assert.match(launch, /mode = taskOutputModeManaged/)
  assert.match(launch, /managed Designer must omit owned_scope/)
})

test('workspace Designer output is explicit, concrete, non-overlapping, and preserved in checkout', async () => {
  const [prompt, runtime, launch] = await Promise.all([
    readFile(promptURL, 'utf8'),
    readFile(runtimeURL, 'utf8'),
    readFile(launchURL, 'utf8'),
  ])

  assert.match(prompt, /Set output_mode=workspace only when the user explicitly needs checkout source output/)
  assert.match(prompt, /Workspace Designers may write\/edit only their declared scope/)
  assert.match(prompt, /workspace variants stay in their declared checkout targets/)
  assert.match(prompt, /Remove unselected or rejected variants only when the user requests or chooses that cleanup/)
  assert.match(runtime, /output_mode=workspace preserves shared-checkout write\/edit behavior/)
  assert.match(runtime, /requires concrete non-overlapping workspace-relative targets/)
  assert.match(launch, /workspace Designer requires a concrete workspace-relative owned_scope/)
  assert.match(launch, /must be a concrete clean workspace-relative path/)
  assert.match(launch, /each concurrent variant requires a distinct output target/)
  assert.match(launch, /task swarm owned_scope_template must contain exactly one \{index\} placeholder/)
})
