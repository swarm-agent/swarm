package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Requirement: Build and unresolvedArtifactV3Targets must retain their existing
// lowercase-substring semantics without rewriting source. This helper-level
// differential test is the narrowest layer for ASCII, Unicode and malformed
// UTF-8 regressions; it does not establish browser or authorization safety.
func TestArtifactV3ContainsFoldCompatibility(t *testing.T) {
	bodies := [][]byte{nil, []byte("<HTML><BODY id=\"Hero\">"), []byte("prefix <BoDy"), []byte("İ K Σ ẞ <BODY>"), {0xff, '<', 'B', 'O', 'D', 'Y'}, []byte("<bod"), []byte("nothing"), []byte(strings.Repeat("a", 1024) + "B"), []byte(strings.Repeat("a", 1024) + "İ")}
	needles := []string{"", "<html", "<body", "ID=\"hero\"", "missing", "i", "k", "σ", "ß", string([]byte{0xff}), strings.Repeat("a", 63) + "b", strings.Repeat("a", 64) + "b", "aab"}
	for _, body := range bodies {
		before := bytes.Clone(body)
		for _, needle := range needles {
			want := strings.Contains(strings.ToLower(string(body)), strings.ToLower(needle))
			if got := bytesContainsFold(body, needle); got != want {
				t.Fatalf("body=%q needle=%q got=%v want=%v", body, needle, got, want)
			}
		}
		if !bytes.Equal(body, before) {
			t.Fatal("source mutated")
		}
	}
}

// Representative 1 MiB HTML marker scans, including missing and Unicode
// markers. Compare the same benchmark on the unchanged and optimized helper;
// no renderer, provider, filesystem or timing assertion is involved. The baseline
// is the exact pre-optimization algorithm; repeated prefixes expose candidate
// work amplification without relying on unstable wall-clock test assertions.
func BenchmarkArtifactV3ContainsFold(b *testing.B) {
	for _, tc := range []struct{ name, prefix, needle string }{
		{"document", "<HTML><BODY>", "<body"},
		{"missing", "<HTML><BODY>", "id=\"missing\""},
		{"unicode", "İ <HTML><BODY>", "<body"},
		{"prefix64", "", strings.Repeat("a", 63) + "b"},
		{"long65", "", strings.Repeat("a", 64) + "b"},
	} {
		for _, impl := range []struct {
			name  string
			match func([]byte, string) bool
		}{
			{"optimized", bytesContainsFold},
			{"baseline", func(body []byte, text string) bool {
				return strings.Contains(strings.ToLower(string(body)), strings.ToLower(text))
			}},
		} {
			b.Run(tc.name+"/"+impl.name, func(b *testing.B) {
				body := []byte(tc.prefix + strings.Repeat("a", 1<<20))
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = impl.match(body, tc.needle)
				}
			})
		}
	}
}

// Requirement: bounded marker matching must equal the prior algorithm even
// across overlapping candidates, length fallback and invalid UTF-8. Fixed-seed
// differential coverage targets the helper rather than unrelated runtime state.
func TestArtifactV3ContainsFoldDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 4000; i++ {
		body := make([]byte, rng.Intn(256))
		for j := range body {
			if i%3 == 0 {
				body[j] = "aAbB<>"[rng.Intn(6)]
			} else {
				body[j] = byte(rng.Intn(256))
			}
		}
		needle := make([]byte, rng.Intn(90))
		for j := range needle {
			needle[j] = byte(rng.Intn(128))
		}
		if len(body) > 0 && i%2 == 0 {
			start := rng.Intn(len(body))
			needle = body[start : start+rng.Intn(len(body)-start+1)]
		}
		want := strings.Contains(strings.ToLower(string(body)), strings.ToLower(string(needle)))
		if got := bytesContainsFold(body, string(needle)); got != want {
			t.Fatalf("case %d body=%q needle=%q got=%v want=%v", i, body, needle, got, want)
		}
	}
}

// Requirement: runtime Build must still reject incomplete HTML and must not
// register a successful build or mutate source on failure. Adapter-level checks
// are the narrowest observable consumer proof for the optimized search helper.
func TestArtifactV3MarkerBuildRejection(t *testing.T) {
	manifest, err := json.Marshal(pebblestore.ArtifactV3Manifest{SchemaVersion: pebblestore.ArtifactV3ManifestVersion, Entrypoint: "index.html", Parts: []pebblestore.ArtifactV3Part{{ID: "main", Label: "Main", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#main"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		html  string
		valid bool
	}{
		{`<HTML><BODY><main id="main">ok</main></BODY></HTML>`, true},
		{`<html><main id="main">missing body</main></html>`, false},
		{`<body><main id="main">missing html</main></body>`, false},
	} {
		adapter := &artifactV3RuntimeAdapter{builds: make(map[string]tool.ArtifactV3BuildResult)}
		project := map[string][]byte{pebblestore.ArtifactV3ManifestFilename: manifest, "index.html": []byte(tc.html)}
		result, err := adapter.Build(context.Background(), tool.ArtifactV3BuildRequest{Project: project})
		if err != nil {
			t.Fatal(err)
		}
		if tc.valid {
			if result.Status != "succeeded" || len(adapter.builds) != 1 {
				t.Fatalf("valid build: %+v", result)
			}
		} else if result.Status != "failed" || len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "html_document_invalid" || len(adapter.builds) != 0 {
			t.Fatalf("failed build registered: %+v", result)
		}
		if string(project["index.html"]) != tc.html || !bytes.Equal(project[pebblestore.ArtifactV3ManifestFilename], manifest) {
			t.Fatal("source mutated")
		}
	}
}
