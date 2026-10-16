package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/privacy"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Confirmed client tools. A client tool whose definition requires confirmation
// (pebblestore.ClientToolRequiresConfirmation: write tools by default) is never
// handed to the client when the model calls it. Instead:
//
//  1. The call is validated (the tool's schema plus the summary argument Swarm
//     adds), the session's permission policy is applied, and a confirmation is
//     stored in the session's metadata under ClientToolConfirmationMetadataKey
//     with the tool, the arguments (without summary), their SHA-256 hash, the
//     summary and an expiry. The model's call returns "awaiting confirmation"
//     and the run ends without another model call.
//  2. The person's decision (the API's confirmations route) approves or rejects
//     it, or it expires. Approval turns it into an ordinary pending client tool
//     call with exactly the stored arguments, which the client answers.
//  3. The outcome (the client's result or failure, the decline, or "no answer")
//     is appended to the conversation as a tool result and a new run lets the
//     model react to it.
//
// A session holds at most one open confirmation. All state is durable session
// metadata written through ApplySessionMutation, so it survives a restart.

const ClientToolConfirmationMetadataKey = "client_tool_confirmation"

const (
	ClientToolConfirmationPending  = "pending"
	ClientToolConfirmationApproved = "approved"
	ClientToolConfirmationRejected = "rejected"
	ClientToolConfirmationExpired  = "expired"
)

const clientToolConfirmationPathID = "tool.client.confirmation.v1"

// ClientToolConfirmation is the durable record of one confirmed call.
type ClientToolConfirmation struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Tool   string `json:"tool"`
	// Summary is the model's one-sentence description for the person.
	Summary string `json:"summary"`
	// Arguments are what the client will receive on approval, bound by
	// ArgumentsHash ("sha256:<hex>" of their canonical JSON).
	Arguments     map[string]any `json:"arguments"`
	ArgumentsHash string         `json:"arguments_hash"`
	RunID         string         `json:"run_id"`
	CallID        string         `json:"call_id"`
	CreatedAt     int64          `json:"created_at"`
	ExpiresAt     int64          `json:"expires_at"`
	DecidedAt     int64          `json:"decided_at,omitempty"`
	Note          string         `json:"note,omitempty"`
	// ExecutionCallID is the call ID of the approved call the client answers
	// and of the outcome the model sees.
	ExecutionCallID       string `json:"execution_call_id"`
	ExecutionPermissionID string `json:"execution_permission_id,omitempty"`
	ExecutionDeadline     int64  `json:"execution_deadline,omitempty"`
	// Delivered is set once the outcome is in the conversation.
	Delivered         bool   `json:"delivered,omitempty"`
	DeliveredAt       int64  `json:"delivered_at,omitempty"`
	OutcomeMessageID  string `json:"outcome_message_id,omitempty"`
	ContinuationRunID string `json:"continuation_run_id,omitempty"`
}

// Open reports whether the confirmation still blocks a new one.
func (c ClientToolConfirmation) Open() bool {
	return c.ID != "" && !c.Delivered
}

// ClientToolConfirmationFromSession returns the session's latest confirmation.
func ClientToolConfirmationFromSession(session pebblestore.SessionSnapshot) (ClientToolConfirmation, bool) {
	raw, ok := session.Metadata[ClientToolConfirmationMetadataKey]
	if !ok || raw == nil {
		return ClientToolConfirmation{}, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return ClientToolConfirmation{}, false
	}
	var out ClientToolConfirmation
	if err := json.Unmarshal(encoded, &out); err != nil || strings.TrimSpace(out.ID) == "" {
		return ClientToolConfirmation{}, false
	}
	return out, true
}

// ClientToolArgumentsHash returns the canonical JSON of args and its hash.
// encoding/json writes map keys in sorted order, so equal arguments always
// produce the same bytes.
func ClientToolArgumentsHash(args map[string]any) (string, string, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return string(raw), "sha256:" + hex.EncodeToString(sum[:]), nil
}

// VerifyArguments reports an error unless the stored arguments still hash to
// ArgumentsHash.
func (c ClientToolConfirmation) VerifyArguments() (string, error) {
	canonical, hash, err := ClientToolArgumentsHash(c.Arguments)
	if err != nil {
		return "", err
	}
	if hash != c.ArgumentsHash {
		return "", errors.New("confirmation arguments do not match their hash")
	}
	return canonical, nil
}

// A fixed set of striped locks: bounded memory however many sessions exist.
var clientToolConfirmationLocks [64]sync.Mutex

// LockClientToolConfirmations serializes every change to one session's
// confirmation (recording, deciding, expiring, delivering).
func LockClientToolConfirmations(sessionID string) func() {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(strings.TrimSpace(sessionID)))
	mu := &clientToolConfirmationLocks[hash.Sum32()%uint32(len(clientToolConfirmationLocks))]
	mu.Lock()
	return mu.Unlock
}

// PutClientToolConfirmation stores c in session's metadata. Each transition
// has its own idempotency key, so a retried write replays.
func PutClientToolConfirmation(apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error), session pebblestore.SessionSnapshot, c ClientToolConfirmation, now int64) error {
	if apply == nil {
		return errors.New("client tool confirmations require a V3 session mutation path")
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		return err
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return err
	}
	next := session
	next.Metadata = cloneGenericMap(session.Metadata)
	if next.Metadata == nil {
		next.Metadata = map[string]any{}
	}
	next.Metadata[ClientToolConfirmationMetadataKey] = value
	next.UpdatedAt = now
	transition := c.Status
	if c.ExecutionPermissionID != "" {
		transition += ":executing"
	}
	if c.Delivered {
		transition += ":delivered"
	}
	requestID := "client-tool-confirmation:" + c.ID + ":" + transition
	sum := sha256.Sum256(append([]byte(session.ID+"\x00"+requestID+"\x00"), encoded...))
	payloadHash := hex.EncodeToString(sum[:])
	eventPayload, err := json.Marshal(map[string]any{"session_id": session.ID, "metadata": next.Metadata, "client_tool_confirmation": value, "updated_at": now})
	if err != nil {
		return err
	}
	_, err = apply(sessionruntime.SessionMutationInput{
		SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
		ClientRequestID: requestID, IdempotencyKey: requestID, PayloadHash: payloadHash, RequestHash: payloadHash,
		Kind: sessionruntime.SessionMutationUpdateMetadata, EventPayload: eventPayload, Session: &next, NowUnixMs: now,
	})
	return err
}

func clientToolConfirmationID(sessionID, runID, callID string, now int64) string {
	seed := sessionID + "\x00" + runID + "\x00" + callID
	if strings.TrimSpace(callID) == "" {
		seed += fmt.Sprintf("\x00%d", now)
	}
	sum := sha256.Sum256([]byte(seed))
	return "conf_" + hex.EncodeToString(sum[:12])
}

// requestClientToolConfirmation handles a model's call to a confirmed client
// tool: it records the confirmation and returns the "awaiting confirmation"
// result. Nothing reaches the client until the person approves.
func (s *Service) requestClientToolConfirmation(config providerToolInvokerConfig, sessionID string, call tool.Call, definition pebblestore.AgentCustomToolDefinition) tool.Result {
	result := tool.Result{CallID: strings.TrimSpace(call.CallID), Name: strings.TrimSpace(call.Name)}
	fail := func(status, message string) tool.Result {
		result.Output = permissionOutputPayload(false, status, message, call.Name, call.Arguments)
		result.Error = message
		return result
	}
	args, summary, err := agentruntime.ValidateConfirmedClientToolArguments(definition.InputSchema, call.Arguments)
	if err != nil {
		return fail("error", err.Error())
	}
	if config.applySessionMutation == nil || !config.providerManagedV3 {
		return fail("error", "this tool needs the person's confirmation, which only V3 sessions support")
	}
	if s.sessions == nil || s.permissions == nil {
		return fail("error", "client tool confirmation is not configured")
	}
	unlock := LockClientToolConfirmations(sessionID)
	defer unlock()
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return fail("error", "session not found")
	}
	explain, err := s.permissions.ExplainToolForAccount(session.AccountScopeID, config.sessionMode, canonicalToolName(call.Name), strings.TrimSpace(call.Arguments), config.policy)
	if err != nil {
		return fail("error", "permission authorization failed")
	}
	if explain.Decision == permission.PolicyDecisionDeny {
		reason := strings.TrimSpace(explain.Reason)
		if reason == "" {
			reason = "permission denied"
		}
		return fail("denied", reason)
	}
	if existing, found := ClientToolConfirmationFromSession(session); found && existing.Open() {
		return fail("error", "another action in this conversation is waiting for the person's confirmation; wait for its outcome before asking for another")
	}
	// Client tool records store arguments privacy-sanitized; confirm exactly
	// what the client will receive.
	canonical, _, err := ClientToolArgumentsHash(args)
	if err != nil {
		return fail("error", "arguments could not be encoded")
	}
	args = nil
	if err := json.Unmarshal([]byte(privacy.SanitizeJSONText(canonical)), &args); err != nil || args == nil {
		return fail("error", "arguments could not be encoded")
	}
	_, hash, err := ClientToolArgumentsHash(args)
	if err != nil {
		return fail("error", "arguments could not be encoded")
	}
	now := time.Now().UnixMilli()
	id := clientToolConfirmationID(sessionID, config.runID, call.CallID, now)
	confirmation := ClientToolConfirmation{
		ID: id, Kind: "confirmation", Status: ClientToolConfirmationPending, Tool: canonicalToolName(definition.Name),
		Summary: summary, Arguments: args, ArgumentsHash: hash, RunID: strings.TrimSpace(config.runID), CallID: strings.TrimSpace(call.CallID),
		CreatedAt: now, ExpiresAt: now + int64(pebblestore.ClientToolConfirmTimeoutMS(definition)),
		ExecutionCallID: "confirmed_" + strings.TrimPrefix(id, "conf_"),
	}
	if err := PutClientToolConfirmation(config.applySessionMutation, session, confirmation, now); err != nil {
		return fail("error", "the confirmation could not be recorded")
	}
	encoded, err := json.Marshal(map[string]any{
		"tool":            confirmation.Tool,
		"status":          "awaiting_confirmation",
		"confirmation_id": confirmation.ID,
		"summary":         summary,
		"message":         "The person was asked to approve this. Nothing has happened yet. The outcome (approved and done, declined, or no answer) arrives as a separate result of this tool.",
		"path_id":         clientToolConfirmationPathID,
	})
	if err != nil {
		return fail("error", "the confirmation could not be recorded")
	}
	result.Output = string(encoded)
	return result
}

// ClientToolConfirmationOutcome is what the model sees once a confirmation is
// settled. execution is the approved call's permission record, when there is
// one. It returns the tool output and, for anything but success, the error.
func ClientToolConfirmationOutcome(definition pebblestore.AgentCustomToolDefinition, c ClientToolConfirmation, execution *pebblestore.PermissionRecord) (string, string) {
	if strings.TrimSpace(definition.Name) == "" {
		definition = pebblestore.AgentCustomToolDefinition{Name: c.Tool, Kind: pebblestore.AgentCustomToolKindClient}
	}
	failWith := func(message string) (string, string) {
		return withConfirmationStatus(clientToolErrorOutput(definition, message), c.Status), message
	}
	switch c.Status {
	case ClientToolConfirmationRejected:
		message := "the person declined"
		if note := strings.TrimSpace(c.Note); note != "" {
			message += ": " + note
		}
		return failWith(message)
	case ClientToolConfirmationExpired:
		return failWith("no answer: the person did not confirm in time, so nothing was done")
	case ClientToolConfirmationApproved:
	default:
		return failWith("the confirmation ended in an unknown state")
	}
	if execution == nil {
		return failWith("the person approved, but the tool is no longer available, so nothing was done")
	}
	switch strings.ToLower(strings.TrimSpace(execution.Status)) {
	case pebblestore.PermissionStatusApproved:
		output, err := clientToolOutput(definition, execution.ApprovedArguments)
		if err != nil {
			return failWith(err.Error())
		}
		return withConfirmationStatus(output, c.Status), ""
	case pebblestore.PermissionStatusDenied:
		reason := strings.TrimSpace(execution.Reason)
		if reason == "" {
			reason = "client tool failed"
		}
		return failWith(reason)
	default:
		return failWith("the approved call was cancelled before it ran")
	}
}

func withConfirmationStatus(output, status string) string {
	var payload map[string]any
	if json.Unmarshal([]byte(output), &payload) != nil || payload == nil {
		return output
	}
	payload["confirmation"] = status
	encoded, err := json.Marshal(payload)
	if err != nil {
		return output
	}
	return string(encoded)
}

// ClientToolConfirmationOutcomeRecord renders the outcome as a durable tool
// message: the confirmed call (same tool, the model's arguments) and its
// result, under ExecutionCallID so it pairs with no other call.
func ClientToolConfirmationOutcomeRecord(c ClientToolConfirmation, runID, output, errText string) (string, map[string]any, error) {
	modelArgs := map[string]any{}
	for key, value := range c.Arguments {
		modelArgs[key] = value
	}
	modelArgs[agentruntime.ClientToolSummaryArgument] = c.Summary
	rawArgs, err := json.Marshal(modelArgs)
	if err != nil {
		return "", nil, err
	}
	call := tool.Call{CallID: c.ExecutionCallID, Name: c.Tool, Arguments: string(rawArgs)}
	metadata := map[string]any{
		"run_id":                      strings.TrimSpace(runID),
		"step":                        1,
		"step_id":                     providerManagedV3ToolStepID(1),
		"tool_call_id":                c.ExecutionCallID,
		"call_id":                     c.ExecutionCallID,
		"tool_instance_id":            providerManagedV3ToolInstanceID(1, c.ExecutionCallID),
		"tool_name":                   c.Tool,
		"executor_kind":               "v3_provider_tool",
		"client_tool_confirmation_id": c.ID,
		"confirmation_status":         c.Status,
	}
	if errText = strings.TrimSpace(errText); errText != "" {
		metadata["error"] = errText
	}
	content, err := formatV3ProviderManagedToolResultRecord(call, metadata, tool.Result{CallID: c.ExecutionCallID, Name: c.Tool, Output: output, Error: errText})
	if err != nil {
		return "", nil, err
	}
	return content, metadata, nil
}

// ClientToolTimeout is how long an approved call waits for the client.
func ClientToolTimeout(definition pebblestore.AgentCustomToolDefinition) time.Duration {
	return clientToolTimeout(definition)
}

// clientToolConfirmationRequired reports whether a model's call to name in
// sessionID must wait for the person's confirmation.
func (s *Service) clientToolConfirmationRequired(sessionID, name string) (pebblestore.AgentCustomToolDefinition, bool) {
	definition, ok := s.clientToolForSession(sessionID, name)
	if !ok || !pebblestore.ClientToolRequiresConfirmation(definition) {
		return pebblestore.AgentCustomToolDefinition{}, false
	}
	return definition, true
}
