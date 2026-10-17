// Online hours for Bots HQ: when the on-call staff count as "available for
// chats" on each bot, without keeping a console tab open. One schedule for all
// bots, and any bot may use its own instead. Modes:
//   off     nobody is offered from the schedule (the console toggle still works)
//   hours   on call during the weekly hours, in the schedule's time zone
//   always  on call around the clock (for upgraded support later)
// Pure functions: the Worker stores the schedule in KV and applies it every minute.

export const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
const TIME = /^([01]\d|2[0-3]):[0-5]\d$|^24:00$/;
const MODES = ['off', 'hours', 'always'];

export const DEFAULT_SCHEDULE = { mode: 'off', tz: 'UTC', week: { mon: [], tue: [], wed: [], thu: [], fri: [], sat: [], sun: [] } };

/** Checks and normalizes one schedule; throws a plain message on bad input. */
export function cleanSchedule(input) {
  if (!input || typeof input !== 'object') throw new Error('schedule must be an object');
  const mode = MODES.includes(input.mode) ? input.mode : null;
  if (!mode) throw new Error('mode must be off, hours or always');
  const tz = typeof input.tz === 'string' && input.tz.length <= 64 ? input.tz : '';
  try { new Intl.DateTimeFormat('en-GB', { timeZone: tz }); } catch { throw new Error('unknown time zone'); }
  const week = {};
  for (const day of DAYS) {
    const ranges = Array.isArray(input.week?.[day]) ? input.week[day] : [];
    if (ranges.length > 4) throw new Error(`at most 4 ranges on ${day}`);
    week[day] = ranges.map((r) => {
      if (!Array.isArray(r) || r.length !== 2 || !TIME.test(r[0]) || !TIME.test(r[1]) || r[0] >= r[1]) throw new Error(`bad range on ${day}: use ["09:00","17:00"]`);
      return [r[0], r[1]];
    });
  }
  return { mode, tz, week };
}

/** The whole hours config: one shared schedule and per-bot choices. */
export function cleanHours(input, botIds) {
  const all = cleanSchedule(input?.all ?? DEFAULT_SCHEDULE);
  const bots = {};
  for (const id of botIds) {
    const b = input?.bots?.[id];
    bots[id] = b && b.own === true ? { own: true, schedule: cleanSchedule(b.schedule) } : { own: false };
  }
  return { all, bots };
}

/** The schedule that applies to one bot. */
export function scheduleFor(hours, botId) {
  const b = hours?.bots?.[botId];
  return b?.own ? b.schedule : (hours?.all ?? DEFAULT_SCHEDULE);
}

/** Weekday and HH:MM of an instant in a time zone. */
export function localTime(at, tz) {
  const parts = Object.fromEntries(new Intl.DateTimeFormat('en-GB', { timeZone: tz, weekday: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
    .formatToParts(new Date(at)).map((p) => [p.type, p.value]));
  return { day: String(parts.weekday).slice(0, 3).toLowerCase(), time: `${parts.hour}:${parts.minute}` };
}

/** Whether the on-call staff count as available for a schedule at an instant. */
export function onCall(schedule, at = Date.now()) {
  if (!schedule || schedule.mode === 'off') return false;
  if (schedule.mode === 'always') return true;
  const { day, time } = localTime(at, schedule.tz || 'UTC');
  return (schedule.week?.[day] ?? []).some(([from, to]) => time >= from && time < to);
}
