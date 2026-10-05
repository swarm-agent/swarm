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
	Index int    `json:"index"`
	Kind  string `json:"kind"`
}

// Push-only, bounded multiplexer. Unlike the compatibility realtime long poll,
// this route never reads snapshots or periodically invokes Git. Authorization is
// checked before watcher allocation and again before every delivered notice.
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
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	requests := make([]*http.Request, len(body.Repositories))
	paths := make([]string, len(body.Repositories))
	// Authorize the whole request first: foreign selectors allocate no watchers.
	for i, selector := range body.Repositories {
		req := r.Clone(ctx)
		u := *r.URL
		u.RawQuery = url.Values{"workspace_path": {selector.WorkspacePath}, "session_id": {selector.SessionID}}.Encode()
		req.URL = &u
		path, err := s.resolveGitStatusWorkspacePath(req, p)
		if err != nil {
			writeError(w, 403, err)
			return
		}
		requests[i], paths[i] = req, path
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, errors.New("streaming unavailable"))
		return
	}
	notices := make(chan gitSubscriptionNotice, 256)
	for i, selector := range body.Repositories {
		resolveCtx, resolveCancel := context.WithTimeout(ctx, 2*time.Second)
		pathsResolved, err := gitstatus.ResolveWatchPaths(resolveCtx, paths[i])
		resolveCancel()
		if err != nil {
			writeError(w, 400, err)
			return
		}
		ch, release, err := s.gitRealtime.subscriptions.Acquire(gitwatch.Config{WorktreeRoot: pathsResolved.RepoRoot, GitDir: pathsResolved.GitDir, CommonDir: pathsResolved.CommonDir}, selector.Branch)
		if err != nil {
			writeError(w, 503, err)
			return
		}
		defer release()
		go func(index int, changes <-chan gitwatch.Notice) {
			for {
				select {
				case <-ctx.Done():
					return
				case notice := <-changes:
					select {
					case notices <- gitSubscriptionNotice{Index: index, Kind: notice.Kind}:
					case <-ctx.Done():
						return
					}
				}
			}
		}(i, ch)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	for {
		select {
		case <-ctx.Done():
			return
		case notice := <-notices:
			resolved, err := s.resolveGitStatusWorkspacePath(requests[notice.Index], p)
			if err != nil || resolved != paths[notice.Index] {
				return
			}
			// Set only a write deadline; an idle stream has no heartbeat/Git timer.
			controller := http.NewResponseController(w)
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			payload, _ := json.Marshal(notice)
			if _, err := w.Write(append(append([]byte("data: "), payload...), '\n', '\n')); err != nil {
				return
			}
			flusher.Flush()
			_ = controller.SetWriteDeadline(time.Time{})
		}
	}
}
