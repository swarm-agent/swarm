package htmlcapture

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestValidateAnimationRequestBounds(t *testing.T) {
	valid := AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": []byte("ok")}, DurationMS: 1200, FPS: 24}
	if frames, err := validateAnimationRequest(valid); err != nil || frames != 29 {
		t.Fatalf("valid request frames=%d err=%v", frames, err)
	}
	long := AnimationRequest{Entry: "index.html", Files: valid.Files, DurationMS: 74_920, FPS: 60}
	if frames, err := validateAnimationRequest(long); err != nil || frames != 4496 {
		t.Fatalf("long request frames=%d err=%v", frames, err)
	}
	for _, invalid := range []AnimationRequest{
		{Entry: "index.html", Files: valid.Files, DurationMS: 99, FPS: 30},
		{Entry: "index.html", Files: valid.Files, DurationMS: MaxAnimationDurationMS + 1, FPS: 30},
		{Entry: "index.html", Files: valid.Files, DurationMS: 1000, FPS: MaxAnimationFPS + 1},
	} {
		if _, err := validateAnimationRequest(invalid); err == nil {
			t.Fatalf("expected bounds error for %+v", invalid)
		}
	}
}

func TestChromedpRendererCapturesDeterministicAnimationWithSystemRuntimes(t *testing.T) {
	if _, err := os.Stat(SystemChromePath); err != nil {
		t.Skipf("system-managed Chrome unavailable: %v", err)
	}
	if renderer := NewChromedpRenderer(SystemChromePath, t.TempDir()); renderer.EncoderPath == "." {
		t.Skip("system-managed FFmpeg unavailable")
	}
	html := []byte(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<script>globalThis.__SWARM_ANIMATION_V1__={version:"swarm.animation/v1",ready(){return {duration_ms:400,fps:10}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);document.getElementById("box").style.transform="translateX("+(timeMs/2)+"px)";return {time_ms:timeMs}}};</script>
<style>html,body{width:100%;height:100%;margin:0;overflow:hidden;background:#08111f}#box{width:240px;height:240px;background:#7dd3fc}</style></head><body><div id="box"></div></body></html>`)
	renderer := NewChromedpRenderer(SystemChromePath, t.TempDir())
	var browserOutput strings.Builder
	renderer.browserOutput = &browserOutput
	result, err := renderer.RenderAnimation(context.Background(), AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": html}, DurationMS: 400, FPS: 10})
	if err != nil {
		t.Fatalf("RenderAnimation: %v; cause: %v; browser output: %s", err, errors.Unwrap(err), browserOutput.String())
	}
	if result.DurationMS != 400 || result.FPS != 10 || result.FrameCount != 4 || len(result.MP4) < 12 || !bytes.Equal(result.MP4[4:8], []byte("ftyp")) {
		t.Fatalf("result = %+v, bytes=%d", result, len(result.MP4))
	}
}
