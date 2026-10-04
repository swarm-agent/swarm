package run

import (
	"encoding/json"
	"reflect"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement: read validation rejects precisely the malformed stored contracts
// execution rejects, without changing profile authority. The shared state resolver
// and canonical runtime validators own this boundary; this unit layer compares
// against the pre-validation compiler and checks the registry name projection.
func TestStoredV3CheapValidationParity(t *testing.T) {
	s := &Service{tools: &tool.Runtime{}}
	known := s.knownRunToolNamesForAccount("")
	cheapKnown, err := s.storedV3ToolNamesForAccount("")
	if err != nil || !reflect.DeepEqual(cheapKnown, known) {
		t.Fatalf("registry inventory drift: cheap=%v execution=%v err=%v", cheapKnown, known, err)
	}
	for _, preset := range []string{"", "custom", "read_only", "read_write", "bash_git_only", "background_commit", "invalid"} {
		for _, variant := range []string{"enabled", "disabled", "implicit", "unknown", "unknown-disabled", "empty", "alias", "prefix"} {
			t.Run(preset+"/"+variant, func(t *testing.T) {
				tools := map[string]pebblestore.AgentToolConfig{"media_inspect": {Enabled: pebblestore.BoolPtr(true)}}
				switch variant {
				case "disabled":
					tools["media_inspect"] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(false)}
				case "implicit":
					delete(tools, "media_inspect")
				case "unknown":
					tools["not_a_runtime"] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(true)}
				case "unknown-disabled":
					tools["not_a_runtime"] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(false)}
				case "empty":
					tools[" "] = pebblestore.AgentToolConfig{}
				case "alias":
					delete(tools, "media_inspect")
					tools["media-inspect"] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(true)}
				case "prefix":
					tools["media_inspect"] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(false), BashPrefixes: []string{"git status"}}
				}
				profile := pebblestore.AgentProfile{Name: "validation-test", Mode: "subagent", ToolContract: &pebblestore.AgentToolContract{Preset: preset, Tools: tools}}
				before, _ := json.Marshal(profile)
				wantErr := validateStoredV3AgentToolContractRuntime(profile.Name, profile.ToolContract, known)
				if wantErr == nil {
					resolved, _, _, err := s.compileResolvedAgentToolContract("", profile)
					wantErr = err
					if wantErr == nil {
						wantErr = validateResolvedV3AgentToolRuntime(profile.Name, resolved, known)
					}
				}
				gotErr := s.ValidateStoredV3AgentToolContract("", profile)
				if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
					t.Fatalf("validation drift: got=%v want=%v", gotErr, wantErr)
				}
				after, _ := json.Marshal(profile)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("validation mutated profile")
				}
			})
		}
	}
	for _, profile := range []pebblestore.AgentProfile{{}, {Name: "missing-contract"}} {
		if err := s.ValidateStoredV3AgentToolContract("", profile); err == nil {
			t.Fatal("incomplete stored profile accepted")
		}
	}
}

// Requirement: presets cannot enable missing runtimes, and the subagent task
// boundary must be applied before validation.
// The real Service validator/compiler is the narrowest boundary proving both
// rejection and empty returned authority without initializing an execution.
func TestStoredV3CheapValidationUnavailablePreset(t *testing.T) {
	s := &Service{}
	profile := pebblestore.AgentProfile{Name: "validation-test", Mode: "subagent", ToolContract: &pebblestore.AgentToolContract{Preset: "background_commit"}}
	if err := s.ValidateStoredV3AgentToolContract("", profile); err == nil {
		t.Fatal("preset admitted missing runtime")
	}
	resolved, disabled, err := s.CompileStoredV3AgentToolContract("", profile)
	if err == nil || !reflect.DeepEqual(resolved, ResolvedAgentToolContract{}) || disabled != nil {
		t.Fatalf("invalid preset returned execution authority: %+v %v %v", resolved, disabled, err)
	}
	profile.ToolContract = &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"task": {Enabled: pebblestore.BoolPtr(true)}}}
	if err := s.ValidateStoredV3AgentToolContract("", profile); err != nil {
		t.Fatal(err)
	}
	resolved, _, err = s.CompileStoredV3AgentToolContract("", profile)
	if err != nil || resolved.Tools["task"].Enabled {
		t.Fatalf("subagent boundary changed: %+v %v", resolved, err)
	}
}
