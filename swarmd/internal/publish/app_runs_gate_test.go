package publish

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Purpose: command and landFailure must carry only a bounded smoke step into
// delivery_reason, never privileged logs. Real script execution is the narrowest
// layer proving the process-output boundary, including malformed receipts.
func TestAppRunsFailureReceipt(t *testing.T) {
	for _, receipt := range []string{"APP_RUNS_FAILED:health", "APP_RUNS_FAILED:health secret"} {
		dir := t.TempDir()
		script := filepath.Join(dir, "gate")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho private-log >&2\necho '"+receipt+"' >&2\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
		output, err := command(context.Background(), Rule{Command: script, Folder: dir}, dir, time.Second, "land")
		if err == nil {
			t.Fatal("failed gate accepted")
		}
		reason := landFailure(output).Error()
		if strings.Contains(reason, "private-log") || strings.Contains(reason, "secret") {
			t.Fatal(reason)
		}
		if receipt == "APP_RUNS_FAILED:health" && reason != "app-runs landing check failed: health" {
			t.Fatal(reason)
		}
		if receipt != "APP_RUNS_FAILED:health" && reason != "land command failed" {
			t.Fatal(reason)
		}
	}
	t.Setenv("SWARM_LAND_TIMEOUT", "")
	if landTimeout() != 10*time.Minute {
		t.Fatal("wrong default")
	}
	t.Setenv("SWARM_LAND_TIMEOUT", "12m")
	if landTimeout() != 12*time.Minute {
		t.Fatal("override ignored")
	}
}
