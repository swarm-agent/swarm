package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	pathpkg "path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Fleet keys: the home is its fleet's key manager for provider API keys.
//
// A person enters a key once, on the home, in Swarm's own key page (never in
// a chat or a client app). The home verifies it with the provider, keeps it
// sealed in its secret store, installs it on itself when asked, and sends it
// to each chosen machine over the pinned link. Each machine verifies it with
// the provider again, saves it as an ordinary provider credential tagged with
// the fleet key id and its home, and answers with the outcome; the home keeps
// each outcome. Revoking removes exactly that credential from each machine;
// once it is gone everywhere, the home deletes its stored value.
//
// Routes on the home (owner sessions, or AI keys with swarm:approve, write;
// sessions:read reads; agents' gateway tokens never):
//
//	GET  /v3/fleet/keys                   keys and each machine's state, never values
//	POST /v3/fleet/keys/requests          a one-time key page link
//	POST /v3/fleet/keys/{id}/install      install a kept key on more machines
//	POST /v3/fleet/keys/{id}/revoke       remove it (from all when none named)
//
// Routes on a linked machine, answered only for its linked home over the
// link (the home's raw proxy refuses them, so only its key manager sends them):
//
//	POST /v3/fleet/keys/install           verify and save one key
//	POST /v3/fleet/keys/{id}/remove       delete the credential that key installed

const (
	fleetKeysPath         = "/v3/fleet/keys"
	fleetKeyMaxMachines   = 64
	fleetKeyParallel      = 4
	fleetKeyCallTimeout   = 30 * time.Second
	fleetKeyRunTimeout    = 3 * time.Minute
	fleetKeyMaxBodyBytes  = 16 << 10
	fleetKeyTagPrefix     = "fleet-key:"
	fleetKeyHomeTagPrefix = "fleet-home:"
)

var fleetKeyIDPattern = regexp.MustCompile(`^fk_[0-9a-f]{16}$`)

// fleetKeyPending is what a key page link for the fleet carries until the
// person submits the key.
type fleetKeyPending struct {
	id          string
	machines    []string
	includeHome bool
	createdBy   string
	// targets name where it goes, for the key page.
	targets []string
}

// fleetKeySlot is the key page registry slot of one fleet key's link.
func fleetKeySlot(id string) string { return "fleet:" + id }

func newFleetKeyID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", errors.New("could not make a fleet key id")
	}
	return "fk_" + hex.EncodeToString(raw[:]), nil
}

// fleetKeyFingerprint is a short SHA-256 prefix and the last four characters:
// enough to tell keys apart, never enough to use one.
func fleetKeyFingerprint(key string) (string, string) {
	sum := sha256.Sum256([]byte(key))
	last4 := key
	if len(key) > 4 {
		last4 = key[len(key)-4:]
	}
	return hex.EncodeToString(sum[:4]), last4
}

// fleetKeyProxyRefused is true for the machine-side fleet key routes: a
// client may never reach them through the home's raw proxy, so only the
// home's own key manager sends a key to a machine.
func fleetKeyProxyRefused(path string) bool {
	cleaned := pathpkg.Clean("/" + strings.TrimLeft(path, "/"))
	return cleaned == fleetKeysPath || strings.HasPrefix(cleaned, fleetKeysPath+"/")
}

// fleetKeyWriteAuthority admits owner sessions and AI keys with
// swarm:approve. Agents' gateway tokens never manage keys.
func fleetKeyWriteAuthority(r *http.Request) error {
	if fleetActivityGatewayCaller(r) {
		return errors.New("agents never manage fleet keys")
	}
	if record, ok := ScopedTokenFromRequest(r); ok && !record.HasScope(sessionAccessScope) {
		return errors.New("managing fleet keys needs swarm:approve")
	}
	return nil
}

func (s *Server) handleFleetKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.fleet == nil || s.fleetTokens == nil || s.auth == nil {
		writeError(w, http.StatusInternalServerError, errors.New("the fleet is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, fleetKeysPath), "/")
	parts := strings.Split(rest, "/")
	// The machine side: only this Swarm's linked home, over the link.
	if rest == "install" || (len(parts) == 2 && parts[1] == "remove") {
		home, fromHome := fleetHomeFromRequest(r)
		if !fromHome {
			writeError(w, http.StatusForbidden, errors.New("fleet keys are installed and removed only by this Swarm's linked home, over the link"))
			return
		}
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if rest == "install" {
			s.handleFleetKeyInstallFromHome(w, r, principal, home)
		} else {
			s.handleFleetKeyRemoveFromHome(w, r, principal, home, parts[0])
		}
		return
	}
	if _, fromHome := fleetHomeFromRequest(r); fromHome {
		writeError(w, http.StatusForbidden, errors.New("a home may only install and remove fleet keys here"))
		return
	}
	if fleetActivityGatewayCaller(r) {
		writeError(w, http.StatusForbidden, errors.New("agents never see or manage fleet keys"))
		return
	}
	switch {
	case rest == "":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		if !s.requireScope(w, r, "sessions:read") {
			return
		}
		s.handleFleetKeyList(w, principal)
		return
	case rest == "requests", len(parts) == 2 && (parts[1] == "install" || parts[1] == "revoke"):
	default:
		writeError(w, http.StatusNotFound, errors.New("no such fleet key route"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if err := fleetKeyWriteAuthority(r); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	if rest == "requests" {
		s.handleFleetKeyRequest(w, r, principal)
		return
	}
	if !fleetKeyIDPattern.MatchString(parts[0]) {
		writeError(w, http.StatusNotFound, pebblestore.ErrFleetKeyNotFound)
		return
	}
	if parts[1] == "install" {
		s.handleFleetKeyInstall(w, r, principal, parts[0])
	} else {
		s.handleFleetKeyRevoke(w, r, principal, parts[0])
	}
}

func (s *Server) handleFleetKeyList(w http.ResponseWriter, principal identity.Principal) {
	keys, err := s.fleet.GetFleetKeys(principal.AccountScopeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": keys.Revision, "home": s.fleetName(), "keys": keys.Keys})
}

// fleetKeyMachines checks that each name is a machine this account linked,
// once each.
func (s *Server) fleetKeyMachines(account string, names []string) ([]string, error) {
	if len(names) > fleetKeyMaxMachines {
		return nil, fmt.Errorf("at most %d machines at once", fleetKeyMaxMachines)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if !fleet.ValidName(name) {
			return nil, fmt.Errorf("%q is not a machine name", raw)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		_, linked, err := s.fleet.GetMachine(name)
		if err != nil {
			return nil, err
		}
		_, owned, err := s.fleetTokens.GetFleetLinkToken(account, name)
		if err != nil {
			return nil, err
		}
		if !linked || !owned {
			return nil, fmt.Errorf("no linked machine named %s", name)
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// fleetKeyProviderTakesKey is whether this Swarm has the provider and it
// takes an API key.
func (s *Server) fleetKeyProviderTakesKey(ctx context.Context, provider string) bool {
	if s.providers == nil || provider == "" {
		return false
	}
	adapter, ok := s.providers.Get(provider)
	if !ok || adapter == nil {
		return false
	}
	status, err := adapter.Status(ctx)
	if err != nil {
		return false
	}
	for _, method := range status.AuthMethods {
		if method.CredentialType == "api" {
			return true
		}
	}
	return false
}

func fleetKeyLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if len([]rune(label)) > providerKeyMaxLabel {
		return "", fmt.Errorf("label exceeds %d characters", providerKeyMaxLabel)
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return "", errors.New("label must be plain text")
		}
	}
	return label, nil
}

func (s *Server) handleFleetKeyRequest(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
	var req struct {
		Provider    string   `json:"provider"`
		Label       string   `json:"label"`
		Machines    []string `json:"machines"`
		IncludeHome bool     `json:"include_home"`
	}
	if err := decodeJSONLimited(w, r, &req, fleetKeyMaxBodyBytes); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if !s.fleetKeyProviderTakesKey(r.Context(), provider) {
		writeError(w, http.StatusBadRequest, errors.New("provider must be one that takes an API key (see /v1/providers)"))
		return
	}
	label, err := fleetKeyLabel(req.Label)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	machines, err := s.fleetKeyMachines(principal.AccountScopeID, req.Machines)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(machines) == 0 && !req.IncludeHome {
		writeError(w, http.StatusBadRequest, errors.New("name at least one machine, or include_home"))
		return
	}
	id, err := newFleetKeyID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	targets := fleetKeyTargetNames(s.fleetName(), req.IncludeHome, machines)
	request := providerKeyRequest{
		tailnet:        providerKeyTailnetRequest(r),
		provider:       provider,
		label:          label,
		accountScopeID: principal.AccountScopeID,
		userID:         principal.UserID,
		fleet:          &fleetKeyPending{id: id, machines: machines, includeHome: req.IncludeHome, createdBy: "owner", targets: targets},
	}
	if token, ok := ScopedTokenFromRequest(r); ok {
		request.token = *token
		request.fleet.createdBy = token.Name
	} else if actor, ok := productActorFromRequest(r); ok {
		request.clientSessionID = actor.ClientSessionID
	}
	_, link, err := s.providerKeys.issue(request)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	device := "this home"
	if strings.HasPrefix(link, "https://") {
		device = "a device in your tailnet"
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":                 id,
		"provider":           provider,
		"url":                link,
		"expires_in_seconds": int(providerKeyCodeTTL.Seconds()),
		"machines":           machines,
		"include_home":       req.IncludeHome,
		"tell_the_person": fmt.Sprintf("Open %s in a browser on %s and paste your %s API key there. Swarm keeps it sealed on the home and installs it on %s. Do not paste the key into a chat. The link works once and expires in %d minutes.",
			link, device, provider, strings.Join(targets, ", "), int(providerKeyCodeTTL.Minutes())),
	})
}

func fleetKeyTargetNames(homeName string, includeHome bool, machines []string) []string {
	out := []string{}
	if includeHome {
		if homeName == "" {
			out = append(out, "this home")
		} else {
			out = append(out, homeName+" (this home)")
		}
	}
	return append(out, machines...)
}

// fleetKeyRequesterValid re-checks, when the person submits the key, that
// whoever asked for the link still may: its AI key (and every key above it)
// or its client session.
func (s *Server) fleetKeyRequesterValid(request providerKeyRequest) bool {
	if request.token.ID != "" {
		return s.providerKeyTokenStillValid(request.token)
	}
	if request.clientSessionID == "" || s.identitySessions == nil {
		return true
	}
	sessions, err := s.identitySessions.ListClientSessions()
	if err != nil {
		return false
	}
	now := time.Now().UnixMilli()
	for _, session := range sessions {
		if session.SessionID == request.clientSessionID {
			return session.RevokedAt == 0 && (session.ExpiresAt == 0 || session.ExpiresAt > now)
		}
	}
	// Not a client session (the owner socket, the desktop): the actor check
	// in saveFleetKey still applies.
	return true
}

// saveFleetKey is the key page's submission for a fleet key: verify on the
// home, keep the value sealed, record the key, then install it on its
// targets. It returns a plain outcome for the page, never the key.
func (s *Server) saveFleetKey(r *http.Request, request providerKeyRequest, key string) (string, error) {
	if s.identitySessions == nil || s.fleet == nil || s.fleetTokens == nil {
		return "Swarm's fleet is not configured.", errors.New("fleet unavailable")
	}
	actor, err := s.identitySessions.ActorForUserID(request.userID)
	if err != nil || !isCompleteProductActor(actor) || actor.AccountScopeID != request.accountScopeID {
		return "the account that asked for this link is no longer available", errors.New("actor unavailable")
	}
	if request.tailnet && s.providerKeys.gatewayOrigin() == "" {
		return "the tailnet gateway is no longer available", errors.New("gateway unavailable")
	}
	if !request.tailnet && !s.fleetKeyRequesterValid(request) {
		return "whoever asked for this link was signed out or its key was revoked", errors.New("requester invalid")
	}
	pending := request.fleet
	connection, verifyErr := s.verifyCredentialMaterialForAccount(r.Context(), request.accountScopeID, provideriface.AuthCredential{Provider: request.provider, Type: "api", APIKey: key})
	if verifyErr != nil || !authCredentialVerificationAccepted(connection) {
		return "provider verification failed; check the key and try again", errors.New("verification refused")
	}
	if err := s.fleetTokens.PutFleetKeyValue(request.accountScopeID, pending.id, []byte(key)); err != nil {
		if errors.Is(err, pebblestore.ErrVaultLocked) {
			return "the home's vault is locked; unlock it and ask for a new link", err
		}
		return "the home could not keep the key", err
	}
	fingerprint, last4 := fleetKeyFingerprint(key)
	now := time.Now().UnixMilli()
	_, err = s.fleet.UpdateFleetKeys(request.accountScopeID, func(keys *pebblestore.FleetKeys) error {
		if _, exists := keys.Key(pending.id); exists {
			return errors.New("this key was saved already")
		}
		keys.Keys = append(keys.Keys, pebblestore.FleetKey{
			ID: pending.id, Provider: request.provider, Label: request.label,
			Fingerprint: fingerprint, Last4: last4, CreatedAt: now, CreatedBy: pending.createdBy,
			Machines: map[string]pebblestore.FleetKeyTarget{},
		})
		return nil
	})
	if err != nil {
		_ = s.fleetTokens.DeleteFleetKeyValue(request.accountScopeID, pending.id)
		return "the home could not record the key", err
	}
	// The installs finish even if the person closes the page.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), fleetKeyRunTimeout)
	defer cancel()
	record := s.runFleetKeyInstall(ctx, actor, pending.id, pending.includeHome, pending.machines, key)
	verified := "Swarm verified the key with " + request.provider
	if connection == nil {
		verified = request.provider + " offers no key check, so the key was not verified"
	}
	return verified + " and keeps it sealed on this home. " + fleetKeySummary(record, key), nil
}

// fleetKeySummary is each target's outcome in one line, for the key page.
func fleetKeySummary(record pebblestore.FleetKey, key string) string {
	installed, failed := []string{}, []string{}
	if record.Home != nil {
		if record.Home.State == pebblestore.FleetKeyInstalled {
			installed = append(installed, "this home")
		} else if record.Home.State == pebblestore.FleetKeyFailed {
			failed = append(failed, "this home ("+providerKeyRedact(record.Home.Error, key)+")")
		}
	}
	names := make([]string, 0, len(record.Machines))
	for name := range record.Machines {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		target := record.Machines[name]
		switch target.State {
		case pebblestore.FleetKeyInstalled:
			installed = append(installed, name)
		case pebblestore.FleetKeyFailed:
			failed = append(failed, name+" ("+providerKeyRedact(target.Error, key)+")")
		}
	}
	out := []string{}
	if len(installed) > 0 {
		out = append(out, "Installed on "+strings.Join(installed, ", ")+".")
	}
	if len(failed) > 0 {
		out = append(out, "Not installed on "+strings.Join(failed, "; ")+".")
	}
	return strings.Join(out, " ")
}

// setFleetKeyTarget changes one target of one key.
func (s *Server) setFleetKeyTarget(account, id, machine string, home bool, change func(*pebblestore.FleetKeyTarget)) {
	_, err := s.fleet.UpdateFleetKeys(account, func(keys *pebblestore.FleetKeys) error {
		key, ok := keys.Key(id)
		if !ok {
			return pebblestore.ErrFleetKeyNotFound
		}
		var target pebblestore.FleetKeyTarget
		if home {
			if key.Home != nil {
				target = *key.Home
			}
		} else {
			if key.Machines == nil {
				key.Machines = map[string]pebblestore.FleetKeyTarget{}
			}
			target = key.Machines[machine]
		}
		change(&target)
		target.UpdatedAt = time.Now().UnixMilli()
		if home {
			key.Home = &target
		} else {
			key.Machines[machine] = target
		}
		return nil
	})
	if err != nil {
		log.Printf("swarmd fleet keys: record %s on %s: %v", id, fleetKeyTargetLabel(machine, home), err)
	}
}

func fleetKeyTargetLabel(machine string, home bool) string {
	if home {
		return "this home"
	}
	return machine
}

func fleetKeyInstalledTarget(verified bool) func(*pebblestore.FleetKeyTarget) {
	return func(target *pebblestore.FleetKeyTarget) {
		target.State, target.Error, target.Verified = pebblestore.FleetKeyInstalled, "", verified
		target.InstalledAt, target.RemovedAt = time.Now().UnixMilli(), 0
	}
}

func fleetKeyFailedTarget(reason string) func(*pebblestore.FleetKeyTarget) {
	return func(target *pebblestore.FleetKeyTarget) {
		target.State, target.Error, target.Verified = pebblestore.FleetKeyFailed, controlMCPTruncate(reason, 300), false
	}
}

// fleetKeyCallError is a machine's refusal or an unreachable machine, in
// words, with any key material removed.
func fleetKeyCallError(err error, key string) string {
	var refusal *fleet.Error
	if errors.As(err, &refusal) && refusal.Status == http.StatusNotFound {
		return "this machine's Swarm does not take fleet keys yet; update it"
	}
	return providerKeyRedact(err.Error(), key)
}

// runFleetKeyInstall installs one key on the home (when asked) and on each
// machine over the link, a few at a time, and records each outcome.
func (s *Server) runFleetKeyInstall(ctx context.Context, actor identity.ActorContext, id string, home bool, machines []string, key string) pebblestore.FleetKey {
	account := actor.AccountScopeID
	keys, err := s.fleet.GetFleetKeys(account)
	record, found := keys.Key(id)
	if err != nil || !found {
		return pebblestore.FleetKey{ID: id}
	}
	install := fleetKeyInstall{ID: id, Provider: record.Provider, Label: record.Label, Key: key}
	pending := func(target *pebblestore.FleetKeyTarget) {
		target.State, target.Error = pebblestore.FleetKeyPending, ""
	}
	if home {
		s.setFleetKeyTarget(account, id, "", true, pending)
	}
	for _, name := range machines {
		s.setFleetKeyTarget(account, id, name, false, pending)
	}
	if home {
		name, fingerprint := s.fleetKeyOwnHome()
		result, _, err := s.installFleetCredential(fleetKeyActorContext(ctx, actor), actor.AccountScopeID, install, name, fingerprint)
		if err != nil {
			s.setFleetKeyTarget(account, id, "", true, fleetKeyFailedTarget(providerKeyRedact(err.Error(), key)))
		} else {
			s.setFleetKeyTarget(account, id, "", true, fleetKeyInstalledTarget(result.Verified))
		}
	}
	slots := make(chan struct{}, fleetKeyParallel)
	var wg sync.WaitGroup
	for _, name := range machines {
		wg.Add(1)
		slots <- struct{}{}
		go func(name string) {
			defer wg.Done()
			defer func() { <-slots }()
			client, machine, _, err := s.fleetMachineClient(account, name)
			if err != nil {
				s.setFleetKeyTarget(account, id, name, false, fleetKeyFailedTarget(err.Error()))
				return
			}
			callCtx, cancel := context.WithTimeout(ctx, fleetKeyCallTimeout)
			defer cancel()
			var out fleetKeyInstalled
			callErr := client.JSON(callCtx, http.MethodPost, fleetKeysPath+"/install", install, &out)
			s.noteFleetContact(machine, fleetKeyContactError(callErr))
			if callErr != nil {
				s.setFleetKeyTarget(account, id, name, false, fleetKeyFailedTarget(fleetKeyCallError(callErr, key)))
				return
			}
			s.setFleetKeyTarget(account, id, name, false, fleetKeyInstalledTarget(out.Verified))
		}(name)
	}
	wg.Wait()
	keys, _ = s.fleet.GetFleetKeys(account)
	if record, found := keys.Key(id); found {
		return *record
	}
	return pebblestore.FleetKey{ID: id}
}

// fleetKeyContactError is what counts as losing contact with a machine: not
// reaching it. A refusal (a key that failed verification) means it answered.
func fleetKeyContactError(err error) error {
	var refusal *fleet.Error
	if err == nil || errors.As(err, &refusal) {
		return nil
	}
	return err
}

// fleetKeyActorContext is ctx acting as actor, for the home's own
// credential routes.
func fleetKeyActorContext(ctx context.Context, actor identity.ActorContext) context.Context {
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	return requestWithActorContext(r, actor).Context()
}

// fleetKeyOwnHome is how the home names and tags itself on its own
// credentials.
func (s *Server) fleetKeyOwnHome() (string, string) {
	name := s.fleetName()
	if name == "" {
		name = "this Swarm"
	}
	node, _, _ := s.swarmLocalNode()
	fingerprint := node.Fingerprint
	if fingerprint == "" {
		fingerprint = "self"
	}
	return name, fingerprint
}

// fleetKeyInstall is what a home sends a machine: the key travels only in
// this body, over the pinned link.
type fleetKeyInstall struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Label    string `json:"label,omitempty"`
	Key      string `json:"key"`
}

// fleetKeyInstalled is a machine's answer: never the key.
type fleetKeyInstalled struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	CredentialID string `json:"credential_id"`
	Verified     bool   `json:"verified"`
	Message      string `json:"message,omitempty"`
}

func fleetKeyCredentialTags(id, homeFingerprint string) []string {
	return []string{fleetKeyTagPrefix + id, fleetKeyHomeTagPrefix + short16(homeFingerprint)}
}

// fleetKeyCredentials are the credentials one fleet key from one home
// installed in this account.
func (s *Server) fleetKeyCredentials(account, id, homeFingerprint string) ([]auth.CredentialStatus, error) {
	list, err := s.auth.ListCredentialsForAccount(account, "", "", 1000)
	if err != nil {
		return nil, err
	}
	want := fleetKeyCredentialTags(id, homeFingerprint)
	out := []auth.CredentialStatus{}
	for _, record := range list.Records {
		has := 0
		for _, tag := range record.Tags {
			if tag == want[0] || tag == want[1] {
				has++
			}
		}
		if has == len(want) {
			out = append(out, record)
		}
	}
	return out, nil
}

// fleetKeyRoute runs one of this Swarm's own credential routes as the actor
// in ctx and decodes the answer.
func (s *Server) fleetKeyRoute(ctx context.Context, path string, body any, out any) (int, string) {
	data, err := json.Marshal(body)
	if err != nil {
		return http.StatusInternalServerError, "the request could not be encoded"
	}
	sub, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(data))
	if err != nil {
		return http.StatusInternalServerError, "the request could not be made"
	}
	sub.Header.Set("Content-Type", "application/json")
	recorder := &controlMCPRecorder{header: http.Header{}, status: http.StatusOK}
	s.internalRouteHandler().ServeHTTP(recorder, sub)
	if recorder.overflow || recorder.status < 200 || recorder.status > 299 {
		var refusal struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(recorder.body.Bytes(), &refusal)
		reason := strings.TrimSpace(refusal.Error)
		if reason == "" {
			reason = strings.ToLower(http.StatusText(recorder.status))
		}
		status := recorder.status
		if status < 400 {
			status = http.StatusBadGateway
		}
		return status, reason
	}
	if out != nil {
		_ = json.Unmarshal(recorder.body.Bytes(), out)
	}
	return recorder.status, ""
}

// installFleetCredential verifies a fleet key with the provider and saves it
// as an ordinary, active provider credential through the canonical
// credential route, labelled and tagged with its home and fleet key id. ctx
// must act as this Swarm's owner (the linked home's session, or the home
// itself). A reinstall replaces the same credential.
func (s *Server) installFleetCredential(ctx context.Context, account string, in fleetKeyInstall, homeName, homeFingerprint string) (fleetKeyInstalled, int, error) {
	if s.providers == nil {
		return fleetKeyInstalled{}, http.StatusConflict, errors.New("this Swarm has no providers")
	}
	if adapter, ok := s.providers.Get(in.Provider); !ok || adapter == nil {
		return fleetKeyInstalled{}, http.StatusBadRequest, fmt.Errorf("this Swarm has no provider %s", in.Provider)
	}
	connection, err := s.verifyCredentialMaterialForAccount(ctx, account, provideriface.AuthCredential{Provider: in.Provider, Type: "api", APIKey: in.Key})
	if err != nil {
		return fleetKeyInstalled{}, http.StatusBadGateway, errors.New("the provider could not be asked about the key")
	}
	if !authCredentialVerificationAccepted(connection) {
		return fleetKeyInstalled{}, http.StatusBadRequest, errors.New("provider verification failed: " + providerKeyRedact(connection.Message, in.Key))
	}
	existing, err := s.fleetKeyCredentials(account, in.ID, homeFingerprint)
	if err != nil {
		return fleetKeyInstalled{}, http.StatusInternalServerError, errors.New("this Swarm's credentials could not be read")
	}
	credentialID := "fleet-" + strings.TrimPrefix(in.ID, "fk_")
	for _, record := range existing {
		if record.Provider == in.Provider {
			credentialID = record.ID
		}
	}
	base := in.Label
	if base == "" {
		base = in.Provider
	}
	var saved struct {
		ID         string                 `json:"id"`
		Connection *auth.ConnectionStatus `json:"connection"`
	}
	status, reason := s.fleetKeyRoute(context.WithValue(ctx, providerKeyVerifiedContext{}, connection), "/v1/auth/credentials", map[string]any{
		"id":       credentialID,
		"provider": in.Provider,
		"type":     "api",
		"label":    base + " (managed by home " + homeName + ", fleet key " + in.ID + ")",
		"tags":     fleetKeyCredentialTags(in.ID, homeFingerprint),
		"api_key":  in.Key,
		"active":   true,
	}, &saved)
	if reason != "" {
		return fleetKeyInstalled{}, status, errors.New("the key was not saved: " + providerKeyRedact(reason, in.Key))
	}
	out := fleetKeyInstalled{ID: in.ID, Provider: in.Provider, CredentialID: saved.ID, Verified: connection != nil && connection.Connected}
	if connection == nil {
		out.Message = in.Provider + " offers no key check, so the key is saved unverified"
	} else {
		out.Message = "verified with " + in.Provider
	}
	return out, http.StatusOK, nil
}

// removeFleetCredentials deletes exactly the credentials one fleet key from
// one home installed, through the canonical delete route.
func (s *Server) removeFleetCredentials(ctx context.Context, account, id, homeFingerprint string) (int, int, error) {
	existing, err := s.fleetKeyCredentials(account, id, homeFingerprint)
	if err != nil {
		return 0, http.StatusInternalServerError, errors.New("this Swarm's credentials could not be read")
	}
	removed := 0
	for _, record := range existing {
		if status, reason := s.fleetKeyRoute(ctx, "/v1/auth/credentials/delete", map[string]string{"provider": record.Provider, "id": record.ID}, nil); reason != "" && status != http.StatusNotFound {
			return removed, status, errors.New("the credential was not removed: " + reason)
		}
		removed++
	}
	return removed, http.StatusOK, nil
}

func fleetHomeName(home pebblestore.FleetHome) string {
	if name := strings.TrimSpace(home.Name); name != "" {
		return name
	}
	return short16(home.Fingerprint)
}

// handleFleetKeyInstallFromHome is the machine side of an install.
func (s *Server) handleFleetKeyInstallFromHome(w http.ResponseWriter, r *http.Request, principal identity.Principal, home pebblestore.FleetHome) {
	var in fleetKeyInstall
	if err := decodeJSONLimited(w, r, &in, fleetKeyMaxBodyBytes); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid fleet key install"))
		return
	}
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	label, labelErr := fleetKeyLabel(in.Label)
	in.Label = label
	switch {
	case !fleetKeyIDPattern.MatchString(in.ID):
		writeError(w, http.StatusBadRequest, errors.New("invalid fleet key id"))
		return
	case in.Provider == "":
		writeError(w, http.StatusBadRequest, errors.New("provider is required"))
		return
	case labelErr != nil:
		writeError(w, http.StatusBadRequest, labelErr)
		return
	case !providerKeyValid(in.Key):
		writeError(w, http.StatusBadRequest, errors.New("the key is empty or has spaces or control characters"))
		return
	}
	result, status, err := s.installFleetCredential(r.Context(), principal.AccountScopeID, in, fleetHomeName(home), home.Fingerprint)
	if err != nil {
		log.Printf("swarmd fleet keys: %s from home %s not installed (HTTP %d)", in.ID, fleetHomeName(home), status)
		writeError(w, status, errors.New(providerKeyRedact(err.Error(), in.Key)))
		return
	}
	log.Printf("swarmd fleet keys: %s for %s installed from home %s", in.ID, in.Provider, fleetHomeName(home))
	writeJSON(w, http.StatusOK, result)
}

// handleFleetKeyRemoveFromHome is the machine side of a revoke.
func (s *Server) handleFleetKeyRemoveFromHome(w http.ResponseWriter, r *http.Request, principal identity.Principal, home pebblestore.FleetHome, id string) {
	if !fleetKeyIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, errors.New("invalid fleet key id"))
		return
	}
	removed, status, err := s.removeFleetCredentials(r.Context(), principal.AccountScopeID, id, home.Fingerprint)
	if err != nil {
		writeError(w, status, err)
		return
	}
	log.Printf("swarmd fleet keys: %s from home %s removed (%d credentials)", id, fleetHomeName(home), removed)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "removed": removed})
}

// fleetKeyTargets reads an install or revoke body: machines named, and
// whether the home itself is meant. given is false for an empty body.
func fleetKeyTargets(w http.ResponseWriter, r *http.Request) (machines []string, includeHome bool, given bool, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, fleetKeyMaxBodyBytes)
	body, err := readRequestBody(r)
	if err != nil {
		return nil, false, false, errors.New("invalid request body")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, false, false, nil
	}
	var req struct {
		Machines    *[]string `json:"machines"`
		IncludeHome *bool     `json:"include_home"`
	}
	if err := decodeJSONBytes(body, &req); err != nil {
		return nil, false, false, err
	}
	if req.Machines != nil {
		machines, given = *req.Machines, true
	}
	if req.IncludeHome != nil {
		includeHome, given = *req.IncludeHome, true
	}
	return machines, includeHome, given, nil
}

// handleFleetKeyInstall installs a key the home keeps on more machines (or
// on the home itself): a newly linked machine, or one whose install failed.
func (s *Server) handleFleetKeyInstall(w http.ResponseWriter, r *http.Request, principal identity.Principal, id string) {
	account := principal.AccountScopeID
	names, includeHome, _, err := fleetKeyTargets(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	machines, err := s.fleetKeyMachines(account, names)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(machines) == 0 && !includeHome {
		writeError(w, http.StatusBadRequest, errors.New("name at least one machine, or include_home"))
		return
	}
	keys, err := s.fleet.GetFleetKeys(account)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	record, found := keys.Key(id)
	if !found {
		writeError(w, http.StatusNotFound, pebblestore.ErrFleetKeyNotFound)
		return
	}
	if record.RevokedAt != 0 {
		writeError(w, http.StatusConflict, errors.New("this key was revoked everywhere and the home no longer keeps it; ask for a new key"))
		return
	}
	value, kept, err := s.fleetTokens.GetFleetKeyValue(account, id)
	if errors.Is(err, pebblestore.ErrVaultLocked) {
		writeError(w, http.StatusLocked, errors.New("the home's vault is locked; unlock it first"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("the home could not open the key"))
		return
	}
	if !kept {
		writeError(w, http.StatusConflict, errors.New("the home no longer keeps this key; ask for a new key"))
		return
	}
	actor, err := s.identitySessions.ActorForUserID(principal.UserID)
	if err != nil || !isCompleteProductActor(actor) || actor.AccountScopeID != account {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), fleetKeyRunTimeout)
	defer cancel()
	updated := s.runFleetKeyInstall(ctx, actor, id, includeHome, machines, string(value))
	writeJSON(w, http.StatusOK, map[string]any{"key": updated})
}

// handleFleetKeyRevoke removes a key from the named machines (and the home,
// with include_home), or from every target when none is named. Once it is
// removed everywhere, the home deletes the value it kept.
func (s *Server) handleFleetKeyRevoke(w http.ResponseWriter, r *http.Request, principal identity.Principal, id string) {
	account := principal.AccountScopeID
	names, includeHome, given, err := fleetKeyTargets(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	keys, err := s.fleet.GetFleetKeys(account)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	record, found := keys.Key(id)
	if !found {
		writeError(w, http.StatusNotFound, pebblestore.ErrFleetKeyNotFound)
		return
	}
	machines := []string{}
	home := false
	if !given {
		for name := range record.Machines {
			machines = append(machines, name)
		}
		home = record.Home != nil
	} else {
		seen := map[string]bool{}
		for _, raw := range names {
			name := strings.TrimSpace(raw)
			if _, ok := record.Machines[name]; !ok {
				writeError(w, http.StatusBadRequest, fmt.Errorf("this key was never sent to %q", raw))
				return
			}
			if !seen[name] {
				seen[name] = true
				machines = append(machines, name)
			}
		}
		if includeHome && record.Home == nil {
			writeError(w, http.StatusBadRequest, errors.New("this key was never installed on the home"))
			return
		}
		home = includeHome
		if len(machines) == 0 && !home {
			writeError(w, http.StatusBadRequest, errors.New("name at least one machine, or include_home"))
			return
		}
	}
	sort.Strings(machines)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), fleetKeyRunTimeout)
	defer cancel()
	removed := func(target *pebblestore.FleetKeyTarget) {
		target.State, target.Error, target.Verified, target.RemovedAt = pebblestore.FleetKeyRemoved, "", false, time.Now().UnixMilli()
	}
	stillThere := func(reason string) func(*pebblestore.FleetKeyTarget) {
		return func(target *pebblestore.FleetKeyTarget) {
			target.Error = controlMCPTruncate("not removed: "+reason, 300)
		}
	}
	if home {
		actor, err := s.identitySessions.ActorForUserID(principal.UserID)
		if err != nil || !isCompleteProductActor(actor) || actor.AccountScopeID != account {
			writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
			return
		}
		_, fingerprint := s.fleetKeyOwnHome()
		if _, _, err := s.removeFleetCredentials(fleetKeyActorContext(ctx, actor), account, id, fingerprint); err != nil {
			s.setFleetKeyTarget(account, id, "", true, stillThere(err.Error()))
		} else {
			s.setFleetKeyTarget(account, id, "", true, removed)
		}
	}
	slots := make(chan struct{}, fleetKeyParallel)
	var wg sync.WaitGroup
	for _, name := range machines {
		wg.Add(1)
		slots <- struct{}{}
		go func(name string) {
			defer wg.Done()
			defer func() { <-slots }()
			client, machine, _, err := s.fleetMachineClient(account, name)
			if err != nil {
				s.setFleetKeyTarget(account, id, name, false, stillThere(err.Error()))
				return
			}
			callCtx, cancel := context.WithTimeout(ctx, fleetKeyCallTimeout)
			defer cancel()
			callErr := client.JSON(callCtx, http.MethodPost, fleetKeysPath+"/"+id+"/remove", map[string]string{}, nil)
			s.noteFleetContact(machine, fleetKeyContactError(callErr))
			if callErr != nil {
				s.setFleetKeyTarget(account, id, name, false, stillThere(fleetKeyCallError(callErr, "")))
				return
			}
			s.setFleetKeyTarget(account, id, name, false, removed)
		}(name)
	}
	wg.Wait()
	valueDeleted := false
	updated, err := s.fleet.UpdateFleetKeys(account, func(keys *pebblestore.FleetKeys) error {
		key, ok := keys.Key(id)
		if !ok {
			return pebblestore.ErrFleetKeyNotFound
		}
		if key.RevokedAt == 0 && key.RemovedEverywhere() {
			if err := s.fleetTokens.DeleteFleetKeyValue(account, id); err != nil {
				return err
			}
			key.RevokedAt, valueDeleted = time.Now().UnixMilli(), true
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out, _ := updated.Key(id)
	writeJSON(w, http.StatusOK, map[string]any{"key": out, "value_deleted": valueDeleted})
}
