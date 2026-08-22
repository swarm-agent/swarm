package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3ProviderInputReplaysDurableMediaAfterContractRefresh(t *testing.T) {
	fixture := newRoutedMediaTestFixture(t)
	staged := fixture.stage(t, fixture.principal.AccountScopeID, "contract-refresh")
	response := fixture.post(t, fixture.principal.AccountScopeID, "contract-refresh", staged.ID, map[string]string{"modality": "image", "file_type": "png"})
	if response.Code != http.StatusOK {
		t.Fatalf("routed media status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode routed response: %v", err)
	}
	session, found, err := fixture.sessions.GetSession(body.SessionID)
	if err != nil || !found {
		t.Fatalf("get session found=%t err=%v", found, err)
	}
	messages, err := fixture.sessions.ListSessionMessages(body.SessionID, 0, 10)
	if err != nil || len(messages) != 1 || len(messages[0].Media) != 1 {
		t.Fatalf("list durable media messages err=%v messages=%+v", err, messages)
	}
	originalContractHash := messages[0].Media[0].ContractHash
	if originalContractHash == "" || originalContractHash == "refreshed-contract" {
		t.Fatalf("unexpected original contract hash %q", originalContractHash)
	}

	resolved := sessionV3ResolvedRuntime{
		Session: session,
		MediaContract: provideriface.SessionMediaContract{
			Hash: "refreshed-contract",
			Capabilities: []provideriface.MediaContractCapability{{
				Modality: "image", State: provideriface.MediaCapabilityStateAllowed,
				MIMETypes: []string{"image/png"}, MaxBytes: 1024, MaxCount: 2,
			}},
		},
	}
	input, err := fixture.server.v3SessionExecutor.sessionsV3ProviderInputWithMedia(resolved, messages, sessionsV3ProviderInputOptions{})
	if err != nil {
		t.Fatalf("replay after compatible contract refresh: %v", err)
	}
	if len(input) != 1 || !strings.Contains(string(mustJSON(t, input[0])), `"type":"session_media"`) {
		t.Fatalf("provider input did not retain durable media: %+v", input)
	}

	denied := resolved
	denied.MediaContract.Capabilities = append([]provideriface.MediaContractCapability(nil), resolved.MediaContract.Capabilities...)
	denied.MediaContract.Capabilities[0].State = provideriface.MediaCapabilityStateDenied
	if _, err := fixture.server.v3SessionExecutor.sessionsV3ProviderInputWithMedia(denied, messages, sessionsV3ProviderInputOptions{}); err == nil || !strings.Contains(err.Error(), "denied by the current run contract") {
		t.Fatalf("current contract denial error=%v", err)
	}

	tampered := append([]pebblestore.MessageSnapshot(nil), messages...)
	tampered[0].Media = append([]pebblestore.SessionMediaReference(nil), messages[0].Media...)
	tampered[0].Media[0].ContractHash = "forged-contract"
	if _, err := fixture.server.v3SessionExecutor.sessionsV3ProviderInputWithMedia(resolved, tampered, sessionsV3ProviderInputOptions{}); err == nil || !strings.Contains(err.Error(), "does not match its durable reference") {
		t.Fatalf("tampered durable contract reference error=%v", err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	return encoded
}
