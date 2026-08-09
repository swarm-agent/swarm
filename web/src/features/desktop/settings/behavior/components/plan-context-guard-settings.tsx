import { useEffect, useState, type FormEvent } from 'react'
import { Button } from '../../../../../components/ui/button'
import { Select } from '../../../../../components/ui/select'
import {
  normalizePlanContextGuardMaxCompactions,
  normalizePlanContextGuardUsedPercent,
} from '../../swarm/types/swarm-settings'
import type { PlanContextGuardSettings } from '../../swarm/types/swarm-settings'

export interface PlanContextGuardSettingsProps {
  value: PlanContextGuardSettings
  saving: boolean
  error?: string | null
  onSave: (value: PlanContextGuardSettings) => void
}

export function PlanContextGuardSettingsSection({ value, saving, error, onSave }: PlanContextGuardSettingsProps) {
  const [enabled, setEnabled] = useState(value.enabled)
  const [usedPercent, setUsedPercent] = useState(value.usedPercent)
  const [maxCompactions, setMaxCompactions] = useState(value.maxCompactions)

  useEffect(() => {
    setEnabled(value.enabled)
    setUsedPercent(value.usedPercent)
    setMaxCompactions(value.maxCompactions)
  }, [value.enabled, value.maxCompactions, value.usedPercent])

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    onSave({
      enabled,
      usedPercent: normalizePlanContextGuardUsedPercent(usedPercent),
      maxCompactions: normalizePlanContextGuardMaxCompactions(maxCompactions),
    })
  }

  const fieldClass = 'grid gap-1.5 text-xs font-medium text-[var(--app-text-muted)]'
  return (
    <section aria-labelledby="plan-context-guard-title" className="space-y-4">
      <div>
        <h3 id="plan-context-guard-title" className="text-base font-semibold text-[var(--app-text)]">Plan context guard</h3>
        <p className="mt-1 text-sm text-[var(--app-text-muted)]">
          Warns Plan when trustworthy usage reaches the selected context boundary. Plan can finalize or create a durable handoff in fresh context; after the compaction allowance is exhausted, it must finalize instead of starting another research cycle.
        </p>
      </div>
      {error ? <div role="alert" className="text-sm text-[var(--app-danger)]">{error}</div> : null}
      <form className="grid gap-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] md:items-end" onSubmit={submit}>
        <label className={fieldClass}>
          Used-context warning
          <Select value={usedPercent} disabled={saving || !enabled} onChange={(event) => setUsedPercent(Number(event.target.value))}>
            {[50, 60, 70, 75, 80, 85, 90, 95].map((percent) => <option key={percent} value={percent}>{percent}% used</option>)}
          </Select>
        </label>
        <label className={fieldClass}>
          Maximum durable handoffs
          <Select value={maxCompactions} disabled={saving || !enabled} onChange={(event) => setMaxCompactions(Number(event.target.value))}>
            {[0, 1, 2, 3].map((count) => <option key={count} value={count}>{count === 0 ? 'None — finalize immediately' : `${count}`}</option>)}
          </Select>
        </label>
        <div className="flex flex-wrap items-center justify-end gap-3">
          <label className="flex items-center gap-2 text-sm text-[var(--app-text)]">
            <input type="checkbox" checked={enabled} disabled={saving} onChange={(event) => setEnabled(event.target.checked)} />
            Enabled
          </label>
          <Button type="submit" variant="primary" disabled={saving}>{saving ? 'Saving…' : 'Save guard'}</Button>
        </div>
      </form>
    </section>
  )
}
