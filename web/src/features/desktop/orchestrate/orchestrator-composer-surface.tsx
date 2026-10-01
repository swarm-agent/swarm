export function ContextRemaining({ usage }: { usage: Record<string, unknown> | null }) {
  const windowSize = usage?.context_window ?? usage?.contextWindow
  const remaining = usage?.remaining_tokens ?? usage?.remainingTokens
  const known = typeof windowSize === 'number' && Number.isFinite(windowSize) && windowSize > 0
    && typeof remaining === 'number' && Number.isFinite(remaining) && remaining >= 0 && remaining <= windowSize
  return <span data-testid="orchestrator-context-label" title={known ? `${remaining} of ${windowSize} context tokens remaining` : 'Current context occupancy is unavailable'}>
    {known ? `${Math.round(remaining / windowSize * 100)}% context remaining` : 'Context unknown'}
  </span>
}
