package permission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const (
	hostedPermissionPeerCreatePath        = "/v1/swarm/peer/permissions/create"
	hostedPermissionPeerWaitPath          = "/v1/swarm/peer/permissions/wait"
	hostedPermissionPeerCancelRunPath     = "/v1/swarm/peer/permissions/cancel_run"
	hostedPermissionPeerMarkStartedPath   = "/v1/swarm/peer/permissions/mark_started"
	hostedPermissionPeerMarkCompletedPath = "/v1/swarm/peer/permissions/mark_completed"

	hostedPermissionLocalTransportBaseURL = "http://swarm-local-transport"
)

type HostedSyncClient struct {
	startupConfigPath string
	swarmStore        *pebblestore.SwarmStore
	httpClient        *http.Client
	waitHTTPClient    *http.Client
}

func NewHostedSyncClient(startupConfigPath string, swarmStore *pebblestore.SwarmStore) *HostedSyncClient {
	return &HostedSyncClient{
		startupConfigPath: strings.TrimSpace(startupConfigPath),
		swarmStore:        swarmStore,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		waitHTTPClient: &http.Client{},
	}
}

func (c *HostedSyncClient) CreatePending(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, input CreateInput) (pebblestore.PermissionRecord, error) {
	var response struct {
		OK         bool                         `json:"ok"`
		Permission pebblestore.PermissionRecord `json:"permission"`
	}
	if err := c.postJSON(ctx, descriptor, hostedPermissionPeerCreatePath, map[string]any{"input": input}, &response, false); err != nil {
		return pebblestore.PermissionRecord{}, err
	}
	return response.Permission, nil
}

func (c *HostedSyncClient) WaitForResolution(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, permissionID string) (pebblestore.PermissionRecord, error) {
	var response struct {
		OK         bool                         `json:"ok"`
		Permission pebblestore.PermissionRecord `json:"permission"`
	}
	if err := c.postJSON(ctx, descriptor, hostedPermissionPeerWaitPath, map[string]any{
		"session_id":    sessionID,
		"permission_id": permissionID,
	}, &response, true); err != nil {
		return pebblestore.PermissionRecord{}, err
	}
	return response.Permission, nil
}

func (c *HostedSyncClient) CancelRunPending(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, runID, reason string) ([]pebblestore.PermissionRecord, error) {
	var response struct {
		OK          bool                           `json:"ok"`
		Permissions []pebblestore.PermissionRecord `json:"permissions"`
	}
	if err := c.postJSON(ctx, descriptor, hostedPermissionPeerCancelRunPath, map[string]any{
		"session_id": sessionID,
		"run_id":     runID,
		"reason":     reason,
	}, &response, false); err != nil {
		return nil, err
	}
	return response.Permissions, nil
}

func (c *HostedSyncClient) MarkToolStarted(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, runID, callID string, step int, startedAt int64) (pebblestore.PermissionRecord, bool, error) {
	var response struct {
		OK         bool                         `json:"ok"`
		Found      bool                         `json:"found"`
		Permission pebblestore.PermissionRecord `json:"permission"`
	}
	if err := c.postJSON(ctx, descriptor, hostedPermissionPeerMarkStartedPath, map[string]any{
		"session_id": sessionID,
		"run_id":     runID,
		"call_id":    callID,
		"step":       step,
		"started_at": startedAt,
	}, &response, false); err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	return response.Permission, response.Found, nil
}

func (c *HostedSyncClient) MarkToolCompleted(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, runID, callID string, step int, result tool.Result, completedAt int64) (pebblestore.PermissionRecord, bool, error) {
	var response struct {
		OK         bool                         `json:"ok"`
		Found      bool                         `json:"found"`
		Permission pebblestore.PermissionRecord `json:"permission"`
	}
	if err := c.postJSON(ctx, descriptor, hostedPermissionPeerMarkCompletedPath, map[string]any{
		"session_id":   sessionID,
		"run_id":       runID,
		"call_id":      callID,
		"step":         step,
		"result":       result,
		"completed_at": completedAt,
	}, &response, false); err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	return response.Permission, response.Found, nil
}

func (c *HostedSyncClient) postJSON(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, path string, payload any, out any, wait bool) error {
	if c == nil {
		return errors.New("hosted permission client is not configured")
	}
	endpoint, client, localTransport, err := c.resolveEndpoint(descriptor, path, wait)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if !localTransport {
		if err := c.addPeerAuthHeaders(req, descriptor.HostSwarmID); err != nil {
			return err
		}
	}
	resp, err := client.Do(req)
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
		return fmt.Errorf("hosted permission request failed with status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *HostedSyncClient) resolveEndpoint(descriptor sessionruntime.HostedSessionDescriptor, path string, wait bool) (string, *http.Client, bool, error) {
	cfg, err := startupconfig.Load(c.startupConfigPath)
	if err != nil {
		return "", nil, false, err
	}
	if socketPath := strings.TrimSpace(cfg.DeployContainer.LocalTransportSocketPath); socketPath != "" {
		return strings.TrimRight(hostedPermissionLocalTransportBaseURL, "/") + path, c.newUnixSocketClient(socketPath, wait), true, nil
	}
	baseURL := strings.TrimSpace(descriptor.HostBackendURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(cfg.DeployContainer.HostAPIBaseURL)
	}
	if baseURL == "" {
		return "", nil, false, errors.New("hosted permission host backend url is not configured")
	}
	if wait {
		return strings.TrimRight(baseURL, "/") + path, c.waitHTTPClient, false, nil
	}
	return strings.TrimRight(baseURL, "/") + path, c.httpClient, false, nil
}

func (c *HostedSyncClient) addPeerAuthHeaders(req *http.Request, hostSwarmID string) error {
	if req == nil {
		return errors.New("request is required")
	}
	if c.swarmStore == nil {
		return errors.New("swarm store is not configured")
	}
	hostSwarmID = strings.TrimSpace(hostSwarmID)
	if hostSwarmID == "" {
		return errors.New("host swarm id is required")
	}
	localNode, ok, err := c.swarmStore.GetLocalNode()
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(localNode.SwarmID) == "" {
		return errors.New("local swarm id is not configured")
	}
	peer, ok, err := c.swarmStore.GetTrustedPeer(hostSwarmID)
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(peer.OutgoingPeerAuthToken) == "" {
		return fmt.Errorf("trusted peer %q is missing outgoing peer auth", hostSwarmID)
	}
	req.Header.Set("X-Swarm-Peer-ID", strings.TrimSpace(localNode.SwarmID))
	req.Header.Set("X-Swarm-Peer-Token", strings.TrimSpace(peer.OutgoingPeerAuthToken))
	return nil
}

func (c *HostedSyncClient) newUnixSocketClient(socketPath string, wait bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimSpace(socketPath))
	}
	client := &http.Client{Transport: transport}
	if !wait {
		client.Timeout = 15 * time.Second
	}
	return client
}
