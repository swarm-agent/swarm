import { useEffect, useMemo, useState } from 'react'
import { requestJson } from '../../../../../app/api'
import { Button } from '../../../../../components/ui/button'
import { Input } from '../../../../../components/ui/input'
import { cn } from '../../../../../lib/cn'

export interface PermissionRule {
  id: string
  kind: string
  decision: string
  tool?: string
  pattern?: string
  created_at?: number
  updated_at?: number
}

interface PermissionPolicy {
  version: number
  rules: PermissionRule[]
  updated_at?: number
}

interface PermissionExplain {
  decision: string
  source: string
  reason: string
  tool_name?: string
  command?: string
  rule_preview?: string
}

interface PermissionPolicyResponse {
  ok?: boolean
  policy?: PermissionPolicy
}

interface PermissionRuleResponse {
  ok?: boolean
  rule?: PermissionRule
}

interface PermissionResetResponse {
  ok?: boolean
  policy?: PermissionPolicy
}

interface PermissionExplainResponse {
  ok?: boolean
  explain?: PermissionExplain
}

const DECISION_OPTIONS = [
  { value: 'allow', label: 'Allow' },
  { value: 'ask', label: 'Ask' },
  { value: 'deny', label: 'Deny' },
] as const

const KIND_OPTIONS = [
  { value: 'tool', label: 'Tool' },
  { value: 'bash-prefix', label: 'Bash prefix' },
  { value: 'phrase', label: 'Phrase' },
] as const

function normalizeRuleKind(kind: string): 'tool' | 'bash-prefix' | 'phrase' {
  switch (kind.trim().toLowerCase()) {
    case 'bash_prefix':
    case 'bash-prefix':
      return 'bash-prefix'
    case 'phrase':
      return 'phrase'
    default:
      return 'tool'
  }
}

function describeRule(rule: PermissionRule): string {
  const decision = (rule.decision || 'allow').trim().toLowerCase()
  const kind = normalizeRuleKind(rule.kind)
  if (kind === 'bash-prefix') {
    return `${decision} bash prefix: ${rule.pattern?.trim() || '—'}`
  }
  if (kind === 'phrase') {
    return `${decision} phrase: ${rule.pattern?.trim() || '—'}`
  }
  return `${decision} tool: ${rule.tool?.trim() || '—'}`
}

function formatTimestamp(timestamp?: number): string {
  if (typeof timestamp !== 'number' || timestamp <= 0) {
    return ''
  }
  try {
    return new Date(timestamp).toLocaleString()
  } catch {
    return ''
  }
}

async function fetchPermissionPolicy(): Promise<PermissionPolicy> {
  const response = await requestJson<PermissionPolicyResponse>('/v1/permissions')
  return response.policy ?? { version: 0, rules: [] }
}

async function createPermissionRule(input: { decision: string; kind: string; value: string }): Promise<PermissionRule> {
  const kind = normalizeRuleKind(input.kind)
  const body =
    kind === 'tool'
      ? { decision: input.decision, kind: 'tool', tool: input.value.trim() }
      : kind === 'bash-prefix'
        ? { decision: input.decision, kind: 'bash_prefix', tool: 'bash', pattern: input.value.trim() }
        : { decision: input.decision, kind: 'phrase', pattern: input.value.trim() }
  const response = await requestJson<PermissionRuleResponse>('/v1/permissions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!response.rule) {
    throw new Error('Permission rule save returned no rule')
  }
  return response.rule
}

async function deletePermissionRule(ruleID: string): Promise<void> {
  await requestJson(`/v1/permissions/${encodeURIComponent(ruleID)}`, {
    method: 'DELETE',
  })
}

async function resetPermissionPolicy(): Promise<PermissionPolicy> {
  const response = await requestJson<PermissionResetResponse>('/v1/permissions/reset', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({}),
  })
  return response.policy ?? { version: 0, rules: [] }
}

async function explainPermission(toolName: string, argumentsText: string): Promise<PermissionExplain> {
  const params = new URLSearchParams({
    mode: 'auto',
    tool: toolName.trim(),
    arguments: argumentsText.trim(),
  })
  const response = await requestJson<PermissionExplainResponse>(`/v1/permissions/explain?${params.toString()}`)
  return response.explain ?? { decision: '', source: '', reason: '', rule_preview: '' }
}

export function PermissionsSettingsPage() {
  const [policy, setPolicy] = useState<PermissionPolicy>({ version: 0, rules: [] })
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [resetting, setResetting] = useState(false)
  const [busyRuleID, setBusyRuleID] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [status, setStatus] = useState<string | null>(null)
  const [decision, setDecision] = useState<'allow' | 'ask' | 'deny'>('allow')
  const [kind, setKind] = useState<'tool' | 'bash-prefix' | 'phrase'>('tool')
  const [value, setValue] = useState('')
  const [explainTool, setExplainTool] = useState('bash')
  const [explainArguments, setExplainArguments] = useState('{"command":"git status"}')
  const [explainResult, setExplainResult] = useState<PermissionExplain | null>(null)
  const [explaining, setExplaining] = useState(false)

  const load = async () => {
    setLoading(true)
    setError(null)
    try {
      setPolicy(await fetchPermissionPolicy())
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load permission policy')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  const sortedRules = useMemo(
    () => [...policy.rules].sort((left, right) => (right.updated_at || 0) - (left.updated_at || 0)),
    [policy.rules],
  )

  const valueLabel = kind === 'tool' ? 'Tool name' : kind === 'bash-prefix' ? 'Bash prefix' : 'Phrase'
  const valuePlaceholder =
    kind === 'tool'
      ? 'bash'
      : kind === 'bash-prefix'
        ? 'git status'
        : 'rm -rf /'

  const handleCreateRule = async () => {
    const trimmed = value.trim()
    if (!trimmed) {
      setError(`${valueLabel} is required.`)
      return
    }
    setSaving(true)
    setError(null)
    setStatus(null)
    try {
      const rule = await createPermissionRule({ decision, kind, value: trimmed })
      setPolicy((current) => ({
        ...current,
        version: Math.max(current.version, policy.version),
        rules: [rule, ...current.rules.filter((item) => item.id !== rule.id)],
      }))
      setValue('')
      setStatus(`Saved rule ${rule.id}`)
      void load()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save permission rule')
    } finally {
      setSaving(false)
    }
  }

  const handleDeleteRule = async (ruleID: string) => {
    setBusyRuleID(ruleID)
    setError(null)
    setStatus(null)
    try {
      await deletePermissionRule(ruleID)
      setPolicy((current) => ({ ...current, rules: current.rules.filter((rule) => rule.id !== ruleID) }))
      setStatus(`Removed rule ${ruleID}`)
      void load()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to remove permission rule')
    } finally {
      setBusyRuleID(null)
    }
  }

  const handleReset = async () => {
    setResetting(true)
    setError(null)
    setStatus(null)
    try {
      setPolicy(await resetPermissionPolicy())
      setStatus('Permission policy reset to defaults')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to reset permission policy')
    } finally {
      setResetting(false)
    }
  }

  const handleExplain = async () => {
    if (!explainTool.trim()) {
      setError('Tool name is required for explain.')
      return
    }
    setExplaining(true)
    setError(null)
    try {
      setExplainResult(await explainPermission(explainTool, explainArguments))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to explain permission')
    } finally {
      setExplaining(false)
    }
  }

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6">
        <h1 className="text-xl font-semibold text-[var(--app-text)]">Permissions</h1>
        <p className="mt-1 text-sm text-[var(--app-text-muted)]">
          Mirror the TUI permission controls here: view the policy, add always-allow or always-deny rules, remove rules, reset defaults, and preview how a tool request will resolve.
        </p>
      </div>

      {error ? <div className="mb-4 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">{error}</div> : null}
      {status ? <div className="mb-4 rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-4 py-3 text-sm text-[var(--app-success)]">{status}</div> : null}

      <div className="grid gap-6 overflow-y-auto pb-12 pr-2">
        <section className="rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-5 shadow-sm">
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <div className="text-sm font-semibold text-[var(--app-text)]">Permission policy</div>
              <div className="text-xs text-[var(--app-text-muted)]">
                Version {policy.version || 0}{policy.updated_at ? ` · updated ${formatTimestamp(policy.updated_at)}` : ''}
              </div>
            </div>
            <Button variant="outline" onClick={() => void handleReset()} disabled={loading || resetting || saving}>
              {resetting ? 'Resetting…' : 'Reset defaults'}
            </Button>
          </div>

          <div className="mt-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)]">
            {loading ? (
              <div className="px-4 py-4 text-sm text-[var(--app-text-muted)]">Loading permission policy…</div>
            ) : sortedRules.length === 0 ? (
              <div className="px-4 py-4 text-sm text-[var(--app-text-muted)]">
                No explicit rules. Built-in defaults still apply, like asking for most actions and allowing safe bash prefixes.
              </div>
            ) : (
              <div className="divide-y divide-[var(--app-border)]">
                {sortedRules.map((rule) => (
                  <div key={rule.id} className="flex flex-col gap-3 px-4 py-4 sm:flex-row sm:items-start sm:justify-between">
                    <div className="min-w-0 flex-1">
                      <div className="text-sm font-medium text-[var(--app-text)]">{describeRule(rule)}</div>
                      <div className="mt-1 text-xs text-[var(--app-text-muted)]">
                        {rule.id}
                        {rule.updated_at ? ` · updated ${formatTimestamp(rule.updated_at)}` : ''}
                      </div>
                    </div>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="self-start text-[var(--app-danger)] hover:text-[var(--app-danger)]"
                      onClick={() => void handleDeleteRule(rule.id)}
                      disabled={busyRuleID === rule.id}
                    >
                      {busyRuleID === rule.id ? 'Removing…' : 'Remove'}
                    </Button>
                  </div>
                ))}
              </div>
            )}
          </div>
        </section>

        <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-5">
          <div className="text-sm font-semibold text-[var(--app-text)]">Add policy rule</div>
          <div className="mt-1 text-xs text-[var(--app-text-muted)]">
            This is the desktop equivalent of commands like /permissions allow tool bash or /permissions deny bash-prefix rm.
          </div>

          <div className="mt-4 grid gap-4 md:grid-cols-3">
            <label className="grid gap-2">
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Decision</span>
              <select
                value={decision}
                onChange={(event) => setDecision(event.target.value as 'allow' | 'ask' | 'deny')}
                className="h-10 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] px-3 text-sm text-[var(--app-text)] outline-none transition-colors hover:border-[var(--app-border-strong)] focus:border-[var(--app-primary)]"
              >
                {DECISION_OPTIONS.map((option) => (
                  <option key={option.value} value={option.value}>{option.label}</option>
                ))}
              </select>
            </label>

            <label className="grid gap-2">
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Match type</span>
              <select
                value={kind}
                onChange={(event) => setKind(event.target.value as 'tool' | 'bash-prefix' | 'phrase')}
                className="h-10 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] px-3 text-sm text-[var(--app-text)] outline-none transition-colors hover:border-[var(--app-border-strong)] focus:border-[var(--app-primary)]"
              >
                {KIND_OPTIONS.map((option) => (
                  <option key={option.value} value={option.value}>{option.label}</option>
                ))}
              </select>
            </label>

            <label className="grid gap-2 md:col-span-1">
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">{valueLabel}</span>
              <Input
                value={value}
                onChange={(event) => setValue(event.target.value)}
                placeholder={valuePlaceholder}
                className="bg-[var(--app-bg)] border-[var(--app-border)] text-[var(--app-text)]"
              />
            </label>
          </div>

          <div className="mt-4 flex justify-end">
            <Button variant="primary" onClick={() => void handleCreateRule()} disabled={saving || loading}>
              {saving ? 'Saving…' : 'Save rule'}
            </Button>
          </div>
        </section>

        <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-5">
          <div className="text-sm font-semibold text-[var(--app-text)]">Explain a request</div>
          <div className="mt-1 text-xs text-[var(--app-text-muted)]">
            Equivalent to /permissions explain &lt;tool&gt; [arguments]. Use it to preview what the backend will do for a tool call.
          </div>

          <div className="mt-4 grid gap-4 lg:grid-cols-[180px_minmax(0,1fr)]">
            <label className="grid gap-2">
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Tool</span>
              <Input
                value={explainTool}
                onChange={(event) => setExplainTool(event.target.value)}
                placeholder="bash"
                className="bg-[var(--app-bg)] border-[var(--app-border)] text-[var(--app-text)]"
              />
            </label>

            <label className="grid gap-2">
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Arguments JSON/text</span>
              <textarea
                value={explainArguments}
                onChange={(event) => setExplainArguments(event.target.value)}
                rows={4}
                className={cn(
                  'min-h-[120px] rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 text-sm text-[var(--app-text)] outline-none transition-colors hover:border-[var(--app-border-strong)] focus:border-[var(--app-primary)]',
                )}
                placeholder='{"command":"git status"}'
              />
            </label>
          </div>

          <div className="mt-4 flex justify-between gap-3">
            <div className="text-xs text-[var(--app-text-muted)]">
              Try bash, write, task, or any tool name used by the agent runtime.
            </div>
            <Button variant="outline" onClick={() => void handleExplain()} disabled={explaining}>
              {explaining ? 'Checking…' : 'Explain'}
            </Button>
          </div>

          {explainResult ? (
            <div className="mt-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
              <div className="grid gap-2 text-sm text-[var(--app-text)] sm:grid-cols-3">
                <div><span className="text-[var(--app-text-muted)]">Decision:</span> {explainResult.decision || '—'}</div>
                <div><span className="text-[var(--app-text-muted)]">Source:</span> {explainResult.source || '—'}</div>
                <div><span className="text-[var(--app-text-muted)]">Rule:</span> {explainResult.rule_preview || '—'}</div>
              </div>
              <div className="mt-3 text-sm text-[var(--app-text-muted)]">{explainResult.reason || 'No explanation available.'}</div>
            </div>
          ) : null}
        </section>
      </div>
    </div>
  )
}
