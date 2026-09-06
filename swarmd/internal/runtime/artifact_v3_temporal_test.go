package runtime

import (
	"fmt"
	"reflect"
	"strings"
	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: temporal preview samples each scene without relaxing static
// selector visibility, source immutability, profile or renderer bounds. This
// adapter test checks exact capture requests; browser pixels remain a live gate.
func TestArtifactV3TemporalPreviewRequest(t *testing.T) {
	profile, _ := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "motion_ui"})
	one, two := int64(2000), int64(6000)
	manifest := pebblestore.ArtifactV3Manifest{Entrypoint: "index.html", AnimationProfile: profile, Parts: []pebblestore.ArtifactV3Part{
		{ID: "controls", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#controls"}},
		{ID: "one", CaptureTimeMS: &one, Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#one"}},
		{ID: "two", CaptureTimeMS: &two, Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#two"}},
	}}
	files := map[string][]byte{"index.html": []byte("<html><body>unchanged</body></html>")}
	request, err := artifactV3PreviewCaptureRequest(manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	if request.DocumentSections || !request.TemporalStates || !reflect.DeepEqual(request.StateIDs, []string{"one", "two"}) || !reflect.DeepEqual(request.RequiredSelectors, []string{"#controls"}) || !reflect.DeepEqual(request.StateRequiredSelectors, map[string][]string{"one": {"#one"}, "two": {"#two"}}) {
		t.Fatalf("capture contract lost: %#v", request)
	}
	if string(files["index.html"]) != "<html><body>unchanged</body></html>" || !strings.Contains(string(request.Files["index.html"]), `"one":2000`) {
		t.Fatal("source mutated or sample missing")
	}
	manifest.AnimationProfile = nil
	if _, err := artifactV3PreviewCaptureRequest(manifest, files); err == nil {
		t.Fatal("temporal capture accepted absent profile")
	}
	manifest.AnimationProfile = profile
	profile.Budgets.NetworkAllowed = true
	if _, err := artifactV3PreviewCaptureRequest(manifest, files); err == nil {
		t.Fatal("accepted altered profile")
	}
	profile.Budgets.NetworkAllowed = false
	one = -1
	if _, err := artifactV3PreviewCaptureRequest(manifest, files); err == nil {
		t.Fatal("accepted negative time")
	}
	one = 120001
	if _, err := artifactV3PreviewCaptureRequest(manifest, files); err == nil {
		t.Fatal("accepted unbounded time")
	}
	manifest.Parts = manifest.Parts[:1]
	manifest.AnimationProfile = nil
	request, err = artifactV3PreviewCaptureRequest(manifest, files)
	if err != nil || request.TemporalStates || !request.DocumentSections || len(request.RequiredSelectors) != 0 || !reflect.DeepEqual(request.StateRequiredSelectors, map[string][]string{"controls": {"#controls"}}) || !reflect.DeepEqual(request.StateIDs, []string{"controls"}) {
		t.Fatal("static section capture contract lost")
	}
}

// Requirement: many semantic sections retain independent targets without source
// mutation. The native request adapter owns static-versus-temporal routing and
// is the narrowest layer proving exact selectors and bounded section counts.
func TestArtifactV3DocumentPreviewRequest(t *testing.T) {
	manifest := pebblestore.ArtifactV3Manifest{Entrypoint: "index.html"}
	for i := 0; i < 24; i++ {
		id := fmt.Sprintf("section-%d", i)
		manifest.Parts = append(manifest.Parts, pebblestore.ArtifactV3Part{ID: id, Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#" + id}})
	}
	files := map[string][]byte{"index.html": []byte("<html><body>unchanged</body></html>")}
	before := cloneArtifactProject(files)
	request, err := artifactV3PreviewCaptureRequest(manifest, files)
	if err != nil || !request.DocumentSections || request.TemporalStates || len(request.StateIDs) != 24 || len(request.RequiredSelectors) != 0 {
		t.Fatalf("document routing lost: %+v, %v", request, err)
	}
	for _, part := range manifest.Parts {
		if !reflect.DeepEqual(request.StateRequiredSelectors[part.ID], []string{part.Locator.Value}) {
			t.Fatalf("section target lost: %s", part.ID)
		}
	}
	if !reflect.DeepEqual(before, files) || reflect.DeepEqual(request.Files, files) {
		t.Fatal("capture injection must be ephemeral")
	}
	for len(manifest.Parts) <= htmlcapture.MaxDocumentSections {
		id := fmt.Sprintf("section-%d", len(manifest.Parts))
		manifest.Parts = append(manifest.Parts, pebblestore.ArtifactV3Part{ID: id, Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#" + id}})
	}
	if _, err := artifactV3PreviewCaptureRequest(manifest, files); err == nil || !reflect.DeepEqual(before, files) {
		t.Fatal("document quota accepted or changed source")
	}
}
