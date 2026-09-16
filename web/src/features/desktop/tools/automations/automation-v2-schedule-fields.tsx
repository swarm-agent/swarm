import { useState } from 'react'
import { intervalLabel, intervalPresets, scheduleFrequency, scheduleMode, scheduleTime, weekdays, type AutomationSchedule } from './automation-v2-schedule'

const field = 'mt-1.5 h-10 w-full min-w-0 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 text-sm text-[var(--app-text)] focus-visible:outline-[var(--app-primary)]'

export function AutomationV2ScheduleFields({ schedule, onChange }: { schedule: AutomationSchedule; onChange: (value: AutomationSchedule) => void }) {
  const [custom, setCustom] = useState(false)
  const [unit, setUnit] = useState(60)
  const localZone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  const zones = [...new Set([schedule.timezone, localZone, 'UTC', ...((Intl as typeof Intl & { supportedValuesOf?: (key: string) => string[] }).supportedValuesOf?.('timeZone') ?? [])].filter((v): v is string => !!v))]
  const mode = scheduleMode(schedule)
  const seconds = schedule.interval_seconds ?? 3600
  const customInterval = custom || !intervalPresets.includes(seconds)
  const cronFields = (schedule.cron ?? '').trim().split(/\s+/)
  function changeMode(value: string) {
    if (value === 'interval') { setCustom(false); onChange({ kind: 'interval', interval_seconds: 3600 }); return }
    const time = scheduleTime(schedule) || '09:00'
    const [hour, minute] = time.split(':').map(Number)
    onChange({ kind: 'cron', timezone: schedule.timezone || localZone, cron: value === 'advanced' ? '0 */2 * * *' : `${minute} ${hour} * * ${value === 'weekly' ? '1' : '*'}` })
  }
  return <div className="min-w-0 rounded-xl border border-[var(--app-border)]/60 bg-[var(--app-bg-alt)] p-3">
    <div className="grid min-w-0 gap-3 sm:grid-cols-2">
      <label className="min-w-0 text-xs font-medium">Repeat<select aria-label="Repeat" className={field} value={mode} onChange={e => changeMode(e.target.value)}>
        <option value="interval">At a regular interval</option><option value="daily">Every day</option><option value="weekly">Every week</option><option value="advanced">Advanced schedule</option>
      </select></label>
      {mode === 'interval' ? <>
        <label className="min-w-0 text-xs font-medium">Frequency<select aria-label="Frequency" className={field} value={customInterval ? 'custom' : seconds} onChange={e => { setCustom(e.target.value === 'custom'); if (e.target.value !== 'custom') onChange({ kind: 'interval', interval_seconds: Number(e.target.value) }) }}>
          {intervalPresets.map(value => <option key={value} value={value}>{intervalLabel(value)}</option>)}<option value="custom">Custom interval…</option>
        </select></label>
        {customInterval && <div className="grid grid-cols-2 gap-2 sm:col-span-2">
          <label className="text-xs">Every<input aria-label="Interval amount" className={field} type="number" min={60 / unit} max={31622400 / unit} step="any" value={Number.isFinite(seconds) ? seconds / unit : ''} onChange={e => onChange({ kind: 'interval', interval_seconds: Number(e.target.value) * unit })} /></label>
          <label className="text-xs">Unit<select aria-label="Interval unit" className={field} value={unit} onChange={e => { const nextUnit = Number(e.target.value); setUnit(nextUnit); onChange({ kind: 'interval', interval_seconds: seconds / unit * nextUnit }) }}><option value={60}>Minutes</option><option value={3600}>Hours</option><option value={86400}>Days</option></select></label>
        </div>}
      </> : <>
        {mode !== 'advanced' && <label className="text-xs font-medium">Time<input aria-label="Time" className={field} type="time" value={scheduleTime(schedule)} onChange={e => { if (!e.target.value) return; const [hour, minute] = e.target.value.split(':').map(Number); onChange({ ...schedule, cron: `${minute} ${hour} * * ${mode === 'weekly' ? cronFields[4] : '*'}` }) }} /></label>}
        {mode === 'weekly' && <label className="text-xs font-medium">Day<select aria-label="Day" className={field} value={cronFields[4]} onChange={e => onChange({ ...schedule, cron: `${cronFields[0]} ${cronFields[1]} * * ${e.target.value}` })}>{weekdays.map((day, index) => <option key={day} value={index}>{day}</option>)}</select></label>}
        {mode === 'advanced' && <label className="text-xs font-medium">Cron expression<input aria-label="Cron expression" className={field} maxLength={128} value={schedule.cron ?? ''} onChange={e => onChange({ ...schedule, cron: e.target.value })} /></label>}
        <label className="min-w-0 text-xs font-medium">Timezone<select aria-label="Timezone" className={field} value={schedule.timezone ?? ''} onChange={e => onChange({ ...schedule, timezone: e.target.value })}><option value="" disabled>Choose a timezone</option>{zones.map(zone => <option key={zone} value={zone}>{zone.replace(/_/g, ' ')}</option>)}</select></label>
      </>}
    </div>
    <p className="mt-3 text-xs font-medium text-[var(--app-primary)]" aria-live="polite">{scheduleFrequency(schedule)}</p>
    <p className="mt-1 text-[11px] leading-4 text-[var(--app-text-muted)]">{mode === 'interval' ? 'Starts one interval after acceptance. Daily totals are an average, not guaranteed runs.' : mode === 'advanced' ? 'Minute · hour · day · month · weekday. Use numbers, * or */n. No ranges or lists; restrict only one day field.' : 'Follows the selected timezone. The first run is at the next scheduled time.'}</p>
  </div>
}
