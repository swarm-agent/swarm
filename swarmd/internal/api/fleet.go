package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// The fleet: Swarms linked one way. A machine invites a home with a one-time
// code (made only on the machine, over its owner socket: sudo swarmctl fleet
// invite); the home redeems it and gets an owner session on that machine,
// which it sends with every call over the machine's link listener, where
// TLS pins both keys. The machine gets nothing that reaches the home. A home
// uses the machine's own API, the same one any client of Swarm uses.
//
// Routes, owner only:
//
//	GET    /v3/fleet                         machines, homes, open invites, listener
//	POST   /v3/fleet/invites                 a link code (owner socket only)
//	DELETE /v3/fleet/invites/{id}            cancel an unused code
//	DELETE /v3/fleet/homes/{fingerprint}     end a home's control of this Swarm
//	POST   /v3/fleet/machines                link with a code
//	GET    /v3/fleet/machines/{name}         check the link (the machine answers)
//	DELETE /v3/fleet/machines/{name}         unlink (the machine is told)
//	*      /v3/fleet/machines/{name}/api/... the machine's API, as its home
//
// Read like /v3/activity (sessions:read; fleet_activity.go):
//
//	GET    /v3/fleet/activity[/stream]       the fleet's merged activity
//
// Fleet keys, the fleet's provider key manager: fleet_keys.go.

const fleetPath = "/v3/fleet"

type fleetTokenStore interface {
	PutFleetLinkToken(accountScopeID, machine, token string) error
	GetFleetLinkToken(accountScopeID, machine string) (string, bool, error)
	DeleteFleetLinkToken(accountScopeID, machine string) error
	// Fleet keys' values, sealed the same way (fleet_keys.go).
	PutFleetKeyValue(accountScopeID, id string, value []byte) error
	GetFleetKeyValue(accountScopeID, id string) ([]byte, bool, error)
	DeleteFleetKeyValue(accountScopeID, id string) error
}

// SetFleet wires the fleet's records and the sealed store for the sessions
// machines give this home.
func (s *Server) SetFleet(store *pebblestore.FleetStore, tokens fleetTokenStore) {
	if s == nil {
		return
	}
	s.fleet = store
	s.fleetTokens = tokens
}

func (s *Server) registerFleetRoutes(mux *http.ServeMux) {
	mux.HandleFunc(fleetPath, s.ownerOnlyScope(s.handleFleet))
	mux.HandleFunc(fleetPath+"/", s.ownerOnlyScope(s.handleFleet))
	// The fleet's activity reads like /v3/activity (sessions:read), not as an owner route.
	mux.HandleFunc(fleetActivityPagePath, s.handleFleetActivity)
	mux.HandleFunc(fleetActivityStreamPath, s.handleFleetActivity)
	// Fleet keys authorize per route (fleet_keys.go): reads like the activity,
	// writes for owners and swarm:approve, and the machine side only over the link.
	mux.HandleFunc(fleetKeysPath, s.handleFleetKeys)
	mux.HandleFunc(fleetKeysPath+"/", s.handleFleetKeys)
}

// swarmName is this Swarm's own name, the one the person gave it at setup
// (install.sh --name, or onboarding's swarm_name).
func (s *Server) swarmName() string {
	if cfg, err := s.loadStartupConfig(); err == nil {
		if name := strings.TrimSpace(cfg.SwarmName); name != "" {
			return name
		}
	}
	if node, found, _ := s.swarmLocalNode(); found {
		return strings.TrimSpace(node.Name)
	}
	return ""
}

// fleetName is this Swarm's name in the fleet: its own name, written the
// way a machine name is (lowercase letters, digits and dashes).
func (s *Server) fleetName() string {
	return fleet.NameFrom(s.swarmName())
}

func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil || s.fleetTokens == nil {
		writeError(w, http.StatusInternalServerError, errors.New("the fleet is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, fleetPath), "/")
	parts := strings.SplitN(rest, "/", 4)
	switch {
	case rest == "" && r.Method == http.MethodGet:
		s.handleFleetList(w)
	case rest == "invites" && r.Method == http.MethodPost:
		s.handleFleetInvite(w, r)
	case len(parts) == 2 && parts[0] == "invites" && r.Method == http.MethodDelete:
		s.handleFleetInviteCancel(w, parts[1])
	case len(parts) == 2 && parts[0] == "homes" && r.Method == http.MethodDelete:
		s.handleFleetHomeDrop(w, parts[1])
	case rest == "machines" && r.Method == http.MethodPost:
		s.handleFleetLink(w, r, principal.AccountScopeID)
	case len(parts) == 2 && parts[0] == "machines" && r.Method == http.MethodGet:
		s.handleFleetCheck(w, r, principal.AccountScopeID, parts[1])
	case len(parts) == 2 && parts[0] == "machines" && r.Method == http.MethodDelete:
		s.handleFleetUnlink(w, r, principal.AccountScopeID, parts[1])
	case len(parts) >= 3 && parts[0] == "machines" && parts[2] == "api":
		path := "/"
		if len(parts) == 4 {
			path += parts[3]
		}
		s.handleFleetForward(w, r, principal.AccountScopeID, parts[1], path)
	case rest == "" || rest == "invites" || rest == "machines" || (len(parts) == 2 && (parts[0] == "invites" || parts[0] == "homes" || parts[0] == "machines")):
		methodNotAllowed(w)
	default:
		writeError(w, http.StatusNotFound, errors.New("no such fleet route"))
	}
}

type fleetInviteView struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	Name      string `json:"name,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Server) handleFleetList(w http.ResponseWriter) {
	machines, err := s.fleet.ListMachines()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	homes, err := s.fleet.ListHomes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	invites, err := s.fleet.ListInvites()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	open := []fleetInviteView{}
	for _, invite := range invites {
		if invite.UsedAt == 0 && invite.CancelledAt == 0 {
			open = append(open, fleetInviteView{ID: invite.ID, Address: invite.Address, Name: invite.Name, CreatedAt: invite.CreatedAt})
		}
	}
	node, _, _ := s.swarmLocalNode()
	writeJSON(w, http.StatusOK, map[string]any{
		"name":        s.fleetName(),
		"swarm_name":  s.swarmName(),
		"fingerprint": node.Fingerprint,
		"machines":    machines,
		"homes":       homes,
		"invites":     open,
		"listen":      s.fleetListenAddress(),
	})
}

// handleFleetInvite makes a link code. Only on the machine itself (the owner
// socket): whoever redeems a code becomes an owner here, so a leaked client
// session must not be able to make one.
func (s *Server) handleFleetInvite(w http.ResponseWriter, r *http.Request) {
	if !isLocalTransportRequest(r) {
		writeError(w, http.StatusForbidden, errors.New("a link code is made on the machine: sudo swarmctl fleet invite ADDRESS"))
		return
	}
	var req struct {
		Address string `json:"address"`
		Name    string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid link invite request"))
		return
	}
	address := strings.TrimSpace(req.Address)
	if address == "" {
		address = s.fleetListenAddress()
	}
	if address == "" {
		writeError(w, http.StatusBadRequest, errors.New("give the address homes reach this Swarm on (its tailnet or LAN IP)"))
		return
	}
	address, err := fleet.ListenAddress(address)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	node, found, err := s.swarmLocalNode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found || node.Fingerprint == "" {
		writeError(w, http.StatusConflict, errors.New("this Swarm has no node key yet"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = s.fleetName()
	}
	if !fleet.ValidName(name) {
		writeError(w, http.StatusBadRequest, errors.New("this Swarm has no name to link by: give it one (its setup, or --name NAME)"))
		return
	}
	listening, err := s.listenForHomes(address)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.fleet.PutListen(pebblestore.FleetListen{Address: listening}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	id, secret, err := fleet.NewSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	invite := pebblestore.FleetInvite{ID: id, SecretHash: fleet.HashSecret(secret), Address: listening, Name: name, CreatedAt: time.Now().UnixMilli()}
	if err := s.fleet.PutInvite(invite); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	code, err := fleet.Invite{Address: listening, Fingerprint: node.Fingerprint, ID: id, Secret: secret, Name: name}.Encode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	log.Printf("swarmd fleet: link code %s made, listening on %s", id, listening)
	writeJSON(w, http.StatusCreated, map[string]any{
		"code":   code,
		"invite": fleetInviteView{ID: id, Address: listening, Name: name, CreatedAt: invite.CreatedAt},
	})
}

func (s *Server) handleFleetInviteCancel(w http.ResponseWriter, id string) {
	invite, found, err := s.fleet.GetInvite(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("no link code with that id"))
		return
	}
	if invite.UsedAt != 0 {
		writeError(w, http.StatusConflict, errors.New("that code was used: end the home's control instead (DELETE /v3/fleet/homes/{fingerprint})"))
		return
	}
	if invite.CancelledAt == 0 {
		invite.CancelledAt = time.Now().UnixMilli()
		if err := s.fleet.PutInvite(invite); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	s.stopListeningIfUnused()
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": true, "id": invite.ID})
}

// handleFleetHomeDrop ends a home's control of this Swarm: its session is
// revoked (it stops at once) and its key is forgotten.
func (s *Server) handleFleetHomeDrop(w http.ResponseWriter, fingerprint string) {
	home, found, err := s.fleet.GetHome(fingerprint)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("no home with that key"))
		return
	}
	if err := s.endFleetHome(home); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.afterFleetHomeLeft()
	log.Printf("swarmd fleet: home %s (%s) dropped by the owner", home.Name, short16(home.Fingerprint))
	writeJSON(w, http.StatusOK, map[string]any{"dropped": true, "home": home})
}

func (s *Server) endFleetHome(home pebblestore.FleetHome) error {
	if s.identitySessions != nil {
		if _, err := s.identitySessions.RevokeClientSession(home.SessionID); err != nil && !errors.Is(err, identity.ErrClientSessionNotFound) {
			return err
		}
	}
	return s.fleet.DeleteHome(home.Fingerprint)
}

// handleFleetLink redeems a machine's link code: this Swarm becomes its home.
func (s *Server) handleFleetLink(w http.ResponseWriter, r *http.Request, account string) {
	var req struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid link request"))
		return
	}
	invite, err := fleet.Decode(req.Code)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = invite.Name
	}
	if !fleet.ValidName(name) {
		writeError(w, http.StatusBadRequest, errors.New("name the machine: lowercase letters, digits and dashes"))
		return
	}
	if _, exists, err := s.fleet.GetMachine(name); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	} else if exists {
		writeError(w, http.StatusConflict, errors.New("a machine named "+name+" is linked already; unlink it or pick another name"))
		return
	}
	cert, own, err := s.fleetCertificate()
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if own.Fingerprint == invite.Fingerprint {
		writeError(w, http.StatusBadRequest, errors.New("that code is this Swarm's own"))
		return
	}
	var redeemed struct {
		Token     string `json:"token"`
		SessionID string `json:"session_id"`
		SwarmID   string `json:"swarm_id"`
	}
	client := fleet.NewClient(cert, invite.Address, invite.Fingerprint, "")
	if err := client.JSON(r.Context(), http.MethodPost, "/link/v1/redeem", map[string]string{"invite_id": invite.ID, "secret": invite.Secret, "name": s.fleetName()}, &redeemed); err != nil {
		writeError(w, http.StatusBadGateway, errors.New("the machine at "+invite.Address+" did not link: "+err.Error()))
		return
	}
	if redeemed.Token == "" || redeemed.SessionID == "" {
		writeError(w, http.StatusBadGateway, errors.New("the machine gave no session"))
		return
	}
	if err := s.fleetTokens.PutFleetLinkToken(account, name, redeemed.Token); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UnixMilli()
	machine := pebblestore.FleetMachine{
		Name: name, Address: invite.Address, Fingerprint: invite.Fingerprint, SwarmID: redeemed.SwarmID,
		SessionID: redeemed.SessionID, AccountScopeID: account, LinkedAt: now, LastContactAt: now,
	}
	if err := s.fleet.PutMachine(machine); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	log.Printf("swarmd fleet: linked to %s at %s (%s)", name, invite.Address, short16(invite.Fingerprint))
	writeJSON(w, http.StatusCreated, map[string]any{"machine": machine})
}

// fleetMachineClient is a client for a linked machine, with its session.
func (s *Server) fleetMachineClient(account, name string) (*fleet.Client, pebblestore.FleetMachine, int, error) {
	machine, found, err := s.fleet.GetMachine(name)
	if err != nil {
		return nil, machine, http.StatusInternalServerError, err
	}
	if !found {
		return nil, machine, http.StatusNotFound, errors.New("no linked machine named " + name)
	}
	token, found, err := s.fleetTokens.GetFleetLinkToken(account, name)
	if err != nil {
		return nil, machine, http.StatusInternalServerError, err
	}
	if !found {
		return nil, machine, http.StatusConflict, errors.New("this home holds no session for " + name + "; link it again")
	}
	cert, _, err := s.fleetCertificate()
	if err != nil {
		return nil, machine, http.StatusConflict, err
	}
	return fleet.NewClient(cert, machine.Address, machine.Fingerprint, token), machine, 0, nil
}

// noteFleetContact keeps whether the last call to a machine went through.
func (s *Server) noteFleetContact(machine pebblestore.FleetMachine, callErr error) {
	if callErr == nil {
		machine.LastContactAt, machine.LastError = time.Now().UnixMilli(), ""
	} else {
		machine.LastError = callErr.Error()
	}
	if err := s.fleet.PutMachine(machine); err != nil {
		log.Printf("swarmd fleet: keep %s's contact: %v", machine.Name, err)
	}
}

func (s *Server) handleFleetCheck(w http.ResponseWriter, r *http.Request, account, name string) {
	client, machine, status, err := s.fleetMachineClient(account, name)
	if err != nil {
		writeError(w, status, err)
		return
	}
	var hello map[string]any
	callErr := client.JSON(r.Context(), http.MethodGet, "/link/v1/hello", nil, &hello)
	s.noteFleetContact(machine, callErr)
	machine, _, _ = s.fleet.GetMachine(name)
	if callErr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"machine": machine, "reached": false, "error": callErr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"machine": machine, "reached": true, "hello": hello})
}

// handleFleetUnlink tells the machine its home leaves (it revokes the
// session), then forgets the machine here, told or not.
func (s *Server) handleFleetUnlink(w http.ResponseWriter, r *http.Request, account, name string) {
	client, machine, status, err := s.fleetMachineClient(account, name)
	if err != nil && status == http.StatusNotFound {
		writeError(w, status, err)
		return
	}
	told, detail := false, ""
	if client != nil {
		if err := client.JSON(r.Context(), http.MethodDelete, "/link/v1/link", nil, nil); err != nil {
			detail = err.Error()
		} else {
			told = true
		}
	} else if err != nil {
		detail = err.Error()
	}
	if err := s.fleetTokens.DeleteFleetLinkToken(account, name); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.fleet.DeleteMachine(name); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	log.Printf("swarmd fleet: unlinked %s (machine told: %v)", name, told)
	out := map[string]any{"unlinked": true, "machine": machine, "machine_told": told}
	if detail != "" {
		out["detail"] = detail
	}
	writeJSON(w, http.StatusOK, out)
}

// fleetHopHeaders are not passed through, either way: they belong to one
// connection, or carry this home's own credentials.
var fleetHopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
	"X-Swarm-Token": true, "Authorization": true, "Cookie": true, "Set-Cookie": true,
}

// handleFleetForward sends one request to the machine's own API as its home
// and passes the answer back as it comes.
func (s *Server) handleFleetForward(w http.ResponseWriter, r *http.Request, account, name, path string) {
	if fleetKeyProxyRefused(path) {
		writeError(w, http.StatusForbidden, errors.New("fleet keys reach a machine only from this home's key manager (/v3/fleet/keys), never through the proxy"))
		return
	}
	client, machine, status, err := s.fleetMachineClient(account, name)
	if err != nil {
		writeError(w, status, err)
		return
	}
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	header := http.Header{}
	for k, v := range r.Header {
		if !fleetHopHeaders[http.CanonicalHeaderKey(k)] {
			header[k] = v
		}
	}
	if fleetHeaderToken(r.Header, "Connection", "upgrade") && fleetHeaderToken(r.Header, "Upgrade", "websocket") {
		s.forwardFleetWebSocket(w, r, client, machine, path, header)
		return
	}
	resp, err := client.Do(r.Context(), r.Method, path, r.Body, header)
	s.noteFleetContact(machine, err)
	if err != nil {
		writeError(w, http.StatusBadGateway, errors.New("could not reach "+name+": "+err.Error()))
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		if !fleetHopHeaders[http.CanonicalHeaderKey(k)] {
			w.Header()[k] = v
		}
	}
	w.Header().Set("X-Swarm-Fleet-Machine", name)
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				log.Printf("swarmd fleet: %s's answer to %s %s broke: %v", name, r.Method, path, readErr)
			}
			return
		}
	}
}

func short16(fingerprint string) string {
	if len(fingerprint) > 16 {
		return fingerprint[:16]
	}
	return fingerprint
}
