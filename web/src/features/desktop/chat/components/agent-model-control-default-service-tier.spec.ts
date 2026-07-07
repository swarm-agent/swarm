import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')
const mutationSource = readFileSync(new URL('../queries/agent-preference-mutations.ts', import.meta.url), 'utf8')

assert.match(
  source,
  /type DraftMode = 'single' \| 'split'/,
  'agent model control must expose only Single and Split model modes',
)

assert.doesNotMatch(
  source,
  /draftMode === 'default'|kind: 'default'|defaultPreference/,
  'agent model control must not expose or save a Default/inherit agent model mode',
)

assert.match(
  source,
  /const splitModeAllowed = isPlanCapableAgent\(draftProfile\)/,
  'Split mode availability must be gated by plan-capable agent state',
)

assert.match(
  source,
  /const effectiveDraftMode: DraftMode = draftMode === 'split' && !splitModeAllowed \? 'single' : draftMode/,
  'non-plan-capable agents must render as Single even with stale split profile fields',
)

assert.match(
  mutationSource,
  /const modelMode = next\.modelMode === 'split' && agentIsPlanCapable\(next\) \? 'split' : 'single'/,
  'profile save payload must downgrade Split to Single when the agent is not plan-capable',
)

assert.match(
  mutationSource,
  /plan_provider: modelMode === 'split' \? next\.planProvider : ''/,
  'profile save payload must clear stale split Plan fields when saving Single mode',
)

assert.match(
  source,
  /const hasExplicitSingleModel = Boolean\(profile\?\.provider\.trim\(\) \|\| profile\?\.model\.trim\(\)\)/,
  'single draft may use the current resolved model to seed explicit Single settings',
)

assert.match(
  source,
  /serviceTier: hasExplicitSingleModel \? normalizeDraftServiceTier\(provider, profile\?\.autoServiceTier \?\? ''\) : fallback\.serviceTier/,
  'single draft must preserve the resolved service tier when seeding explicit Single settings',
)

assert.match(
  source,
  /import \{ defaultModelThinking, displayModelName,[^}]*modelThinkingOptions,[^}]*normalizeModelThinking,[^}]*\} from '..\/services\/model-options'/,
  'agent model control must use the shared catalog-driven thinking option helpers',
)

assert.doesNotMatch(
  source,
  /const FALLBACK_THINKING_OPTIONS = \['off', 'low', 'medium', 'high', 'xhigh'\]/,
  'agent model control must not keep its own low/medium thinking fallback that bypasses catalog thinking_options',
)
