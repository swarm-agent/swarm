package api

import (
	"net/http"
	"os"
	"sort"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

type permissionsDevice struct {
	store.DevicePermissionRule
	ID         string `json:"id"`
	Name       string `json:"name"`
	ThisDevice bool   `json:"this_device"`
	Home       bool   `json:"home"`
	Connected  bool   `json:"connected"`
}
type permissionsLink struct {
	store.LinkedPermissionRule
	Name        string `json:"name"`
	Reachable   bool   `json:"reachable"`
	LastContact int64  `json:"last_contact"`
}
type permissionsView struct {
	Revision uint64 `json:"revision"`
	Home     struct {
		Name         string `json:"name"`
		Tasks        string `json:"tasks"`
		WaitingTasks int    `json:"waiting_tasks"`
	} `json:"home"`
	Devices []permissionsDevice `json:"devices"`
	Linked  []permissionsLink   `json:"linked"`
}

// Connected means an authenticated request in the last two minutes, not a
// guarantee of a running client executor. Browser attachment is not observable.
func (s *Server) accountPermissionsView(r *http.Request, p store.AccountPermissions) (permissionsView, error) {
	principal, _ := PrincipalFromRequest(r)
	actor, _ := productActorFromRequest(r)
	v := permissionsView{Revision: p.Revision, Devices: []permissionsDevice{}, Linked: []permissionsLink{}}
	var err error
	v.Home.Name, err = os.Hostname()
	if err != nil {
		return v, err
	}
	v.Home.Tasks = p.Tasks
	v.Home.WaitingTasks, err = s.sessions.Store().Underlying().CountPermissionWaitingTasks(principal.AccountScopeID)
	if err != nil {
		return v, err
	}
	clients, err := s.identitySessions.ListClientSessions()
	if err != nil {
		return v, err
	}
	now := time.Now().UnixMilli()
	for _, c := range clients {
		if c.AccountScopeID != principal.AccountScopeID || c.Device == "" || c.RevokedAt != 0 || c.ExpiresAt <= now {
			continue
		}
		v.Devices = append(v.Devices, permissionsDevice{DevicePermissionRule: p.Device(c.SessionID), ID: c.SessionID, Name: c.Device, ThisDevice: actor.ClientSessionID == c.SessionID, Home: c.Device == v.Home.Name, Connected: c.LastSeenAt > 0 && c.LastSeenAt >= now-120000})
	}
	if s.fleet != nil && s.fleetTokens != nil {
		machines, err := s.fleet.ListMachines()
		if err != nil {
			return v, err
		}
		for _, m := range machines {
			_, owned, err := s.fleetTokens.GetFleetLinkToken(principal.AccountScopeID, m.Name)
			if err != nil {
				return v, err
			}
			if owned {
				v.Linked = append(v.Linked, permissionsLink{LinkedPermissionRule: p.Link(m.Name), Name: m.Name, Reachable: m.LastContactAt > 0 && m.LastError == "", LastContact: m.LastContactAt})
			}
		}
	}
	sort.Slice(v.Devices, func(i, j int) bool { return v.Devices[i].ID < v.Devices[j].ID })
	sort.Slice(v.Linked, func(i, j int) bool { return v.Linked[i].Name < v.Linked[j].Name })
	return v, nil
}
func (s *Server) writeAccountPermissions(w http.ResponseWriter, r *http.Request, status int, p store.AccountPermissions) {
	v, err := s.accountPermissionsView(r, p)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if status == http.StatusConflict {
		writeJSON(w, status, map[string]any{"error": store.ErrAccountPermissionsConflict.Error(), "current": v})
		return
	}
	writeJSON(w, status, v)
}
