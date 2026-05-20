package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/identity"
)

const (
	gitRealtimeDebounce = 180 * time.Millisecond
	gitRealtimeMaxDelay = 1200 * time.Millisecond
	gitRealtimePoll     = 750 * time.Millisecond
)

type gitRealtimePayload struct {
	WorkspacePath string             `json:"workspace_path"`
	Status        gitstatus.Snapshot `json:"status"`
}

type gitRealtimeManager struct {
	server *Server
	mu     sync.Mutex
	repos  map[string]*gitRealtimeRepo
}

type gitRealtimeRepo struct {
	manager       *gitRealtimeManager
	workspacePath string
	repoRoot      string
	gitDir        string
	commonDir     string
	watchPaths    []string
	stop          chan struct{}
	stopped       chan struct{}
	wake          chan struct{}
}

func newGitRealtimeManager(server *Server) *gitRealtimeManager {
	return &gitRealtimeManager{server: server, repos: make(map[string]*gitRealtimeRepo)}
}

func (m *gitRealtimeManager) ensure(workspacePath string) error {
	if m == nil {
		return errors.New("git realtime manager is not configured")
	}
	target := gitstatus.NormalizePath(workspacePath)
	if target == "" {
		return errors.New("workspace_path is required")
	}
	m.mu.Lock()
	if _, ok := m.repos[target]; ok {
		m.mu.Unlock()
		return nil
	}
	repo, err := newGitRealtimeRepo(m, target)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.repos[target] = repo
	m.mu.Unlock()
	go repo.run()
	return nil
}

func (m *gitRealtimeManager) stopAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	repos := make([]*gitRealtimeRepo, 0, len(m.repos))
	for _, repo := range m.repos {
		repos = append(repos, repo)
	}
	m.repos = make(map[string]*gitRealtimeRepo)
	m.mu.Unlock()
	for _, repo := range repos {
		repo.stopWatching()
	}
}

func newGitRealtimeRepo(manager *gitRealtimeManager, workspacePath string) (*gitRealtimeRepo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	paths, err := gitstatus.ResolveWatchPaths(ctx, workspacePath)
	if err != nil {
		return nil, err
	}
	return &gitRealtimeRepo{
		manager:       manager,
		workspacePath: workspacePath,
		repoRoot:      paths.RepoRoot,
		gitDir:        paths.GitDir,
		commonDir:     paths.CommonDir,
		watchPaths:    gitRealtimeMetadataWatchPaths(paths),
		stop:          make(chan struct{}),
		stopped:       make(chan struct{}),
		wake:          make(chan struct{}, 1),
	}, nil
}

func (r *gitRealtimeRepo) run() {
	defer close(r.stopped)
	lastFingerprint := ""
	dirty := true
	var firstDirtyAt time.Time
	var lastSignalAt time.Time
	for {
		if dirty {
			now := time.Now()
			if firstDirtyAt.IsZero() {
				firstDirtyAt = now
			}
			wait := gitRealtimeDebounce - now.Sub(lastSignalAt)
			maxWait := gitRealtimeMaxDelay - now.Sub(firstDirtyAt)
			if maxWait < wait {
				wait = maxWait
			}
			if wait > 0 {
				select {
				case <-r.stop:
					return
				case <-r.wake:
					lastSignalAt = time.Now()
					continue
				case <-time.After(wait):
				}
			}
			lastFingerprint = r.refreshAndPublish(lastFingerprint)
			dirty = false
			firstDirtyAt = time.Time{}
		}
		select {
		case <-r.stop:
			return
		case <-r.wake:
			dirty = true
			lastSignalAt = time.Now()
		case <-time.After(gitRealtimePoll):
			fingerprint := r.watchFingerprint()
			if fingerprint != "" && fingerprint != lastFingerprint {
				dirty = true
				lastSignalAt = time.Now()
			}
		}
	}
}

func (r *gitRealtimeRepo) stopWatching() {
	if r == nil {
		return
	}
	select {
	case <-r.stopped:
		return
	default:
	}
	close(r.stop)
	<-r.stopped
}

func (r *gitRealtimeRepo) signalRefresh() {
	if r == nil {
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *gitRealtimeRepo) refreshAndPublish(previous string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	snapshot, err := gitstatus.SnapshotForPath(ctx, r.workspacePath, gitstatus.Options{RecentLimit: 8, IncludeDetails: true})
	if err != nil {
		log.Printf("git realtime snapshot failed workspace=%q err=%v", r.workspacePath, err)
		return previous
	}
	fingerprint := gitSnapshotFingerprint(snapshot)
	if fingerprint == previous {
		return fingerprint
	}
	if r.manager == nil || r.manager.server == nil || r.manager.server.events == nil || r.manager.server.hub == nil {
		return fingerprint
	}
	payload, err := json.Marshal(gitRealtimePayload{WorkspacePath: r.workspacePath, Status: snapshot})
	if err != nil {
		return fingerprint
	}
	event, err := r.manager.server.events.Append("workspace_git:"+r.workspacePath, "workspace.git.status.updated", r.workspacePath, payload, "", "")
	if err != nil {
		log.Printf("git realtime event append failed workspace=%q err=%v", r.workspacePath, err)
		return fingerprint
	}
	r.manager.server.hub.Publish(event)
	return fingerprint
}

func (r *gitRealtimeRepo) watchFingerprint() string {
	parts := make([]string, 0, len(r.watchPaths))
	for _, path := range r.watchPaths {
		clean := strings.TrimSpace(path)
		if clean == "" {
			continue
		}
		info, err := os.Stat(clean)
		if err != nil {
			parts = append(parts, clean+":missing")
			continue
		}
		parts = append(parts, clean+":"+info.ModTime().UTC().Format(time.RFC3339Nano)+":"+formatInt64(info.Size()))
	}
	return strings.Join(parts, "\n")
}

func gitRealtimeMetadataWatchPaths(paths gitstatus.WatchPaths) []string {
	candidates := []string{
		paths.RepoRoot,
		filepath.Join(paths.GitDir, "index"),
		filepath.Join(paths.GitDir, "HEAD"),
		filepath.Join(paths.GitDir, "FETCH_HEAD"),
		filepath.Join(paths.GitDir, "ORIG_HEAD"),
		filepath.Join(paths.GitDir, "MERGE_HEAD"),
		filepath.Join(paths.GitDir, "CHERRY_PICK_HEAD"),
		filepath.Join(paths.GitDir, "REBASE_HEAD"),
		filepath.Join(paths.GitDir, "packed-refs"),
		filepath.Join(paths.GitDir, "rebase-apply"),
		filepath.Join(paths.GitDir, "rebase-merge"),
	}
	for _, root := range []string{paths.GitDir, paths.CommonDir} {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		candidates = append(candidates, gitstatus.CandidateGitWatchPaths(root)...)
	}
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		clean := filepath.Clean(strings.TrimSpace(candidate))
		if clean == "." || clean == "" {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func gitSnapshotFingerprint(snapshot gitstatus.Snapshot) string {
	payload, err := json.Marshal(struct {
		Branch         string                 `json:"branch"`
		HeadOID        string                 `json:"head_oid"`
		Upstream       string                 `json:"upstream"`
		AheadCount     int                    `json:"ahead_count"`
		BehindCount    int                    `json:"behind_count"`
		StashCount     int                    `json:"stash_count"`
		DirtyCount     int                    `json:"dirty_count"`
		StagedCount    int                    `json:"staged_count"`
		ModifiedCount  int                    `json:"modified_count"`
		UntrackedCount int                    `json:"untracked_count"`
		ConflictCount  int                    `json:"conflict_count"`
		Files          []gitstatus.FileStatus `json:"files"`
	}{
		Branch:         snapshot.Branch,
		HeadOID:        snapshot.HeadOID,
		Upstream:       snapshot.Upstream,
		AheadCount:     snapshot.AheadCount,
		BehindCount:    snapshot.BehindCount,
		StashCount:     snapshot.StashCount,
		DirtyCount:     snapshot.DirtyCount,
		StagedCount:    snapshot.StagedCount,
		ModifiedCount:  snapshot.ModifiedCount,
		UntrackedCount: snapshot.UntrackedCount,
		ConflictCount:  snapshot.ConflictCount,
		Files:          snapshot.Files,
	})
	if err != nil {
		return snapshot.RefreshedAt.String()
	}
	return string(payload)
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

func (s *Server) handleGitRealtime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	workspacePath, err := s.resolveGitStatusWorkspacePath(r, principal)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.gitRealtime == nil {
		s.gitRealtime = newGitRealtimeManager(s)
	}
	if err := s.gitRealtime.ensure(workspacePath); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace_path": gitstatus.NormalizePath(workspacePath)})
}
