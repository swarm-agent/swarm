package providerlimit

import (
	"testing"
	"time"
)

// Requirement: a provider's usage, rate or session limit is recognised in an
// agent job's failure text and its stated reset is read (plus a minute's
// margin), and anything else (Swarm's own daily budget, an ordinary failure,
// an unreadable reset) is not a limit or has no stated reset.
// Threat: a session limit recorded as a failure or a reviewer's block, or a
// rerun scheduled before the reset or at a misread time.
// Owners: Is, ResetAt and Backoff; pure functions, so a unit test is the
// narrowest layer.
func TestIsAndResetAt(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 58, 58, 0, time.UTC)
	for _, tc := range []struct {
		msg   string
		limit bool
		reset time.Time // zero: no stated reset
	}{
		{"claude code turn failed (success): You've hit your session limit · resets 4pm (UTC)", true, time.Date(2026, 10, 27, 16, 1, 0, 0, time.UTC)},
		{"You've hit your usage limit · resets 9am (UTC)", true, time.Date(2026, 10, 28, 9, 1, 0, 0, time.UTC)},
		{"weekly limit reached · resets Oct 30, 2:30pm (UTC)", true, time.Date(2026, 10, 30, 14, 31, 0, 0, time.UTC)},
		{"429 Too Many Requests: rate limit exceeded, try again in 30 seconds", true, now.Add(30*time.Second + time.Minute)},
		{"rate_limit_error: please retry after 2 hours", true, now.Add(2*time.Hour + time.Minute)},
		{"You've hit your session limit", true, time.Time{}},
		{"You've hit your session limit · resets 4pm (Not/AZone)", true, time.Time{}},
		{"daily usage limit exceeded ($5.0100 spent today, limit is $5.00)", false, time.Time{}},
		{"required input missing", false, time.Time{}},
		{"", false, time.Time{}},
	} {
		if got := Is(tc.msg); got != tc.limit {
			t.Errorf("Is(%q) = %v", tc.msg, got)
		}
		at, ok := ResetAt(tc.msg, now)
		if ok != !tc.reset.IsZero() || (ok && !at.Equal(tc.reset)) {
			t.Errorf("ResetAt(%q) = %v %v, want %v", tc.msg, at, ok, tc.reset)
		}
	}
	// Swarm's own quotas and caps are never provider limits, however they
	// are worded (they were matched by "quota exceeded"/"limit reached").
	for _, msg := range []string{
		"artifact v3 git: quota exceeded",
		"artifactgit: quota exceeded",
		"artifact v3 author: quota exceeded: repair attempt limit reached; source and last diagnostics retained",
		"artifact storage quota exceeded",
		"session media asset count quota exceeded",
		"session media byte quota exceeded",
		"media staging account quota exceeded",
		"worker budget blocked: account daily_cost limit reached; held until 2026-10-28T00:00:00Z",
		"design: spending limit reached",
		"trigger rate limit exceeded",
		"gateway token message rate limit reached",
		"rendered video output 9 bytes exceeds quota 8 bytes",
		"worker deployment limit reached",
		"box worker concurrency limit reached; waiting for an active run to finish",
	} {
		if Is(msg) {
			t.Errorf("Swarm's own cap %q counted as a provider limit", msg)
		}
	}
	// A job that gave up waiting is a final failure, never waited on again.
	gaveUp := Exhausted(12, "You've hit your session limit · resets 4pm (UTC)")
	if Is(gaveUp) || !IsExhausted(gaveUp) || IsExhausted("You've hit your session limit") {
		t.Fatalf("exhausted %q: Is=%v IsExhausted=%v", gaveUp, Is(gaveUp), IsExhausted(gaveUp))
	}
	if got := Waiting(time.Date(2026, 10, 27, 16, 1, 0, 0, time.UTC), true, " limit "); got != "waiting for the provider's limit to reset (until 2026-10-27T16:01:00Z): limit" {
		t.Fatalf("waiting = %q", got)
	}
	if Backoff(1) != 5*time.Minute || Backoff(3) != 20*time.Minute || Backoff(50) != time.Hour {
		t.Fatalf("backoff = %v %v %v", Backoff(1), Backoff(3), Backoff(50))
	}
}
