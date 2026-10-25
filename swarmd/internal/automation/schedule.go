package automation

import (
	"strings"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Cron uses the shared store parser. DST gaps skip nonexistent wall times;
// folds fire once at the first instant. Misfires never backfill an unbounded queue.
func NormalizeSchedule(s store.AutomationSchedulePolicy) (store.AutomationSchedulePolicy, error) {
	if s.MissedPolicy == "" {
		s.MissedPolicy = "skip"
	}
	if s.OverlapPolicy == "" {
		s.OverlapPolicy = "independent"
	}
	if s.MissedPolicy != "skip" && s.MissedPolicy != "coalesce" {
		return s, ErrInvalid
	}
	if s.OverlapPolicy != "independent" && s.OverlapPolicy != "serialize" {
		return s, ErrInvalid
	}
	switch s.Kind {
	case "manual":
		if s.Expression != "" || s.IntervalSeconds != 0 || s.TriggerSource != "" || s.Timezone != "" {
			return s, ErrInvalid
		}
	case "event":
		if strings.TrimSpace(s.TriggerSource) == "" || len(s.TriggerSource) > 256 || s.Expression != "" || s.IntervalSeconds != 0 || s.Timezone != "" {
			return s, ErrInvalid
		}
	case "interval":
		if s.IntervalSeconds < 60 || s.IntervalSeconds > 366*86400 || s.Expression != "" || s.TriggerSource != "" || s.Timezone != "" {
			return s, ErrInvalid
		}
	case "cron":
		if s.IntervalSeconds != 0 || s.TriggerSource != "" || s.Timezone == "" || s.Timezone == "Local" {
			return s, ErrInvalid
		}
		if _, err := time.LoadLocation(s.Timezone); err != nil {
			return s, ErrInvalid
		}
		if _, err := store.ParseCron(s.Expression); err != nil {
			return s, ErrInvalid
		}
		s.Expression = strings.Join(strings.Fields(s.Expression), " ")
	default:
		return s, ErrInvalid
	}
	return s, nil
}

// Due evaluates one exact UTC minute, not a polling loop. The caller owns the
// durable trigger identity and misfire coalescing cursor. anchor fixes intervals.
func Due(s store.AutomationSchedulePolicy, at, anchor time.Time) (bool, error) {
	s, err := NormalizeSchedule(s)
	if err != nil {
		return false, err
	}
	if s.Kind == "interval" {
		delta := at.Unix() - anchor.Unix()
		return delta >= 0 && delta%s.IntervalSeconds == 0, nil
	}
	if s.Kind != "cron" {
		return false, nil
	}
	loc, _ := time.LoadLocation(s.Timezone)
	local := at.In(loc)
	if local.Second() != 0 || local.Nanosecond() != 0 {
		return false, nil
	}
	cron, err := store.ParseCron(s.Expression)
	if err != nil {
		return false, err
	}
	if !cron.Matches(local) {
		return false, nil
	}
	// Detect repeated civil minutes after backward transitions, including
	// half-hour folds. Bounded at 24h for historical date-line transitions.
	for minutes := 1; minutes <= 24*60; minutes++ {
		prior := at.Add(-time.Duration(minutes) * time.Minute).In(loc)
		if prior.Year() == local.Year() && prior.YearDay() == local.YearDay() && prior.Hour() == local.Hour() && prior.Minute() == local.Minute() {
			return false, nil
		}
	}
	return true, nil
}

// AdmitTick never queues all missed ticks and never interprets a blocked
// occurrence as permission to run through a serialize target safety gate.
func AdmitTick(s store.AutomationSchedulePolicy, missed, active bool) (bool, error) {
	s, err := NormalizeSchedule(s)
	if err != nil {
		return false, err
	}
	return !(active && s.OverlapPolicy == "serialize") && !(missed && s.MissedPolicy == "skip"), nil
}
