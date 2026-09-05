package runtime

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/api"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: OpenPreview injects code-owned selection only into ephemeral
// entrypoint bytes with exact revision/manifest membership. Threat: selector data
// becomes script, non-entrypoint selectors gain targets, or source bytes change.
// This narrow injection test proves serialization; the headless Desktop test
// executes this same embedded JS and asserts DOM interaction and rejection.
func TestArtifactV3PreviewSelectionInjection(t *testing.T) {
	body := []byte("<!doctype html><html><head><title>Fixture</title></head><body></body></html>")
	original := bytes.Clone(body)
	revision := api.ArtifactV3Revision{RevisionRef: "revision-" + strings.Repeat("a", 40), Manifest: pebblestore.ArtifactV3Manifest{Entrypoint: "index.html", Parts: []pebblestore.ArtifactV3Part{
		{ID: "narration-one", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#narration-one"}},
		{ID: "elsewhere", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "other.html", Value: "#elsewhere"}},
		{ID: "file", Locator: pebblestore.ArtifactV3Locator{Kind: "file", Path: "index.html"}},
		{ID: "escaped", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "</script><script>bad()</script>"}},
	}}}
	result := string(injectArtifactV3PreviewSelection(body, revision))
	if !bytes.Equal(body, original) || !strings.Contains(result, "<head><script>") || strings.Count(result, "</script>") != 1 || strings.Contains(result, "__SWARM_ARTIFACT_V3_SELECTION_CONFIG__") {
		t.Fatal("injection changed input or allowed script termination")
	}
	start := strings.Index(result, "const config = ") + len("const config = ")
	end := strings.Index(result[start:], ";\n") + start
	var config struct {
		RevisionRef string                          `json:"revision_ref"`
		Parts       []struct{ ID, Selector string } `json:"parts"`
	}
	if err := json.Unmarshal([]byte(result[start:end]), &config); err != nil {
		t.Fatal(err)
	}
	if config.RevisionRef != revision.RevisionRef || len(config.Parts) != 2 || config.Parts[0].ID != "narration-one" || config.Parts[1].Selector != revision.Manifest.Parts[3].Locator.Value {
		t.Fatalf("wrong exact revision/selector set: %+v", config)
	}
	if got := injectArtifactV3PreviewSelection([]byte("<p>Fragment</p>"), revision); !bytes.HasPrefix(got, []byte("<p>Fragment</p><script>")) {
		t.Fatal("fragment preview lost its content")
	}
}
