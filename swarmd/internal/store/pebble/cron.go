package pebblestore

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronExpression is a validated five-field cron schedule. Timezone conversion
// and DST occurrence policy belong to the caller.
type CronExpression struct {
	fields [5]uint64
}

// ParseCron accepts numbers, ranges, lists, wildcard/range steps, and month and
// weekday names. Both day fields may not be restricted (anything except "*").
func ParseCron(expression string) (CronExpression, error) {
	var result CronExpression
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return result, fmt.Errorf("five cron fields required")
	}
	if fields[2] != "*" && fields[4] != "*" {
		return result, fmt.Errorf("both cron day fields cannot be restricted")
	}
	bounds := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, field := range fields {
		bits, err := parseCronField(field, bounds[i][0], bounds[i][1], i)
		if err != nil {
			return CronExpression{}, fmt.Errorf("cron field %d: %w", i+1, err)
		}
		result.fields[i] = bits
	}
	return result, nil
}

func cronNumber(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty value")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid number %q", s)
		}
	}
	return strconv.Atoi(s)
}

func parseCronField(field string, min, max, index int) (uint64, error) {
	value := func(s string) (int, error) {
		names := ""
		base := 0
		if index == 3 {
			names, base = "JAN FEB MAR APR MAY JUN JUL AUG SEP OCT NOV DEC", 1
		} else if index == 4 {
			names = "SUN MON TUE WED THU FRI SAT"
		}
		for n, name := range strings.Fields(names) {
			if strings.EqualFold(s, name) {
				return n + base, nil
			}
		}
		n, err := cronNumber(s)
		if err != nil || n < min || n > max {
			return 0, fmt.Errorf("invalid or out-of-bounds value %q", s)
		}
		return n, nil
	}
	var bits uint64
	items := strings.Split(field, ",")
	for _, item := range items {
		parts := strings.Split(item, "/")
		if len(parts) > 2 {
			return 0, fmt.Errorf("invalid step")
		}
		base, step := parts[0], 1
		if len(parts) == 2 {
			var err error
			step, err = cronNumber(parts[1])
			width := max - min + 1
			if index == 4 {
				width = 7 // Sunday alias does not extend the week.
			}
			if err != nil || step < 1 || step > width {
				return 0, fmt.Errorf("step outside bounds")
			}
		}
		lo, hi := min, max
		if base == "*" {
			if len(items) > 1 {
				return 0, fmt.Errorf("lists require numbers or ranges")
			}
			// Sunday's alias must not add an extra day to wildcard steps.
			if index == 4 {
				hi = 6
			}
		} else {
			rangeParts := strings.Split(base, "-")
			if len(rangeParts) > 2 || (len(parts) == 2 && len(rangeParts) != 2) {
				return 0, fmt.Errorf("step requires wildcard or range")
			}
			var err error
			lo, err = value(rangeParts[0])
			if err != nil {
				return 0, err
			}
			hi = lo
			if len(rangeParts) == 2 {
				hi, err = value(rangeParts[1])
				if err != nil {
					return 0, err
				}
			}
			if hi < lo {
				return 0, fmt.Errorf("reversed range")
			}
		}
		for n := lo; n <= hi; n += step {
			v := n
			if index == 4 && v == 7 {
				v = 0
			}
			bits |= uint64(1) << v
		}
	}
	return bits, nil
}

// Matches reports whether a local civil minute matches this expression.
func (c CronExpression) Matches(local time.Time) bool {
	values := [5]int{local.Minute(), local.Hour(), local.Day(), int(local.Month()), int(local.Weekday())}
	for i, value := range values {
		if c.fields[i]&(uint64(1)<<value) == 0 {
			return false
		}
	}
	return true
}
