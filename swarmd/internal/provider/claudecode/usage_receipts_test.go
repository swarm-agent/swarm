package claudecode

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// streamRequest renders one provider request as Claude Code stream-json: the
// partial message_start/message_delta pair, then the assistant line the CLI
// repeats once per content block.
func streamRequest(id string, input, read, write, output int) string {
	start := `{"type":"stream_event","event":{"type":"message_start","message":{"id":"` + id + `","model":"claude-test[1m]","usage":{"input_tokens":` + strconv.Itoa(input) + `,"cache_read_input_tokens":` + strconv.Itoa(read) + `,"cache_creation_input_tokens":` + strconv.Itoa(write) + `,"output_tokens":1}}}}`
	delta := `{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":` + strconv.Itoa(output) + `}}}`
	return start + "\n" + delta + "\n" + assistantLine(id, input, read, write, output) + "\n" + assistantLine(id, input, read, write, output) + "\n"
}

func assistantLine(id string, input, read, write, output int) string {
	return `{"type":"assistant","message":{"id":"` + id + `","model":"claude-test","usage":{"input_tokens":` + strconv.Itoa(input) + `,"cache_read_input_tokens":` + strconv.Itoa(read) + `,"cache_creation_input_tokens":` + strconv.Itoa(write) + `,"output_tokens":` + strconv.Itoa(output) + `},"content":[{"type":"text","text":"part"}]}}`
}

// useStreamScript replaces the fixture CLI with one that prints the slot's
// recorded stream verbatim, as the real CLI prints stream-json.
func useStreamScript(t *testing.T, r *Runner) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "claude")
	body := `#!/bin/sh
case "$1" in
 --version) printf '2.1.248\n'; exit 0;;
 auth) printf '{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"max"}\n'; exit 0;;
esac
IFS= read -r first
cat "$CLAUDE_CONFIG_DIR/stream"
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	r.cli.lookupEnv = func(key string) (string, bool) {
		if key == CLIPathEnv {
			return script, true
		}
		return "", false
	}
}

// Purpose: Swarm must record one usage receipt per Claude Code provider
// request, attributed to the account slot that served it, with the turn's
// total equal to the sum of the requests (not the last request, which
// undercounted real consumption by ~25-50x). Runner.runAccounts over a real
// subprocess emitting stream-json is the narrowest layer that proves parsing,
// per-message dedupe and slot attribution together. The negative case: the
// CLI repeats each assistant message per content block and may report a
// request only on its assistant line; neither may add or lose a receipt.
func TestClaudeCodeRecordsOneReceiptPerProviderRequest(t *testing.T) {
	r, ctx, _, a, _ := accountFixture(t)
	useStreamScript(t, r)
	stream := streamRequest("msg_1", 10, 1000, 200, 50) +
		streamRequest("msg_2", 5, 1200, 0, 300) +
		// The third request appears only through its (repeated) assistant line.
		assistantLine("msg_3", 3, 1500, 0, 700) + "\n" + assistantLine("msg_3", 3, 1500, 0, 700) + "\n" +
		`{"type":"result","subtype":"success","result":"done","num_turns":3,"usage":{"input_tokens":18,"output_tokens":1050}}` + "\n"
	if err := os.WriteFile(filepath.Join(r.cli.slotsRoot, a, "stream"), []byte(stream), 0600); err != nil {
		t.Fatal(err)
	}
	response, err := r.CreateResponseStreaming(ctx, provideriface.Request{SessionID: "s", Model: "claude-test", Input: []map[string]any{{"role": "user", "content": "go"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipts := pebblestore.UsageRequestReceiptsFromHistory(response.Usage.APIUsageHistory, 1)
	if len(receipts) != 3 {
		t.Fatalf("receipts = %+v, want 3", receipts)
	}
	want := []pebblestore.SessionUsageRequestReceipt{
		{Step: 1, Model: "claude-test", AccountID: a, MessageID: "msg_1", InputTokens: 10, CacheReadTokens: 1000, CacheWriteTokens: 200, OutputTokens: 50, TotalTokens: 1260},
		{Step: 1, Model: "claude-test", AccountID: a, MessageID: "msg_2", InputTokens: 5, CacheReadTokens: 1200, OutputTokens: 300, TotalTokens: 1505},
		{Step: 1, Model: "claude-test", AccountID: a, MessageID: "msg_3", InputTokens: 3, CacheReadTokens: 1500, OutputTokens: 700, TotalTokens: 2203},
	}
	var sum int64
	for i, receipt := range receipts {
		if receipt != want[i] {
			t.Fatalf("receipt %d = %+v, want %+v", i, receipt, want[i])
		}
		sum += receipt.TotalTokens
	}
	usage := response.Usage
	if usage.TotalTokens != sum || usage.InputTokens != 18 || usage.CacheReadTokens != 3700 || usage.CacheWriteTokens != 200 || usage.OutputTokens != 1050 {
		t.Fatalf("turn usage = %+v, want the sum of %d receipts (%d tokens)", usage, len(receipts), sum)
	}
	if usage.Source != usageSource || usage.APIUsageRawPath != usageRawPath {
		t.Fatalf("usage provenance = %q/%q", usage.Source, usage.APIUsageRawPath)
	}
}

// Purpose: a request spent on an account that then hits its limit is real
// consumption and stays attributed to that slot; the retried turn's requests
// belong to the slot that served them. Prevents both dropping the rejected
// attempt's receipts and re-attributing them to the serving account.
func TestClaudeCodeFailoverReceiptsKeepTheirAccountSlot(t *testing.T) {
	r, ctx, _, a, b := accountFixture(t)
	useStreamScript(t, r)
	rejected := streamRequest("msg_a1", 7, 900, 0, 20) + `{"type":"result","is_error":true,"result":"You've hit your session limit · resets 1pm (UTC)"}` + "\n"
	served := streamRequest("msg_b1", 11, 0, 900, 30) + streamRequest("msg_b2", 2, 910, 0, 40) + `{"type":"result","subtype":"success","result":"done"}` + "\n"
	if err := os.WriteFile(filepath.Join(r.cli.slotsRoot, a, "stream"), []byte(rejected), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.cli.slotsRoot, b, "stream"), []byte(served), 0600); err != nil {
		t.Fatal(err)
	}
	response, err := r.CreateResponseStreaming(ctx, provideriface.Request{SessionID: "s", Model: "claude-test", Input: []map[string]any{{"role": "user", "content": "go"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipts := pebblestore.UsageRequestReceiptsFromHistory(response.Usage.APIUsageHistory, 2)
	got := make([]string, 0, len(receipts))
	var sum int64
	for _, receipt := range receipts {
		got = append(got, receipt.MessageID+"@"+receipt.AccountID)
		sum += receipt.TotalTokens
	}
	if strings.Join(got, ",") != "msg_a1@"+a+",msg_b1@"+b+",msg_b2@"+b {
		t.Fatalf("receipts = %v", got)
	}
	if response.Usage.TotalTokens != sum || sum != 927+941+952 {
		t.Fatalf("total = %d, receipt sum = %d", response.Usage.TotalTokens, sum)
	}
}

// Purpose: a turn whose requests were never observed reports the CLI's own
// aggregate but no request receipts; receipts are never invented.
func TestClaudeCodeUnobservedRequestsYieldNoReceipts(t *testing.T) {
	state := newTurnState("claude-test", newToolUseCorrelator())
	state.handleLine([]byte(`{"type":"result","subtype":"success","result":"done","usage":{"input_tokens":4,"output_tokens":6}}`), nil)
	usage := state.response().Usage
	if usage.TotalTokens != 10 {
		t.Fatalf("aggregate usage = %+v", usage)
	}
	if receipts := pebblestore.UsageRequestReceiptsFromHistory(usage.APIUsageHistory, 1); len(receipts) != 0 {
		t.Fatalf("receipts = %+v, want none", receipts)
	}
}
