import { useEffect, useState, type FormEvent } from 'react'
import { Button } from '../../../../../components/ui/button'
import { Select } from '../../../../../components/ui/select'
import { normalizeTaskContextMaxCompactions, type TaskContextSettings } from '../../swarm/types/swarm-settings'

export interface TaskContextSettingsProps {
  value: TaskContextSettings
  saving: boolean
  error?: string | null
  onSave: (value: TaskContextSettings) => void
}

export function TaskContextSettingsSection({ value, saving, error, onSave }: TaskContextSettingsProps) {
  const [maxCompactions, setMaxCompactions] = useState(value.maxCompactions)

  useEffect(() => setMaxCompactions(value.maxCompactions), [value.maxCompactions])

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    onSave({ maxCompactions: normalizeTaskContextMaxCompactions(maxCompactions) })
  }

  return (
    <section aria-labelledby="task-context-title" className="space-y-4">
      <div>
        <h3 id="task-context-title" className="text-base font-semibold text-[var(--app-text)]">Task context compaction</h3>
        <p className="mt-1 text-sm text-[var(--app-text-muted)]">
          Task-tool children compact in the same durable session at their verified provider-usage boundary. Compact keeps the immutable assignment, logical Task identity, prior compact structure, and trusted workspace/Git state without creating successor launches.
        </p>
      </div>
      {error ? <div role="alert" className="text-sm text-[var(--app-danger)]">{error}</div> : null}
      <form className="flex flex-wrap items-end justify-between gap-4" onSubmit={submit}>
        <label className="grid gap-1.5 text-xs font-medium text-[var(--app-text-muted)]">
          Maximum compactions per Task
          <Select value={maxCompactions} disabled={saving} onChange={(event) => setMaxCompactions(Number(event.target.value))}>
            {[1, 2, 3, 4, 5, 6, 8, 10].map((count) => <option key={count} value={count}>{count}</option>)}
          </Select>
        </label>
        <Button type="submit" variant="primary" disabled={saving}>{saving ? 'Saving…' : 'Save Task behavior'}</Button>
      </form>
    </section>
  )
}
