package claudecode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// SetSessionMCP connects the runner to the daemon's standing session endpoint.
func (r *Runner) SetSessionMCP(mcp *SessionMCP) {
	r.mcp = mcp
}

var errCLIResumeUnavailable = errors.New("Claude Code session cannot be resumed")

func resumeRefused(detail string) bool {
	detail = strings.ToLower(detail)
	return strings.Contains(detail, "no conversation found with session id") || strings.Contains(detail, "session not found") || strings.Contains(detail, "failed to resume session")
}

// conversationBoundaries are the reasons the V3 session executor gives its own
// conversation turns. Any other reason (titles, routers, design or metadata
// one-shots) is a side request: it must never resume, record or share the
// conversation's CLI session, even when it carries the conversation's SessionID.
var conversationBoundaries = map[string]bool{
	"session_turn":                   true,
	"checkpoint_fresh_context":       true,
	"epoch_fresh_context":            true,
	"provider_model_runtime_handoff": true,
	"context_compaction_plan_guard":  true,
	"restart_after_tool":             true,
}

// conversationTurn reports whether req is a turn of a V3 conversation whose
// CLI session may persist. It needs the session, execution epoch and provider
// lineage the executor assigns; everything else runs stateless.
func conversationTurn(req provideriface.Request) bool {
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.ExecutionEpochID) == "" || strings.TrimSpace(req.ProviderLineageID) == "" {
		return false
	}
	reason := strings.TrimSpace(req.BoundaryReason)
	if reason == "" {
		return false
	}
	for _, part := range strings.Split(reason, "+") {
		if !conversationBoundaries[strings.TrimSpace(part)] {
			return false
		}
	}
	return true
}

// cliContinuation is the provider-owned record of the CLI session that holds a
// conversation's context: which epoch and lineage it belongs to, and a digest
// of the exact input it was seeded or last resumed with.
type cliContinuation struct {
	ExecutionEpochID  string `json:"execution_epoch_id"`
	ProviderLineageID string `json:"provider_lineage_id"`
	CLISessionID      string `json:"cli_session_id"`
	InputItems        int    `json:"input_items"`
	InputDigest       string `json:"input_digest"`
}

func inputDigest(input []map[string]any) string {
	raw, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validCLISessionID(id string) bool {
	return strings.TrimSpace(id) != "" && len(id) <= 256 && !strings.ContainsAny(id, "/\\\r\n\x00")
}

func continuationPath(dir string) string { return filepath.Join(dir, "continuation.json") }

func loadContinuation(dir string) (cliContinuation, bool, error) {
	raw, err := readPrivateFile(continuationPath(dir))
	if os.IsNotExist(err) {
		return cliContinuation{}, false, nil
	}
	if err != nil {
		return cliContinuation{}, false, err
	}
	var record cliContinuation
	if err = json.Unmarshal(raw, &record); err != nil || !validCLISessionID(record.CLISessionID) {
		// An unreadable record only loses resume; the next turn reseeds.
		return cliContinuation{}, false, nil
	}
	return record, true, nil
}

func saveContinuation(dir string, record cliContinuation) error {
	if !validCLISessionID(record.CLISessionID) {
		return errors.New("invalid Claude Code session id")
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if existing, err := readPrivateFile(continuationPath(dir)); err == nil && string(existing) == string(raw) {
		return nil
	}
	return writePrivateFile(continuationPath(dir), raw)
}

// resumable reports whether the recorded CLI session still holds exactly the
// context this request continues. Swarm's boundaries (compaction, checkpoint
// or plan-guard fresh context, bounded history, restart after a tool, provider
// or model handoff) change the epoch, the lineage, the boundary reason or the
// input prefix, and each of those starts a fresh, fully seeded CLI session.
func resumable(req provideriface.Request, record cliContinuation) bool {
	if strings.TrimSpace(req.BoundaryReason) != "session_turn" {
		return false
	}
	if previous := strings.TrimSpace(req.PreviousProviderLineageID); previous != "" && previous != req.ProviderLineageID {
		return false
	}
	if record.ExecutionEpochID != req.ExecutionEpochID || record.ProviderLineageID != req.ProviderLineageID {
		return false
	}
	if record.InputItems <= 0 || record.InputItems >= len(req.Input) || record.InputDigest == "" {
		return false
	}
	return inputDigest(req.Input[:record.InputItems]) == record.InputDigest
}

// resumedInput selects what the CLI has not seen: the items after the recorded
// prefix, without the assistant messages, tool calls and tool results the CLI
// produced itself.
func resumedInput(input []map[string]any, start int) []map[string]any {
	var result []map[string]any
	for _, item := range input[start:] {
		switch strings.ToLower(strings.TrimSpace(stringField(item, "type"))) {
		case "function_call", "function_call_output", "reasoning":
			continue
		}
		if strings.EqualFold(strings.TrimSpace(stringField(item, "role")), "assistant") {
			continue
		}
		result = append(result, item)
	}
	return result
}
