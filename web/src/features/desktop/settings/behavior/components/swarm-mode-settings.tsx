import { useEffect, useState, type FormEvent } from 'react'
import { Button } from '../../../../../components/ui/button'
import { MAX_SWARM_AGENTS, MIN_SWARM_AGENTS, normalizeMaxSwarmAgents } from '../../swarm/types/swarm-settings'
import type { SwarmModeSettings } from '../../swarm/types/swarm-settings'

export interface SwarmModeSettingsProps {
  value: SwarmModeSettings
  saving: boolean
  error?: string | null
  onSave: (maxAgents: number) => void
}

export function SwarmModeSettingsSection({ value, saving, error, onSave }: SwarmModeSettingsProps) {
  const [maxAgents, setMaxAgents] = useState(value.maxAgents)

  useEffect(() => setMaxAgents(value.maxAgents), [value.maxAgents])

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const normalized = normalizeMaxSwarmAgents(maxAgents)
    setMaxAgents(normalized)
    onSave(normalized)
  }

  return (
    <section aria-labelledby="swarm-mode-title" className="space-y-4">
      <div>
        <h3 id="swarm-mode-title" className="text-base font-semibold text-[var(--app-text)]">Swarm mode</h3>
        <p className="mt-1 text-sm text-[var(--app-text-muted)]">
          Sets the account maximum for one hierarchical swarm launch. Effective capacity is the lower of this maximum and the current subagent concurrency policy.
        </p>
        <p className="mt-2 text-xs leading-5 text-[var(--app-text-subtle)]">
          Routers expand themes, then refine each prompt in two internal stages. Those Router calls are not child sessions; only final Designer or Coder agents appear in the session grid.
        </p>
      </div>
      {error ? <div role="alert" className="text-sm text-[var(--app-danger)]">{error}</div> : null}
      <form className="flex flex-wrap items-end gap-3" onSubmit={submit}>
        <label className="grid min-w-48 gap-1.5 text-xs font-medium text-[var(--app-text-muted)]">
          Maximum agents
          <input
            className="h-9 rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-3 text-sm text-[var(--app-text)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]"
            type="number"
            min={MIN_SWARM_AGENTS}
            max={MAX_SWARM_AGENTS}
            step={1}
            value={maxAgents}
            disabled={saving}
            aria-describedby="swarm-mode-maximum-help"
            onChange={(event) => setMaxAgents(Number(event.target.value))}
          />
        </label>
        <Button type="submit" variant="primary" disabled={saving}>{saving ? 'Saving…' : 'Save maximum'}</Button>
      </form>
      <p id="swarm-mode-maximum-help" className="text-xs text-[var(--app-text-subtle)]">Choose 1–100 final agents. The default is 10.</p>
    </section>
  )
}
