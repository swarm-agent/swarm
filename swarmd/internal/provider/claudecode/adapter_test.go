package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: provider readiness is account-scoped. The machine-level Claude Code
// sign-in becomes usable only for accounts that enabled the secret-free `cli`
// credential, and only while the CLI reports a sign-in. Threats: cross-account
// use of one person's Claude sign-in; reporting ready while signed out; storing
// or accepting credential material. Owners: Adapter.Status,
// Adapter.VerifyCredential, activeCLICredential.
func TestAdapterStatusIsAccountScopedAndReflectsCLISignIn(t *testing.T) {
	harness := newFakeCLIHarness(t, "success", true)
	adapter := NewAdapter(harness.authStore, harness.cli)

	enabledCtx := identity.ContextWithPrincipal(context.Background(), harness.principal)
	status, err := adapter.Status(enabledCtx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || !strings.Contains(status.Reason, "signed in") || status.DefaultModel == "" {
		t.Fatalf("enabled account status = %+v", status)
	}
	if len(status.AuthMethods) != 1 || status.AuthMethods[0].CredentialType != pebblestore.AuthTypeCLI {
		t.Fatalf("auth methods = %+v", status.AuthMethods)
	}

	otherCtx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-2", AccountScopeID: "account-2"})
	other, err := adapter.Status(otherCtx)
	if err != nil {
		t.Fatal(err)
	}
	if other.Ready || !strings.Contains(other.Reason, "not enabled for this account") {
		t.Fatalf("other account status = %+v", other)
	}
	// Status text reaches every client (desktop, TUI, SDK, MCP), so it must not
	// name one client's UI.
	for _, clientSpecific := range []string{"Settings", "/auth"} {
		if strings.Contains(other.Reason, clientSpecific) || strings.Contains(status.Reason, clientSpecific) {
			t.Fatalf("reason names a client-specific surface %q: %q / %q", clientSpecific, other.Reason, status.Reason)
		}
	}

	if anonymous, _ := adapter.Status(context.Background()); anonymous.Ready {
		t.Fatalf("anonymous status ready: %+v", anonymous)
	}

	verification, err := adapter.VerifyCredential(enabledCtx, provideriface.AuthCredential{Provider: ProviderID, Type: pebblestore.AuthTypeCLI})
	if err != nil || !verification.Connected {
		t.Fatalf("verify cli = %+v, %v", verification, err)
	}
	if _, err := adapter.VerifyCredential(enabledCtx, provideriface.AuthCredential{Provider: ProviderID, Type: pebblestore.AuthTypeAPI, APIKey: "sk-not-accepted"}); err == nil {
		t.Fatal("api credential type must be rejected")
	}
	record, ok, err := harness.authStore.GetActiveCredentialForAccount(harness.principal.AccountScopeID, ProviderID)
	if err != nil || !ok {
		t.Fatalf("active credential: %v %v", ok, err)
	}
	if record.APIKey != "" || record.AccessToken != "" || record.RefreshToken != "" {
		t.Fatalf("claude code credential stored secret material: %+v", record)
	}
}

// Purpose: the tool bridge is a loopback listener that executes Swarm tools, so
// it must refuse callers without the per-turn bearer token and refuse tools that
// were not offered for the turn. Threats: another local process invoking Swarm
// tools through the bridge port; Claude calling a tool outside the request's
// tool set. Owner: mcpBridge.authorized / callTool. Asserts rejection and that
// the invoker never ran.
func TestMCPBridgeRejectsUnauthorizedCallersAndUnknownTools(t *testing.T) {
	invoker := &recordingInvoker{result: provideriface.ToolExecutionResult{TextForModel: "ok"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bridge, err := newMCPBridge(ctx, []provideriface.ToolDefinition{{Name: "read", Parameters: map[string]any{"type": "object"}}}, invoker, newToolUseCorrelator(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	if !strings.HasPrefix(bridge.URL(), "http://127.0.0.1:") {
		t.Fatalf("bridge must listen on loopback: %s", bridge.URL())
	}

	call := func(token string, body map[string]any) (int, map[string]any) {
		encoded, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, bridge.URL(), bytes.NewReader(encoded))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var decoded map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&decoded)
		return resp.StatusCode, decoded
	}
	toolsCall := func(name string) map[string]any {
		return map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": map[string]any{"name": name, "arguments": map[string]any{}, "_meta": map[string]any{mcpToolUseIDMetaKey: "toolu_meta"}}}
	}

	for _, token := range []string{"", "wrong-token", strings.Repeat("0", 64)} {
		if status, _ := call(token, toolsCall("read")); status != http.StatusUnauthorized {
			t.Fatalf("token %q status = %d", token, status)
		}
	}
	if status, body := call(bridge.token, toolsCall("bash")); status != http.StatusOK || body["error"] == nil {
		t.Fatalf("unknown tool response = %d %v", status, body)
	}
	if len(invoker.invocations) != 0 {
		t.Fatalf("invoker ran for rejected calls: %+v", invoker.invocations)
	}

	if status, body := call(bridge.token, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}); status != http.StatusOK {
		t.Fatalf("tools/list = %d %v", status, body)
	} else if tools := body["result"].(map[string]any)["tools"].([]any); len(tools) != 1 || tools[0].(map[string]any)["name"] != "read" {
		t.Fatalf("tools = %v", tools)
	}
	if status, body := call(bridge.token, toolsCall("read")); status != http.StatusOK || body["error"] != nil {
		t.Fatalf("authorized call = %d %v", status, body)
	}
	if len(invoker.invocations) != 1 || invoker.invocations[0].CallID != "toolu_meta" {
		t.Fatalf("invocations = %+v", invoker.invocations)
	}
}
