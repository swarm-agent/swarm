package runtime

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"regexp"
	"strings"

	"swarm/packages/swarmd/internal/api"
)

//go:embed artifact_v3_preview_selection.js
var artifactV3PreviewSelectionScript string

var artifactV3PreviewHead = regexp.MustCompile(`(?i)<head(?:\s[^>]*)?>`)

// Inject only into ephemeral entrypoint preview bytes, never the Git project or
// rendered publication. Selector membership comes from the exact native manifest.
func injectArtifactV3PreviewSelection(body []byte, revision api.ArtifactV3Revision) []byte {
	type target struct {
		ID       string `json:"id"`
		Selector string `json:"selector"`
	}
	parts := make([]target, 0)
	ids := make([]string, 0, len(revision.Manifest.Parts))
	for _, part := range revision.Manifest.Parts {
		ids = append(ids, part.ID)
		if part.Locator.Kind == "selector" && part.Locator.Path == revision.Manifest.Entrypoint && strings.TrimSpace(part.Locator.Value) != "" {
			parts = append(parts, target{part.ID, part.Locator.Value})
		}
	}
	config, _ := json.Marshal(struct {
		RevisionRef string   `json:"revision_ref"`
		Parts       []target `json:"parts"`
		PartIDs     []string `json:"part_ids"`
	}{revision.RevisionRef, parts, ids}) // JSON escapes HTML/script terminators.
	script := []byte("<script>" + strings.Replace(artifactV3PreviewSelectionScript, "__SWARM_ARTIFACT_V3_SELECTION_CONFIG__", string(config), 1) + "</script>")
	at := len(body)
	if head := artifactV3PreviewHead.FindIndex(body); head != nil {
		at = head[1]
	}
	return bytes.Join([][]byte{body[:at], script, body[at:]}, nil)
}
