import type { WorkerAutomation } from '../state/desktop-workers-api'

export function formatWorkerSchedule(auto: WorkerAutomation): string {
  const s = auto.schedule
  if (!s) return auto.activation_mode === 'manual' ? 'Manual' : `${auto.activation_mode} (schedule unavailable)`
  if (s.kind === 'interval') return `Every ${s.interval_seconds ?? '?'} seconds (${s.timezone || 'timezone not specified'})`
  if (s.kind === 'cron') return `${s.cron || 'Cron not specified'} (${s.timezone || 'timezone not specified'})`
  return `External trigger (${auto.trigger?.trigger_kind || 'configuration unavailable'})`
}
