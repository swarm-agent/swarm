import type { AutomationV2Settings } from '../../state/desktop-automation-v2-api'

export type AutomationSchedule = AutomationV2Settings['schedule']
export const intervalPresets = [300, 900, 1800, 3600, 7200, 14400, 43200, 86400]
export const weekdays = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

export function intervalLabel(seconds: number): string {
  if (!Number.isSafeInteger(seconds) || seconds < 60) return 'Choose an interval'
  const units = [[86400, 'day'], [3600, 'hour'], [60, 'minute']] as const
  for (const [size, name] of units) if (seconds % size === 0) {
    const count = seconds / size
    return `Every ${count === 1 ? '' : `${count} `}${name}${count === 1 ? '' : 's'}`
  }
  const minutes = Math.floor(seconds / 60), remainder = seconds % 60
  return `Every ${minutes} min ${remainder} sec`
}

// Only losslessly representable schedules enter the simple editor. All other
// supported cron expressions remain intact in Advanced, including */n fields.
export function scheduleMode(schedule: AutomationSchedule): 'interval' | 'daily' | 'weekly' | 'advanced' {
  if (schedule.kind === 'interval') return 'interval'
  const fields = (schedule.cron ?? '').trim().split(/\s+/)
  if (fields.length === 5 && /^\d+$/.test(fields[0]) && Number(fields[0]) < 60 && /^\d+$/.test(fields[1]) && Number(fields[1]) < 24 && fields[2] === '*' && fields[3] === '*') {
    if (fields[4] === '*') return 'daily'
    if (/^[0-6]$/.test(fields[4])) return 'weekly'
  }
  return 'advanced'
}

export function scheduleTime(schedule: AutomationSchedule): string {
  if (!['daily', 'weekly'].includes(scheduleMode(schedule))) return ''
  const [minute, hour] = schedule.cron!.trim().split(/\s+/)
  return `${hour.padStart(2, '0')}:${minute.padStart(2, '0')}`
}

export function scheduleLabel(schedule: AutomationSchedule): string {
  if (schedule.kind === 'interval') return intervalLabel(schedule.interval_seconds ?? 0)
  const mode = scheduleMode(schedule)
  if (mode === 'daily') return `Daily at ${scheduleTime(schedule)}`
  if (mode === 'weekly') return `${weekdays[Number(schedule.cron!.trim().split(/\s+/)[4])]} at ${scheduleTime(schedule)}`
  return `Custom schedule · ${schedule.cron ?? ''}`
}

// A nominal cadence, not admissions or guaranteed completed runs. Wall-clock
// days can vary with DST, and intervals are anchored to acceptance, not midnight.
export function scheduleFrequency(schedule: AutomationSchedule): string {
  if (schedule.kind === 'interval') {
    const seconds = schedule.interval_seconds ?? 0
    if (!Number.isSafeInteger(seconds) || seconds < 60 || seconds > 31622400) return 'Choose a valid interval'
    const rate = 86400 / seconds
    if (rate < 1) return 'Less than 1 run / day on average'
    return `${Number.isInteger(rate) ? rate : `≈${Number(rate.toFixed(2))}`} ${rate === 1 ? 'run' : 'runs'} / day on average`
  }
  const mode = scheduleMode(schedule)
  if (mode === 'weekly') return '1 scheduled run / week'
  const fields = (schedule.cron ?? '').trim().split(/\s+/)
  const count = (field: string, size: number): number | null => {
    if (field === '*') return size
    if (/^\d+$/.test(field) && Number(field) < size) return 1
    if (/^\*\/\d+$/.test(field)) {
      const step = Number(field.slice(2))
      if (step >= 1 && step <= size) return Math.ceil(size / step)
    }
    return null
  }
  if (fields.length === 5 && fields.slice(2).every(f => f === '*')) {
    const minutes = count(fields[0], 60), hours = count(fields[1], 24)
    if (minutes !== null && hours !== null) {
      const rate = minutes * hours
      return `${rate} scheduled ${rate === 1 ? 'run' : 'runs'} / day · DST may vary`
    }
  }
  return 'Runs on matching calendar days'
}

export function formatScheduleTime(ms: number, timeZone?: string): string {
  try {
    return new Intl.DateTimeFormat(undefined, {
      ...(timeZone ? { timeZone } : {}),
      hour: '2-digit',
      minute: '2-digit',
    }).format(ms)
  } catch {
    return new Intl.DateTimeFormat(undefined, {
      hour: '2-digit',
      minute: '2-digit',
    }).format(ms)
  }
}

export function formatScheduleDateTime(ms: number, timeZone?: string): string {
  try {
    return new Intl.DateTimeFormat(undefined, {
      ...(timeZone ? { timeZone } : {}),
      dateStyle: 'medium',
      timeStyle: 'short',
    }).format(ms)
  } catch {
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: 'medium',
      timeStyle: 'short',
    }).format(ms)
  }
}

export function getOccurrenceDayKey(ms: number, timeZone: string): string {
  try {
    const formatter = new Intl.DateTimeFormat('en-CA', { timeZone, year: 'numeric', month: '2-digit', day: '2-digit' })
    return formatter.format(new Date(ms))
  } catch {
    return new Date(ms).toISOString().slice(0, 10)
  }
}

export function getScheduleDailyTotal(schedule?: AutomationSchedule, now = Date.now(), timezone?: string): number {
  if (!schedule) return 0
  if (schedule.kind === 'interval') {
    const seconds = schedule.interval_seconds ?? 0
    if (!Number.isSafeInteger(seconds) || seconds < 60 || seconds > 31622400) return 0
    return Math.floor(86400 / seconds)
  }
  if (schedule.kind === 'cron' && schedule.cron) {
    const tz = schedule.timezone || timezone || Intl.DateTimeFormat().resolvedOptions().timeZone
    const fields = schedule.cron.trim().split(/\s+/)
    if (fields.length !== 5) return 0
    if (fields[4] !== '*') {
      try {
        const dayStr = new Intl.DateTimeFormat('en-US', { timeZone: tz, weekday: 'short' }).format(new Date(now))
        const map: Record<string, number> = { Sun: 0, Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6 }
        const todayDow = map[dayStr] ?? new Date(now).getUTCDay()
        if (/^\d+$/.test(fields[4]) && Number(fields[4]) % 7 !== todayDow) {
          return 0
        }
      } catch {
        // Ignore timezone error
      }
    }
    const countField = (field: string, size: number): number | null => {
      if (field === '*') return size
      if (/^\d+$/.test(field) && Number(field) < size) return 1
      if (/^\*\/\d+$/.test(field)) {
        const step = Number(field.slice(2))
        if (step >= 1 && step <= size) return Math.ceil(size / step)
      }
      return null
    }
    const minutes = countField(fields[0], 60) ?? 1
    const hours = countField(fields[1], 24) ?? 1
    return minutes * hours
  }
  return 0
}

export function getScheduleUpcomingCount(
  schedule?: AutomationSchedule,
  nextDueAt?: number,
  now = Date.now(),
  timezone?: string,
  forecast?: number[],
): number {
  const tz = schedule?.timezone || timezone || Intl.DateTimeFormat().resolvedOptions().timeZone
  const todayKey = getOccurrenceDayKey(now, tz)

  const fromForecast = forecast && forecast.length > 0
    ? forecast.filter(ms => ms > now && getOccurrenceDayKey(ms, tz) === todayKey).length
    : 0

  if (schedule?.kind === 'interval' && typeof schedule.interval_seconds === 'number' && schedule.interval_seconds >= 60) {
    const intervalMs = schedule.interval_seconds * 1000
    if (nextDueAt && nextDueAt > now && getOccurrenceDayKey(nextDueAt, tz) === todayKey) {
      let count = 0
      let t = nextDueAt
      while (t > now && getOccurrenceDayKey(t, tz) === todayKey && count < 2000) {
        count++
        t += intervalMs
      }
      return Math.max(count, fromForecast)
    }
    const dailyTotal = Math.floor(86400 / schedule.interval_seconds)
    if (fromForecast > 0) return Math.max(fromForecast, 1)
    if (nextDueAt && nextDueAt > now && getOccurrenceDayKey(nextDueAt, tz) !== todayKey) {
      return 0
    }
    return dailyTotal
  }

  if (fromForecast > 0) return fromForecast

  if (nextDueAt && nextDueAt > now && getOccurrenceDayKey(nextDueAt, tz) === todayKey) {
    return 1
  }

  return 0
}


