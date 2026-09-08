package pebblestore

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Requirement: ValidateArtifactV3Project must reject malformed or downgraded
// required chapters before Git writes. Pure project validation is the narrowest
// authority test; shared Canvas and legacy single-scene behavior must remain valid.
func TestArtifactV3SceneContractValidation(t *testing.T) {
	fresh := func() ArtifactV3Manifest {
		scenes := []ArtifactV3TemporalScene{{"opening", 0, 4000}, {"resolve", 4000, 8000}}
		m := ArtifactV3Manifest{SchemaVersion: ArtifactV3ManifestVersion, Entrypoint: "index.html", AnimationProfile: &SessionArtifactAnimationProfile{}, SceneContract: &ArtifactV3SceneContract{8000, scenes}}
		for _, s := range scenes {
			s := s
			m.Parts = append(m.Parts, ArtifactV3Part{ID: s.SceneID, Label: s.SceneID, Temporal: &s, Locator: ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#canvas"}})
		}
		return m
	}
	cases := map[string]func(*ArtifactV3Manifest){
		"missing": func(m *ArtifactV3Manifest) { m.Parts = nil },
		"midpoint-only": func(m *ArtifactV3Manifest) {
			for i := range m.Parts {
				m.Parts[i].Temporal = nil
			}
		},
		"renamed": func(m *ArtifactV3Manifest) { m.Parts[1].Temporal.SceneID = "new" },
		"overlap": func(m *ArtifactV3Manifest) { m.Parts[1].Temporal.StartMS = 3999 },
		"gap":     func(m *ArtifactV3Manifest) { m.Parts[1].Temporal.StartMS = 4001 },
		"bounds":  func(m *ArtifactV3Manifest) { m.Parts[1].Temporal.EndMS = 9000 },
		"sample":  func(m *ArtifactV3Manifest) { v := int64(4000); m.Parts[0].CaptureTimeMS = &v },
		"file":    func(m *ArtifactV3Manifest) { m.Parts[0].Locator = ArtifactV3Locator{Kind: "file", Path: "index.html"} },
	}
	validate := func(m ArtifactV3Manifest) error {
		b, _ := json.Marshal(m)
		files := map[string][]byte{ArtifactV3ManifestFilename: b, "index.html": []byte("<canvas id='canvas'></canvas>")}
		before, _ := json.Marshal(files)
		_, err := ValidateArtifactV3Project(ArtifactV3Project{Files: files}, ArtifactV3Limits{})
		after, _ := json.Marshal(files)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("validation mutated source")
		}
		return err
	}
	if err := validate(fresh()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := fresh()
			mutate(&m)
			if validate(m) == nil {
				t.Fatal("accepted malformed scenes")
			}
		})
	}
	m := fresh()
	m.SceneContract = nil
	m.Parts = m.Parts[:1]
	m.Parts[0].Temporal.EndMS = 8000
	if err := validate(m); err != nil {
		t.Fatal("single scene rejected", err)
	}
	m.Parts[0].Temporal = nil
	m.AnimationProfile = nil
	if err := validate(m); err != nil {
		t.Fatal("legacy output rejected", err)
	}
}

// Requirement: publication rejects absent, stale-sample and foreign-Part scene
// evidence before Git writes; the service preflight is the narrowest boundary.
func TestArtifactV3SceneEvidenceBinding(t *testing.T) {
	scene := ArtifactV3TemporalScene{SceneID: "opening", StartMS: 0, EndMS: 100}
	m := ArtifactV3Manifest{SchemaVersion: ArtifactV3ManifestVersion, Entrypoint: "index.html", AnimationProfile: &SessionArtifactAnimationProfile{}, Parts: []ArtifactV3Part{{ID: "opening", Label: "Opening", Temporal: &scene, Locator: ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#canvas"}}}}
	b, _ := json.Marshal(m)
	p := ArtifactV3Project{Files: map[string][]byte{ArtifactV3ManifestFilename: b, "index.html": []byte("<canvas id='canvas'></canvas>")}}
	valid := ArtifactV3SceneEvidence{PartID: "opening", SampleMS: 50, DigestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := validateArtifactV3SceneEvidence(p, ArtifactV3EvidenceProjection{Scenes: []ArtifactV3SceneEvidence{valid}}, ArtifactV3Limits{}); err != nil {
		t.Fatal(err)
	}
	for _, scenes := range [][]ArtifactV3SceneEvidence{nil, {{PartID: "foreign", SampleMS: 50, DigestSHA256: valid.DigestSHA256}}, {{PartID: "opening", SampleMS: 49, DigestSHA256: valid.DigestSHA256}}, {{PartID: "opening", SampleMS: 50, DigestSHA256: "bad"}}, {valid, valid}} {
		if validateArtifactV3SceneEvidence(p, ArtifactV3EvidenceProjection{Scenes: scenes}, ArtifactV3Limits{}) == nil {
			t.Fatal("unbound evidence accepted")
		}
	}
}
