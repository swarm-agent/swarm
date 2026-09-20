import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { dailyUsageCost, usageCostBreakdown } from './services/usage-costs'

// Requirement: summary and daily cost fields already include media. Prevent the
// KPI, cost bars, axis and tooltip from adding that subset again. Authority:
// UsagePage/UsageChartTokens and their shared usage-costs presentation helpers.
// Pure value assertions are the narrowest proof of the arithmetic; source checks
// below only guard wiring, not browser rendering or API correctness.
test('usage cost presentation counts media once and separates token cost', () => {
  for (const [total, media, tokens] of [[75.35, 11.48, 63.87], [2, .75, 1.25], [.5, .5, 0], [1, 0, 1], [0, 0, 0]]) {
    const cost = usageCostBreakdown(total, media)
    assert.equal(cost.total, total)
    assert.equal(cost.media, media)
    assert.ok(Math.abs(cost.tokens - tokens) < 1e-9)
    assert.ok(Math.abs(cost.tokens + cost.media - cost.total) < 1e-9)
    const day = { cost_usd: total, media_cost_usd: media }
    assert.equal(dailyUsageCost(day), total)
    if (media > 0) assert.notEqual(dailyUsageCost(day), total + media)
  }
})

test('KPI and chart wire inclusive cost helpers without re-adding media', () => {
  const page = readFileSync(new URL('./pages/usage-page.tsx', import.meta.url), 'utf8')
  const chart = readFileSync(new URL('./components/usage-chart-tokens.tsx', import.meta.url), 'utf8')
  assert.match(page, /total: billedCost, tokens: tokenCost, media: mediaCost.*usageCostBreakdown/)
  assert.match(page, /tokenCost\.toFixed\(2\)/)
  assert.match(page, /billedCost\.toFixed\(2\)/)
  assert.match(chart, /items\.map\(dailyUsageCost\)/)
  assert.match(chart, /const dayCost = dailyUsageCost\(item\)/)
  assert.match(chart, /dailyUsageCost\(hoveredItem\)\.toFixed\(4\)/)
  assert.doesNotMatch(page + chart, /cost_usd\s*\+/)
})
