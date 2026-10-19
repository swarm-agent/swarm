package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"swarm/packages/swarmd/internal/identity"
)

// boxBusy is what a restart of this daemon would interrupt, across every
// account on the machine: agent runs in progress (session lifecycles),
// unfinished worker runs and sandbox environment builds. Counts only.
type boxBusy struct {
	Followups         int  `json:"followups"`
	Reviews           int  `json:"reviews"`
	Busy              bool `json:"busy"`
	Sessions          int  `json:"sessions"`
	WorkerRuns        int  `json:"worker_runs"`
	EnvironmentBuilds int  `json:"environment_builds"`
}

func (s *Server) boxBusy() (boxBusy, error) {
	var out boxBusy
	if s.sessions != nil {
		active, err := s.sessions.ListActiveLifecycles(100_000)
		if err != nil {
			return out, errors.New("read active runs: " + err.Error())
		}
		out.Sessions = len(active)
		out.Followups, out.Reviews, err = s.sessions.Store().WorkerStore().BoxExecutionCounts()
		if err != nil {
			return out, err
		}
		if out.WorkerRuns, err = s.sessions.CountUnfinishedWorkerRuns(); err != nil {
			return out, errors.New("count unfinished worker runs: " + err.Error())
		}
	}
	if s.sandboxEnvironments != nil {
		building, err := s.sandboxEnvironments.CountBuilding()
		if err != nil {
			return out, errors.New("read environment builds: " + err.Error())
		}
		out.EnvironmentBuilds = building
	}
	out.Busy = out.Sessions+out.WorkerRuns+out.EnvironmentBuilds+out.Followups+out.Reviews > 0
	return out, nil
}

// handleBoxBusy answers "may this machine restart Swarm now?" (GET
// /v3/box/busy, scope signals:read). The installer's update builds a new
// release while work runs and switches to it only when this says not busy.
func (s *Server) handleBoxBusy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if !s.requireScope(w, r, signalsReadScope) {
		return
	}
	busy, err := s.boxBusy()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, busy)
}

// boxUpdate is the installer's last word on updates (SWARM_UPDATE_STATE, a
// small file the installer writes): whether automatic updates are on, the
// branch, the build waiting to start and why, or the commit that was rolled
// back. Only these fields are passed on.
type boxUpdate struct {
	Auto      string `json:"auto,omitempty"`
	Channel   string `json:"channel,omitempty"`
	State     string `json:"state,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Running   string `json:"running,omitempty"`
	Candidate string `json:"candidate,omitempty"`
	At        int64  `json:"at,omitempty"`
}

func boxUpdateState() *boxUpdate {
	path := strings.TrimSpace(os.Getenv("SWARM_UPDATE_STATE"))
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 4096 {
		return nil
	}
	var state boxUpdate
	if json.Unmarshal(data, &state) != nil {
		return nil
	}
	clip := func(v string, n int) string {
		v = strings.TrimSpace(v)
		if len(v) > n {
			v = v[:n]
		}
		return v
	}
	state.Auto, state.Channel, state.State = clip(state.Auto, 8), clip(state.Channel, 100), clip(state.State, 32)
	state.Reason, state.Running, state.Candidate = clip(state.Reason, 300), clip(state.Running, 40), clip(state.Candidate, 40)
	return &state
}
