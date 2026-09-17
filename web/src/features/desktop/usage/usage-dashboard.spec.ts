import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { fetchSessionUsageDashboard } from './services/usage-api'

const pageSource = readFileSync(new URL('./pages/usage-page.tsx', import.meta.url), 'utf8')
const routerSource = readFileSync(new URL('../../../app/router.tsx', import.meta.url), 'utf8')
const sidebarSource = readFileSync(new URL('../layout/desktop-app-page.tsx', import.meta.url), 'utf8')

test('Router registers /usage and /$workspaceSlug/usage routes with reserved segments', () => {
  assert.match(routerSource, /path: '\/usage'/)
  assert.match(routerSource, /path: '\/\$workspaceSlug\/usage'/)
  assert.match(routerSource, /ROOT_RESERVED_ROUTE_SEGMENTS[\s\S]*?'usage'/)
  assert.match(routerSource, /WORKSPACE_RESERVED_ROUTE_SEGMENTS[\s\S]*?'usage'/)
  assert.match(routerSource, /usageRoute/)
  assert.match(routerSource, /workspaceUsageRoute/)
})

test('Desktop sidebar renders Usage navigation button', () => {
  assert.match(sidebarSource, /data-testid="sidebar-usage-btn"/)
  assert.match(sidebarSource, /title="Usage & Analytics"/)
  assert.match(sidebarSource, /to: '\/\$workspaceSlug\/usage'/)
})

test('UsagePage defines dashboard sections: Overview, Models, Media Gen, Sessions', () => {
  assert.match(pageSource, /<UsageChartTokens/)
  assert.match(pageSource, /<UsageProviderCards/)
  assert.match(pageSource, /<UsageModelsTable/)
  assert.match(pageSource, /<UsageMediaSection/)
  assert.match(pageSource, /<UsageSessionsTable/)
  assert.match(pageSource, /Est\. Billed Cost/)
  assert.match(pageSource, /Total Tokens/)
  assert.match(pageSource, /Prompt Cache/)
  assert.match(pageSource, /Media Calls/)
})

test('fetchSessionUsageDashboard queries /v3/usage with parameters', async () => {
  const originalFetch = globalThis.fetch
  try {
    let capturedURL = ''
    globalThis.fetch = async (input: RequestInfo | URL) => {
      capturedURL = String(input)
      return new Response(
        JSON.stringify({
          ok: true,
          summary: {
            total_tokens: 150000,
            input_tokens: 100000,
            output_tokens: 10000,
            cached_tokens: 40000,
            thinking_tokens: 2000,
            total_cost_usd: 0.12,
            codex_nominal_cost_usd: 0.50,
            total_turns: 5,
            total_sessions: 2,
            total_media_calls: 1,
            media_cost_usd: 0.04,
          },
          daily: [
            {
              date: '2026-09-17',
              timestamp: 1789603200000,
              total_tokens: 150000,
              input_tokens: 100000,
              output_tokens: 10000,
              cached_tokens: 40000,
              thinking_tokens: 2000,
              cost_usd: 0.12,
              codex_nominal_cost_usd: 0.50,
              turns: 5,
              media_calls: 1,
              media_cost_usd: 0.04,
              models_used: { 'gemini-3.8-flash': 150000 },
            },
          ],
          by_provider: [
            {
              provider: 'google',
              display_name: 'Google Gemini',
              total_tokens: 150000,
              input_tokens: 100000,
              output_tokens: 10000,
              cached_tokens: 40000,
              thinking_tokens: 2000,
              cost_usd: 0.12,
              codex_nominal_cost_usd: 0,
              is_subscription: false,
              turns: 5,
              sessions: 2,
              models: ['gemini-3.8-flash'],
            },
          ],
          by_model: [
            {
              model: 'gemini-3.8-flash',
              provider: 'google',
              display_name: 'Gemini 3.8 Flash',
              total_tokens: 150000,
              input_tokens: 100000,
              output_tokens: 10000,
              cached_tokens: 40000,
              thinking_tokens: 2000,
              cost_usd: 0.12,
              codex_nominal_cost_usd: 0,
              turns: 5,
              sessions: 2,
              input_price_per_million: 0.75,
              output_price_per_million: 3.75,
              cached_price_per_million: 0.1875,
            },
          ],
          media: {
            total_count: 1,
            total_cost_usd: 0.04,
            image_count: 1,
            image_cost_usd: 0.04,
            video_count: 0,
            video_cost_usd: 0,
            audio_count: 0,
            audio_cost_usd: 0,
            recent_items: [],
          },
          recent_sessions: [],
          meta: {
            generated_at: Date.now(),
            time_range: '7d',
            total_records_analyzed: 5,
          },
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      )
    }

    const res = await fetchSessionUsageDashboard({ timeRange: '7d', provider: 'google' })
    assert.strictEqual(res.ok, true)
    assert.strictEqual(res.summary.total_tokens, 150000)
    assert.strictEqual(res.summary.cached_tokens, 40000)
    assert.match(capturedURL, /\/v3\/usage\?time_range=7d&provider=google/)
  } finally {
    globalThis.fetch = originalFetch
  }
})
