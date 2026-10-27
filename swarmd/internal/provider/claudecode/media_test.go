package claudecode

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

type mediaToolInvoker struct {
	result provideriface.ToolExecutionResult
}

func (i mediaToolInvoker) ExecuteTool(context.Context, provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	return i.result, nil
}

// Purpose: callTool must emit authorized bytes only as MCP image content, never
// text, and reject malformed or unauthorized payloads without leaking bytes.
// Direct bridge calls exercise serialization and admission without a live CLI.
func TestClaudeCodeMCPImageContent(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	payload := provideriface.SessionMediaPayload{AssetID: "fixture-image", Modality: "image", MIMEType: "image/png", Size: int64(len(data)), Bytes: data, DigestSHA256: hex.EncodeToString(digest[:])}
	declaration := claudeCodeMediaDeclaration(pebblestore.AuthCredentialRecord{})
	contract := provideriface.SessionMediaContract{Hash: "fixture-contract", ProviderID: ProviderID, AdapterID: declaration.AdapterID, ProviderSurface: declaration.ProviderSurface, CredentialSurface: declaration.CredentialSurface, Capabilities: []provideriface.MediaContractCapability{{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: "native", MIMETypes: []string{"image/png"}, ContentTypes: []string{"image"}, MaxBytes: claudeCodeImageMaxBytes, MaxCount: 1}}}
	for _, name := range []string{"image", "text", "digest", "mime", "size", "surface", "denied", "count"} {
		t.Run(name, func(t *testing.T) {
			p := payload
			c := contract
			c.Capabilities = append([]provideriface.MediaContractCapability(nil), contract.Capabilities...)
			result := provideriface.ToolExecutionResult{TextForModel: "inspection metadata", Output: "fallback", Media: &p}
			switch name {
			case "text":
				result.Media = nil
			case "digest":
				p.DigestSHA256 = "bad"
			case "mime":
				p.MIMEType = "application/pdf"
			case "size":
				p.Size++
			case "surface":
				c.ProviderID = "anthropic"
			case "denied":
				c.Capabilities[0].State = provideriface.MediaCapabilityStateDenied
			}
			bridge := &mcpBridge{ctx: context.Background(), invoker: mediaToolInvoker{result}, byName: map[string]string{"media_inspect": "media_inspect"}, mediaContract: c}
			if name == "count" {
				bridge.mediaCount.Store(1)
			}
			response, rpcErr := bridge.callTool(context.Background(), json.RawMessage(`{"name":"media_inspect","arguments":{}}`))
			if rpcErr != nil {
				t.Fatal(rpcErr)
			}
			content := response["content"].([]map[string]any)
			if name == "image" {
				if response["isError"] != false || len(content) != 2 || content[0]["text"] != "inspection metadata" || content[1]["type"] != "image" || content[1]["mimeType"] != "image/png" || content[1]["data"] != base64.StdEncoding.EncodeToString(data) {
					t.Fatalf("unexpected image response: %+v", response)
				}
			} else {
				if len(content) != 1 || content[0]["type"] != "text" || content[0]["data"] != nil {
					t.Fatal("image leaked into rejected/text result")
				}
				if response["isError"] != (name != "text") {
					t.Fatalf("unexpected error state: %+v", response)
				}
				if name == "text" && content[0]["text"] != "inspection metadata" {
					t.Fatal("text changed")
				}
			}
		})
	}
}
