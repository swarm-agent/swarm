package runtime

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: artifactV3PreviewCaptureRequest must distinguish malformed APIs,
// ready/seek failures and incorrect acknowledgements without accepting a frame
// or exposing authored exception text. Execute the actual injected bridge in JS;
// this narrow contract test does not claim browser loading or WebGL evidence.
func TestArtifactV3PreviewDiagnosticBridge(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node required for executable bridge regression")
	}
	profile, _ := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "motion_ui"})
	scene := pebblestore.ArtifactV3TemporalScene{SceneID: "opening", StartMS: 0, EndMS: 8000}
	manifest := pebblestore.ArtifactV3Manifest{Entrypoint: "index.html", AnimationProfile: profile, Parts: []pebblestore.ArtifactV3Part{{ID: "opening", Temporal: &scene, Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#canvas"}}}}
	files := map[string][]byte{"index.html": []byte("<canvas id='canvas'></canvas>" + nativePreviewAnimationManifest)}
	before := cloneArtifactProject(files)
	request, err := artifactV3PreviewCaptureRequest(manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	body := string(request.Files["index.html"])
	bridge := strings.Split(strings.Split(body, "<script data-swarm-capture-ui>")[1], "</script>")[0]
	script := `const assert=require('node:assert/strict');globalThis.document={documentElement:{dataset:{}}};` + bridge + `
 const good=()=>({version:'swarm.animation/v1',ready:async()=>({duration_ms:8000,fps:30}),seek:async ms=>({time_ms:ms,scene_id:'opening'})});
 const cases=[
 ['missing',undefined,'capture_animation_runtime_invalid'],
 ['no-version',{...good(),version:undefined},'capture_animation_runtime_invalid'],
 ['wrong-version',{...good(),version:'other'},'capture_animation_runtime_invalid'],
 ['no-seek',{...good(),seek:undefined},'capture_animation_runtime_invalid'],
 ['ready-reject',{...good(),ready:async()=>{throw Error('private ready text')}},'capture_animation_ready_failed'],
 ['timing',{...good(),ready:async()=>({duration_ms:1,fps:30})},'capture_animation_timing_mismatch'],
 ['fps',{...good(),ready:async()=>({duration_ms:8000,fps:60})},'capture_animation_timing_mismatch'],
 ['seek-reject',{...good(),seek:async()=>{throw Error('private seek text')}},'capture_animation_seek_failed'],
 ['time',{...good(),seek:async()=>({time_ms:0,scene_id:'opening'})},'capture_animation_time_mismatch'],
 ['scene',{...good(),seek:async ms=>({time_ms:ms,scene_id:'foreign'})},'capture_animation_scene_mismatch']];
 (async()=>{for(const [name,api,code] of cases){document.documentElement.dataset={};globalThis.__SWARM_ANIMATION_V1__=api;await assert.rejects(__SWARM_CAPTURE_V1__.select('opening'),{message:code},name);assert.equal(document.documentElement.dataset.swarmCaptureState,undefined,name)}
 let paused=false;const api=good();api.pause=async()=>{paused=true};api.seek=async ms=>{assert.equal(paused,true);assert.equal(ms,4000);return {time_ms:ms,scene_id:'opening'}};globalThis.__SWARM_ANIMATION_V1__=api;await __SWARM_CAPTURE_V1__.select('opening');assert.equal(document.documentElement.dataset.swarmCaptureState,'opening');assert.deepEqual(await __SWARM_CAPTURE_V1__.ready('opening'),{state_id:'opening'});
 })().catch(e=>{console.error(e);process.exitCode=1});`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("bridge: %v: %.4000s", err, out)
	}
	if !reflect.DeepEqual(before, files) {
		t.Fatal("authored source changed")
	}
}
