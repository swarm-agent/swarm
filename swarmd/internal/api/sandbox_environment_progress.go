package api

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A folder environment build's progress while it runs in this process: the
// step it is on and its log so far. It lives only as long as the build (a
// daemon restart interrupts builds, InterruptBuilds); the store keeps the
// outcome and the whole log once the build ends. Reads page the log by byte
// offset in the build's log, so nothing is cut without saying so.

// sandboxEnvironmentLogPageBytes is how much of the log one read returns.
const sandboxEnvironmentLogPageBytes = 8000

// sandboxEnvironmentProgress is one running build's progress.
type sandboxEnvironmentProgress struct {
	mu        sync.Mutex
	hash      string
	startedAt int64
	phase     string
	step      int
	steps     int
	command   string
	// log is the retained log; dropped counts bytes dropped from its start
	// to keep it within the store's bound (offsets stay absolute).
	log     string
	dropped int
}

func (p *sandboxEnvironmentProgress) appendLog(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.log += text
	if over := len(p.log) - pebblestore.MaxSandboxEnvironmentLogBytes; over > 0 {
		p.log = p.log[over:]
		p.dropped += over
	}
}

func (p *sandboxEnvironmentProgress) setStep(phase string, step, steps int, command string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase, p.step, p.steps, p.command = phase, step, steps, truncateCommandText(command, 300)
}

// snapshot is the progress view and the log so far.
func (p *sandboxEnvironmentProgress) snapshot(now time.Time) (map[string]any, string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	view := map[string]any{"phase": p.phase, "started_at": p.startedAt, "elapsed_ms": max(now.UnixMilli()-p.startedAt, 0)}
	if p.phase == "" {
		view["phase"] = "preparing"
	}
	if p.steps > 0 {
		view["setup_steps"] = p.steps
	}
	if p.step > 0 {
		view["setup_step"] = p.step
	}
	if p.command != "" {
		view["command"] = p.command
	}
	return view, p.log, p.dropped
}

func truncateCommandText(command string, limit int) string {
	command = strings.Join(strings.Fields(command), " ")
	if len(command) <= limit {
		return command
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(command[cut]) {
		cut--
	}
	return command[:cut] + "…"
}

// startSandboxEnvironmentProgress records a build starting in this process.
func (s *Server) startSandboxEnvironmentProgress(env pebblestore.SandboxEnvironment) *sandboxEnvironmentProgress {
	started := env.BuildStartedAt
	if started <= 0 {
		started = time.Now().UnixMilli()
	}
	p := &sandboxEnvironmentProgress{hash: env.Hash, startedAt: started, steps: len(env.Definition.Setup)}
	s.envBuildMu.Lock()
	defer s.envBuildMu.Unlock()
	if s.envBuildLive == nil {
		s.envBuildLive = map[string]*sandboxEnvironmentProgress{}
	}
	s.envBuildLive[env.WorkspacePath] = p
	return p
}

// endSandboxEnvironmentProgress forgets a build's progress once its outcome
// is stored.
func (s *Server) endSandboxEnvironmentProgress(root string, p *sandboxEnvironmentProgress) {
	s.envBuildMu.Lock()
	defer s.envBuildMu.Unlock()
	if s.envBuildLive[root] == p {
		delete(s.envBuildLive, root)
	}
}

func (s *Server) sandboxEnvironmentProgressFor(root, hash string) *sandboxEnvironmentProgress {
	s.envBuildMu.Lock()
	defer s.envBuildMu.Unlock()
	if p := s.envBuildLive[root]; p != nil && p.hash == hash {
		return p
	}
	return nil
}

// sandboxEnvironmentLogPage is one page of a build log: from offset (an
// absolute byte offset in the build's log; nil: the last page) up to
// sandboxEnvironmentLogPageBytes. dropped bytes before the retained log are
// named, never hidden; a page ends on a whole character.
func sandboxEnvironmentLogPage(log string, dropped int, offset *int, building bool) (string, map[string]any) {
	total := dropped + len(log)
	start := max(total-sandboxEnvironmentLogPageBytes, dropped)
	if offset != nil {
		start = min(max(*offset, dropped), total)
	}
	end := min(start+sandboxEnvironmentLogPageBytes, total)
	rel := func(abs int) int { return abs - dropped }
	for start < end && !utf8.RuneStart(log[rel(start)]) {
		start++
	}
	for end < total && end > start && !utf8.RuneStart(log[rel(end)]) {
		end--
	}
	page := map[string]any{
		"offset":      start,
		"next_offset": end,
		"total_bytes": total,
		"more":        end < total,
		"complete":    !building,
	}
	if dropped > 0 {
		page["dropped_bytes"] = dropped
		page["note"] = "the first dropped_bytes bytes of this build's log were not kept"
	}
	if offset != nil && *offset < dropped {
		page["requested_offset"] = *offset
	}
	if building {
		page["read_on"] = "the build is still running: read again from next_offset for what follows"
	} else if end < total {
		page["read_on"] = "read again with log_offset next_offset for the rest"
	}
	return log[rel(start):rel(end)], page
}

// sandboxEnvironmentDetail adds to a view what the build is doing (while it
// runs in this process), how the last build ended, and one page of the log:
// the running build's log so far, else the last build's.
func (s *Server) sandboxEnvironmentDetail(view map[string]any, env pebblestore.SandboxEnvironment, found bool, logOffset *int) map[string]any {
	if !found {
		return view
	}
	log, dropped := env.Log, env.LogDroppedBytes
	building := env.Status == pebblestore.SandboxEnvironmentBuilding
	if building {
		log, dropped = "", 0
		if p := s.sandboxEnvironmentProgressFor(env.WorkspacePath, env.Hash); p != nil {
			var progress map[string]any
			progress, log, dropped = p.snapshot(time.Now())
			view["progress"] = progress
		} else if env.BuildStartedAt > 0 {
			view["progress"] = map[string]any{"phase": "unknown", "started_at": env.BuildStartedAt}
		}
	} else if env.FinishedAt > 0 {
		last := map[string]any{"status": env.Status, "hash": env.Hash, "finished_at": env.FinishedAt}
		if env.BuildStartedAt > 0 {
			last["started_at"] = env.BuildStartedAt
			last["duration_ms"] = max(env.FinishedAt-env.BuildStartedAt, 0)
		}
		if env.Status == pebblestore.SandboxEnvironmentReady && env.ReadyHash == env.Hash {
			last["image"] = env.ReadyImage
		}
		if env.Error != "" {
			last["error"] = env.Error
		}
		view["last_build"] = last
	}
	if log != "" || dropped > 0 {
		text, page := sandboxEnvironmentLogPage(log, dropped, logOffset, building)
		view["log"], view["log_page"] = text, page
	}
	return view
}
