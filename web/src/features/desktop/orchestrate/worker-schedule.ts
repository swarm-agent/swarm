import type { WorkerAutomation } from '../state/desktop-workers-api'

export function formatWorkerSchedule(auto: WorkerAutomation): string {
  const s = auto.schedule
  if (!s) return auto.activation_mode === 'manual' ? 'Manual' : `${auto.activation_mode} (schedule unavailable)`
  if (s.kind === 'interval') return `Every ${s.interval_seconds ?? '?'} seconds (${s.timezone || 'timezone not specified'})`
  if (s.kind === 'cron') return `${s.cron || 'Cron not specified'} (${s.timezone || 'timezone not specified'})`
  return `External trigger (${auto.trigger?.trigger_kind || 'configuration unavailable'})`
}

/** Start mechanism is independent of the local execution target. */
export function proposalJobTiming(auto: WorkerAutomation): string {
  let timing: string
  const s = auto.schedule
  switch (auto.activation_mode) {
    case 'manual': timing = 'On demand · waits for an explicit task; not recurring'; break
    case 'external_trigger': timing = `External trigger · ${auto.trigger?.trigger_kind || 'configuration unavailable'} · runs on each accepted event`; break
    case 'interval': {
      const seconds = s?.kind === 'interval' ? s.interval_seconds : undefined
      const unit = seconds && seconds % 86400 === 0 ? [seconds / 86400, 'day'] : seconds && seconds % 3600 === 0 ? [seconds / 3600, 'hour'] : seconds && seconds % 60 === 0 ? [seconds / 60, 'minute'] : [seconds, 'second']
      timing = seconds && seconds > 0 ? `Recurring · every ${unit[0]} ${unit[1]}${unit[0] === 1 ? '' : 's'} (${s?.timezone || 'timezone not specified'}) · starts after approval` : 'Recurring interval · timing unavailable'
      break
    }
    case 'cron': {
      const cron = s?.kind === 'cron' ? s.cron : undefined
      const daily = cron?.match(/^(\d{1,2}) (\d{1,2}) \* \* \*$/)
      const readable = daily && Number(daily[1]) < 60 && Number(daily[2]) < 24 ? `daily at ${daily[2].padStart(2, '0')}:${daily[1].padStart(2, '0')}` : cron ? `cron ${cron}` : 'timing unavailable'
      timing = `Recurring · ${readable} (${s?.timezone || 'timezone not specified'}) · starts after approval`
      break
    }
    default: timing = 'Start configuration unavailable'
  }
  return auto.enabled ? timing : `Disabled; will not run · ${timing}`
}
