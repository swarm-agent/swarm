package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

const (
	peerMirrorSnapshotPath = "/v1/swarm/peer/mirror/snapshot"
	peerMirrorWatchPath    = "/v1/swarm/peer/mirror/watch"

	mirrorResourceHost       = "host"
	mirrorResourceWorkspace  = "workspace"
	mirrorResourceContainer  = "container"
	mirrorResourceDeployment = "deployment"
	mirrorResourceTarget     = "target"

	managedMirrorSyncInterval = 3 * time.Second
	managedMirrorHTTPTimeout  = 5 * time.Second
)

type peerMirrorResource struct {
	Kind     string          `json:"kind"`
	ID       string          `json:"id"`
	Sequence uint64          `json:"sequence"`
	Resource json.RawMessage `json:"resource"`
}

type peerMirrorSnapshotResponse struct {
	OK             bool                                 `json:"ok"`
	SwarmID        string                               `json:"swarm_id,omitempty"`
	Sequence       uint64                               `json:"sequence"`
	Resources      []peerMirrorResource                 `json:"resources"`
	Events         []pebblestore.SwarmMirrorEventRecord `json:"events,omitempty"`
	ResyncRequired bool                                 `json:"resync_required,omitempty"`
	ResyncReason   string                               `json:"resync_reason,omitempty"`
}

type peerMirrorWatchResponse struct {
	OK             bool                                 `json:"ok"`
	SwarmID        string                               `json:"swarm_id,omitempty"`
	Sequence       uint64                               `json:"sequence"`
	Events         []pebblestore.SwarmMirrorEventRecord `json:"events"`
	Bookmark       *pebblestore.SwarmMirrorEventRecord  `json:"bookmark,omitempty"`
	ResyncRequired bool                                 `json:"resync_required,omitempty"`
	ResyncReason   string                               `json:"resync_reason,omitempty"`
}

type peerMirrorHostResource struct {
	SwarmID        string `json:"swarm_id"`
	Name           string `json:"name"`
	Role           string `json:"role"`
	PairingState   string `json:"pairing_state,omitempty"`
	ParentSwarmID  string `json:"parent_swarm_id,omitempty"`
	CurrentGroupID string `json:"current_group_id,omitempty"`
	BackendURL     string `json:"backend_url,omitempty"`
	DesktopURL     string `json:"desktop_url,omitempty"`
	Online         bool   `json:"online"`
	UpdatedAt      int64  `json:"updated_at"`
}

type swarmMirrorResourceView struct {
	ManagedSwarmID string          `json:"managed_swarm_id"`
	Kind           string          `json:"kind"`
	ID             string          `json:"id"`
	Sequence       uint64          `json:"sequence"`
	UpdatedAt      int64           `json:"updated_at"`
	Resource       json.RawMessage `json:"resource"`
}

type swarmMirrorResourcesResponse struct {
	OK        bool                      `json:"ok"`
	Resources []swarmMirrorResourceView `json:"resources"`
}

func (s *Server) SetSwarmMirrorStore(store *pebblestore.SwarmMirrorStore) {
	if s == nil {
		return
	}
	s.swarmMirror = store
}

func (s *Server) StartManagedMirrorSync(ctx context.Context) {
	if s == nil || s.swarmMirror == nil || s.swarm == nil {
		return
	}
	if !s.mirrorSyncStarted.CompareAndSwap(false, true) {
		return
	}
	go s.managedMirrorSyncLoop(ctx)
}

func (s *Server) handleSwarmMirrorResources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s == nil || s.swarmMirror == nil {
		writeJSON(w, http.StatusOK, swarmMirrorResourcesResponse{OK: true, Resources: []swarmMirrorResourceView{}})
		return
	}
	resources, err := s.swarmMirror.ListRemoteResources(strings.TrimSpace(r.URL.Query().Get("managed_swarm_id")), parseMirrorResources(r), 100000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]swarmMirrorResourceView, 0, len(resources))
	for _, resource := range resources {
		out = append(out, swarmMirrorResourceView{ManagedSwarmID: resource.ManagedSwarmID, Kind: resource.Kind, ID: resource.ID, Sequence: resource.Sequence, UpdatedAt: resource.UpdatedAt, Resource: append([]byte(nil), resource.Resource...)})
	}
	writeJSON(w, http.StatusOK, swarmMirrorResourcesResponse{OK: true, Resources: out})
}

func (s *Server) handlePeerMirrorSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.requirePeerAuth(w, r) {
		return
	}
	if s.swarmMirror == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm mirror store is not configured"))
		return
	}
	if err := s.refreshLocalMirrorProjections(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	current, err := s.swarmMirror.CurrentLocalSequence()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	since, err := parseUintQuery(r, "since_seq")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if since > current {
		writeJSON(w, http.StatusConflict, peerMirrorSnapshotResponse{OK: false, Sequence: current, ResyncRequired: true, ResyncReason: "cursor is newer than current mirror sequence"})
		return
	}
	resources, err := s.swarmMirror.ListLocalResources(parseMirrorResources(r), 100000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]peerMirrorResource, 0, len(resources))
	for _, resource := range resources {
		out = append(out, peerMirrorResource{Kind: resource.Kind, ID: resource.ID, Sequence: resource.Sequence, Resource: append([]byte(nil), resource.Resource...)})
	}
	var events []pebblestore.SwarmMirrorEventRecord
	if since > 0 {
		events, err = s.swarmMirror.ListLocalEventsSince(since, 1000)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, peerMirrorSnapshotResponse{OK: true, SwarmID: s.localSwarmID(), Sequence: current, Resources: out, Events: events})
}

func (s *Server) handlePeerMirrorWatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.requirePeerAuth(w, r) {
		return
	}
	if s.swarmMirror == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm mirror store is not configured"))
		return
	}
	since, err := parseUintQuery(r, "since_seq")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	wait := parseMirrorWait(r)
	deadline := time.Now().Add(wait)
	for {
		if err := s.refreshLocalMirrorProjections(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		current, err := s.swarmMirror.CurrentLocalSequence()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if since > current {
			writeJSON(w, http.StatusConflict, peerMirrorWatchResponse{OK: false, Sequence: current, ResyncRequired: true, ResyncReason: "cursor is newer than current mirror sequence"})
			return
		}
		events, err := s.swarmMirror.ListLocalEventsSince(since, 1000)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if len(events) > 0 || wait <= 0 || time.Now().After(deadline) {
			resp := peerMirrorWatchResponse{OK: true, SwarmID: s.localSwarmID(), Sequence: current, Events: events}
			if len(events) == 0 && mirrorAllowBookmarks(r) {
				bookmark := pebblestore.SwarmMirrorEventRecord{Sequence: current, EventType: pebblestore.SwarmMirrorEventTypeBookmark, TsUnixMs: time.Now().UnixMilli()}
				resp.Bookmark = &bookmark
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (s *Server) refreshLocalMirrorProjections(ctx context.Context) error {
	if s == nil || s.swarmMirror == nil {
		return errors.New("swarm mirror store is not configured")
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return err
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return err
	}
	localSwarmID := strings.TrimSpace(state.Node.SwarmID)
	backendURL := firstNonEmpty(firstLocalTransportURL(state.Node.Transports), strings.TrimSpace(state.Node.AdvertiseAddr))
	host := peerMirrorHostResource{SwarmID: localSwarmID, Name: firstNonEmpty(strings.TrimSpace(state.Node.Name), strings.TrimSpace(cfg.SwarmName), localSwarmID), Role: strings.TrimSpace(state.Node.Role), PairingState: strings.TrimSpace(state.Pairing.PairingState), ParentSwarmID: strings.TrimSpace(state.Pairing.ParentSwarmID), CurrentGroupID: strings.TrimSpace(state.CurrentGroupID), BackendURL: backendURL, DesktopURL: backendURL, Online: true}
	if _, _, err := s.swarmMirror.UpsertLocalResource(mirrorResourceHost, firstNonEmpty(localSwarmID, "local"), host); err != nil {
		return err
	}
	if s.workspace != nil {
		entries, err := s.workspace.ListKnown(100000)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			id := strings.TrimSpace(entry.Path)
			if id == "" {
				continue
			}
			if _, _, err := s.swarmMirror.UpsertLocalResource(mirrorResourceWorkspace, id, entry); err != nil {
				return err
			}
		}
	}
	if s.localContainers != nil {
		containers, err := s.localContainers.List(ctx)
		if err != nil {
			return err
		}
		for _, item := range containers {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				continue
			}
			if _, _, err := s.swarmMirror.UpsertLocalResource(mirrorResourceContainer, id, item); err != nil {
				return err
			}
		}
	}
	if s.deployContainers != nil {
		deployments, err := s.deployContainers.List(ctx)
		if err != nil {
			return err
		}
		for _, item := range deployments {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				continue
			}
			if _, _, err := s.swarmMirror.UpsertLocalResource(mirrorResourceDeployment, id, item); err != nil {
				return err
			}
			if target, ok := mapDeployContainerTarget(item); ok {
				if _, _, err := s.swarmMirror.UpsertLocalResource(mirrorResourceTarget, firstNonEmpty(target.SwarmID, target.DeploymentID), target); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Server) managedMirrorSyncLoop(ctx context.Context) {
	ticker := time.NewTicker(managedMirrorSyncInterval)
	defer ticker.Stop()
	s.syncManagedMirrorsOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncManagedMirrorsOnce(ctx)
		}
	}
}

func (s *Server) syncManagedMirrorsOnce(ctx context.Context) {
	if s == nil || s.swarmMirror == nil || s.swarm == nil {
		return
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		log.Printf("managed mirror sync: load startup config: %v", err)
		return
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		log.Printf("managed mirror sync: load swarm state: %v", err)
		return
	}
	for _, target := range listTrustedPeerTargets(state.TrustedPeers) {
		if strings.TrimSpace(target.BackendURL) == "" || strings.EqualFold(target.SwarmID, state.Node.SwarmID) {
			continue
		}
		if err := s.syncMirrorFromTarget(ctx, target); err != nil {
			log.Printf("managed mirror sync: target=%q err=%v", strings.TrimSpace(target.SwarmID), err)
		}
	}
}

func (s *Server) syncMirrorFromTarget(ctx context.Context, target swarmTarget) error {
	if s == nil || s.swarmMirror == nil {
		return errors.New("swarm mirror store is not configured")
	}
	managedSwarmID := strings.TrimSpace(target.SwarmID)
	if managedSwarmID == "" {
		return errors.New("target swarm id is required")
	}
	cursor, ok, err := s.swarmMirror.GetRemoteCursor(managedSwarmID)
	if err != nil {
		return err
	}
	since := uint64(0)
	if ok {
		since = cursor.LastSequence
	}
	var snapshot peerMirrorSnapshotResponse
	path := fmt.Sprintf("%s?resources=host,workspaces,containers,deployments,targets&since_seq=%d", peerMirrorSnapshotPath, since)
	if err := s.getPeerJSONFromSwarmTarget(ctx, target, path, &snapshot); err != nil {
		return err
	}
	if snapshot.ResyncRequired {
		path = peerMirrorSnapshotPath + "?resources=host,workspaces,containers,deployments,targets"
		if err := s.getPeerJSONFromSwarmTarget(ctx, target, path, &snapshot); err != nil {
			return err
		}
	}
	if !snapshot.OK {
		return errors.New("mirror snapshot failed")
	}
	maxSeq := since
	for _, resource := range snapshot.Resources {
		if resource.Sequence == 0 || resource.Sequence <= since {
			continue
		}
		event := pebblestore.SwarmMirrorEventRecord{Sequence: resource.Sequence, EventType: pebblestore.SwarmMirrorEventTypeUpsert, Kind: resource.Kind, ID: resource.ID, Resource: resource.Resource, TsUnixMs: time.Now().UnixMilli()}
		if err := s.applyRemoteMirrorEvent(managedSwarmID, event); err != nil {
			return err
		}
		if resource.Sequence > maxSeq {
			maxSeq = resource.Sequence
		}
	}
	for _, event := range snapshot.Events {
		if event.Sequence == 0 || event.Sequence <= since {
			continue
		}
		if err := s.applyRemoteMirrorEvent(managedSwarmID, event); err != nil {
			return err
		}
		if event.Sequence > maxSeq {
			maxSeq = event.Sequence
		}
	}
	if snapshot.Sequence > maxSeq {
		maxSeq = snapshot.Sequence
	}
	_, err = s.swarmMirror.SetRemoteCursor(managedSwarmID, maxSeq)
	return err
}

func (s *Server) applyRemoteMirrorEvent(managedSwarmID string, event pebblestore.SwarmMirrorEventRecord) error {
	if event.EventType == pebblestore.SwarmMirrorEventTypeBookmark || event.Kind == "" || event.ID == "" {
		return nil
	}
	if _, err := s.swarmMirror.UpsertRemoteResource(managedSwarmID, event); err != nil {
		return err
	}
	if event.Kind == mirrorResourceHost && s.swarmNodes != nil && event.EventType != pebblestore.SwarmMirrorEventTypeDelete {
		var host peerMirrorHostResource
		if err := json.Unmarshal(event.Resource, &host); err == nil && strings.TrimSpace(host.SwarmID) != "" && strings.TrimSpace(host.BackendURL) != "" {
			_, _ = s.swarmNodes.Put(pebblestore.SwarmNodeRecord{SwarmID: strings.TrimSpace(host.SwarmID), Name: firstNonEmpty(strings.TrimSpace(host.Name), strings.TrimSpace(host.SwarmID)), Role: firstNonEmpty(strings.TrimSpace(host.Role), "child"), Kind: "remote", Transport: "tailscale", BackendURL: strings.TrimSpace(host.BackendURL), DesktopURL: strings.TrimSpace(host.DesktopURL), Source: "mirror", Status: "online", LastSeenAt: time.Now().UnixMilli()})
		}
	}
	if s.events != nil {
		payload, _ := json.Marshal(map[string]any{"managed_swarm_id": managedSwarmID, "kind": event.Kind, "id": event.ID, "sequence": event.Sequence})
		envelope, err := s.events.Append("swarm.mirror", "swarm.mirror.updated", managedSwarmID, payload, "", "")
		if err == nil && s.hub != nil {
			s.hub.Publish(envelope)
		}
	}
	return nil
}

func (s *Server) getPeerJSONFromSwarmTarget(ctx context.Context, target swarmTarget, path string, out any) error {
	if s.swarm == nil {
		return errors.New("swarm service not configured")
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return err
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return err
	}
	peerToken, err := s.outgoingPeerAuthTokenForTarget(nil, target)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, managedMirrorHTTPTimeout)
	defer cancel()
	endpoint := strings.TrimRight(strings.TrimSpace(target.BackendURL), "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set(peerAuthSwarmIDHeader, strings.TrimSpace(state.Node.SwarmID))
	req.Header.Set(peerAuthTokenHeader, peerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&failure)
		if strings.TrimSpace(failure.Error) != "" {
			return errors.New(strings.TrimSpace(failure.Error))
		}
		return errors.New(resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (s *Server) listMirroredSwarmTargets() ([]swarmTarget, error) {
	if s == nil || s.swarmMirror == nil {
		return nil, nil
	}
	resources, err := s.swarmMirror.ListRemoteResources("", []string{mirrorResourceTarget}, 100000)
	if err != nil {
		return nil, err
	}
	out := make([]swarmTarget, 0, len(resources))
	for _, resource := range resources {
		var target swarmTarget
		if err := json.Unmarshal(resource.Resource, &target); err != nil {
			continue
		}
		if strings.TrimSpace(target.SwarmID) == "" {
			continue
		}
		target.Kind = "mirrored"
		if target.Relationship == "" {
			target.Relationship = "child"
		}
		if !target.Online {
			target.Selectable = false
		}
		out = append(out, target)
	}
	return out, nil
}

func parseMirrorResources(r *http.Request) []string {
	if r == nil || r.URL == nil {
		return nil
	}
	raw := strings.TrimSpace(r.URL.Query().Get("resources"))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		kind := normalizeMirrorResourceKind(part)
		if kind != "" {
			out = append(out, kind)
		}
	}
	return out
}

func normalizeMirrorResourceKind(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.TrimSuffix(value, "s")
	switch value {
	case mirrorResourceHost, mirrorResourceWorkspace, mirrorResourceContainer, mirrorResourceDeployment, mirrorResourceTarget:
		return value
	default:
		return ""
	}
}

func parseUintQuery(r *http.Request, key string) (uint64, error) {
	if r == nil || r.URL == nil {
		return 0, nil
	}
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an unsigned integer", key)
	}
	return value, nil
}

func parseMirrorWait(r *http.Request) time.Duration {
	if r == nil || r.URL == nil {
		return 0
	}
	raw := strings.TrimSpace(r.URL.Query().Get("wait_ms"))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0
	}
	if value > 5000 {
		value = 5000
	}
	return time.Duration(value) * time.Millisecond
}

func mirrorAllowBookmarks(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("allow_bookmarks")))
	return value == "1" || value == "true" || value == "yes"
}

func (s *Server) localSwarmID() string {
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return ""
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(state.Node.SwarmID)
}

func firstLocalTransportURL(transports []swarmruntime.TransportSummary) string {
	if endpoint := firstTrustedPeerTransportForKind(transports, "tailscale"); endpoint != "" {
		return normalizeRemoteSwarmEndpoint(endpoint)
	}
	for _, transport := range transports {
		if endpoint := firstTrustedPeerTransportValue(transport); endpoint != "" {
			return normalizeRemoteSwarmEndpoint(endpoint)
		}
	}
	return ""
}
