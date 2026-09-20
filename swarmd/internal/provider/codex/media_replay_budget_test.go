package codex

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func replayBudgetRequest(count int) Request {
	return Request{Model: "gpt-test", ProviderConfigurationHash: "config", MediaContract: allowedMediaContract(
		"codex", "chatgpt_codex", "codex_oauth", "codex-chatgpt-v1", provideriface.MediaContractCapability{
			Modality: "image", State: provideriface.MediaCapabilityStateAllowed,
			Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"},
			ContentTypes: []string{"input_image"}, MaxBytes: maxCodexFullReplayMediaBytes, MaxCount: count,
		})}
}

func replayImage(body string) map[string]any {
	return map[string]any{"role": "user", "content": []map[string]any{{
		"type": "session_media", "media": testMediaPayload("image", "image/png", "png", []byte(body)),
	}}}
}

// Requirement: repeated inspections must not turn a per-request cap into a lifetime
// cap. Threat: reconnecting after image 21 aborts a valid small inspection. The
// request builder is the narrowest layer proving selection, wire bytes, ordering
// and input immutability together; no provider or durable store is contacted.
func TestCodexMediaReplayProtectsLatestInspection(t *testing.T) {
	req := replayBudgetRequest(20)
	for i := 0; i < 20; i++ {
		req.Input = append(req.Input, replayImage("old"))
	}
	req.Input = append(req.Input,
		map[string]any{"type": "function_call", "call_id": "inspect-new", "name": "media_inspect", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": "inspect-new", "output": "exact image reference remains"},
		replayImage("new-pixels"))
	before, _ := json.Marshal(req.Input)
	body, err := buildCodexRequestBody(req, req.Input)
	if err != nil {
		t.Fatal(err)
	}
	out := body["input"].([]map[string]any)
	if len(out) != len(req.Input) || out[20]["call_id"] != "inspect-new" || out[21]["output"] != "exact image reference remains" {
		t.Fatal("message/tool reference ordering changed")
	}
	old, _ := inputContentMaps(out[0]["content"])
	if old[0]["type"] != "input_text" || !strings.Contains(asString(old[0]["text"]), "omitted") {
		t.Fatal("oldest image lacks explicit omission marker")
	}
	count := 0
	for _, item := range out {
		parts, _ := inputContentMaps(item["content"])
		for _, part := range parts {
			if part["type"] == "input_image" {
				count++
			}
		}
	}
	latest, _ := inputContentMaps(out[22]["content"])
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("new-pixels"))
	if count != 20 || latest[0]["image_url"] != want {
		t.Fatal("latest pixels or contract count not preserved")
	}
	after, _ := json.Marshal(req.Input)
	if string(before) != string(after) {
		t.Fatal("durable input representation mutated")
	}
}

// Requirement: a fresh batch cannot be silently trimmed, including synthetic user
// messages from parallel tools. Threat: dropping current pixels produces a false
// visual review. Direct materialization proves rejection and no partial output.
func TestCodexMediaBudgetRejectsCurrentBatch(t *testing.T) {
	req := replayBudgetRequest(2)
	for _, tc := range []struct {
		name   string
		input  []map[string]any
		budget int64
		replay bool
		want   string
	}{
		{"fresh count", []map[string]any{replayImage("a"), replayImage("b"), replayImage("c")}, 10, true, "count limit"},
		{"reconnect current count", []map[string]any{{"type": "function_call", "call_id": "batch"}, replayImage("a"), replayImage("b"), replayImage("c")}, 10, true, "count limit"},
		{"fresh bytes", []map[string]any{replayImage("123456")}, 5, true, "byte budget"},
		{"current split bytes", []map[string]any{replayImage("history"), {"role": "assistant", "content": "inspect both"}, replayImage("123"), replayImage("456")}, 5, true, "byte budget"},
		{"delta bytes", []map[string]any{replayImage("123"), replayImage("456")}, 5, false, "byte budget"},
		{"delta count", []map[string]any{replayImage("a"), replayImage("b"), replayImage("c")}, 10, false, "count limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.input)
			out, err := materializeSessionMediaInputBudget(req, tc.input, tc.budget, tc.replay)
			if err == nil || !strings.Contains(err.Error(), tc.want) || out != nil {
				t.Fatalf("want fail-closed %s, got output=%v err=%v", tc.want, out, err)
			}
			after, _ := json.Marshal(tc.input)
			if string(before) != string(after) {
				t.Fatal("rejected batch mutated")
			}
		})
	}
	// Exact aggregate boundary is valid, not a reason to trim current media.
	out, err := materializeSessionMediaInputBudget(req, []map[string]any{replayImage("123"), replayImage("45")}, 5, false)
	if err != nil || len(out) != 2 {
		t.Fatalf("exact delta byte boundary: %v", err)
	}
}

// Requirement: count zero, corrupted metadata and wrong provider surfaces remain
// denied even when the bad image would be omitted. Threat: pruning as a validation
// bypass. This helper-level test is narrower than a provider/network test.
func TestCodexMediaReplayValidationBeforeOmission(t *testing.T) {
	for _, kind := range []string{"zero count", "size mismatch", "identity", "malformed", "surface"} {
		t.Run(kind, func(t *testing.T) {
			req := replayBudgetRequest(1)
			old := replayImage("old")
			parts, _ := inputContentMaps(old["content"])
			payload := parts[0]["media"].(provideriface.SessionMediaPayload)
			switch kind {
			case "zero count":
				req.MediaContract.Capabilities[0].MaxCount = 0
			case "size mismatch":
				payload.Size++
			case "identity":
				payload.AssetID = ""
			case "surface":
				req.MediaContract.CredentialSurface = "wrong"
			}
			parts[0]["media"] = payload
			if kind == "malformed" {
				parts[0]["media"] = "not an immutable payload"
			}
			input := []map[string]any{old, {"role": "assistant", "content": "already reviewed"}, replayImage("new")}
			out, err := materializeSessionMediaInputWithReplayBudget(req, input, 3)
			if err == nil || out != nil {
				t.Fatal("invalid history was silently omitted")
			}
		})
	}
}
