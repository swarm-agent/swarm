package session

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
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tailscalehttp"
)

const (
	hostedSessionPeerOpenPath          = "/v1/swarm/peer/sessions/open"
	hostedSessionPeerAppendMessagePath = "/v1/swarm/peer/sessions/append_message"
	hostedSessionPeerSetModePath       = "/v1/swarm/peer/sessions/mode"
	hostedSessionPeerSetTitlePath      = "/v1/swarm/peer/sessions/title"
	hostedSessionPeerMetadataPath      = "/v1/swarm/peer/sessions/metadata"
	hostedSessionPeerLifecyclePath     = "/v1/swarm/peer/sessions/lifecycle"

	hostedSessionLocalTransportBaseURL = "http://swarm-local-transport"
)

type HostedSyncClient struct {
	startupConfigPath string
	swarmStore        *pebblestore.SwarmStore
	httpClient        *http.Client
}

func NewHostedSyncClient(startupConfigPath string, swarmStore *pebblestore.SwarmStore) *HostedSyncClient {
	return &HostedSyncClient{
		startupConfigPath: strings.TrimSpace(startupConfigPath),
		swarmStore:        swarmStore,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (c *HostedSyncClient) OpenMirroredSession(ctx context.Context, descriptor HostedSessionDescriptor, session pebblestore.SessionSnapshot) error {
	var response struct {
		OK bool `json:"ok"`
	}
	return c.postJSON(ctx, descriptor, hostedSessionPeerOpenPath, map[string]any{
		"session": session,
	}, &response)
}

func (c *HostedSyncClient) AppendMessage(ctx context.Context, descriptor HostedSessionDescriptor, sessionID, role, content string, metadata map[string]any) (pebblestore.MessageSnapshot, pebblestore.SessionSnapshot, error) {
	var response struct {
		OK      bool                        `json:"ok"`
		Message pebblestore.MessageSnapshot `json:"message"`
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	err := c.postJSON(ctx, descriptor, hostedSessionPeerAppendMessagePath, map[string]any{
		"session_id": sessionID,
		"role":       role,
		"content":    content,
		"metadata":   metadata,
	}, &response)
	if err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, err
	}
	return response.Message, response.Session, nil
}

func (c *HostedSyncClient) SetMode(ctx context.Context, descriptor HostedSessionDescriptor, sessionID, mode string) (pebblestore.SessionSnapshot, error) {
	var response struct {
		OK      bool                        `json:"ok"`
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	err := c.postJSON(ctx, descriptor, hostedSessionPeerSetModePath, map[string]any{
		"session_id": sessionID,
		"mode":       mode,
	}, &response)
	if err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	return response.Session, nil
}

func (c *HostedSyncClient) SetTitle(ctx context.Context, descriptor HostedSessionDescriptor, sessionID, title string) (pebblestore.SessionSnapshot, error) {
	var response struct {
		OK      bool                        `json:"ok"`
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	err := c.postJSON(ctx, descriptor, hostedSessionPeerSetTitlePath, map[string]any{
		"session_id": sessionID,
		"title":      title,
	}, &response)
	if err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	return response.Session, nil
}

func (c *HostedSyncClient) UpdateMetadata(ctx context.Context, descriptor HostedSessionDescriptor, sessionID string, metadata map[string]any) (pebblestore.SessionSnapshot, error) {
	var response struct {
		OK      bool                        `json:"ok"`
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	err := c.postJSON(ctx, descriptor, hostedSessionPeerMetadataPath, map[string]any{
		"session_id": sessionID,
		"metadata":   metadata,
	}, &response)
	if err != nil {
		return pebblestore.SessionSnapshot{}, err
	}
	return response.Session, nil
}

func (c *HostedSyncClient) UpsertLifecycle(ctx context.Context, descriptor HostedSessionDescriptor, snapshot pebblestore.SessionLifecycleSnapshot) error {
	var response struct {
		OK bool `json:"ok"`
	}
	return c.postJSON(ctx, descriptor, hostedSessionPeerLifecyclePath, map[string]any{
		"lifecycle": snapshot,
	}, &response)
}

func (c *HostedSyncClient) postJSON(ctx context.Context, descriptor HostedSessionDescriptor, path string, payload any, out any) error {
	if c == nil {
		return errors.New("hosted sync client is not configured")
	}
	endpoint, client, localTransport, err := c.resolveEndpoint(descriptor, path)
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
		return fmt.Errorf("hosted sync request failed with status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *HostedSyncClient) resolveEndpoint(descriptor HostedSessionDescriptor, path string) (string, *http.Client, bool, error) {
	cfg, err := startupconfig.Load(c.startupConfigPath)
	if err != nil {
		return "", nil, false, err
	}
	socketPath := strings.TrimSpace(cfg.DeployContainer.LocalTransportSocketPath)
	if socketPath != "" {
		return strings.TrimRight(hostedSessionLocalTransportBaseURL, "/") + path, c.newUnixSocketClient(socketPath), true, nil
	}
	baseURL := strings.TrimSpace(descriptor.HostBackendURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(cfg.DeployContainer.HostAPIBaseURL)
	}
	if baseURL == "" {
		return "", nil, false, errors.New("hosted session host backend url is not configured")
	}
	client, err := tailscalehttp.ClientForEndpoint(baseURL, c.httpClient)
	if err != nil {
		return "", nil, false, err
	}
	return strings.TrimRight(baseURL, "/") + path, client, false, nil
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

func (c *HostedSyncClient) newUnixSocketClient(socketPath string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimSpace(socketPath))
	}
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
	}
}
