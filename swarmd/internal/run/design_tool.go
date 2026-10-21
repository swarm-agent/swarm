package run

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/design"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// errDesignerManagedOutputRemoved refuses every request for the retired
// managed (Artifact V3) Designer output.
var errDesignerManagedOutputRemoved = errors.New("Designer managed artifact output was removed: Designer makes and edits HTML designs with swarm_design in its workspace; omit output_mode (or use workspace) and give an owned_scope")

// errDesignerIterationSwarmReplaced refuses Designer Iteration Swarms.
var errDesignerIterationSwarmReplaced = errors.New("task Designer Iteration Swarms were replaced by swarm_design: call swarm_design with count (and variations) for several designs, or launch one Designer with the brief")

// SetDesignChecker installs the browser check in-session swarm_design calls
// use; the daemon passes the same checker its design jobs use.
func (s *Service) SetDesignChecker(checker design.Checker) {
	if s != nil {
		s.designChecker = checker
	}
}

type swarmDesignToolArgs struct {
	Brief      string   `json:"brief"`
	Count      int      `json:"count"`
	Style      string   `json:"style"`
	Variations []string `json:"variations"`
	BatchName  string   `json:"batch_name"`
	Folder     string   `json:"folder"`
	EditPath   string   `json:"edit_path"`
}

const maxSwarmDesignArgumentBytes = 1 << 20

// parseSwarmDesignToolArgs validates the tool arguments before anything is
// read or spent. Folder is the parent folder; the batch folder is added later.
func parseSwarmDesignToolArgs(raw string) (swarmDesignToolArgs, design.Request, error) {
	var in swarmDesignToolArgs
	if len(raw) > maxSwarmDesignArgumentBytes {
		return in, design.Request{}, errors.New("swarm_design arguments are larger than 1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(strings.TrimSpace(raw))))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, design.Request{}, fmt.Errorf("invalid swarm_design arguments: %w", err)
	}
	in.Brief, in.Style, in.BatchName = strings.TrimSpace(in.Brief), strings.TrimSpace(in.Style), strings.TrimSpace(in.BatchName)
	in.EditPath = strings.TrimSpace(in.EditPath)
	if folder := strings.TrimSpace(in.Folder); folder != "" {
		if in.Folder = strings.TrimRight(folder, "/"); in.Folder == "" {
			return in, design.Request{}, errors.New("folder must be a relative path inside the workspace")
		}
	}
	if utf8.RuneCountInString(in.BatchName) > 200 {
		return in, design.Request{}, errors.New("batch_name is longer than 200 characters")
	}
	if in.Count == 0 {
		in.Count = 1
	}
	if in.EditPath != "" {
		if in.Folder != "" || in.BatchName != "" || len(in.Variations) > 0 {
			return in, design.Request{}, errors.New("an edit changes edit_path in place: folder, batch_name and variations do not apply")
		}
		if !pebblestore.ValidDesignRelativePath(in.EditPath) {
			return in, design.Request{}, errors.New("edit_path must be a relative path inside the workspace (no '..', no absolute path)")
		}
	} else {
		if in.Folder == "" {
			in.Folder = design.DefaultFolder
		}
		if !pebblestore.ValidDesignRelativePath(in.Folder) {
			return in, design.Request{}, errors.New("folder must be a relative path inside the workspace (no '..', no absolute path)")
		}
	}
	req := design.Request{Brief: in.Brief, Count: in.Count, Style: in.Style, Variations: in.Variations, Folder: in.Folder, EditPath: in.EditPath}
	if err := design.Validate(req); err != nil {
		return in, design.Request{}, err
	}
	return in, req, nil
}

var swarmDesignCallIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// swarmDesignJobID makes a unique design job id from the call id and returns
// its short suffix for the batch folder.
func swarmDesignJobID(callID string) (string, string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	suffix := hex.EncodeToString(b)[:6]
	base := strings.Trim(swarmDesignCallIDUnsafe.ReplaceAllString(strings.TrimSpace(callID), "_"), "_")
	if len(base) > 64 {
		base = base[:64]
	}
	if base == "" {
		base = "call"
	}
	return "swarm_design-" + base + "-" + suffix, suffix, nil
}

// swarmDesignOwnedScopeAllows reports whether rel lies inside one of the
// declared owned scopes ("." or no scope allows everything).
func swarmDesignOwnedScopeAllows(scopes []string, rel string) bool {
	if len(scopes) == 0 {
		return true
	}
	for _, scope := range scopes {
		scope = strings.Trim(strings.TrimSpace(scope), "/")
		if scope == "" || scope == "." || rel == scope || strings.HasPrefix(rel, scope+"/") {
			return true
		}
	}
	return false
}

// executeSwarmDesignTool runs the design pipeline synchronously inside the
// calling run, over the calling session's workspace or worktree. Model calls
// use the account's Designer model and record usage on the calling session.
func (s *Service) executeSwarmDesignTool(ctx context.Context, sessionID string, call tool.Call) (string, error) {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return "", errors.New("swarm_design is not available: run service is not configured")
	}
	if s.designChecker == nil {
		return "", errors.New("swarm_design is not available: no design checker is configured on this daemon")
	}
	in, req, err := parseSwarmDesignToolArgs(call.Arguments)
	if err != nil {
		return "", err
	}
	sessionID = strings.TrimSpace(sessionID)
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	if _, managed := session.Metadata[tool.ManagedExecutionMetadataKey]; managed {
		return "", errors.New("swarm_design cannot run in a session with a managed execution binding: designs are written by the daemon into the session's own workspace, not the managed checkout")
	}
	principal, _ := identity.PrincipalFromContext(ctx)
	if principal.Valid() && (principal.AccountScopeID != session.AccountScopeID || principal.UserID != session.UserID) {
		return "", errors.New("swarm_design principal does not own the calling session")
	}
	principal, err = principalForRunWorkspaceScope(session, principal)
	if err != nil {
		return "", err
	}
	scope, err := s.resolveRunWorkspaceScope(session, principal)
	if err != nil {
		return "", fmt.Errorf("swarm_design workspace: %w", err)
	}
	root := strings.TrimSpace(scope.PrimaryPath)
	if root == "" {
		return "", errors.New("swarm_design needs a workspace or worktree; this session has none")
	}
	jobID, suffix, err := swarmDesignJobID(call.CallID)
	if err != nil {
		return "", err
	}
	resultsPath := ""
	target := req.EditPath
	if req.Mode() == design.ModeCreate {
		name := in.BatchName
		if name == "" {
			name = in.Brief
		}
		req.Folder = path.Join(in.Folder, design.BatchSlug(name)+"-"+suffix)
		resultsPath = path.Join(req.Folder, design.ResultsFile)
		target = req.Folder
	}
	if agentruntime.IsDesignerAgentName(mapString(session.Metadata, "requested_subagent")) && !swarmDesignOwnedScopeAllows(mapStringSlice(session.Metadata, "owned_scope"), target) {
		return "", fmt.Errorf("swarm_design target %q is outside this Designer's owned_scope %v", target, mapStringSlice(session.Metadata, "owned_scope"))
	}
	files, err := tool.NewDesignFiles(root, resultsPath)
	if err != nil {
		return "", err
	}
	if req.Mode() == design.ModeEdit {
		if _, _, err := files.Read(ctx, req.EditPath); err != nil {
			return "", fmt.Errorf("edit_path must be an existing HTML file inside the workspace: %w", err)
		}
	} else if err := files.CheckFolder(ctx, req.Folder); err != nil {
		return "", err
	}
	model, err := s.DesignModel(principal, sessionID, jobID)
	if err != nil {
		return "", err
	}
	// A worker-owned session under a budget may hold only one unsettled
	// operation, so its designs run one call at a time and every call
	// reserves and settles its own operation (designModel.Complete).
	budgeted, err := s.sessions.Store().WorkerBudgetActive(session.AccountScopeID, sessionID)
	if err != nil {
		return "", fmt.Errorf("check the worker budget: %w", err)
	}
	concurrency := design.DefaultConcurrency
	if budgeted {
		concurrency = 1
	}
	runCtx := withWorkerBudget(ctx, s.sessions.Store(), session.AccountScopeID, sessionID)
	pipeline := design.Pipeline{Model: model, Checker: s.designChecker, Files: files, Concurrency: concurrency}
	batch, err := pipeline.Run(runCtx, req)
	if err != nil {
		return "", err
	}
	return marshalSwarmDesignResult(batch, resultsPath)
}

type swarmDesignItemSummary struct {
	N       int    `json:"n"`
	Path    string `json:"path,omitempty"`
	Status  string `json:"status"`
	Finding string `json:"finding,omitempty"`
}

// marshalSwarmDesignResult returns the compact result the calling agent sees;
// results.json keeps the full record.
func marshalSwarmDesignResult(batch design.BatchResult, resultsPath string) (string, error) {
	items := make([]swarmDesignItemSummary, 0, len(batch.Items))
	for _, item := range batch.Items {
		items = append(items, swarmDesignItemSummary{N: item.Index, Path: item.Path, Status: string(item.Status), Finding: swarmDesignFirstFinding(item)})
	}
	counts := make(map[string]int, len(batch.Counts))
	for status, n := range batch.Counts {
		counts[string(status)] = n
	}
	out := map[string]any{
		"tool":   tool.SwarmDesignToolName,
		"mode":   batch.Mode,
		"model":  batch.Model,
		"counts": counts,
		"items":  items,
		"totals": batch.Totals,
	}
	if batch.Mode == design.ModeEdit {
		out["edit_path"] = batch.EditPath
	} else {
		out["folder"] = batch.Folder
		out["results_path"] = resultsPath
	}
	if batch.ResultsError != "" {
		out["results_error"] = batch.ResultsError
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// swarmDesignFirstFinding is the first problem worth reporting for one design:
// its error, a refused repair, or the first diagnostic of its last check.
func swarmDesignFirstFinding(item design.ItemResult) string {
	if item.Error != "" {
		return truncateRunes(item.Error, 300)
	}
	if item.Repair != nil && item.Repair.Refused != "" {
		return truncateRunes("repair refused: "+item.Repair.Refused, 300)
	}
	check := item.CheckAfter
	if check == nil {
		check = item.CheckBefore
	}
	if check == nil {
		return ""
	}
	if check.Status == design.CheckUnavailable {
		if check.Code != "" {
			return "browser check unavailable: " + check.Code
		}
		return "browser check unavailable"
	}
	if len(check.Diagnostics) == 0 {
		return ""
	}
	d := check.Diagnostics[0]
	text := d.Kind + ": " + d.Message
	if d.Line > 0 {
		text += fmt.Sprintf(" (line %d)", d.Line)
	}
	return truncateRunes(text, 300)
}
