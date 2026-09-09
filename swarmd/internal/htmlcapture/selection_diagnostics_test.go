package htmlcapture

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Requirement: captureState reports only allowlisted contract diagnostics, never
// arbitrary exception content. Execute its actual selection catch in JS; this
// tests the privacy boundary without a browser or authored private fixtures.
func TestCaptureSelectionDiagnostics(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node required for executable diagnostic regression")
	}
	codes := []string{"capture_animation_runtime_invalid", "capture_animation_ready_failed", "capture_animation_timing_mismatch", "capture_animation_seek_failed", "capture_animation_time_mismatch", "capture_animation_scene_mismatch"}
	for _, code := range codes {
		if safeMessage(code) == "trusted HTML capture failed" {
			t.Fatalf("no actionable message for %s", code)
		}
	}
	script := `const assert=require('node:assert/strict');const select=async api=>{const id='opening';` + captureSelectionScript + `;return {code:'ok'}};
 (async()=>{for(const code of ['` + strings.Join(codes, "','") + `'])assert.deepEqual(await select({select:async()=>{throw Error(code)}}),{code});
 for(const value of [Error('private authored text'), 'private string', null, {message:'capture_animation_scene_mismatch private suffix'}, {message:'unknown'}])assert.deepEqual(await select({select:async()=>{throw value}}),{code:'capture_state_select_failed'});
 assert.deepEqual(await select({select:async()=>{}}),{code:'ok'});
 })().catch(e=>{console.error(e);process.exitCode=1});`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("selection: %v: %.4000s", err, out)
	}
}
