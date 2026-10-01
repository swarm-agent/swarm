// Purpose: usageCostSummary is the compact card/budget pricing boundary. Unknown
// receipts must not appear free; estimates, subscriptions and incomplete history
// must survive collapsed disclosures. Pure presentation is the narrowest layer.
import test from 'node:test'
import assert from 'node:assert/strict'
import { usageCostSummary } from './scope-usage'
import type { UsageScopeTotal } from '../state/desktop-usage-state'
const usage: UsageScopeTotal = { kind: 'worker', id: 'fixture', revision: 1, total_tokens: 1, input_tokens: 1, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, media_cost_usd: 0, media_receipts: 0, coverage: 'observed_receipts_only', catalog_cost_usd: 0, provider_cost_usd: 0, provider_estimate_cost_usd: 0, nominal_subscription_cost_usd: 0, unknown_receipts: 1, free_receipts: 0, subscription_receipts: 0, receipt_count: 1, history_complete: false }
test('compact pricing distinguishes unknown, known subtotal, estimate and subscription', () => {
  assert.match(usageCostSummary(usage), /Cost unknown.*history incomplete/)
  assert.doesNotMatch(usageCostSummary(usage), /\$0|free/)
  assert.match(usageCostSummary({ ...usage, receipt_count: 2, provider_cost_usd: 1 }), /provider cost.*known subtotal only.*partial pricing/)
  assert.match(usageCostSummary({ ...usage, unknown_receipts: 0, catalog_cost_usd: 1, provider_estimate_cost_usd: 2 }), /catalog estimate.*provider estimate/)
  assert.match(usageCostSummary({ ...usage, unknown_receipts: 0, subscription_receipts: 1 }), /Subscription.*nominal \(not charged\)/)
})
