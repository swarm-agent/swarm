let debugSequence = 0
const DEBUG_STORAGE_KEY = 'swarm.web.debug'

function isEnabledFlag(value: unknown): boolean {
  if (typeof value === 'boolean') {
    return value
  }
  if (typeof value !== 'string') {
    return false
  }
  switch (value.trim().toLowerCase()) {
    case '1':
    case 'true':
    case 'yes':
    case 'on':
    case 'debug':
      return true
    default:
      return false
  }
}

function isDebugLoggingEnabled(): boolean {
  const globalDebug = Reflect.get(globalThis as object, '__SWARM_DEBUG__')
  if (typeof globalDebug !== 'undefined') {
    return isEnabledFlag(globalDebug)
  }
  if (typeof window === 'undefined') {
    return false
  }
  try {
    return isEnabledFlag(window.localStorage.getItem(DEBUG_STORAGE_KEY))
  } catch {
    return false
  }
}

function nowMs(): number {
  if (typeof performance !== 'undefined' && typeof performance.now === 'function') {
    return performance.now()
  }
  return Date.now()
}

function roundMs(value: number): number {
  return Math.round(value * 10) / 10
}

export function debugLog(scope: string, message: string, details?: Record<string, unknown>): void {
  if (!isDebugLoggingEnabled()) {
    return
  }
  const payload = {
    seq: ++debugSequence,
    t: roundMs(nowMs()),
    ...(details ?? {}),
  }
  console.info(`[debug:${scope}] ${message}`, payload)
}

export function createDebugTimer(scope: string, message: string, details?: Record<string, unknown>) {
  if (!isDebugLoggingEnabled()) {
    return () => {}
  }
  const startedAt = nowMs()
  debugLog(scope, `${message}:start`, details)
  return (finishDetails?: Record<string, unknown>) => {
    debugLog(scope, `${message}:end`, {
      durationMs: roundMs(nowMs() - startedAt),
      ...(finishDetails ?? {}),
    })
  }
}
