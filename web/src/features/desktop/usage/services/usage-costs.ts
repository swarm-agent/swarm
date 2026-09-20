import type { SessionUsageDailyItem } from './usage-api'

// API totals already include media. The media field is a subset, not an addend.
export function usageCostBreakdown(total: number, media: number) {
  return { total, tokens: total - media, media }
}

export function dailyUsageCost(day: Pick<SessionUsageDailyItem, 'cost_usd'>): number {
  return day.cost_usd
}
