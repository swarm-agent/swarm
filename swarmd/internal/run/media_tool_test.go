package run

import (
	"encoding/json"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// Purpose: inspection input instructions must not prohibit independent media work
// or advertise an absent tool. AppendSessionMediaInstructions and
// sessionMediaContractInstructions own this text; a direct rendering test is the
// narrowest layer proving both tool-set cases and unchanged contract data.
func TestSessionMediaContractInstructionsInspectionOnly(t *testing.T) {
	contract := provideriface.SessionMediaContract{
		Hash: "inspection-contract",
		Capabilities: []provideriface.MediaContractCapability{{
			Modality: "image", State: provideriface.MediaCapabilityStateAllowed,
			Semantics: "native", MIMETypes: []string{"image/png"},
			FileTypes: []string{"png"}, MaxBytes: 1024, MaxCount: 2,
		}},
	}
	before, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		tools      []provideriface.ToolDefinition
		wantManage bool
	}{
		{name: "no tools"},
		{name: "inspection only", tools: []provideriface.ToolDefinition{{Name: "media_inspect"}}},
		{name: "manage media", tools: []provideriface.ToolDefinition{{Name: "media_inspect"}, {Name: "manage_media"}}, wantManage: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := AppendSessionMediaInstructions("Existing instructions", contract, tc.tools...)
			for _, forbidden := range []string{"generation", "transcription", "unsupported"} {
				if strings.Contains(strings.ToLower(got), forbidden) {
					t.Errorf("inspection instructions contain %q: %s", forbidden, got)
				}
			}
			for _, required := range []string{
				"Existing instructions\n\nCurrent run media_inspect input contract (backend-authoritative):",
				"- image; semantics=native; mime_types=image/png; file_types=png; max_bytes=1024; max_count=2",
				"- Media kinds and types not listed cannot be inspected in this run.",
			} {
				if !strings.Contains(got, required) {
					t.Errorf("missing %q in %s", required, got)
				}
			}
			const guidance = "- Generating, editing, saving and transcribing media are done with manage_media using its own models and options, not with media_inspect."
			if strings.Contains(got, guidance) != tc.wantManage || strings.Contains(got, "manage_media") != tc.wantManage {
				t.Errorf("manage_media guidance mismatch: %s", got)
			}
			after, err := json.Marshal(contract)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("rendering changed contract hash, capabilities or limits")
			}
		})
	}
}
