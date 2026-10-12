package claudecode

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// RateLimitWindow is one Claude subscription usage window (for example
// five_hour or seven_day) as Claude Code reports it.
type RateLimitWindow struct {
	Name        string  `json:"name"`
	Utilization float64 `json:"utilization"`
	ResetsAt    int64   `json:"resets_at,omitempty"`
}

// RateLimits is the latest subscription usage reading Claude Code reported in
// a turn's stream (its rate_limit_event). It holds no credential material.
// The Claude login belongs to the daemon's OS user, so one reading covers every
// Swarm account that uses Claude Code on this daemon.
type RateLimits struct {
	Status         string            `json:"status"`
	LimitType      string            `json:"limit_type,omitempty"`
	ResetsAt       int64             `json:"resets_at,omitempty"`
	OverageStatus  string            `json:"overage_status,omitempty"`
	IsUsingOverage bool              `json:"is_using_overage"`
	Windows        []RateLimitWindow `json:"windows"`
	ObservedAt     time.Time         `json:"observed_at"`
}

type rateLimitInfo struct {
	Status         string `json:"status"`
	ResetsAt       int64  `json:"resetsAt"`
	RateLimitType  string `json:"rateLimitType"`
	OverageStatus  string `json:"overageStatus"`
	IsUsingOverage bool   `json:"isUsingOverage"`
	UnifiedWindows map[string]struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    int64   `json:"resetsAt"`
	} `json:"unifiedWindows"`
}

// parseRateLimits reads a rate_limit_event's rate_limit_info. It returns false
// for anything that does not carry a status.
func parseRateLimits(raw json.RawMessage, now time.Time) (RateLimits, bool) {
	var info rateLimitInfo
	if len(raw) == 0 || json.Unmarshal(raw, &info) != nil || strings.TrimSpace(info.Status) == "" {
		return RateLimits{}, false
	}
	limits := RateLimits{
		Status:         strings.TrimSpace(info.Status),
		LimitType:      strings.TrimSpace(info.RateLimitType),
		ResetsAt:       info.ResetsAt,
		OverageStatus:  strings.TrimSpace(info.OverageStatus),
		IsUsingOverage: info.IsUsingOverage,
		Windows:        make([]RateLimitWindow, 0, len(info.UnifiedWindows)),
		ObservedAt:     now.UTC(),
	}
	for name, window := range info.UnifiedWindows {
		limits.Windows = append(limits.Windows, RateLimitWindow{Name: name, Utilization: window.Utilization, ResetsAt: window.ResetsAt})
	}
	sort.Slice(limits.Windows, func(i, j int) bool { return limits.Windows[i].Name < limits.Windows[j].Name })
	return limits, true
}

// RecordRateLimits keeps the latest reading from a turn's stream.
func (c *CLI) RecordRateLimits(raw json.RawMessage) {
	if c == nil {
		return
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	limits, ok := parseRateLimits(raw, now())
	if !ok {
		return
	}
	c.mu.Lock()
	c.limits = &limits
	c.mu.Unlock()
}

// LatestRateLimits returns the latest reading, if a turn has reported one since
// the daemon started.
func (c *CLI) LatestRateLimits() (RateLimits, bool) {
	if c == nil {
		return RateLimits{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.limits == nil {
		return RateLimits{}, false
	}
	limits := *c.limits
	limits.Windows = append([]RateLimitWindow(nil), c.limits.Windows...)
	return limits, true
}
