package flow

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	CadenceDaily    = "daily"
	CadenceWeekly   = "weekly"
	CadenceMonthly  = "monthly"
	CadenceOnDemand = "on_demand"

	CatchUpSkip = "skip"
	CatchUpOnce = "once"
	CatchUpAll  = "all"
)

func NormalizeCadence(cadence string) string {
	value := strings.ToLower(strings.TrimSpace(cadence))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	switch value {
	case "day", "daily":
		return CadenceDaily
	case "week", "weekly":
		return CadenceWeekly
	case "month", "monthly":
		return CadenceMonthly
	case "on_demand", "ondemand", "manual":
		return CadenceOnDemand
	default:
		return value
	}
}

func NormalizeCatchUpPolicy(policy CatchUpPolicy) CatchUpPolicy {
	policy.Mode = strings.ToLower(strings.TrimSpace(policy.Mode))
	policy.Mode = strings.ReplaceAll(policy.Mode, "-", "_")
	policy.Mode = strings.ReplaceAll(policy.Mode, " ", "_")
	switch policy.Mode {
	case "", "default", "latest", "latest_only":
		policy.Mode = CatchUpOnce
	case "skip", "skip_missed", "none":
		policy.Mode = CatchUpSkip
	case "once", "one", "run_once":
		policy.Mode = CatchUpOnce
	case "all", "run_all":
		policy.Mode = CatchUpAll
	}
	if policy.MaxCatchUp < 0 {
		policy.MaxCatchUp = 0
	}
	return policy
}

func ValidateSchedule(spec ScheduleSpec) error {
	cadence := NormalizeCadence(spec.Cadence)
	switch cadence {
	case CadenceDaily, CadenceWeekly, CadenceMonthly:
	case CadenceOnDemand:
		return nil
	case "":
		return errors.New("schedule cadence is required")
	default:
		return fmt.Errorf("unsupported schedule cadence %q", spec.Cadence)
	}
	if strings.TrimSpace(spec.Timezone) == "" {
		return errors.New("schedule timezone is required")
	}
	if _, err := time.LoadLocation(strings.TrimSpace(spec.Timezone)); err != nil {
		return fmt.Errorf("load schedule timezone: %w", err)
	}
	if _, _, err := parseScheduleClock(spec.Time); err != nil {
		return err
	}
	switch cadence {
	case CadenceWeekly:
		if _, err := parseWeekday(spec.Weekday); err != nil {
			return err
		}
	case CadenceMonthly:
		if spec.MonthDay < 1 || spec.MonthDay > 31 {
			return errors.New("schedule month_day must be between 1 and 31")
		}
	}
	return nil
}

func ValidateAssignment(assignment Assignment) error {
	assignment.FlowID = strings.TrimSpace(assignment.FlowID)
	if assignment.FlowID == "" {
		return errors.New("flow_id is required")
	}
	if assignment.Revision <= 0 {
		return errors.New("revision is required")
	}
	if strings.TrimSpace(assignment.Agent.TargetKind) == "" || strings.TrimSpace(assignment.Agent.TargetName) == "" {
		return errors.New("agent target_kind and target_name are required")
	}
	if strings.TrimSpace(assignment.Intent.Prompt) == "" && len(assignment.Intent.Tasks) == 0 {
		return errors.New("flow intent prompt or tasks are required")
	}
	return ValidateSchedule(assignment.Schedule)
}

func NextFire(assignment Assignment, now time.Time) (time.Time, bool, error) {
	return nextFireAfter(assignment, now.UTC())
}

func NextFireAfter(assignment Assignment, after time.Time) (time.Time, bool, error) {
	return nextFireAfter(assignment, after.UTC())
}

func CatchUpFireTimes(assignment Assignment, lastScheduledAt time.Time, now time.Time) ([]time.Time, error) {
	policy := NormalizeCatchUpPolicy(assignment.CatchUpPolicy)
	if policy.Mode == CatchUpSkip {
		return nil, nil
	}
	limit := policy.MaxCatchUp
	if limit <= 0 {
		limit = 100
	}
	fires := make([]time.Time, 0, limit)
	cursor := lastScheduledAt.UTC()
	now = now.UTC()
	for scanned := 0; scanned < limit; scanned++ {
		next, ok, err := NextFireAfter(assignment, cursor)
		if err != nil || !ok {
			return fires, err
		}
		if next.After(now) {
			break
		}
		if policy.Mode == CatchUpOnce {
			fires = fires[:0]
		}
		fires = append(fires, next)
		cursor = next
	}
	return fires, nil
}

func nextFireAfter(assignment Assignment, after time.Time) (time.Time, bool, error) {
	if !assignment.Enabled {
		return time.Time{}, false, nil
	}
	if err := ValidateSchedule(assignment.Schedule); err != nil {
		return time.Time{}, false, err
	}
	cadence := NormalizeCadence(assignment.Schedule.Cadence)
	if cadence == CadenceOnDemand {
		return time.Time{}, false, nil
	}
	loc, err := time.LoadLocation(strings.TrimSpace(assignment.Schedule.Timezone))
	if err != nil {
		return time.Time{}, false, fmt.Errorf("load schedule timezone: %w", err)
	}
	hour, minute, err := parseScheduleClock(assignment.Schedule.Time)
	if err != nil {
		return time.Time{}, false, err
	}
	localAfter := after.In(loc)
	switch cadence {
	case CadenceDaily:
		return nextDailyFire(localAfter, after, loc, hour, minute), true, nil
	case CadenceWeekly:
		weekday, err := parseWeekday(assignment.Schedule.Weekday)
		if err != nil {
			return time.Time{}, false, err
		}
		return nextWeeklyFire(localAfter, after, loc, weekday, hour, minute), true, nil
	case CadenceMonthly:
		return nextMonthlyFire(localAfter, after, loc, assignment.Schedule.MonthDay, hour, minute), true, nil
	default:
		return time.Time{}, false, fmt.Errorf("unsupported schedule cadence %q", assignment.Schedule.Cadence)
	}
}

func nextDailyFire(localAfter time.Time, after time.Time, loc *time.Location, hour, minute int) time.Time {
	year, month, day := localAfter.Date()
	candidate := localWallTimeAtOrAfter(year, month, day, hour, minute, loc)
	if !candidate.After(after) {
		localNext := localAfter.AddDate(0, 0, 1)
		year, month, day = localNext.Date()
		candidate = localWallTimeAtOrAfter(year, month, day, hour, minute, loc)
	}
	return candidate.UTC()
}

func nextWeeklyFire(localAfter time.Time, after time.Time, loc *time.Location, weekday time.Weekday, hour, minute int) time.Time {
	daysUntil := (int(weekday) - int(localAfter.Weekday()) + 7) % 7
	localCandidate := localAfter.AddDate(0, 0, daysUntil)
	year, month, day := localCandidate.Date()
	candidate := localWallTimeAtOrAfter(year, month, day, hour, minute, loc)
	if !candidate.After(after) {
		localCandidate = localCandidate.AddDate(0, 0, 7)
		year, month, day = localCandidate.Date()
		candidate = localWallTimeAtOrAfter(year, month, day, hour, minute, loc)
	}
	return candidate.UTC()
}

func nextMonthlyFire(localAfter time.Time, after time.Time, loc *time.Location, monthDay, hour, minute int) time.Time {
	year, month, _ := localAfter.Date()
	candidate := monthlyWallTime(year, month, monthDay, hour, minute, loc)
	if !candidate.After(after) {
		nextMonth := time.Date(year, month, 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)
		year, month, _ = nextMonth.Date()
		candidate = monthlyWallTime(year, month, monthDay, hour, minute, loc)
	}
	return candidate.UTC()
}

func monthlyWallTime(year int, month time.Month, monthDay, hour, minute int, loc *time.Location) time.Time {
	day := monthDay
	if maxDay := daysInMonth(year, month); day > maxDay {
		day = maxDay
	}
	return localWallTimeAtOrAfter(year, month, day, hour, minute, loc)
}

func localWallTimeAtOrAfter(year int, month time.Month, day int, hour int, minute int, loc *time.Location) time.Time {
	candidate := time.Date(year, month, day, hour, minute, 0, 0, loc)
	cy, cm, cd := candidate.In(loc).Date()
	cl := candidate.In(loc)
	if cy == year && cm == month && cd == day && cl.Hour() == hour && cl.Minute() == minute {
		return candidate.UTC()
	}
	midnight := time.Date(year, month, day, 0, 0, 0, 0, loc)
	for offset := 0; offset < 27*60; offset++ {
		probe := midnight.Add(time.Duration(offset) * time.Minute)
		local := probe.In(loc)
		py, pm, pd := local.Date()
		if py != year || pm != month || pd != day {
			continue
		}
		if local.Hour() > hour || (local.Hour() == hour && local.Minute() >= minute) {
			return probe.UTC()
		}
	}
	return candidate.UTC()
}

func parseScheduleClock(value string) (int, int, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, errors.New("schedule time must be HH:MM")
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, errors.New("schedule hour must be between 00 and 23")
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, errors.New("schedule minute must be between 00 and 59")
	}
	return hour, minute, nil
}

func parseWeekday(value string) (time.Weekday, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "sun", "sunday":
		return time.Sunday, nil
	case "mon", "monday":
		return time.Monday, nil
	case "tue", "tues", "tuesday":
		return time.Tuesday, nil
	case "wed", "wednesday":
		return time.Wednesday, nil
	case "thu", "thur", "thurs", "thursday":
		return time.Thursday, nil
	case "fri", "friday":
		return time.Friday, nil
	case "sat", "saturday":
		return time.Saturday, nil
	default:
		return time.Sunday, errors.New("schedule weekday is required")
	}
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
