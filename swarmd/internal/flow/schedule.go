package flow

import (
	"errors"
	"fmt"
	"sort"
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

	maxScheduleTimes = 48
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
	spec = NormalizeScheduleSpec(spec)
	cadence := spec.Cadence
	switch cadence {
	case CadenceDaily, CadenceWeekly, CadenceMonthly, CadenceOnDemand:
	case "":
		return errors.New("schedule cadence is required")
	default:
		return fmt.Errorf("unsupported schedule cadence %q", spec.Cadence)
	}
	if strings.TrimSpace(spec.Cron) != "" {
		if strings.TrimSpace(spec.Timezone) == "" {
			return errors.New("schedule timezone is required")
		}
		if _, err := time.LoadLocation(strings.TrimSpace(spec.Timezone)); err != nil {
			return fmt.Errorf("load schedule timezone: %w", err)
		}
		if _, err := parseCronExpression(spec.Cron); err != nil {
			return err
		}
		return nil
	}
	if cadence == CadenceOnDemand {
		return nil
	}
	if strings.TrimSpace(spec.Timezone) == "" {
		return errors.New("schedule timezone is required")
	}
	if _, err := time.LoadLocation(strings.TrimSpace(spec.Timezone)); err != nil {
		return fmt.Errorf("load schedule timezone: %w", err)
	}
	if _, err := normalizedScheduleTimes(spec); err != nil {
		return err
	}
	switch cadence {
	case CadenceWeekly:
		if _, err := parseWeekdays(spec.Weekday); err != nil {
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
	assignment.Agent = NormalizeAgentSelection(assignment.Agent)
	if strings.TrimSpace(assignment.Agent.ProfileName) == "" {
		return errors.New("agent profile_name is required")
	}
	if strings.TrimSpace(assignment.Agent.ProfileMode) == "" {
		return errors.New("agent profile_mode is required")
	}
	if assignment.Agent.TargetKind == "" || assignment.Agent.TargetName == "" {
		return errors.New("agent profile_mode must resolve to a runtime target")
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
	loc, err := time.LoadLocation(strings.TrimSpace(assignment.Schedule.Timezone))
	if err != nil {
		return time.Time{}, false, fmt.Errorf("load schedule timezone: %w", err)
	}
	if strings.TrimSpace(assignment.Schedule.Cron) != "" {
		next, ok, err := nextCronFire(after, loc, assignment.Schedule.Cron)
		return next, ok, err
	}
	if cadence == CadenceOnDemand {
		return time.Time{}, false, nil
	}
	times, err := normalizedScheduleTimes(assignment.Schedule)
	if err != nil {
		return time.Time{}, false, err
	}
	localAfter := after.In(loc)
	switch cadence {
	case CadenceDaily:
		return nextDailyFire(localAfter, after, loc, times), true, nil
	case CadenceWeekly:
		weekdays, err := parseWeekdays(assignment.Schedule.Weekday)
		if err != nil {
			return time.Time{}, false, err
		}
		return nextWeeklyFire(localAfter, after, loc, weekdays, times), true, nil
	case CadenceMonthly:
		return nextMonthlyFire(localAfter, after, loc, assignment.Schedule.MonthDay, times), true, nil
	default:
		return time.Time{}, false, fmt.Errorf("unsupported schedule cadence %q", assignment.Schedule.Cadence)
	}
}

func nextDailyFire(localAfter time.Time, after time.Time, loc *time.Location, times []scheduleClock) time.Time {
	year, month, day := localAfter.Date()
	candidate := nextDayCandidate(year, month, day, after, loc, times)
	if !candidate.IsZero() {
		return candidate
	}
	localNext := localAfter.AddDate(0, 0, 1)
	year, month, day = localNext.Date()
	return firstScheduleTimeForDate(year, month, day, loc, times)
}

func nextWeeklyFire(localAfter time.Time, after time.Time, loc *time.Location, weekdays []time.Weekday, times []scheduleClock) time.Time {
	for offset := 0; offset <= 7; offset++ {
		localCandidate := localAfter.AddDate(0, 0, offset)
		if !weekdaySelected(localCandidate.Weekday(), weekdays) {
			continue
		}
		year, month, day := localCandidate.Date()
		candidate := nextDayCandidate(year, month, day, after, loc, times)
		if !candidate.IsZero() {
			return candidate
		}
	}
	return time.Time{}
}

func nextMonthlyFire(localAfter time.Time, after time.Time, loc *time.Location, monthDay int, times []scheduleClock) time.Time {
	year, month, _ := localAfter.Date()
	candidate := nextMonthlyCandidate(year, month, monthDay, after, loc, times)
	if !candidate.IsZero() {
		return candidate
	}
	nextMonth := time.Date(year, month, 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)
	year, month, _ = nextMonth.Date()
	return firstScheduleTimeForMonth(year, month, monthDay, loc, times)
}

func nextDayCandidate(year int, month time.Month, day int, after time.Time, loc *time.Location, times []scheduleClock) time.Time {
	for _, clock := range times {
		candidate := localWallTimeAtOrAfter(year, month, day, clock.Hour, clock.Minute, loc)
		if candidate.After(after) {
			return candidate.UTC()
		}
	}
	return time.Time{}
}

func nextMonthlyCandidate(year int, month time.Month, monthDay int, after time.Time, loc *time.Location, times []scheduleClock) time.Time {
	day := monthDay
	if maxDay := daysInMonth(year, month); day > maxDay {
		day = maxDay
	}
	return nextDayCandidate(year, month, day, after, loc, times)
}

func firstScheduleTimeForDate(year int, month time.Month, day int, loc *time.Location, times []scheduleClock) time.Time {
	first := times[0]
	return localWallTimeAtOrAfter(year, month, day, first.Hour, first.Minute, loc).UTC()
}

func firstScheduleTimeForMonth(year int, month time.Month, monthDay int, loc *time.Location, times []scheduleClock) time.Time {
	day := monthDay
	if maxDay := daysInMonth(year, month); day > maxDay {
		day = maxDay
	}
	return firstScheduleTimeForDate(year, month, day, loc, times)
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

type scheduleClock struct {
	Raw    string
	Hour   int
	Minute int
}

func normalizedScheduleTimes(spec ScheduleSpec) ([]scheduleClock, error) {
	rawTimes := make([]string, 0, len(spec.Times)+1)
	for _, value := range spec.Times {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			rawTimes = append(rawTimes, trimmed)
		}
	}
	if trimmed := strings.TrimSpace(spec.Time); trimmed != "" {
		rawTimes = append(rawTimes, trimmed)
	}
	if len(rawTimes) == 0 {
		return nil, errors.New("schedule time is required")
	}
	if len(rawTimes) > maxScheduleTimes {
		return nil, fmt.Errorf("schedule times must contain at most %d entries", maxScheduleTimes)
	}
	seen := make(map[string]struct{}, len(rawTimes))
	clocks := make([]scheduleClock, 0, len(rawTimes))
	for _, raw := range rawTimes {
		hour, minute, err := parseScheduleClock(raw)
		if err != nil {
			return nil, err
		}
		normalized := fmt.Sprintf("%02d:%02d", hour, minute)
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		clocks = append(clocks, scheduleClock{Raw: normalized, Hour: hour, Minute: minute})
	}
	sort.Slice(clocks, func(i, j int) bool {
		if clocks[i].Hour != clocks[j].Hour {
			return clocks[i].Hour < clocks[j].Hour
		}
		return clocks[i].Minute < clocks[j].Minute
	})
	return clocks, nil
}

func NormalizeScheduleSpec(spec ScheduleSpec) ScheduleSpec {
	spec.Cadence = NormalizeCadence(spec.Cadence)
	spec.Time = strings.TrimSpace(spec.Time)
	for index := range spec.Times {
		spec.Times[index] = strings.TrimSpace(spec.Times[index])
	}
	spec.Weekday = strings.TrimSpace(spec.Weekday)
	spec.Timezone = strings.TrimSpace(spec.Timezone)
	spec.Cron = strings.TrimSpace(spec.Cron)
	if spec.Cadence == CadenceOnDemand || spec.Cron != "" {
		return spec
	}
	clocks, err := normalizedScheduleTimes(spec)
	if err != nil {
		return spec
	}
	spec.Times = make([]string, 0, len(clocks))
	for _, clock := range clocks {
		spec.Times = append(spec.Times, clock.Raw)
	}
	if len(spec.Times) > 0 {
		spec.Time = spec.Times[0]
	}
	return spec
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

type cronExpression struct {
	Minutes     map[int]struct{}
	Hours       map[int]struct{}
	MonthDays   map[int]struct{}
	Months      map[int]struct{}
	Weekdays    map[int]struct{}
	AnyMonthDay bool
	AnyWeekday  bool
}

func nextCronFire(after time.Time, loc *time.Location, expression string) (time.Time, bool, error) {
	cron, err := parseCronExpression(expression)
	if err != nil {
		return time.Time{}, false, err
	}
	local := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	limit := local.AddDate(5, 0, 0)
	for cursor := local; !cursor.After(limit); cursor = cursor.Add(time.Minute) {
		if cronMatches(cron, cursor.In(loc)) {
			return cursor.UTC(), true, nil
		}
	}
	return time.Time{}, false, errors.New("schedule cron has no next fire within 5 years")
}

func cronMatches(cron cronExpression, local time.Time) bool {
	if !setContains(cron.Minutes, local.Minute()) || !setContains(cron.Hours, local.Hour()) || !setContains(cron.Months, int(local.Month())) {
		return false
	}
	monthDayMatches := setContains(cron.MonthDays, local.Day())
	weekdayMatches := setContains(cron.Weekdays, int(local.Weekday()))
	if cron.AnyMonthDay && cron.AnyWeekday {
		return true
	}
	if cron.AnyMonthDay {
		return weekdayMatches
	}
	if cron.AnyWeekday {
		return monthDayMatches
	}
	return monthDayMatches || weekdayMatches
}

func setContains(values map[int]struct{}, value int) bool {
	_, ok := values[value]
	return ok
}

func parseCronExpression(value string) (cronExpression, error) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 5 {
		return cronExpression{}, errors.New("schedule cron must contain 5 fields")
	}
	minutes, _, err := parseCronField(fields[0], 0, 59, nil)
	if err != nil {
		return cronExpression{}, fmt.Errorf("schedule cron minute: %w", err)
	}
	hours, _, err := parseCronField(fields[1], 0, 23, nil)
	if err != nil {
		return cronExpression{}, fmt.Errorf("schedule cron hour: %w", err)
	}
	monthDays, anyMonthDay, err := parseCronField(fields[2], 1, 31, nil)
	if err != nil {
		return cronExpression{}, fmt.Errorf("schedule cron day-of-month: %w", err)
	}
	months, _, err := parseCronField(fields[3], 1, 12, cronMonthAliases())
	if err != nil {
		return cronExpression{}, fmt.Errorf("schedule cron month: %w", err)
	}
	weekdays, anyWeekday, err := parseCronField(fields[4], 0, 7, cronWeekdayAliases())
	if err != nil {
		return cronExpression{}, fmt.Errorf("schedule cron day-of-week: %w", err)
	}
	if _, ok := weekdays[7]; ok {
		delete(weekdays, 7)
		weekdays[0] = struct{}{}
	}
	return cronExpression{Minutes: minutes, Hours: hours, MonthDays: monthDays, Months: months, Weekdays: weekdays, AnyMonthDay: anyMonthDay, AnyWeekday: anyWeekday}, nil
}

func parseCronField(field string, min int, max int, aliases map[string]int) (map[int]struct{}, bool, error) {
	values := make(map[int]struct{})
	any := strings.TrimSpace(field) == "*"
	for _, rawPart := range strings.Split(field, ",") {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			return nil, false, errors.New("empty field part")
		}
		rangePart, step := part, 1
		if strings.Contains(part, "/") {
			pieces := strings.Split(part, "/")
			if len(pieces) != 2 || strings.TrimSpace(pieces[1]) == "" {
				return nil, false, errors.New("invalid step")
			}
			rangePart = strings.TrimSpace(pieces[0])
			parsedStep, err := strconv.Atoi(strings.TrimSpace(pieces[1]))
			if err != nil || parsedStep <= 0 {
				return nil, false, errors.New("step must be a positive integer")
			}
			step = parsedStep
		}
		start, end := min, max
		if rangePart == "*" {
			any = true
		} else if strings.Contains(rangePart, "-") {
			pieces := strings.Split(rangePart, "-")
			if len(pieces) != 2 {
				return nil, false, errors.New("invalid range")
			}
			var err error
			start, err = parseCronValue(pieces[0], aliases)
			if err != nil {
				return nil, false, err
			}
			end, err = parseCronValue(pieces[1], aliases)
			if err != nil {
				return nil, false, err
			}
		} else {
			parsed, err := parseCronValue(rangePart, aliases)
			if err != nil {
				return nil, false, err
			}
			start, end = parsed, parsed
		}
		if start < min || start > max || end < min || end > max || start > end {
			return nil, false, fmt.Errorf("value must be between %d and %d", min, max)
		}
		for value := start; value <= end; value += step {
			values[value] = struct{}{}
		}
	}
	if len(values) == 0 {
		return nil, false, errors.New("field has no values")
	}
	return values, any, nil
}

func parseCronValue(value string, aliases map[string]int) (int, error) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if aliases != nil {
		if parsed, ok := aliases[trimmed]; ok {
			return parsed, nil
		}
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", value)
	}
	return parsed, nil
}

func cronMonthAliases() map[string]int {
	return map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
}

func cronWeekdayAliases() map[string]int {
	return map[string]int{"sun": 0, "mon": 1, "tue": 2, "tues": 2, "wed": 3, "thu": 4, "thur": 4, "thurs": 4, "fri": 5, "sat": 6}
}

func parseWeekdays(value string) ([]time.Weekday, error) {
	parts := strings.Split(value, ",")
	weekdays := make([]time.Weekday, 0, len(parts))
	seen := make(map[time.Weekday]struct{}, len(parts))
	for _, part := range parts {
		weekday, err := parseWeekday(part)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[weekday]; ok {
			continue
		}
		seen[weekday] = struct{}{}
		weekdays = append(weekdays, weekday)
	}
	if len(weekdays) == 0 {
		return nil, errors.New("schedule weekday is required")
	}
	sort.Slice(weekdays, func(i, j int) bool { return weekdays[i] < weekdays[j] })
	return weekdays, nil
}

func weekdaySelected(value time.Weekday, weekdays []time.Weekday) bool {
	for _, weekday := range weekdays {
		if value == weekday {
			return true
		}
	}
	return false
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
