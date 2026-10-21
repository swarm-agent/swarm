package api

import (
	"errors"
	"testing"
)

func TestControlMCPUsageDetailKeepsEveryModelAndDay(t *testing.T) {
	response := map[string]any{
		"summary":     map[string]any{"thinking_tokens": float64(40), "codex_nominal_cost_usd": 8.2, "total_sessions": float64(12)},
		"by_provider": []any{map[string]any{"provider": "codex", "is_subscription": true, "total_tokens": float64(900)}},
		"by_model": []any{
			map[string]any{"provider": "codex", "model": "a", "total_tokens": float64(1)},
			map[string]any{"provider": "codex", "model": "b", "total_tokens": float64(2)},
			map[string]any{"provider": "codex", "model": "c", "total_tokens": float64(3)},
			map[string]any{"provider": "codex", "model": "d", "total_tokens": float64(4)},
			map[string]any{"provider": "codex", "model": "e", "total_tokens": float64(5)},
			map[string]any{"provider": "codex", "model": "f", "total_tokens": float64(6)},
		},
		"daily":           []any{map[string]any{"date": "2026-10-20", "total_tokens": float64(10)}, map[string]any{"date": "2026-10-21", "total_tokens": float64(20)}},
		"media":           map[string]any{"total_count": float64(2), "image_count": float64(2), "recent_items": []any{map[string]any{"id": "m"}}},
		"recent_sessions": []any{map[string]any{"session_id": "s1", "title": "Plan", "total_tokens": float64(7)}},
	}
	out := map[string]any{"summary": map[string]any{"total_tokens": float64(900)}}
	controlMCPUsageDetail(response, map[string]bool{"codex": true}, out)

	if got := len(out["by_model"].([]map[string]any)); got != 6 {
		t.Fatalf("by_model has %d models, want all 6", got)
	}
	if billing := out["by_model"].([]map[string]any)[0]["billing"]; billing != "subscription" {
		t.Fatalf("billing = %v, want subscription", billing)
	}
	if got := len(out["daily"].([]map[string]any)); got != 2 {
		t.Fatalf("daily has %d days, want 2", got)
	}
	if _, ok := out["media"].(map[string]any)["recent_items"]; ok {
		t.Fatal("media keeps only its counts and costs")
	}
	if out["summary"].(map[string]any)["codex_nominal_cost_usd"] != 8.2 {
		t.Fatalf("summary = %v", out["summary"])
	}
	if out["sessions"].([]map[string]any)[0]["session_id"] != "s1" {
		t.Fatalf("sessions = %v", out["sessions"])
	}
}

func TestControlMCPPlanUsage(t *testing.T) {
	chatgpt := map[string]any{
		"plan_type": "pro",
		"rate_limit": map[string]any{
			"limit_reached":    false,
			"primary_window":   map[string]any{"used_percent": 34.0, "limit_window_seconds": float64(18000), "reset_at": float64(1793000000)},
			"secondary_window": map[string]any{"used_percent": 12.0, "limit_window_seconds": float64(604800), "reset_at": float64(1793500000)},
		},
		"rate_limit_reset_credits": map[string]any{"available_count": float64(1)},
	}
	out := controlMCPPlanUsage(chatgpt, nil, nil, errors.New("claude code is unavailable"))
	plan := out["chatgpt"].(map[string]any)
	if plan["connected"] != true || plan["plan_type"] != "pro" || plan["reset_credits_available"] != float64(1) {
		t.Fatalf("chatgpt = %v", plan)
	}
	windows := plan["windows"].([]map[string]any)
	if len(windows) != 2 || windows[0]["used_percent"] != 34.0 || windows[1]["limit_window_seconds"] != float64(604800) {
		t.Fatalf("windows = %v", windows)
	}
	claude := out["claude_code"].(map[string]any)
	if claude["connected"] != false || claude["reason"] != "claude code is unavailable" {
		t.Fatalf("claude_code = %v", claude)
	}

	out = controlMCPPlanUsage(nil, errors.New("codex account usage requires a connected ChatGPT OAuth account"),
		map[string]any{"cli": map[string]any{"logged_in": true, "subscription_type": "max"}, "rate_limits": map[string]any{"status": "allowed", "windows": []any{map[string]any{"name": "five_hour", "utilization": 0.4}}}}, nil)
	if out["chatgpt"].(map[string]any)["connected"] != false {
		t.Fatalf("chatgpt = %v", out["chatgpt"])
	}
	claude = out["claude_code"].(map[string]any)
	if claude["connected"] != true || claude["rate_limits"].(map[string]any)["status"] != "allowed" {
		t.Fatalf("claude_code = %v", claude)
	}
}
