package pebblestore

import (
	"testing"
	"time"
)

// Purpose: ParseCron owns the shared syntax and matching contract. These unit
// cases prevent validation/execution drift, invalid bounds and ambiguous day
// rules without needing persistence or a running scheduler.
func TestParseCron(t *testing.T) {
	for _, tc := range []struct{ expression, match, miss string }{
		{"0 9 * * 1-5", "2026-03-30T09:00:00Z", "2026-03-29T09:00:00Z"},
		{"0 9 * * MON-FRI", "2026-03-30T09:00:00Z", "2026-03-29T09:00:00Z"},
		{"*/15 8-18 * * mon,wed,fri", "2026-03-30T08:15:00Z", "2026-03-30T08:16:00Z"},
		{"0 0 1,15 * *", "2026-03-15T00:00:00Z", "2026-03-16T00:00:00Z"},
		{"30 6 * JAN-MAR 1", "2026-03-30T06:30:00Z", "2026-04-06T06:30:00Z"},
		{"0 9 * * 7", "2026-03-29T09:00:00Z", "2026-03-30T09:00:00Z"},
		{"5-25/10 8,10-12 * jan,mar *", "2026-03-30T10:15:00Z", "2026-03-30T10:20:00Z"},
		{"0 9 * * 5-7", "2026-03-29T09:00:00Z", "2026-03-30T09:00:00Z"},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			cron, err := ParseCron(tc.expression)
			if err != nil {
				t.Fatal(err)
			}
			for _, sample := range []struct {
				text string
				want bool
			}{{tc.match, true}, {tc.miss, false}} {
				at, err := time.Parse(time.RFC3339, sample.text)
				if err != nil {
					t.Fatal(err)
				}
				if got := cron.Matches(at); got != sample.want {
					t.Fatalf("Matches(%s)=%v want %v", sample.text, got, sample.want)
				}
			}
		})
	}
	for _, expression := range []string{"60 9 * * *", "0 24 * * *", "0 9 0 * *", "0 9 * 13 *", "0 9 * * 8", "0 9 * * 5-1", "0 9 * * FUNDAY", "0 9 * JAX *", "0 9 * * * *", "0 9 * * 1,,3", "0 9 1 * MON", "*/0 9 * * *", "*/61 9 * * *", "0 9 * * -1", "0 9 * * 1/2", "0 9 * * 1-5/0", "0 9 * * 1-5/2/3"} {
		t.Run("invalid/"+expression, func(t *testing.T) {
			parsed, err := ParseCron(expression)
			if err == nil {
				t.Fatal("accepted invalid cron")
			}
			if parsed != (CronExpression{}) {
				t.Fatal("returned partial parsed schedule")
			}
			if err := ValidateSchedule(&AutomationV2Schedule{Kind: "cron", Cron: expression, Timezone: "UTC"}); err == nil {
				t.Fatal("worker validator accepted invalid cron")
			}
		})
	}
}

// Purpose: ValidateSchedule and AutomationV2NextDue must agree on weekday
// schedules in an explicit zone. Direct boundary calls are the narrowest proof
// against weekend firing and UTC/local confusion, including a DST weekend.
func TestWorkerCronWeekdaysHelsinki(t *testing.T) {
	s := AutomationV2Schedule{Kind: "cron", Cron: "0 9 * * MON-FRI", Timezone: "Europe/Helsinki"}
	if err := ValidateSchedule(&s); err != nil {
		t.Fatal(err)
	}
	policy := AutomationV2Settings{Schedule: s}
	for _, tc := range []struct{ after, want string }{
		{"2026-03-27T18:00:00+02:00", "2026-03-30T09:00:00+03:00"},
		{"2026-03-30T08:00:00+03:00", "2026-03-30T09:00:00+03:00"},
		{"2026-03-30T09:00:00+03:00", "2026-03-31T09:00:00+03:00"},
	} {
		after, _ := time.Parse(time.RFC3339, tc.after)
		want, _ := time.Parse(time.RFC3339, tc.want)
		got, err := AutomationV2NextDue(policy, 0, after.UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		if got != want.UnixMilli() {
			t.Fatalf("after %s got %s want %s", tc.after, time.UnixMilli(got), want)
		}
	}
	for _, zone := range []string{"", "Local", "Not/AZone"} {
		invalid := s
		invalid.Timezone = zone
		if ValidateSchedule(&invalid) == nil {
			t.Fatalf("accepted timezone %q", zone)
		}
		if got, err := AutomationV2NextDue(AutomationV2Settings{Schedule: invalid}, 0, 0); err == nil || got != 0 {
			t.Fatalf("invalid zone produced due=%d err=%v", got, err)
		}
	}
}

// Purpose: AutomationV2NextDue enumerates UTC instants, skipping DST gaps but
// retaining both fold instants. Direct next-due assertions protect that existing
// policy while replacing its matcher with ParseCron.
func TestWorkerCronDST(t *testing.T) {
	policy := AutomationV2Settings{Schedule: AutomationV2Schedule{Kind: "cron", Cron: "30 3 * * SUN", Timezone: "Europe/Helsinki"}}
	for _, tc := range []struct{ after, want string }{
		{"2026-03-29T02:59:00+02:00", "2026-04-05T03:30:00+03:00"},
		{"2026-10-25T03:29:00+03:00", "2026-10-25T03:30:00+03:00"},
		{"2026-10-25T03:30:00+03:00", "2026-10-25T03:30:00+02:00"},
	} {
		after, _ := time.Parse(time.RFC3339, tc.after)
		want, _ := time.Parse(time.RFC3339, tc.want)
		got, err := AutomationV2NextDue(policy, 0, after.UnixMilli())
		if err != nil || got != want.UnixMilli() {
			t.Fatalf("after %s got %s err %v want %s", tc.after, time.UnixMilli(got), err, want)
		}
	}
}
