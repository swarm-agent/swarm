package automation

import (
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
	"time"
)

// Purpose: NormalizeSchedule and Due must share ParseCron's accepted syntax and
// preserve the legacy scheduler's first-fold-only policy. These direct calls
// prove matching and rejection without clocks, timers, or durable fixtures.
func TestCronSharedParserDue(t *testing.T) {
	for _, tc := range []struct {
		expression, zone, at string
		want                 bool
	}{
		{"*/15 8-18 * * mon,wed,fri", "Europe/Helsinki", "2026-03-30T09:15:00+03:00", true},
		{"*/15 8-18 * * mon,wed,fri", "Europe/Helsinki", "2026-03-31T09:15:00+03:00", false},
		{"0 9 * * 7", "UTC", "2026-03-29T09:00:00Z", true},
		{"30 3 * * SUN", "Europe/Helsinki", "2026-10-25T03:30:00+03:00", true},
		{"30 3 * * SUN", "Europe/Helsinki", "2026-10-25T03:30:00+02:00", false},
	} {
		s := store.AutomationSchedulePolicy{Kind: "cron", Expression: tc.expression, Timezone: tc.zone}
		at, err := time.Parse(time.RFC3339, tc.at)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Due(s, at, time.Time{})
		if err != nil || got != tc.want {
			t.Fatalf("%+v got %v err %v", tc, got, err)
		}
	}
	for _, expr := range []string{"0 9 * * MON-NOPE", "0 9 1 * MON", "0 9 * * 1,,3"} {
		s := store.AutomationSchedulePolicy{Kind: "cron", Expression: expr, Timezone: "UTC"}
		if _, err := NormalizeSchedule(s); err == nil {
			t.Fatalf("accepted %q", expr)
		}
		if got, err := Due(s, time.Date(2026, 3, 30, 9, 0, 0, 0, time.UTC), time.Time{}); err == nil || got {
			t.Fatalf("invalid schedule fired: %v %v", got, err)
		}
	}
}
