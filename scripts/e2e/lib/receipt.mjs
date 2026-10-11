// Receipt and secret redaction for scripts/e2e/ai-path.mjs. Pure apart from
// the clock; unit tested in ai-path.test.mjs.

export const RECEIPT_SCHEMA = 'swarm.ai-path-e2e.v1'
export const DETAIL_LIMIT = 300
export const DIAGNOSTIC_PREFIX = 'qualification diagnostic: '

// Token-like material that must never reach a receipt or log even when it
// was not registered as a known secret: Google API keys, Swarm AI/SDK keys,
// generic sk- keys, bearer values and the one-time code of a key page link.
const SECRET_PATTERNS = [
  /AIza[0-9A-Za-z_-]{20,}/g,
  /\bswk_[A-Za-z0-9_-]{8,}/g,
  /\bsk-[A-Za-z0-9_-]{16,}/g,
  /\b(Bearer)\s+[A-Za-z0-9._~+/=-]{8,}/gi,
  /(\/keys\/)[A-Za-z0-9_-]{20,}/g,
  /\b(api[_-]?key|token|secret|password)(["']?\s*[:=]\s*["']?)[^\s"',}]{6,}/gi,
]

export function redact(text, secrets = []) {
  let out = String(text ?? '')
  for (const secret of secrets) {
    const value = String(secret ?? '')
    if (value.length >= 4) out = out.split(value).join('[redacted]')
  }
  out = out
    .replace(SECRET_PATTERNS[0], '[redacted]')
    .replace(SECRET_PATTERNS[1], '[redacted]')
    .replace(SECRET_PATTERNS[2], '[redacted]')
    .replace(SECRET_PATTERNS[3], '$1 [redacted]')
    .replace(SECRET_PATTERNS[4], '$1[redacted]')
    .replace(SECRET_PATTERNS[5], '$1$2[redacted]')
  return out
}

export function shortDetail(value, secrets = [], limit = DETAIL_LIMIT) {
  let text = typeof value === 'string' ? value : JSON.stringify(value) ?? String(value)
  text = redact(text.replace(/\s+/g, ' ').trim(), secrets)
  if (text.length > limit) text = `${text.slice(0, limit - 1)}…`
  return text
}

// Receipt collects assertions in order. Every detail is redacted against the
// registered secrets and the generic patterns, and kept short.
export class Receipt {
  constructor(scenario, { now = () => Date.now() } = {}) {
    this.scenario = scenario
    this.now = now
    this.startedAt = now()
    this.assertions = []
    this.secrets = []
    this.daemonVersion = 'unknown'
  }

  addSecret(value) {
    if (typeof value === 'string' && value.length >= 4) this.secrets.push(value)
  }

  check(name, ok, detail = '') {
    const entry = { name: String(name), ok: Boolean(ok), detail: shortDetail(detail, this.secrets) }
    this.assertions.push(entry)
    return entry.ok
  }

  get passed() {
    return this.assertions.length > 0 && this.assertions.every((a) => a.ok)
  }

  firstFailure() {
    return this.assertions.find((a) => !a.ok) ?? null
  }

  toJSON() {
    return {
      schema: RECEIPT_SCHEMA,
      scenario: this.scenario,
      passed: this.passed,
      assertions: this.assertions.map((a) => ({ ...a })),
      duration_ms: Math.max(0, this.now() - this.startedAt),
      daemon_version: shortDetail(this.daemonVersion, this.secrets, 100) || 'unknown',
    }
  }

  // diagnosticLine is the one line the gate greps on failure, or null.
  diagnosticLine() {
    if (this.passed) return null
    const failed = this.firstFailure()
    const payload = {
      scenario: this.scenario,
      failed: failed ? failed.name : 'no_assertions',
      detail: failed ? failed.detail : 'the scenario recorded no assertions',
    }
    return DIAGNOSTIC_PREFIX + redact(JSON.stringify(payload), this.secrets)
  }
}
