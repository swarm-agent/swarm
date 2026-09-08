package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/artifactv3video"
	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func installThreeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "swarm-animation-runtime")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	hashes := map[string]string{}
	for name, body := range map[string]string{"three.module.js": "export {REVISION} from './three.core.js';", "three.core.js": "export const REVISION='185';"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		hashes[name] = hex.EncodeToString(sum[:])
	}
	manifest, _ := json.Marshal(map[string]any{"version": "0.185.1", "files": hashes})
	if err := os.WriteFile(filepath.Join(dir, "three-manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARM_WEB_DIST_DIR", root)
	return dir
}

// Requirement: artifactV3PreviewCaptureRequest must preserve reviewed profiles
// and source while giving capture the complete offline module graph. This adapter
// test is the narrowest observable boundary before the actual browser renderer.
func TestArtifactV3SpatialPreviewRuntimeGraph(t *testing.T) {
	installThreeFixture(t)
	for _, registry := range []string{artifact.AnimationProfileRegistryVersion, "2026-08-16.v1"} {
		t.Run(registry, func(t *testing.T) {
			profile, _ := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "spatial_3d"})
			profile.RegistryVersion = registry
			before := *profile
			source := []byte(`<!doctype html><html><head><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script></head><body><main id="scene"></main></body></html>`)
			files := map[string][]byte{"scenes/index.html": source}
			request, err := artifactV3PreviewCaptureRequest(pebblestore.ArtifactV3Manifest{Entrypoint: "scenes/index.html", AnimationProfile: profile}, files)
			if err != nil {
				t.Fatal(err)
			}
			if !request.TemporalStates || len(request.Files["swarm-animation-runtime/three.core.js"]) == 0 || len(request.Files["swarm-animation-runtime/three.module.js"]) == 0 {
				t.Fatal("missing temporal capture or transitive runtime")
			}
			if !strings.Contains(string(request.Files["scenes/index.html"]), `../swarm-animation-runtime/three.module.js`) {
				t.Fatal("nested entry lost token-relative imports")
			}
			if len(files) != 1 || string(files["scenes/index.html"]) != string(source) || !reflect.DeepEqual(before, *profile) {
				t.Fatal("source or immutable snapshot changed")
			}
		})
	}
}

// Requirement: prepareArtifactV3Runtime must fail closed before mutating a source
// map for unsafe profiles, substituted runtime bytes, and incomplete installs.
// These negative cases exercise the actual packaging boundary, not source strings.
func TestArtifactV3RuntimeRejectsUnsafeGraphWithoutMutation(t *testing.T) {
	for _, scenario := range []string{"tampered-profile", "unknown-profile", "unsupported-profile", "missing-core", "bad-digest", "authored-runtime", "authored-importmap", "authored-base", "missing-install"} {
		t.Run(scenario, func(t *testing.T) {
			dir := installThreeFixture(t)
			profile, _ := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "spatial_3d"})
			files := map[string][]byte{"index.html": []byte("<html><head></head><body></body></html>")}
			switch scenario {
			case "tampered-profile":
				profile.Budgets.NetworkAllowed = true
			case "unknown-profile":
				profile.ProfileID = "unknown"
			case "unsupported-profile":
				profile, _ = artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "vector_playback"})
			case "missing-core":
				if err := os.Remove(filepath.Join(dir, "three.core.js")); err != nil {
					t.Fatal(err)
				}
			case "bad-digest":
				if err := os.WriteFile(filepath.Join(dir, "three.core.js"), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			case "authored-runtime":
				files["swarm-animation-runtime/three.core.js"] = []byte("authored")
			case "authored-importmap":
				files["index.html"] = []byte(`<html><head><script type="importmap">{}</script></head></html>`)
			case "authored-base":
				files["index.html"] = []byte(`<html><head><base href="/"></head></html>`)
			case "missing-install":
				t.Setenv("SWARM_WEB_DIST_DIR", "")
			}
			before := cloneArtifactProject(files)
			if err := prepareArtifactV3Runtime(profile, "index.html", files); err == nil {
				t.Fatal("unsafe runtime admitted")
			}
			if !reflect.DeepEqual(before, files) {
				t.Fatal("rejection mutated source")
			}
		})
	}
}

// Requirement: the new heavy-runtime packaging path must not introduce a runtime
// installation dependency or mutate source for ordinary reviewed motion_ui HTML.
func TestArtifactV3MotionRuntimeNeedsNoInstallation(t *testing.T) {
	t.Setenv("SWARM_WEB_DIST_DIR", "")
	profile, _ := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "motion_ui"})
	files := map[string][]byte{"index.html": []byte("<html><head></head></html>")}
	before := cloneArtifactProject(files)
	if err := prepareArtifactV3Runtime(profile, "index.html", files); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, files) {
		t.Fatal("motion source mutated")
	}
}

// Requirement: native conversion uses the same offline graph without a CSS
// downgrade and rejects invalid immutable policy before producing render input.
func TestArtifactV3SpatialConversionRuntimeGraph(t *testing.T) {
	installThreeFixture(t)
	profile, _ := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "spatial_3d"})
	manifest := pebblestore.ArtifactV3Manifest{Entrypoint: "index.html", AnimationProfile: profile}
	encoded, _ := json.Marshal(manifest)
	source := []byte(`<html><head><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script></head><body></body></html>`)
	files := map[string][]byte{pebblestore.ArtifactV3ManifestFilename: encoded, "index.html": source}
	before := cloneArtifactProject(files)
	renderer := artifactV3AnimationRenderer{renderer: htmlcapture.NewChromedpRenderer(htmlcapture.SystemChromePath, t.TempDir())}
	input := artifactv3video.RenderRequest{Project: artifactv3video.Project{Files: files}, DurationMs: 1000, FPS: 30, AnimationAdapter: htmlcapture.AnimationVersion}
	req, err := renderer.request(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Files["swarm-animation-runtime/three.core.js"]) == 0 || strings.Contains(string(req.Files["index.html"]), "data-swarm-artifact-v3-animation") {
		t.Fatal("conversion lost runtime or installed CSS fallback")
	}
	if !reflect.DeepEqual(before, files) {
		t.Fatal("conversion changed selected source")
	}
	profile.Budgets.NetworkAllowed = true
	encoded, _ = json.Marshal(manifest)
	files[pebblestore.ArtifactV3ManifestFilename] = encoded
	before = cloneArtifactProject(files)
	if _, err := renderer.request(input); err == nil {
		t.Fatal("tampered conversion profile admitted")
	}
	if !reflect.DeepEqual(before, files) {
		t.Fatal("rejected conversion mutated source")
	}
}

// Requirement: actual native preview must resolve the reviewed offline Three.js
// graph and repeat deterministic seek in the production sandbox. This opt-in
// browser test is separate from hermetic tiers; it needs installed runtime assets
// and system Chrome, and never installs them or weakens the sandbox.
func TestArtifactV3SpatialBrowser(t *testing.T) {
	if os.Getenv("SWARM_TEST_THREE_BROWSER") != "1" {
		t.Skip("opt-in system Chrome and installed runtime required")
	}
	profile, _ := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "spatial_3d"})
	source := []byte(`<!doctype html><html><head><style>html,body{margin:0;width:100%;height:100%;overflow:hidden}canvas{display:block}</style><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script></head><body><main id="scene"><canvas id="view"></canvas></main><script type="module">
import * as THREE from 'three';
const renderer=new THREE.WebGLRenderer({canvas:document.querySelector('#view'),antialias:false,preserveDrawingBuffer:true});renderer.setPixelRatio(1);renderer.setSize(1920,1080);
const scene=new THREE.Scene();scene.background=new THREE.Color('#102035');const camera=new THREE.PerspectiveCamera(45,1920/1080,0.1,100);camera.position.z=5;
const geometry=new THREE.BoxGeometry(1.5,1.5,1.5),material=new THREE.MeshNormalMaterial(),cube=new THREE.Mesh(geometry,material);scene.add(cube);
globalThis.__SWARM_ANIMATION_V1__={version:'swarm.animation/v1',ready:async()=>({duration_ms:1000,fps:30}),seek:async ms=>{cube.rotation.set(ms/1800,ms/1000,0);renderer.render(scene,camera);return {time_ms:ms}},pause:()=>{}};
addEventListener('pagehide',()=>{geometry.dispose();material.dispose();renderer.dispose()});
</script></body></html>`)
	req, err := artifactV3PreviewCaptureRequest(pebblestore.ArtifactV3Manifest{Entrypoint: "index.html", AnimationProfile: profile}, map[string][]byte{"index.html": source})
	if err != nil {
		t.Fatal(err)
	}
	req.RequiredSelectors = []string{"#scene", "#view"}
	req.StateIDs = []string{"animation-preview", "animation-preview"}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	results, err := htmlcapture.NewChromedpRenderer(htmlcapture.SystemChromePath, t.TempDir()).Capture(ctx, req)
	if err != nil {
		t.Fatalf("production capture: %v", err)
	}
	if len(results) != 2 || len(results[0].PNG) == 0 || !bytes.Equal(results[0].PNG, results[1].PNG) {
		t.Fatal("repeated deterministic seek differed or returned no pixels")
	}
	if output := os.Getenv("SWARM_TEST_THREE_PNG"); output != "" {
		if err := os.WriteFile(output, results[0].PNG, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("real Three.js capture: two identical PNGs, %d bytes", len(results[0].PNG))
}
