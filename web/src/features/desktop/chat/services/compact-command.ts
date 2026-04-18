export const COMPACT_THRESHOLD_METADATA_KEY = 'context_compaction_threshold_percent'

export interface ParsedCompactCommand {
  note: string
  hasThreshold: boolean
  thresholdPercent: number
}

function parseCompactThresholdToken(token: string): number | null {
  const trimmed = token.trim()
  if (!trimmed.endsWith('%')) {
    return null
  }
  const raw = trimmed.slice(0, -1).trim()
  if (!raw) {
    return null
  }
  const parsed = Number.parseFloat(raw)
  if (!Number.isFinite(parsed)) {
    return null
  }
  if (parsed <= 0) {
    return 0
  }
  if (parsed >= 100) {
    return 100
  }
  return parsed
}

export function parseCompactCommandInput(input: string): ParsedCompactCommand {
  const trimmed = input.trim()
  const compactMatch = trimmed.match(/^\/compact(?:\s+(.*))?$/i)
  const tail = compactMatch?.[1]?.trim() ?? ''
  if (!tail) {
    return {
      note: '',
      hasThreshold: false,
      thresholdPercent: 0,
    }
  }

  const tokens = tail.split(/\s+/).filter(Boolean)
  let thresholdPercent = 0
  let hasThreshold = false

  if (tokens.length >= 1) {
    const parsed = parseCompactThresholdToken(tokens[0])
    if (parsed !== null) {
      thresholdPercent = parsed
      hasThreshold = true
      tokens.splice(0, 1)
    }
  }
  if (!hasThreshold && tokens.length >= 2 && tokens[0].toLowerCase() === 'threshold') {
    const parsed = parseCompactThresholdToken(tokens[1])
    if (parsed !== null) {
      thresholdPercent = parsed
      hasThreshold = true
      tokens.splice(0, 2)
    }
  }

  return {
    note: tokens.join(' ').trim(),
    hasThreshold,
    thresholdPercent,
  }
}
