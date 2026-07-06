import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

assert.match(
  source,
  /function selectedDraftMode\(profile: AgentProfileRecord \| null\): DraftMode \{[\s\S]*if \(profile\.provider\.trim\(\) \|\| profile\.model\.trim\(\)\) return 'single'[\s\S]*return 'default'/,
  'default and single are distinct UI modes; an empty single-mode profile must stay Default, while explicit provider/model becomes Single',
)

assert.match(
  source,
  /const hasExplicitSingleModel = Boolean\(profile\?\.provider\.trim\(\) \|\| profile\?\.model\.trim\(\)\)/,
  'single draft must distinguish default-mode agents from explicit single-model locks',
)

assert.match(
  source,
  /serviceTier: hasExplicitSingleModel \? normalizeDraftServiceTier\(provider, profile\?\.autoServiceTier \?\? ''\) : fallback\.serviceTier/,
  'default-mode draft must preserve the resolved default service tier instead of reading the cleared agent autoServiceTier field',
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
