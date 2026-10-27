package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/fleet"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A Swarm listens for its homes only while it has an open link code or a
// home, only on the one private address the person named, and answers only
// over TLS with a client key. A home's call must carry both its linked key
// and the owner session it was given for that key; it then reaches this
// Swarm's own API as the owner, like any client of Swarm. Who controls this
// Swarm (its codes, its homes, its client sessions) is decided only here.

type fleetListener struct {
	mu      sync.Mutex
	address string
	server  *http.Server
}

type fleetHomeContextKey struct{}

// fleetHomeFromRequest is the home a request came from over the link
// listener, if it did.
func fleetHomeFromRequest(r *http.Request) (pebblestore.FleetHome, bool) {
	home, ok := r.Context().Value(fleetHomeContextKey{}).(pebblestore.FleetHome)
	return home, ok
}

// fleetCertificate is this Swarm's TLS certificate, from its node key.
func (s *Server) fleetCertificate() (tls.Certificate, pebblestore.SwarmLocalNodeRecord, error) {
	node, found, err := s.swarmLocalNode()
	if err != nil {
		return tls.Certificate{}, node, err
	}
	if !found || node.PublicKey == "" || node.PrivateKey == "" {
		return tls.Certificate{}, node, errors.New("this Swarm has no node key yet")
	}
	cert, err := fleet.Certificate(node.PublicKey, node.PrivateKey)
	if err != nil {
		return tls.Certificate{}, node, err
	}
	return cert, node, nil
}

func (s *Server) fleetListenAddress() string {
	s.fleetListen.mu.Lock()
	defer s.fleetListen.mu.Unlock()
	return s.fleetListen.address
}

// StartFleetListener listens where this Swarm was told to, at startup. It
// does nothing for a Swarm that never made a link code.
func (s *Server) StartFleetListener() error {
	if s == nil || s.fleet == nil {
		return nil
	}
	listen, err := s.fleet.GetListen()
	if err != nil || listen.Address == "" {
		return err
	}
	_, err = s.listenForHomes(listen.Address)
	return err
}

// listenForHomes listens on address (a new one replaces the old) and
// returns where it listens.
func (s *Server) listenForHomes(address string) (string, error) {
	address = strings.TrimSpace(address)
	s.fleetListen.mu.Lock()
	defer s.fleetListen.mu.Unlock()
	if address == s.fleetListen.address && s.fleetListen.server != nil {
		return address, nil
	}
	cert, _, err := s.fleetCertificate()
	if err != nil {
		return "", err
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return "", fmt.Errorf("listen for homes on %s: %w", address, err)
	}
	if s.fleetListen.server != nil {
		_ = s.fleetListen.server.Close()
	}
	server := &http.Server{
		Handler:   s.fleetLinkHandler(),
		TLSConfig: fleet.ServerTLS(cert),
		// HTTP/1.1 only, so the realtime WebSocket can upgrade.
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
		ReadHeaderTimeout: 10 * time.Second,
	}
	address = ln.Addr().String()
	s.fleetListen.server, s.fleetListen.address = server, address
	go func() {
		if err := server.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("swarmd fleet: listener on %s stopped: %v", address, err)
		}
	}()
	log.Printf("swarmd fleet: listening for homes on %s", address)
	return address, nil
}

// stopListeningIfUnused stops listening once no code is open and no home is
// linked: nothing is left to answer.
func (s *Server) stopListeningIfUnused() {
	invites, err := s.fleet.ListInvites()
	if err != nil {
		return
	}
	for _, invite := range invites {
		if invite.UsedAt == 0 && invite.CancelledAt == 0 {
			return
		}
	}
	if homes, err := s.fleet.ListHomes(); err != nil || len(homes) > 0 {
		return
	}
	if err := s.fleet.PutListen(pebblestore.FleetListen{}); err != nil {
		log.Printf("swarmd fleet: %v", err)
	}
	s.CloseFleetListener()
	log.Printf("swarmd fleet: no code open and no home: stopped listening")
}

// CloseFleetListener stops listening for homes.
func (s *Server) CloseFleetListener() {
	if s == nil {
		return
	}
	s.fleetListen.mu.Lock()
	defer s.fleetListen.mu.Unlock()
	if s.fleetListen.server != nil {
		_ = s.fleetListen.server.Close()
		s.fleetListen.server, s.fleetListen.address = nil, ""
	}
}

// fleetLinkRefused is what a home may not do here: decide who controls this
// Swarm. That stays with the person on this machine.
func fleetLinkRefused(path string) bool {
	for _, prefix := range []string{"/v3/fleet/invites", "/v3/fleet/homes", clientSessionsPath, "/link/"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// fleetLinkSecretRefused is what never crosses the link: provider credentials,
// sign-in codes, the vault, secret values, AI keys and this Swarm's own
// permission rules. A home may list secret names and their grants, never read
// or write a value.
func fleetLinkSecretRefused(method, path string) bool {
	for _, prefix := range []string{
		"/v1/auth/credentials",
		"/v1/auth/codex/oauth/complete",
		"/v1/auth/attach/rotate",
		"/v1/onboarding/provider/credential",
		"/v1/vault",
		"/v3/auth/tokens",
		"/v3/permissions",
	} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	if path == "/v1/secrets" || strings.HasPrefix(path, "/v1/secrets/") {
		rest := strings.TrimSuffix(strings.TrimPrefix(path, "/v1/secrets"), "/")
		if strings.HasSuffix(rest, "/value") {
			return true
		}
		// Creating or changing a slot can carry a value; reads of names stay.
		if rest == "" && method != http.MethodGet && method != http.MethodHead {
			return true
		}
	}
	return false
}

func (s *Server) fleetLinkHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil {
			writeError(w, http.StatusUnauthorized, errors.New("TLS required"))
			return
		}
		raw := make([][]byte, 0, len(r.TLS.PeerCertificates))
		for _, cert := range r.TLS.PeerCertificates {
			raw = append(raw, cert.Raw)
		}
		caller := fleet.PeerFingerprint(raw)
		if caller == "" {
			writeError(w, http.StatusUnauthorized, errors.New("an ed25519 client key is required"))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/link/v1/redeem" {
			s.handleFleetRedeem(w, r, caller)
			return
		}
		home, found, err := s.fleet.GetHome(caller)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			writeError(w, http.StatusForbidden, errors.New("this Swarm is not linked to you"))
			return
		}
		actor, err := s.identitySessions.Validate(extractAttachToken(r))
		if err != nil || actor.Principal.SessionID != home.SessionID {
			writeError(w, http.StatusUnauthorized, errors.New("the link's session is not valid here (revoked, ended, or another key's)"))
			return
		}
		if now := time.Now().UnixMilli(); now-home.LastCallAt > int64(time.Minute/time.Millisecond) {
			home.LastCallAt = now
			_ = s.fleet.PutHome(home)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/link/v1/hello":
			node, _, _ := s.swarmLocalNode()
			writeJSON(w, http.StatusOK, map[string]any{
				"swarm_id":    node.SwarmID,
				"name":        s.fleetName(),
				"swarm_name":  s.swarmName(),
				"fingerprint": node.Fingerprint,
				"home":        home.Name,
				"linked_at":   home.LinkedAt,
			})
			return
		case r.Method == http.MethodDelete && r.URL.Path == "/link/v1/link":
			if err := s.endFleetHome(home); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			// Its connections are cut once this answer is out.
			done := r.Context()
			go func() { <-done.Done(); s.afterFleetHomeLeft() }()
			log.Printf("swarmd fleet: home %s (%s) unlinked itself", home.Name, short16(home.Fingerprint))
			writeJSON(w, http.StatusOK, map[string]any{"unlinked": true})
			return
		}
		if fleetLinkRefused(r.URL.Path) {
			writeError(w, http.StatusForbidden, errors.New("who controls this Swarm is decided on it, not by a home"))
			return
		}
		if fleetLinkSecretRefused(r.Method, r.URL.Path) {
			writeError(w, http.StatusForbidden, errors.New("keys, secret values and permission rules never cross the link; change them on this machine"))
			return
		}
		ctx := context.WithValue(r.Context(), fleetHomeContextKey{}, home)
		s.internalRouteHandler().ServeHTTP(w, requestWithActorContext(r.WithContext(ctx), actor))
	})
}

var fleetInviteLocks sync.Map

func lockFleetInvite(id string) func() {
	value, _ := fleetInviteLocks.LoadOrStore(id, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// handleFleetRedeem makes the caller's key a home of this Swarm, once per
// code, and gives it an owner session (a client session named for it).
func (s *Server) handleFleetRedeem(w http.ResponseWriter, r *http.Request, caller string) {
	var req struct {
		InviteID string `json:"invite_id"`
		Secret   string `json:"secret"`
		Name     string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid link request"))
		return
	}
	unlock := lockFleetInvite(req.InviteID)
	defer unlock()
	invite, found, err := s.fleet.GetInvite(req.InviteID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// One answer for every refusal: a guesser learns nothing.
	if !found || invite.UsedAt != 0 || invite.CancelledAt != 0 || !fleet.SecretMatches(req.Secret, invite.SecretHash) {
		writeError(w, http.StatusForbidden, errors.New("this link code is not valid: it was used, cancelled, or it is wrong"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if !fleet.ValidName(name) {
		name = ""
	}
	label := "home " + short16(caller)
	if name != "" {
		label = "home " + name
	}
	issued, err := s.identitySessions.IssueClientSession(label)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// A home that links again replaces its old session.
	if old, found, _ := s.fleet.GetHome(caller); found && old.SessionID != issued.SessionID {
		_, _ = s.identitySessions.RevokeClientSession(old.SessionID)
	}
	now := time.Now().UnixMilli()
	home := pebblestore.FleetHome{Fingerprint: caller, Name: name, SessionID: issued.SessionID, InviteID: invite.ID, LinkedAt: now, LastCallAt: now}
	if err := s.fleet.PutHome(home); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	invite.UsedAt, invite.UsedBy = now, caller
	if err := s.fleet.PutInvite(invite); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	node, _, _ := s.swarmLocalNode()
	log.Printf("swarmd fleet: %s (%s) is now a home of this Swarm", label, short16(caller))
	writeJSON(w, http.StatusOK, map[string]any{
		"token":       issued.Token,
		"session_id":  issued.SessionID,
		"swarm_id":    node.SwarmID,
		"name":        s.fleetName(),
		"swarm_name":  s.swarmName(),
		"fingerprint": node.Fingerprint,
	})
}

// afterFleetHomeLeft cuts every connection to the listener, so a home that
// left keeps no open stream (a home still linked connects again), and stops
// listening when nothing is left to answer.
func (s *Server) afterFleetHomeLeft() {
	address := s.fleetListenAddress()
	s.stopListeningIfUnused()
	if address == "" || s.fleetListenAddress() == "" {
		return
	}
	s.CloseFleetListener()
	if _, err := s.listenForHomes(address); err != nil {
		log.Printf("swarmd fleet: listen again on %s: %v", address, err)
	}
}
