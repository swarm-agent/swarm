import { useEffect, useState, type ChangeEvent } from 'react'
import { requestJson } from '../../../../../app/api'
import { Input } from '../../../../../components/ui/input'
import { Button } from '../../../../../components/ui/button'

interface WorktreeSettingsWire {
  workspace_path?: string
  enabled?: boolean
  use_current_branch?: boolean
  base_branch?: string
  branch_name?: string
  updated_at?: number
}

interface WorktreeSettingsResponseWire {
  ok?: boolean
  worktrees?: WorktreeSettingsWire
}

interface WorktreeSettings {
  workspacePath: string
  enabled: boolean
  useCurrentBranch: boolean
  baseBranch: string
  branchName: string
  updatedAt: number
}

const defaultCreatedBranch = 'agent'

function normalizeBranchPrefix(value: string): string {
  const trimmed = value.trim().replace(/^\/+|\/+$/g, '')
  if (!trimmed) {
    return defaultCreatedBranch
  }
  if (trimmed.toLowerCase() === 'agent/<id>') {
    return defaultCreatedBranch
  }
  if (trimmed.toLowerCase().endsWith('/<id>')) {
    const withoutSuffix = trimmed.slice(0, -'/<id>'.length).replace(/^\/+|\/+$/g, '')
    return withoutSuffix || defaultCreatedBranch
  }
  return trimmed
}

function normalizeSettings(payload?: WorktreeSettingsWire | null): WorktreeSettings {
  return {
    workspacePath: typeof payload?.workspace_path === 'string' ? payload.workspace_path.trim() : '',
    enabled: Boolean(payload?.enabled),
    useCurrentBranch: payload?.use_current_branch !== false,
    baseBranch: typeof payload?.base_branch === 'string' ? payload.base_branch.trim() : '',
    branchName: typeof payload?.branch_name === 'string' && payload.branch_name.trim() !== '' ? normalizeBranchPrefix(payload.branch_name) : defaultCreatedBranch,
    updatedAt: typeof payload?.updated_at === 'number' ? payload.updated_at : 0,
  }
}

async function getWorktreeSettings(): Promise<WorktreeSettings> {
  const response = await requestJson<WorktreeSettingsResponseWire>('/v1/worktrees')
  return normalizeSettings(response.worktrees)
}

async function saveWorktreeSettings(input: {
  enabled: boolean
  useCurrentBranch: boolean
  baseBranch: string
  branchName: string
}): Promise<WorktreeSettings> {
  const response = await requestJson<WorktreeSettingsResponseWire>('/v1/worktrees', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      enabled: input.enabled,
      use_current_branch: input.useCurrentBranch,
      base_branch: input.useCurrentBranch ? '' : input.baseBranch.trim(),
      branch_name: normalizeBranchPrefix(input.branchName),
    }),
  })
  return normalizeSettings(response.worktrees)
}

export function WorktreeSettingsPage() {
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [status, setStatus] = useState<string | null>(null)
  const [settings, setSettings] = useState<WorktreeSettings | null>(null)
  const [enabled, setEnabled] = useState(false)
  const [branchName, setBranchName] = useState(defaultCreatedBranch)
  const [branchSourceMode, setBranchSourceMode] = useState<'current' | 'base'>('current')
  const [baseBranch, setBaseBranch] = useState('')

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    setStatus(null)
    void getWorktreeSettings()
      .then((next) => {
        if (cancelled) {
          return
        }
        setSettings(next)
        setEnabled(next.enabled)
        setBranchName(next.branchName)
        setBranchSourceMode(next.useCurrentBranch ? 'current' : 'base')
        setBaseBranch(next.baseBranch)
      })
      .catch((err) => {
        if (cancelled) {
          return
        }
        setError(err instanceof Error ? err.message : 'Failed to load worktree settings')
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false)
        }
      })
    return () => {
      cancelled = true
    }
  }, [])

  const submit = async () => {
    const createdBranch = normalizeBranchPrefix(branchName)
    if (branchSourceMode === 'base' && !baseBranch.trim()) {
      setError('Base branch is required when branch-off source is a specific branch.')
      return
    }
    setSaving(true)
    setError(null)
    setStatus(null)
    try {
      const next = await saveWorktreeSettings({
        enabled,
        useCurrentBranch: branchSourceMode === 'current',
        baseBranch,
        branchName: createdBranch,
      })
      setSettings(next)
      setEnabled(next.enabled)
      setBranchName(next.branchName)
      setBranchSourceMode(next.useCurrentBranch ? 'current' : 'base')
      setBaseBranch(next.baseBranch)
      setStatus(`Saved worktree settings. Created branch prefix: ${next.branchName}. Worktree branches will be created as ${next.branchName}/<id>. Branch-off source: ${next.useCurrentBranch ? 'current branch' : next.baseBranch}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save worktree settings')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex h-full flex-col">
      <div className="mb-8">
        <h1 className="text-xl font-semibold text-[var(--app-text)]">Worktrees</h1>
        <p className="mt-1 text-sm text-[var(--app-text-muted)]">Configure isolation settings for concurrent agent runs.</p>
      </div>

      {error ? <div className="mb-4 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">{error}</div> : null}
      {status ? <div className="mb-4 rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-4 py-3 text-sm text-[var(--app-success)]">{status}</div> : null}

      <div className="space-y-6">
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-[var(--app-text)]">Enable Automatic Worktrees</label>
          <select
            value={enabled ? 'on' : 'off'}
            onChange={(e) => setEnabled(e.target.value === 'on')}
            disabled={loading || saving}
            className="w-full max-w-md h-10 px-3 rounded-md bg-[var(--app-surface-subtle)] border border-[var(--app-border)] text-sm text-[var(--app-text)] outline-none transition-colors hover:border-[var(--app-border-strong)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)]"
          >
            <option value="on">Enabled</option>
            <option value="off">Disabled</option>
          </select>
          <span className="text-xs text-[var(--app-text-muted)] mt-1">Isolate tasks to prevent workspace collisions.</span>
        </div>

        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-[var(--app-text)]">Created branch prefix</label>
          <div className="flex items-center w-full max-w-md bg-[var(--app-surface-subtle)] border border-[var(--app-border)] rounded-md focus-within:border-[var(--app-primary)] focus-within:ring-1 focus-within:ring-[var(--app-primary)] transition-colors">
            <input
              type="text"
              value={branchName}
              onChange={(event: ChangeEvent<HTMLInputElement>) => setBranchName(event.target.value)}
              disabled={loading || saving}
              placeholder={defaultCreatedBranch}
              autoComplete="off"
              className="flex-1 bg-transparent px-3 py-2 text-sm text-[var(--app-text)] outline-none placeholder:text-[var(--app-text-muted)]"
            />
            <span className="pr-3 text-sm font-mono text-[var(--app-text-muted)] opacity-70">/&lt;id&gt;</span>
          </div>
          <span className="text-xs text-[var(--app-text-muted)] mt-1">Example: <span className="font-mono">{normalizeBranchPrefix(branchName)}/1a2b3c4d</span></span>
        </div>

        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-[var(--app-text)]">Branch-off source</label>
          <select
            value={branchSourceMode}
            onChange={(event: ChangeEvent<HTMLSelectElement>) => setBranchSourceMode(event.target.value === 'base' ? 'base' : 'current')}
            disabled={loading || saving}
            className="w-full max-w-md h-10 px-3 rounded-md bg-[var(--app-surface-subtle)] border border-[var(--app-border)] text-sm text-[var(--app-text)] outline-none transition-colors hover:border-[var(--app-border-strong)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)]"
          >
            <option value="current">Current branch</option>
            <option value="base">Specific base branch</option>
          </select>
        </div>

        {branchSourceMode === 'base' ? (
          <div className="flex flex-col gap-2">
            <label className="text-sm font-medium text-[var(--app-text)]">Base branch name</label>
            <Input
              type="text"
              value={baseBranch}
              onChange={(event: ChangeEvent<HTMLInputElement>) => setBaseBranch(event.target.value)}
              disabled={loading || saving}
              placeholder="main"
              autoComplete="off"
              className="w-full max-w-md bg-[var(--app-surface-subtle)] border-[var(--app-border)] text-[var(--app-text)]"
            />
          </div>
        ) : null}

        <div className="mt-8 pt-6 border-t border-[var(--app-border)] flex items-center justify-between">
          <div className="flex flex-col text-[var(--app-text-muted)] text-sm">
            <span className="font-medium">Workspace:</span>
            <span className="font-mono text-xs opacity-70">{settings?.workspacePath || '—'}</span>
          </div>
          <Button
            className="border border-[var(--app-primary)] bg-transparent text-[var(--app-primary)] hover:bg-[var(--app-surface-subtle)] hover:border-[var(--app-primary)] active:bg-[var(--app-surface-hover)]"
            onClick={() => void submit()}
            disabled={loading || saving}
          >
            {saving ? 'Saving…' : loading ? 'Loading…' : 'Save Changes'}
          </Button>
        </div>
      </div>
    </div>
  )
}
