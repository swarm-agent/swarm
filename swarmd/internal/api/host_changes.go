package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/hostchange"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Host changes: the daemon cannot see the person's folders (it runs as its
// own locked-down user), so whoever sets the machine up (an AI with a scoped
// token holding host:request, or the owner) records a request for exactly
// one folder; only the machine owner approves or denies it; an approval asks
// the installer-placed helper to make that folder visible. Scoped tokens, and
// so every agent and AI key, can never approve.

// ScopeHostRequest lets a scoped token request (and list) host changes.
const ScopeHostRequest = "host:request"

const (
	hostChangesPath               = "/v1/host/changes"
	defaultHostChangeRestartDelay = 500 * time.Millisecond
	hostChangeUnsupportedCode     = "host_changes_unsupported"
)

// hostChangeHelper is the client of the host-change helper socket.
type hostChangeHelper interface {
	Supported() bool
	Run(ctx context.Context, op, path string) (hostchange.Result, error)
}

func (s *Server) hostChangesSupported() bool {
	return s.hostChangeHelper != nil && s.hostChangeHelper.Supported()
}

func (s *Server) handleHostChanges(w http.ResponseWriter, r *http.Request) {
	if s.hostChanges == nil {
		writeError(w, http.StatusInternalServerError, errors.New("host changes are not configured"))
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, hostChangesPath), "/")
	switch {
	case rest == "":
		s.handleHostChangeCollection(w, r)
	case strings.HasSuffix(rest, "/approve"):
		s.handleHostChangeDecision(w, r, strings.TrimSuffix(rest, "/approve"), true)
	case strings.HasSuffix(rest, "/deny"):
		s.handleHostChangeDecision(w, r, strings.TrimSuffix(rest, "/deny"), false)
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown host change route"))
	}
}

func (s *Server) handleHostChangeCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !s.requireScopeAny(w, r, ScopeHostRequest, "swarm:read") {
			return
		}
		principal, ok := PrincipalFromRequest(r)
		if !ok || !principal.Valid() {
			writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
			return
		}
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		if status != "" && !pebblestore.ValidHostChangeStatus(status) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("unknown status %q", status))
			return
		}
		changes, err := s.hostChanges.List(strings.TrimSpace(principal.AccountScopeID), status)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "supported": s.hostChangesSupported(), "changes": changes})
	case http.MethodPost:
		if !s.requireScope(w, r, ScopeHostRequest) {
			return
		}
		principal, ok := PrincipalFromRequest(r)
		if !ok || !principal.Valid() {
			writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
			return
		}
		if !s.hostChangesSupported() {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "code": hostChangeUnsupportedCode, "error": hostchange.ErrUnsupported.Error()})
			return
		}
		var req struct {
			Op     string `json:"op"`
			Path   string `json:"path"`
			Reason string `json:"reason"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !pebblestore.ValidHostChangeOp(req.Op) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("op must be one of %s", strings.Join(pebblestore.HostChangeOps(), ", ")))
			return
		}
		path, err := validateHostChangePath(req.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if len([]rune(strings.TrimSpace(req.Reason))) > pebblestore.MaxHostChangeReasonChars {
			writeError(w, http.StatusBadRequest, fmt.Errorf("reason is limited to %d characters", pebblestore.MaxHostChangeReasonChars))
			return
		}
		account := strings.TrimSpace(principal.AccountScopeID)
		if existing, found, err := s.hostChanges.FindPending(account, req.Op, path); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		} else if found {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "change": existing})
			return
		}
		change, err := s.hostChanges.Create(account, req.Op, path, req.Reason, hostChangeRequester(r, principal))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "change": change})
	default:
		methodNotAllowed(w)
	}
}

// hostChangeRequester labels who asked: the scoped token's name, else the
// owner principal.
func hostChangeRequester(r *http.Request, principal identity.Principal) string {
	if rec, scoped := ScopedTokenFromRequest(r); scoped {
		label := strings.TrimSpace(rec.Name)
		if label == "" {
			label = strings.TrimSpace(rec.ID)
		}
		return "token:" + label
	}
	return "owner:" + strings.TrimSpace(principal.UserID)
}

// validateHostChangePath admits one absolute, clean folder path that is not
// Swarm's own storage or the daemon user's credentials. It is the path the
// helper will bind into the daemon's view; it need not exist from here (that
// is the point) and is never resolved through symlinks the daemon cannot see.
func validateHostChangePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is required")
	}
	if len(path) > pebblestore.MaxHostChangePathBytes {
		return "", fmt.Errorf("path is limited to %d bytes", pebblestore.MaxHostChangePathBytes)
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	if strings.ContainsAny(path, "\x00\n\r") {
		return "", errors.New("path contains unsupported characters")
	}
	for _, segment := range strings.Split(path, string(filepath.Separator)) {
		if segment == ".." {
			return "", errors.New("path must not contain ..")
		}
	}
	if clean := filepath.Clean(path); clean != path {
		return "", fmt.Errorf("path must be clean; use %q", clean)
	}
	if path == string(filepath.Separator) {
		return "", errors.New("path must name a folder, not the filesystem root")
	}
	if err := tool.RefuseProtectedPath(path); err != nil {
		return "", err
	}
	return path, nil
}

// handleHostChangeDecision is owner-only: approving runs the helper.
func (s *Server) handleHostChangeDecision(w http.ResponseWriter, r *http.Request, id string, approve bool) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, scoped := ScopedTokenFromRequest(r); scoped {
		writeError(w, http.StatusForbidden, errors.New("deciding host changes requires the machine owner"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	id = strings.TrimSpace(id)
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, pebblestore.ErrHostChangeNotFound)
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	account := strings.TrimSpace(principal.AccountScopeID)
	if !approve {
		change, err := s.hostChanges.Deny(account, id, req.Reason)
		if err != nil {
			writeHostChangeTransitionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "change": change})
		return
	}
	if !s.hostChangesSupported() {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "code": hostChangeUnsupportedCode, "error": hostchange.ErrUnsupported.Error()})
		return
	}
	change, err := s.hostChanges.MarkApplying(account, id)
	if err != nil {
		writeHostChangeTransitionError(w, err)
		return
	}
	change, restart := s.applyHostChange(r.Context(), account, change)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "change": change})
	if restart {
		// The applied record and this reply are already durable and written;
		// the restart then drops this process under its new unit settings.
		s.scheduleHostRestart(change)
	}
}

// applyHostChange runs the helper for an applying record and persists the
// outcome. restart reports that the helper asked for a daemon restart, which
// the caller performs only after replying.
func (s *Server) applyHostChange(ctx context.Context, account string, change pebblestore.HostChange) (pebblestore.HostChange, bool) {
	result, err := s.hostChangeHelper.Run(context.WithoutCancel(ctx), change.Op, change.Path)
	var updated pebblestore.HostChange
	var finishErr error
	switch {
	case err != nil:
		updated, finishErr = s.hostChanges.Finish(account, change.ID, false, "", err.Error())
	case !result.OK:
		updated, finishErr = s.hostChanges.Finish(account, change.ID, false, result.Raw, result.Error)
	default:
		updated, finishErr = s.hostChanges.Finish(account, change.ID, true, result.Raw, "")
	}
	if finishErr != nil {
		log.Printf("warning: host change %s: record outcome: %v", change.ID, finishErr)
		return change, false
	}
	restart := err == nil && result.OK && result.RestartRequired &&
		(change.Op == pebblestore.HostChangeOpMountWorkspace || change.Op == pebblestore.HostChangeOpUnmountWorkspace)
	return updated, restart
}

func (s *Server) scheduleHostRestart(change pebblestore.HostChange) {
	helper := s.hostChangeHelper
	delay := s.hostChangeRestartDelay
	if delay <= 0 {
		delay = defaultHostChangeRestartDelay
	}
	go func() {
		time.Sleep(delay)
		log.Printf("host change %s applied (%s %s); asking the host-change helper to restart the daemon", change.ID, change.Op, change.Path)
		if result, err := helper.Run(context.Background(), hostchange.OpRestartDaemon, ""); err != nil {
			log.Printf("warning: host change %s: restart-daemon: %v", change.ID, err)
		} else if !result.OK {
			log.Printf("warning: host change %s: restart-daemon refused: %s", change.ID, result.Error)
		}
	}()
}

func writeHostChangeTransitionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pebblestore.ErrHostChangeNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, pebblestore.ErrHostChangeNotPending):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}
