package automation

import (
	"strconv"
	"strings"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Cron deliberately accepts five numeric fields, '*' and '*/n' only. Reject
// unsupported syntax rather than guessing. DST gaps skip nonexistent wall times;
// folds fire once at the first instant. Misfires never backfill an unbounded queue.
func NormalizeSchedule(s store.AutomationSchedulePolicy) (store.AutomationSchedulePolicy, error) {
	if s.MissedPolicy == "" { s.MissedPolicy = "skip" }
	if s.OverlapPolicy == "" { s.OverlapPolicy = "independent" }
	if s.MissedPolicy != "skip" && s.MissedPolicy != "coalesce" { return s, ErrInvalid }
	if s.OverlapPolicy != "independent" && s.OverlapPolicy != "serialize" { return s, ErrInvalid }
	switch s.Kind {
	case "manual":
		if s.Expression != "" || s.IntervalSeconds != 0 || s.TriggerSource != "" || s.Timezone != "" { return s, ErrInvalid }
	case "event":
		if strings.TrimSpace(s.TriggerSource) == "" || len(s.TriggerSource) > 256 || s.Expression != "" || s.IntervalSeconds != 0 || s.Timezone != "" { return s, ErrInvalid }
	case "interval":
		if s.IntervalSeconds < 60 || s.IntervalSeconds > 366*86400 || s.Expression != "" || s.TriggerSource != "" || s.Timezone != "" { return s, ErrInvalid }
	case "cron":
		if s.IntervalSeconds != 0 || s.TriggerSource != "" || s.Timezone == "" || s.Timezone == "Local" { return s, ErrInvalid }
		if _, err := time.LoadLocation(s.Timezone); err != nil { return s, ErrInvalid }
		fields := strings.Fields(s.Expression)
		if len(fields) != 5 { return s, ErrInvalid }
		mins, maxs := []int{0,0,1,1,0}, []int{59,23,31,12,6}
		for i, f := range fields { if !validField(f, mins[i], maxs[i]) { return s, ErrInvalid } }
		// Avoid divergent cron DOM/DOW OR-vs-AND conventions.
		if fields[2] != "*" && fields[4] != "*" { return s, ErrInvalid }
		s.Expression = strings.Join(fields, " ")
	default: return s, ErrInvalid
	}
	return s, nil
}
func validField(f string, min, max int) bool {
	if f == "*" { return true }
	if strings.HasPrefix(f, "*/") { n, err := strconv.Atoi(f[2:]); return err == nil && n > 0 && n <= max-min+1 }
	n, err := strconv.Atoi(f)
	return err == nil && n >= min && n <= max && strconv.Itoa(n) == f
}
func matches(f string, value, min int) bool {
	if f == "*" { return true }
	if strings.HasPrefix(f, "*/") { n, _ := strconv.Atoi(f[2:]); return (value-min)%n == 0 }
	n, _ := strconv.Atoi(f)
	return n == value
}
// Due evaluates one exact UTC minute, not a polling loop. The caller owns the
// durable trigger identity and misfire coalescing cursor. anchor fixes intervals.
func Due(s store.AutomationSchedulePolicy, at, anchor time.Time) (bool, error) {
	s, err := NormalizeSchedule(s)
	if err != nil { return false, err }
	if s.Kind == "interval" {
		delta := at.Unix()-anchor.Unix()
		return delta >= 0 && delta%s.IntervalSeconds == 0, nil
	}
	if s.Kind != "cron" { return false, nil }
	loc, _ := time.LoadLocation(s.Timezone)
	local := at.In(loc)
	if local.Second() != 0 || local.Nanosecond() != 0 { return false, nil }
	f := strings.Fields(s.Expression)
	values, mins := []int{local.Minute(), local.Hour(), local.Day(), int(local.Month()), int(local.Weekday())}, []int{0,0,1,1,0}
	for i := range f { if !matches(f[i], values[i], mins[i]) { return false, nil } }
	// Detect repeated civil minutes after backward transitions, including
	// half-hour folds. Bounded at 24h for historical date-line transitions.
	for minutes := 1; minutes <= 24*60; minutes++ {
		prior := at.Add(-time.Duration(minutes)*time.Minute).In(loc)
		if prior.Year() == local.Year() && prior.YearDay() == local.YearDay() && prior.Hour() == local.Hour() && prior.Minute() == local.Minute() { return false, nil }
	}
	return true, nil
}

// AdmitTick never queues all missed ticks and never interprets a blocked
// occurrence as permission to run through a serialize target safety gate.
func AdmitTick(s store.AutomationSchedulePolicy, missed, active bool) (bool, error) {
	s, err := NormalizeSchedule(s)
	if err != nil { return false, err }
	return !(active && s.OverlapPolicy == "serialize") && !(missed && s.MissedPolicy == "skip"), nil
}
