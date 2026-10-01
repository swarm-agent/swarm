// Purpose: TaskTokenSplit must present canonical task receipt categories, not a
// cumulative session/total counter or client-side sum that double-counts caches.
// Missing receipts must not look like recorded zero; stale receipts must be marked.
// Server rendering is the narrowest layer for the presentation boundary; the
// worker-picker browser test separately proves TaskUsageFooter row geometry.
import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskTokenSplit } from './task-usage-metadata'
import type { UsagePage, UsageScopeTotal } from '../state/desktop-usage-state'

const usage: UsageScopeTotal = {
  kind: 'task', id: 'task-fixture', project_id: 'project-fixture', revision: 1,
  total_tokens: 999999, input_tokens: 12345, output_tokens: 678,
  cache_read_tokens: 9012, cache_write_tokens: 345, thinking_tokens: 67,
  media_cost_usd: 0, media_receipts: 0, coverage: 'observed_receipts_only',
  catalog_cost_usd: 0, provider_cost_usd: 0, provider_estimate_cost_usd: 0,
  nominal_subscription_cost_usd: 0, unknown_receipts: 1, free_receipts: 0,
  subscription_receipts: 0, receipt_count: 1, history_complete: false,
}
const page: UsagePage = {
  input: { accountScopeId: 'account-fixture', scope: { kind: 'task', id: usage.id, project_id: usage.project_id } },
  loading: false, stale: false, generation: 0, recorded: true, usage,
}
const render = (value?: UsagePage) => renderToStaticMarkup(<TaskTokenSplit page={value} />)

test('recorded token categories stay separate with compact text and exact accessible values', () => {
  const html = render(page)
  assert.match(html, /Input: 12,345; Output: 678; Cache read: 9,012; Cache write: 345; Thinking: 67 tokens; history incomplete/)
  for (const label of ['I', 'O', 'CR', 'CW', 'T']) assert.match(html, new RegExp(`>${label}</span>`))
  assert.match(html, /12\.3K/)
  assert.match(html, /9K/)
  assert.doesNotMatch(html, /999,999|999999|Observed|Lifetime|cost|invoice/)
  assert.equal(usage.input_tokens, 12345, 'presentation must not mutate recorded fields')
})

test('missing/loading/error receipts are not zero; stale receipts retain their exact split', () => {
  assert.match(render(), /No receipts/)
  const missing = render({ ...page, recorded: false })
  assert.match(missing, /No receipts/)
  assert.doesNotMatch(missing, /12\.3K|Input:/)
  assert.match(render({ ...page, recorded: false, loading: true }), /Loading…/)
  assert.match(render({ ...page, recorded: false, error: 'Read failed' }), /Unavailable/)
  const stale = render({ ...page, stale: true, error: 'Read failed' })
  assert.match(stale, /last known/)
  assert.match(stale, /12\.3K/)
  const zeros = render({ ...page, usage: { ...usage, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0 } })
  assert.match(zeros, /Input: 0; Output: 0; Cache read: 0; Cache write: 0 tokens/)
  assert.doesNotMatch(zeros, /Thinking:|No receipts/)
})
