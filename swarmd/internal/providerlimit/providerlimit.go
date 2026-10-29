// Package providerlimit recognises a model provider's usage, rate or session
// limit in an agent job's failure text and reads the reset time the provider
// states. A provider limit is transient: the job did not fail on its merits,
// so callers wait for the reset and run it again rather than recording a
// failure or a reviewer's verdict.
package providerlimit

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MaxResetWait bounds how far ahead a stated reset is honoured (a weekly
// limit is the longest a provider states).
const MaxResetWait = 8 * 24 * time.Hour

// MaxWaits bounds how many times one job (a workflow agent step, a worker
// run, a project task) waits for its provider's limit to reset before it
// fails with Exhausted.
const MaxWaits = 12

// resetMargin is added to a stated reset so the rerun starts after it.
const resetMargin = time.Minute

// phrases mark a provider limit. A bare "quota exceeded" or "limit reached"
// is not one: Swarm's own storage, media and artifact quotas and its worker
// and gateway caps use those words.
var phrases = []string{
	"session limit", "usage limit", "rate limit", "rate_limit", "ratelimit", "rate-limit",
	"too many requests", "hit your limit", "weekly limit",
	"exceeded your current quota", "resource_exhausted", "status 429", "status=429", "status: 429", "http 429",
}

// swarmOwned mark Swarm's own caps, which are the owner's settings or
// Swarm's storage bounds, never a provider limit: the account's daily
// budget, worker budgets, storage/media/artifact quotas, trigger and
// gateway-token rate limits.
var swarmOwned = []string{
	"daily usage limit exceeded", "daily spending limit", "spending limit reached", "worker budget",
	"artifact v3 git:", "artifactgit:", "artifact v3 author:", "artifact storage quota", "session media", "media staging",
	"trigger rate limit", "gateway token", "end user's", "rate limits are", "active rate limits",
}

// exhaustedPrefix starts the failure recorded when a job gave up waiting:
// its provider's limit was still in effect after the bounded number of waits.
const exhaustedPrefix = "the provider's limit was still in effect after "

// Exhausted is the final failure of a job that waited waits times for its
// provider's limit to reset and found it still in effect. It is not a limit
// to wait on again (Is is false), and it is not a reviewer's verdict.
func Exhausted(waits int, msg string) string {
	return fmt.Sprintf("%s%d waits for its reset: %s", exhaustedPrefix, waits, strings.TrimSpace(msg))
}

// IsExhausted reports whether msg carries the failure Exhausted records,
// also when a caller prefixed it ("worker job failed: …").
func IsExhausted(msg string) bool {
	return strings.Contains(strings.ToLower(msg), exhaustedPrefix)
}

// Waiting is the reason recorded while a job waits for its provider's limit
// to reset at until; stated is false when the wait is a backoff.
func Waiting(until time.Time, stated bool, msg string) string {
	when := until.UTC().Format(time.RFC3339)
	if !stated {
		when += "; the provider stated no reset time"
	}
	return "waiting for the provider's limit to reset (until " + when + "): " + strings.TrimSpace(msg)
}

// Is reports whether msg is a provider usage, rate or session limit.
func Is(msg string) bool {
	lower := strings.ToLower(msg)
	if strings.TrimSpace(lower) == "" || IsExhausted(msg) {
		return false
	}
	for _, p := range swarmOwned {
		if strings.Contains(lower, p) {
			return false
		}
	}
	for _, p := range phrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

var (
	// "resets 4pm (UTC)", "resets at 4:30 pm UTC", "resets Oct 30, 9am (Europe/Berlin)", "resets 16:00 (UTC)".
	resetClock = regexp.MustCompile(`(?i)\bresets?\s+(?:at\s+)?(?:(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+(\d{1,2}),?\s+(?:at\s+)?)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\s*\(?\s*([A-Za-z][A-Za-z0-9_+\-]*(?:/[A-Za-z0-9_+\-]+)*)?\s*\)?`)
	// "try again in 20 seconds", "retry after 5m", "resets in 2 hours".
	resetIn = regexp.MustCompile(`(?i)\b(?:try again|retry|resets?)\s+(?:in|after)\s+(\d{1,6})\s*(s|sec|secs|seconds?|m|min|mins|minutes?|h|hr|hrs|hours?)\b`)
)

var months = map[string]time.Month{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

// ResetAt is the time the provider states its limit resets, plus a minute's
// margin, read from msg at now. ok is false when msg states no reset Swarm
// can read (an unknown time zone among them) or the reset is more than
// MaxResetWait ahead; the caller then backs off for a bounded time.
func ResetAt(msg string, now time.Time) (time.Time, bool) {
	if m := resetIn.FindStringSubmatch(msg); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return time.Time{}, false
		}
		unit := time.Second
		switch strings.ToLower(m[2])[0] {
		case 'm':
			unit = time.Minute
		case 'h':
			unit = time.Hour
		}
		wait := time.Duration(n) * unit
		if wait > MaxResetWait {
			return time.Time{}, false
		}
		return now.Add(wait + resetMargin), true
	}
	m := resetClock.FindStringSubmatch(msg)
	if m == nil {
		return time.Time{}, false
	}
	hour, _ := strconv.Atoi(m[3])
	minute := 0
	if m[4] != "" {
		minute, _ = strconv.Atoi(m[4])
	}
	switch strings.ToLower(m[5]) {
	case "am":
		if hour < 1 || hour > 12 {
			return time.Time{}, false
		}
		if hour == 12 {
			hour = 0
		}
	case "pm":
		if hour < 1 || hour > 12 {
			return time.Time{}, false
		}
		if hour != 12 {
			hour += 12
		}
	default:
		if m[4] == "" {
			return time.Time{}, false // a bare number is not a clock time
		}
	}
	if hour > 23 || minute > 59 {
		return time.Time{}, false
	}
	loc := time.UTC
	if zone := m[6]; zone != "" && !strings.EqualFold(zone, "utc") && !strings.EqualFold(zone, "gmt") && !strings.EqualFold(zone, "z") {
		l, err := time.LoadLocation(zone)
		if err != nil {
			return time.Time{}, false
		}
		loc = l
	}
	local := now.In(loc)
	var at time.Time
	if m[1] != "" {
		day, _ := strconv.Atoi(m[2])
		month := months[strings.ToLower(m[1])[:3]]
		at = time.Date(local.Year(), month, day, hour, minute, 0, 0, loc)
		if at.Month() != month || at.Day() != day {
			return time.Time{}, false
		}
		if at.Before(now.Add(-24 * time.Hour)) {
			at = at.AddDate(1, 0, 0) // a date early next year
		}
	} else {
		at = time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
		if !at.After(now) {
			at = at.AddDate(0, 0, 1)
		}
	}
	if at.Sub(now) > MaxResetWait {
		return time.Time{}, false
	}
	at = at.Add(resetMargin)
	if !at.After(now) {
		at = now.Add(resetMargin)
	}
	return at, true
}

// Backoff is the bounded wait after the n-th (1-based) provider limit with no
// readable reset: 5, 10, 20, 40 minutes, then an hour.
func Backoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	return min(5*time.Minute<<min(n-1, 4), time.Hour)
}
