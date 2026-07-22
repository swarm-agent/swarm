import { useEffect, useMemo, useState } from 'react'
import { requestJson } from '../../../../../app/api'
import { Button } from '../../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../../components/ui/dialog'
import { Input } from '../../../../../components/ui/input'
import { ModalCloseButton } from '../../../../../components/ui/modal-close-button'
import { cn } from '../../../../../lib/cn'
import { DEFAULT_PLAN_ACCEPTANCE_POLICY, DEFAULT_SESSION_DEPLOY_POLICY, fetchCapabilityPolicies, saveCapabilityPolicies, type PlanAcceptancePolicy, type SessionDeployPolicy } from '../../../permissions/services/capability-policy'

export interface PermissionRule {
  id: string
  kind: string
  decision: string
  tool?: string
  pattern?: string
  created_at?: number
  updated_at?: number
}

interface SubagentPolicy {
  mode: 'direct' | 'ask' | 'bounded'
  automatic_launches_per_parent_run: number
  active_child_limit: number
  over_budget_action: 'ask' | 'deny'
  absolute_wave_maximum: number
  max_depth: number
  require_write_isolation: boolean
}

interface PermissionPolicy {
  version: number
  rules: PermissionRule[]
  subagents?: SubagentPolicy
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
  bypass_permissions?: boolean
}

interface PermissionBypassResponse {
  ok?: boolean
  bypass_permissions?: boolean
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

const SESSION_MUTATION_POLICIES = [
  { tool: 'session_commit', title: 'Session commits', description: 'Create Git commits only for approved session-attributed files.' },
  { tool: 'session_archive', title: 'Session archives', description: 'Move active sessions into durable archive history.' },
  { tool: 'session_unarchive', title: 'Session unarchives', description: 'Restore archived sessions to the active workspace view.' },
] as const

type SessionMutationDecision = 'ask' | 'allow' | 'deny'

export function sessionMutationDecision(rules: PermissionRule[], tool: string): SessionMutationDecision {
  const rule = rules.find((candidate) => normalizeRuleKind(candidate.kind) === 'tool' && candidate.tool?.trim().toLowerCase() === tool)
  return rule?.decision === 'allow' || rule?.decision === 'deny' ? rule.decision : 'ask'
}

const MAX_SUBAGENT_WAVE_SIZE = 256
const MAX_SUBAGENT_DEPTH = 16

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

async function fetchPermissionPolicy(): Promise<{ policy: PermissionPolicy; bypassPermissions: boolean }> {
  const response = await requestJson<PermissionPolicyResponse>('/v1/permissions')
  return { policy: response.policy ?? { version: 0, rules: [] }, bypassPermissions: Boolean(response.bypass_permissions) }
}

async function saveSubagentPolicy(policy: SubagentPolicy): Promise<SubagentPolicy> {
  const response = await requestJson<{ subagents?: SubagentPolicy }>('/v1/permissions/subagents', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(policy),
  })
  if (!response.subagents) throw new Error('Subagent policy save returned no policy')
  return response.subagents
}

async function setBypassPermissions(enabled: boolean): Promise<boolean> {
  const response = await requestJson<PermissionBypassResponse>('/v1/permissions/bypass', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled }),
  })
  return Boolean(response.bypass_permissions)
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
  const [bypassPermissions, setBypassPermissionsState] = useState(false)
  const [subagentPolicy, setSubagentPolicy] = useState<SubagentPolicy>({ mode: 'bounded', automatic_launches_per_parent_run: 5, active_child_limit: 5, over_budget_action: 'ask', absolute_wave_maximum: 16, max_depth: 2, require_write_isolation: true })
  const [subagentBusy, setSubagentBusy] = useState(false)
  const [sessionDeployPolicy, setSessionDeployPolicy] = useState<SessionDeployPolicy>(DEFAULT_SESSION_DEPLOY_POLICY)
  const [planAcceptancePolicy, setPlanAcceptancePolicy] = useState<PlanAcceptancePolicy>(DEFAULT_PLAN_ACCEPTANCE_POLICY)
  const [capabilityBusy, setCapabilityBusy] = useState(false)
  const [bypassBusy, setBypassBusy] = useState(false)
  const [confirmBypassOpen, setConfirmBypassOpen] = useState(false)
  const [busyRuleID, setBusyRuleID] = useState<string | null>(null)
  const [busyMutationTool, setBusyMutationTool] = useState<string | null>(null)
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
      const [result, capabilities] = await Promise.all([fetchPermissionPolicy(), fetchCapabilityPolicies()])
      setPolicy(result.policy)
      if (result.policy.subagents) setSubagentPolicy(result.policy.subagents)
      setSessionDeployPolicy(capabilities.session_deploy)
      setPlanAcceptancePolicy(capabilities.plan_acceptance)
      setBypassPermissionsState(result.bypassPermissions)
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

  const handleSaveSubagents = async () => {
    setSubagentBusy(true)
    setError(null)
    setStatus(null)
    try {
      const saved = await saveSubagentPolicy(subagentPolicy)
      setSubagentPolicy(saved)
      setStatus('Subagent orchestration policy saved')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save subagent policy')
    } finally {
      setSubagentBusy(false)
    }
  }

  const handleSaveCapabilities = async () => {
    setCapabilityBusy(true)
    setError(null)
    setStatus(null)
    try {
      const saved = await saveCapabilityPolicies({ session_deploy: sessionDeployPolicy, plan_acceptance: planAcceptancePolicy })
      setSessionDeployPolicy(saved.session_deploy)
      setPlanAcceptancePolicy(saved.plan_acceptance)
      setStatus('Session deployment and plan acceptance policies saved')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save capability policies')
    } finally {
      setCapabilityBusy(false)
    }
  }

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

  const handleSessionMutationPolicy = async (tool: string, next: SessionMutationDecision) => {
    setBusyMutationTool(tool)
    setError(null)
    setStatus(null)
    try {
      const matching = policy.rules.filter((rule) => normalizeRuleKind(rule.kind) === 'tool' && rule.tool?.trim().toLowerCase() === tool)
      for (const rule of matching) await deletePermissionRule(rule.id)
      if (next !== 'ask') await createPermissionRule({ decision: next, kind: 'tool', value: tool })
      const result = await fetchPermissionPolicy()
      setPolicy(result.policy)
      setStatus(`${SESSION_MUTATION_POLICIES.find((entry) => entry.tool === tool)?.title || tool}: ${next === 'ask' ? 'Ask every time' : next === 'allow' ? 'Always allow' : 'Always deny'}`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update session mutation policy')
      await load()
    } finally {
      setBusyMutationTool(null)
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

  const applyBypassPermissions = async (enabled: boolean) => {
    setBypassBusy(true)
    setError(null)
    setStatus(null)
    try {
      const saved = await setBypassPermissions(enabled)
      setBypassPermissionsState(saved)
      setConfirmBypassOpen(false)
      setStatus(saved ? 'Permissions OFF: bypass permissions enabled' : 'Permissions ON: prompts enabled')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update bypass permissions')
    } finally {
      setBypassBusy(false)
    }
  }

  const handleBypassButton = () => {
    if (bypassPermissions) {
      void applyBypassPermissions(false)
      return
    }
    setError(null)
    setStatus(null)
    setConfirmBypassOpen(true)
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
          Mirror the TUI permission controls here: view the policy, toggle global permissions, add always-allow or always-deny rules, remove rules, reset defaults, and preview how a tool request will resolve.
        </p>
      </div>

      {error ? <div className="mb-4 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">{error}</div> : null}
      {status ? <div className="mb-4 rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-4 py-3 text-sm text-[var(--app-success)]">{status}</div> : null}

      <div className="grid gap-6 overflow-y-auto pb-12 pr-2">
        <section className="rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-5 shadow-sm">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <div className="text-sm font-semibold text-[var(--app-text)]">Global permissions</div>
              <div className="text-xs text-[var(--app-text-muted)]">
                {bypassPermissions ? 'Permissions are OFF. Tool approval prompts are bypassed globally.' : 'Permissions are ON. Tool approval prompts are enforced.'}
              </div>
            </div>
            <Button
              variant="outline"
              onClick={handleBypassButton}
              disabled={loading || bypassBusy}
              className={cn(
                !bypassPermissions &&
                  'border-[var(--app-primary)] text-[var(--app-primary)] hover:bg-[color-mix(in_oklab,var(--app-primary)_10%,transparent)] hover:text-[var(--app-primary-hover)]',
              )}
            >
              {bypassBusy ? 'Saving…' : bypassPermissions ? 'Turn permissions ON' : 'Turn permissions OFF'}
            </Button>
          </div>
        </section>

        <section className="min-w-0 rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-5 shadow-sm">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div className="min-w-0">
              <div className="text-sm font-semibold text-[var(--app-text)]">Subagents</div>
              <div className="mt-1 text-xs text-[var(--app-text-muted)]">Control when Explorer and Coder can help. Limits are maximums, not targets.</div>
            </div>
            <Button className="w-full shrink-0 sm:w-auto" variant="outline" onClick={() => void handleSaveSubagents()} disabled={loading || subagentBusy}>
              {subagentBusy ? 'Saving…' : 'Save'}
            </Button>
          </div>
          <div className="mt-4 grid min-w-0 grid-cols-1 gap-4 sm:grid-cols-2">
            <label className="grid min-w-0 gap-2">
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Mode</span>
              <select value={subagentPolicy.mode} onChange={(event) => setSubagentPolicy((value) => ({ ...value, mode: event.target.value as SubagentPolicy['mode'] }))} className="h-10 min-w-0 w-full rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] px-3 text-sm">
                <option value="direct">Direct — no delegation</option>
                <option value="ask">Ask — review every wave</option>
                <option value="bounded">Bounded automatic</option>
              </select>
            </label>
            <label className="grid min-w-0 gap-2">
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Automatic starts per run</span>
              <Input className="min-w-0 w-full" type="number" min={0} max={MAX_SUBAGENT_WAVE_SIZE} value={subagentPolicy.automatic_launches_per_parent_run} onChange={(event) => setSubagentPolicy((value) => ({ ...value, automatic_launches_per_parent_run: Number(event.target.value) }))} />
            </label>
            <label className="grid min-w-0 gap-2">
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Running at once</span>
              <Input className="min-w-0 w-full" type="number" min={1} max={MAX_SUBAGENT_WAVE_SIZE} value={subagentPolicy.active_child_limit} onChange={(event) => setSubagentPolicy((value) => ({ ...value, active_child_limit: Number(event.target.value) }))} />
            </label>
            <label className="grid min-w-0 gap-2">
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Largest single wave</span>
              <Input className="min-w-0 w-full" type="number" min={1} max={MAX_SUBAGENT_WAVE_SIZE} value={subagentPolicy.absolute_wave_maximum} onChange={(event) => setSubagentPolicy((value) => ({ ...value, absolute_wave_maximum: Number(event.target.value) }))} />
            </label>
            <label className="grid min-w-0 gap-2">
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Delegation depth</span>
              <Input className="min-w-0 w-full" type="number" min={0} max={MAX_SUBAGENT_DEPTH} value={subagentPolicy.max_depth} onChange={(event) => setSubagentPolicy((value) => ({ ...value, max_depth: Number(event.target.value) }))} />
            </label>
            <label className="grid min-w-0 gap-2">
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">When the limit is reached</span>
              <select value={subagentPolicy.over_budget_action} onChange={(event) => setSubagentPolicy((value) => ({ ...value, over_budget_action: event.target.value as SubagentPolicy['over_budget_action'] }))} className="h-10 min-w-0 w-full rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] px-3 text-sm">
                <option value="ask">Ask before starting the wave</option>
                <option value="deny">Do not start the wave</option>
              </select>
            </label>
          </div>
          <div className="mt-4 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-3 text-xs leading-5 text-[var(--app-text-muted)]">
            <div><span className="font-medium text-[var(--app-text)]">Automatic launches per run:</span> The total number of child agents a parent can start without asking during one run.</div>
            <div className="mt-1"><span className="font-medium text-[var(--app-text)]">Children running at once:</span> The independent concurrency ceiling; completed children release capacity without restoring the cumulative automatic budget.</div>
            <div className="mt-1"><span className="font-medium text-[var(--app-text)]">Largest single wave:</span> The maximum children in one task call. It does not cap the cumulative budget or concurrency setting.</div>
            <div className="mt-1"><span className="font-medium text-[var(--app-text)]">Delegation depth:</span> How many nested generations may delegate; zero disables child delegation.</div>
          </div>
        </section>

        <section className="rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-5 shadow-sm">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div><div className="text-sm font-semibold text-[var(--app-text)]">Session deployment &amp; plan acceptance</div><div className="mt-1 text-xs text-[var(--app-text-muted)]">Account-scoped capability policies. Fresh accounts ask for both operations.</div></div>
            <Button variant="outline" onClick={() => void handleSaveCapabilities()} disabled={loading || capabilityBusy}>{capabilityBusy ? 'Saving…' : 'Save'}</Button>
          </div>
          <div className="mt-4 grid gap-4 sm:grid-cols-2">
            <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
              <div className="text-sm font-medium text-[var(--app-text)]">Session deployment</div>
              <label className="mt-3 grid gap-2"><span className="text-xs text-[var(--app-text-muted)]">Policy</span><select aria-label="Session deployment policy" value={sessionDeployPolicy.mode} onChange={(event) => setSessionDeployPolicy((current) => ({ ...current, mode: event.target.value as SessionDeployPolicy['mode'] }))} className="h-10 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] px-3 text-sm"><option value="ask">Ask every time</option><option value="always_allow">Always allow</option><option value="bounded">Bounded automatic</option></select></label>
              <label className="mt-3 grid gap-2"><span className="text-xs text-[var(--app-text-muted)]">Automatic deployments per parent run</span><Input aria-label="Automatic deployments per parent run" type="number" min={0} max={256} disabled={sessionDeployPolicy.mode !== 'bounded'} value={sessionDeployPolicy.automatic_deployments_per_parent_run} onChange={(event) => setSessionDeployPolicy((current) => ({ ...current, automatic_deployments_per_parent_run: Number(event.target.value) }))} /></label>
              <label className="mt-3 grid gap-2"><span className="text-xs text-[var(--app-text-muted)]">When the limit is reached</span><select aria-label="Deployment over-limit action" disabled={sessionDeployPolicy.mode !== 'bounded'} value={sessionDeployPolicy.over_limit_action} onChange={(event) => setSessionDeployPolicy((current) => ({ ...current, over_limit_action: event.target.value as SessionDeployPolicy['over_limit_action'] }))} className="h-10 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] px-3 text-sm disabled:opacity-50"><option value="ask">Ask</option><option value="deny">Deny</option></select></label>
            </div>
            <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
              <div className="text-sm font-medium text-[var(--app-text)]">Plan acceptance</div><div className="mt-1 text-xs text-[var(--app-text-muted)]">Controls validated structured-plan acceptance only; continuation choices remain per plan.</div>
              <label className="mt-3 grid gap-2"><span className="text-xs text-[var(--app-text-muted)]">Policy</span><select aria-label="Plan acceptance policy" value={planAcceptancePolicy.mode} onChange={(event) => setPlanAcceptancePolicy({ mode: event.target.value as PlanAcceptancePolicy['mode'] })} className="h-10 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] px-3 text-sm"><option value="ask">Ask every time</option><option value="always_allow">Always allow</option></select></label>
            </div>
          </div>
        </section>

        <section className="rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-5 shadow-sm">
          <div className="text-sm font-semibold text-[var(--app-text)]">Session mutations</div>
          <div className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">Each operation has an isolated persistent identity. A generic <span className="font-mono">manage_sessions</span> rule does not authorize commits, archives, or unarchives.</div>
          <div className="mt-4 grid gap-3">
            {SESSION_MUTATION_POLICIES.map((item) => {
              const selected = sessionMutationDecision(policy.rules, item.tool)
              return (
                <label key={item.tool} className="flex flex-col gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4 sm:flex-row sm:items-center sm:justify-between">
                  <span className="min-w-0"><span className="block text-sm font-medium text-[var(--app-text)]">{item.title}</span><span className="mt-1 block text-xs text-[var(--app-text-muted)]">{item.description}</span></span>
                  <select aria-label={`${item.title} policy`} value={selected} disabled={loading || busyMutationTool === item.tool} onChange={(event) => void handleSessionMutationPolicy(item.tool, event.target.value as SessionMutationDecision)} className="h-10 w-full rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] px-3 text-sm sm:w-44">
                    <option value="ask">Ask every time</option><option value="allow">Always allow</option><option value="deny">Always deny</option>
                  </select>
                </label>
              )
            })}
          </div>
        </section>

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

      {confirmBypassOpen ? (
        <Dialog role="dialog" aria-modal="true" aria-label="Turn off permissions" className="z-[80] p-4 sm:p-6">
          <DialogBackdrop onClick={() => { if (!bypassBusy) setConfirmBypassOpen(false) }} />
          <DialogPanel className="w-[min(520px,100%)] rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)]">
            <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-6 py-5">
              <div>
                <h2 className="text-lg font-semibold text-[var(--app-text)]">Turn OFF permissions?</h2>
                <p className="mt-1 text-sm text-[var(--app-text-muted)]">This changes the global daemon setting and persists it to swarm.conf.</p>
              </div>
              <ModalCloseButton onClick={() => setConfirmBypassOpen(false)} disabled={bypassBusy} aria-label="Close bypass permissions confirmation" />
            </div>
            <div className="px-6 py-5 text-sm text-[var(--app-text)]">
              This will turn OFF permissions, and may be dangerous depending on how you set up your environment.
            </div>
            <div className="flex justify-end gap-2 border-t border-[var(--app-border)] px-6 py-4">
              <Button variant="ghost" onClick={() => setConfirmBypassOpen(false)} disabled={bypassBusy}>Cancel</Button>
              <Button
                variant="outline"
                onClick={() => { void applyBypassPermissions(true) }}
                disabled={bypassBusy}
                className="border-[var(--app-primary)] text-[var(--app-primary)] hover:bg-[color-mix(in_oklab,var(--app-primary)_10%,transparent)] hover:text-[var(--app-primary-hover)]"
              >
                {bypassBusy ? 'Saving…' : 'Turn permissions OFF'}
              </Button>
            </div>
          </DialogPanel>
        </Dialog>
      ) : null}
    </div>
  )
}
