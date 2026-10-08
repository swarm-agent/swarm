package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/gitwatch"
)

type gitSubscriptionSelector struct {
	WorkspacePath string `json:"workspace_path"`
	SessionID     string `json:"session_id,omitempty"`
	Branch        string `json:"branch"`
}

type gitSubscriptionNotice struct {
	Index      int    `json:"index"`
	Kind       string `json:"kind"`
	ReasonCode string `json:"reason_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Push-only, bounded multiplexer. Request/transport failures are HTTP errors;
// selector failures are bounded, opaque SSE notices, never whole-stream failures.
// Authorization precedes Git inspection/allocation and every delivered notice.
// Failed lanes are retried on a new subscription, not by a Git inspection timer.
func (s *Server) handleGitSubscriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	p, ok := PrincipalFromRequest(r)
	if !ok || !p.Valid() {
		writeError(w, 401, errors.New("principal required"))
		return
	}
	if !s.requireScopeAny(w, r, "sessions:read", "projects:read") {
		return
	}
	if s.gitRealtime == nil || s.gitRealtime.subscriptions == nil {
		writeError(w, 503, errors.New("Git watcher unavailable"))
		return
	}
	var body struct {
		Repositories []gitSubscriptionSelector `json:"repositories"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || len(body.Repositories) == 0 || len(body.Repositories) > 256 {
		writeError(w, 400, errors.New("one to 256 repository selectors required"))
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		writeError(w, 500, errors.New("streaming unavailable"))
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	requests := make([]*http.Request, len(body.Repositories))
	paths := make([]string, len(body.Repositories))
	releases := make([]func(), len(body.Repositories))
	defer func() {
		for _, release := range releases {
			if release != nil {
				release()
			}
		}
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	send := func(notice gitSubscriptionNotice) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		defer controller.SetWriteDeadline(time.Time{})
		payload, _ := json.Marshal(notice)
		if _, err := w.Write(append(append([]byte("data: "), payload...), '\n', '\n')); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if err := controller.Flush(); err != nil {
		return
	}
	fail := func(index int, code, message string) bool {
		if release := releases[index]; release != nil {
			release()
			releases[index] = nil
		}
		return send(gitSubscriptionNotice{Index: index, Kind: "lost", ReasonCode: code, Error: message})
	}
	notices := make(chan gitSubscriptionNotice, 256)
	for i, selector := range body.Repositories {
		if ctx.Err() != nil {
			return
		}
		req := r.Clone(ctx)
		u := *r.URL
		u.RawQuery = url.Values{"workspace_path": {selector.WorkspacePath}, "session_id": {selector.SessionID}}.Encode()
		req.URL = &u
		path, err := s.resolveGitStatusWorkspacePath(req, p)
		if err != nil {
			// Do not distinguish foreign, absent and stale authorization records,
			// or echo resolver errors containing paths/account/session information.
			if !fail(i, "selector_unavailable", "Git repository selector is missing, stale, or not authorized; retry on reconnect") {
				return
			}
			continue
		}
		requests[i], paths[i] = req, path
		resolveCtx, resolveCancel := context.WithTimeout(ctx, 2*time.Second)
		pathsResolved, err := gitstatus.ResolveWatchPaths(resolveCtx, path)
		resolveCancel()
		if err != nil {
			if !fail(i, "repository_unavailable", "Git repository is unavailable or no longer a worktree; retry on reconnect") {
				return
			}
			continue
		}
		ch, release, err := s.gitRealtime.subscriptions.Acquire(gitwatch.Config{WorktreeRoot: pathsResolved.RepoRoot, GitDir: pathsResolved.GitDir, CommonDir: pathsResolved.CommonDir}, selector.Branch)
		if err != nil {
			code, message := "watch_unavailable", "Git watch could not be acquired; retry on reconnect"
			if errors.Is(err, gitwatch.ErrSubscriptionCapacity) {
				code, message = "watch_capacity", "Git watch capacity reached; retry after other subscriptions are released"
			}
			if !fail(i, code, message) {
				return
			}
			continue
		}
		laneCtx, stop := context.WithCancel(ctx)
		releases[i] = func() { stop(); release() }
		go func(index int, changes <-chan gitwatch.Notice) {
			for {
				select {
				case <-laneCtx.Done():
					return
				case notice, open := <-changes:
					if !open {
						return
					}
					select {
					case notices <- gitSubscriptionNotice{Index: index, Kind: notice.Kind}:
					case <-laneCtx.Done():
						return
					}
				}
			}
		}(i, ch)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case notice := <-notices:
			if releases[notice.Index] == nil {
				continue // discard queued facts after authorization was lost
			}
			resolved, err := s.resolveGitStatusWorkspacePath(requests[notice.Index], p)
			if err != nil || resolved != paths[notice.Index] {
				if !fail(notice.Index, "selector_unavailable", "Git repository selector is missing, stale, or not authorized; retry on reconnect") {
					return
				}
				continue
			}
			if !send(notice) {
				return
			}
		}
	}
}
