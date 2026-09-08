package runtime

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
)

// Requirement: OpenPreview's live runtime preparation must supply both offline
// module edges with exact capability/revision URLs without capture or source
// mutation. This narrow adapter test catches dropped transitive query strings;
// browser elapsed playback is a separate integration proof.
func TestArtifactV3LiveRuntimeExactGraph(t *testing.T) {
	installThreeFixture(t)
	profile, _ := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "spatial_3d"})
	source := []byte(`<html><head></head><body><script type="module">import * as THREE from 'three'; requestAnimationFrame(tick);</script></body></html>`)
	before := bytes.Clone(source)
	files, err := artifactV3LiveRuntimeFiles(profile, "scenes/index.html", source, "session", "artifact", "revision-exact", "grant")
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/v3/sessions/session/artifacts-v3/artifact/preview/access/grant/files/swarm-animation-runtime/"
	html := string(files["scenes/index.html"])
	if !strings.Contains(html, prefix+"three.module.js?revision=revision-exact") || !strings.Contains(html, "requestAnimationFrame(tick)") || strings.Contains(html, "__SWARM_CAPTURE") {
		t.Fatal("live source lost runtime or acquired capture ownership")
	}
	encoded, _ := json.Marshal(prefix + "three.core.js?revision=revision-exact")
	if !bytes.Contains(files["swarm-animation-runtime/three.module.js"], encoded) || bytes.Contains(files["swarm-animation-runtime/three.module.js"], []byte("'./three.core.js'")) {
		t.Fatal("transitive import lost exact revision/grant")
	}
	if len(files) != 3 || !bytes.Equal(before, source) {
		t.Fatal("unexpected files or source mutation")
	}
	t.Setenv("SWARM_WEB_DIST_DIR", "")
	rejected, err := artifactV3LiveRuntimeFiles(profile, "scenes/index.html", source, "session", "artifact", "revision-exact", "grant")
	if err == nil || rejected != nil || !bytes.Equal(before, source) {
		t.Fatal("missing install silently succeeded or mutated source")
	}
}
