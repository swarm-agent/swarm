package ui

import (
	"strings"
	"testing"
	"time"
)

func TestShiftTabCycleModeDoesNotAppendGuidanceMessage(t *testing.T) {
	backend := &chatPlanModalBackendStub{sessionMode: "plan"}
	page := NewChatPage(ChatPageOptions{
		Backend:        backend,
		SessionID:      "session-1",
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	wantModes := []string{"auto", "plan", "auto"}
	for i, want := range wantModes {
		page.queueCycleMode()
		deadline := time.Now().Add(300 * time.Millisecond)
		for time.Now().Before(deadline) {
			_ = page.drainPermissionActions()
			if len(backend.setModes) > i && page.SessionMode() == want {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if len(backend.setModes) <= i {
			t.Fatalf("cycle %d did not call SetSessionMode", i+1)
		}
		if got := page.SessionMode(); got != want {
			t.Fatalf("cycle %d session mode = %q, want %q", i+1, got, want)
		}
	}

	if len(page.timeline) != 0 {
		t.Fatalf("timeline messages = %d, want 0 for shift-tab mode cycle guidance", len(page.timeline))
	}
}

func TestNormalizeSessionMode_RemovesLegacyYolo(t *testing.T) {
	if got := normalizeSessionMode("yolo"); got != "plan" {
		t.Fatalf("normalizeSessionMode(\"yolo\") = %q, want %q", got, "plan")
	}
}

func TestNormalizeSessionMode_PreservesExecutionCapabilityModes(t *testing.T) {
	if got := normalizeSessionMode("read"); got != "read" {
		t.Fatalf("normalizeSessionMode(\"read\") = %q, want %q", got, "read")
	}
	if got := normalizeSessionMode("readwrite"); got != "readwrite" {
		t.Fatalf("normalizeSessionMode(\"readwrite\") = %q, want %q", got, "readwrite")
	}
}

func TestNextSessionMode_TogglesPlanAutoOnly(t *testing.T) {
	if got := nextSessionMode("plan"); got != "auto" {
		t.Fatalf("nextSessionMode(plan) = %q, want auto", got)
	}
	if got := nextSessionMode("auto"); got != "plan" {
		t.Fatalf("nextSessionMode(auto) = %q, want plan", got)
	}
	if got := nextSessionMode("read"); got != "read" {
		t.Fatalf("nextSessionMode(read) = %q, want read", got)
	}
	if got := nextSessionMode("readwrite"); got != "readwrite" {
		t.Fatalf("nextSessionMode(readwrite) = %q, want readwrite", got)
	}
}

func TestSessionModeGuidanceMentionsPlanManageAcrossPlanAndAuto(t *testing.T) {
	if got := sessionModeGuidance("plan"); !strings.Contains(got, "plan_manage") || !strings.Contains(got, "same plan/checklist") {
		t.Fatalf("plan guidance = %q", got)
	}
	if got := sessionModeGuidance("auto"); !strings.Contains(got, "plan_manage") || !strings.Contains(got, "exit_plan_mode is only for leaving plan mode") {
		t.Fatalf("auto guidance = %q", got)
	}
}
