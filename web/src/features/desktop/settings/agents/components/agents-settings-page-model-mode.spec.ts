import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agents-settings-page.tsx', import.meta.url), 'utf8')

assert.match(
  source,
  /modelMode: "single" \| "split";/,
  'Agents settings form state must expose only Single and Split model modes',
)

assert.doesNotMatch(
  source,
  /modelMode: "default"|value="default"|Default\/inherit|inherit agent model/i,
  'Agents settings must not expose a Default/inherit per-agent model mode',
)

assert.match(
  source,
  /function profileSupportsSplitModel\(profile: AgentProfileRecord \| null \| undefined\): boolean \{[\s\S]*profile\.runtimeMode === "plan_auto"[\s\S]*profile\.exitPlanModeEnabled[\s\S]*toolConfigEnabled\(tools\.plan_manage\)[\s\S]*toolConfigEnabled\(tools\.exit_plan_mode\)/,
  'Split mode must be allowed only for plan-capable profiles',
)

assert.match(
  source,
  /modelMode: profile\.modelMode === "split" && profileSupportsSplitModel\(profile\) \? "split" : "single"/,
  'draft loading must normalize stale split fields on non-plan-capable profiles to Single',
)

assert.match(
  source,
  /const modelMode = input\.modelMode === "split" && planCapable \? "split" : "single"/,
  'save payload must downgrade Split to Single when the effective profile is not plan-capable',
)

assert.match(
  source,
  /plan_provider: modelMode === "split" \? input\.planProvider : ""/,
  'save payload must clear stale Plan split fields when saving Single mode',
)

assert.match(
  source,
  /<option value="single">Single model<\/option>[\s\S]*<option value="split" disabled=\{!splitModelAllowed\}>Split plan\/auto models<\/option>/,
  'Agents settings model-mode selector must offer only Single and gated Split choices',
)
