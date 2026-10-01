package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// There is intentionally no output, source-content, account or child identity input.
// Source hydration and execution are separate trusted adapters, not model tools.
type designToolArgs struct {
	Files            []DesignFileReference             `json:"files,omitempty"`
	Action           string                            `json:"action"`
	IdempotencyKey   string                            `json:"idempotency_key,omitempty"`
	Candidates       []pebblestore.DesignCandidateSpec `json:"candidates,omitempty"`
	RequestID        string                            `json:"request_id,omitempty"`
	ArtifactID       string                            `json:"artifact_id,omitempty"`
	Ref              *pebblestore.DesignRef            `json:"ref,omitempty"`
	ExpectedCurrent  *pebblestore.DesignRef            `json:"expected_current,omitempty"`
	ExpectedVersion  uint64                            `json:"expected_version,omitempty"`
	ExpectedRevision uint64                            `json:"expected_revision,omitempty"`
	Candidate        int                               `json:"candidate,omitempty"`
	After            uint64                            `json:"after,omitempty"`
	Limit            int                               `json:"limit,omitempty"`
	MaxBytes         int                               `json:"max_bytes,omitempty"`
}

func parseDesignToolArgs(args map[string]any) (designToolArgs, error) {
	var in designToolArgs
	allowed := map[string]string{
		"submit":  " action idempotency_key candidates files ",
		"status":  " action request_id ",
		"history": " action artifact_id after limit ",
		"read":    " action ref max_bytes ",
		"select":  " action idempotency_key ref expected_current expected_version ",
		"cancel":  " action idempotency_key request_id expected_revision candidate ",
	}
	action, ok := args["action"].(string)
	fields, valid := allowed[action]
	if !ok || !valid {
		return in, pebblestore.ErrDesignInvalid
	}
	required := map[string][]string{"submit": {"idempotency_key", "candidates"}, "status": {"request_id"}, "history": {"artifact_id"}, "read": {"ref"}, "select": {"idempotency_key", "ref", "expected_current", "expected_version"}, "cancel": {"idempotency_key", "request_id", "expected_revision", "candidate"}}
	for _, key := range required[action] {
		v, present := args[key]
		if !present || (v == nil && key != "expected_current") {
			return in, pebblestore.ErrDesignInvalid
		}
	}
	for key := range args {
		if !bytes.Contains([]byte(fields), []byte(" "+key+" ")) {
			return in, fmt.Errorf("manage_design: unexpected field %q", key)
		}
	}
	b, err := json.Marshal(args)
	if err != nil {
		return in, err
	}
	if len(b) > 600000 {
		return in, pebblestore.ErrDesignInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&in); err != nil {
		return in, err
	}
	return in, nil
}

func designStableID(parts ...string) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return "design-" + hex.EncodeToString(h[:])
}

func (r *Runtime) executeManageDesign(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	in, err := parseDesignToolArgs(args)
	if err != nil {
		return "", err
	}
	service, ok := r.sessions.(interface{ DesignStore() *pebblestore.Store })
	if !ok || service.DesignStore() == nil {
		return "", errors.New("manage_design store unavailable")
	}
	s := service.DesignStore()
	p := pebblestore.DesignPrincipal{AccountID: scope.Principal.AccountScopeID, PrincipalID: scope.Principal.UserID}
	if !scope.Principal.Valid() || p.AccountID == "" || p.PrincipalID == "" {
		return "", pebblestore.ErrDesignInvalid
	}
	parent, found, err := r.sessions.GetSession(scope.SessionID)
	if err != nil {
		return "", err
	}
	if !found || parent.AccountScopeID != p.AccountID || parent.UserID != p.PrincipalID {
		return "", pebblestore.ErrDesignNotFound
	}
	var result any
	switch in.Action {
	case "submit":
		run, trusted := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext)
		if !trusted || run.RunID == "" || run.SessionID != scope.SessionID || run.ChildSessionID != "" || in.IdempotencyKey == "" {
			return "", pebblestore.ErrDesignInvalid
		}
		id := designStableID(p.AccountID, p.PrincipalID, scope.SessionID, in.IdempotencyKey)
		for i := range in.Candidates {
			c := &in.Candidates[i]
			if c.Operation == pebblestore.DesignGenerate {
				if c.ArtifactID != "" {
					return "", pebblestore.ErrDesignInvalid
				}
				c.ArtifactID = designStableID(id, fmt.Sprint(i))
			}
		}
		snapshots, sourceErr := hydrateDesignSources(ctx, scope, in.Files)
		if sourceErr != nil {
			return "", sourceErr
		}
		submit := pebblestore.DesignSubmit{RequestID: id, IdempotencyKey: id, ParentSessionID: scope.SessionID, ParentRunID: run.RunID, Candidates: in.Candidates, Context: snapshots}
		_, err = r.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{SessionID: scope.SessionID, UserID: p.PrincipalID, AccountScopeID: p.AccountID, Kind: pebblestore.V3SessionMutationAcceptDesign, EventType: "design.accepted", IdempotencyKey: id, ClientRequestID: id, PayloadHash: pebblestore.DesignAcceptanceHash(submit), DesignAcceptance: &pebblestore.DesignAcceptance{Submit: submit}})
		if err == nil {
			result, err = s.GetDesignRequest(p, id)
		}
	case "status":
		result, err = s.GetDesignRequest(p, in.RequestID)
	case "history":
		if in.Limit == 0 {
			in.Limit = 20
		}
		var revisions []pebblestore.DesignRevision
		revisions, err = s.DesignHistory(p, in.ArtifactID, in.After, in.Limit)
		if err == nil {
			var artifact pebblestore.DesignArtifact
			artifact, err = s.GetDesignArtifact(p, in.ArtifactID)
			result = map[string]any{"artifact": artifact, "revisions": revisions}
		}
	case "read":
		if in.Ref == nil {
			return "", pebblestore.ErrDesignInvalid
		}
		if in.MaxBytes == 0 {
			in.MaxBytes = 32768
		}
		if in.MaxBytes < 1 || in.MaxBytes > 65536 {
			return "", pebblestore.ErrDesignInvalid
		}
		var revision pebblestore.DesignRevision
		revision, err = s.ReadDesignRevision(p, *in.Ref)
		if err == nil {
			if len(revision.Content) > in.MaxBytes {
				return "", errors.New("design output exceeds max_bytes; no partial document returned")
			}
			result = map[string]any{"ref": revision.Ref, "kind": revision.Kind, "content": string(revision.Content)}
		}
	case "select":
		if in.Ref == nil {
			return "", pebblestore.ErrDesignInvalid
		}
		result, err = s.SelectDesignRevision(p, pebblestore.DesignSelection{IdempotencyKey: in.IdempotencyKey, Ref: *in.Ref, ExpectedCurrent: in.ExpectedCurrent, ExpectedVersion: in.ExpectedVersion})
	case "cancel":
		var request pebblestore.DesignRequest
		request, err = s.GetDesignRequest(p, in.RequestID)
		if err != nil {
			return "", err
		}
		if in.Candidate < 0 || in.Candidate >= len(request.Candidates) {
			return "", pebblestore.ErrDesignInvalid
		}
		mutation := pebblestore.DesignAttemptMutation{IdempotencyKey: in.IdempotencyKey, ExpectedRevision: in.ExpectedRevision, Candidate: in.Candidate, State: pebblestore.DesignCancelRequested}
		c := request.Candidates[in.Candidate]
		if len(c.Attempts) > 0 {
			a := c.Attempts[len(c.Attempts)-1]
			mutation.ChildSessionID, mutation.RunID = a.ChildSessionID, a.RunID
		}
		// A running cancellation remains durably pending until the execution
		// adapter observes canonical child cancellation. Never fake that outcome.
		result, err = s.RecordDesignAttempt(p, in.RequestID, mutation)
	}
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(result)
	return string(b), err
}

func manageDesignDefinition() Definition {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer", "minimum": 0} }
	ref := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"artifact_id", "revision", "sha256"}, "properties": map[string]any{"artifact_id": str(), "revision": integer(), "sha256": str()}}
	candidate := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"kind", "operation", "brief"}, "properties": map[string]any{"artifact_id": str(), "kind": map[string]any{"type": "string", "enum": []string{"html", "plan"}}, "operation": map[string]any{"type": "string", "enum": []string{"generate", "edit"}}, "brief": str(), "base": ref, "plan_source": ref}}
	file := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"path"}, "properties": map[string]any{"path": str(), "line_start": integer(), "line_end": integer()}}
	return Definition{Type: "function", Name: "manage_design", Description: "Queue a delegated Designer request: one design or an explicit batch of 1–8 HTML/plan candidates. Generation omits artifact_id; edits require artifact_id and exact base. Optional files are shared immutable untrusted source snapshots, at most 32 files and 256 KiB total; inclusive line_start/line_end must both be supplied, at most 2000 lines. Source reads require separate sensitive-read permission. Changed snapshots on retry conflict. HTML generation may supply plan_source as an exact retained plan ref. Accepted requests execute asynchronously in independent Designer runs; status reports outcomes and model availability alerts. Successful siblings remain retained if another fails. No authored output input. Plans never execute automatically. status/history omit bytes; read is bounded to 64 KiB. cancel targets one candidate with request revision CAS. select uses exact expected_current and expected_version. Existing Artifact V3 operations remain separate.", Parameters: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"action"}, "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"submit", "status", "history", "read", "select", "cancel"}}, "idempotency_key": str(), "files": map[string]any{"type": "array", "maxItems": 32, "items": file}, "candidates": map[string]any{"type": "array", "minItems": 1, "maxItems": 8, "items": candidate}, "request_id": str(), "artifact_id": str(), "ref": ref, "expected_current": map[string]any{"anyOf": []any{ref, map[string]any{"type": "null"}}}, "expected_version": integer(), "expected_revision": integer(), "candidate": integer(), "after": integer(), "limit": integer(), "max_bytes": integer()}}}
}
