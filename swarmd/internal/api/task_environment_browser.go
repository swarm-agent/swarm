package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

var errTaskBrowserUnavailable = errors.New("browser access unavailable; refresh attachment and inspect or rebuild the deployment")

type taskBrowserSnapshot struct {
	attachment  environments.TaskEnvironmentAttachment
	deployment  environments.Deployment
	environment environments.Environment
	connection  environments.Connection
}

// Read authority both before and after network I/O. This action creates no lease,
// operation or task mutation, and is never invoked by attachment projections.
func (s *Server) taskBrowserSnapshot(p identity.Principal, sessionID string, req tool.TaskEnvironmentRequest) (*pebblestore.ProjectTaskRecord, taskBrowserSnapshot, error) {
	var snap taskBrowserSnapshot
	task, err := s.authorizeTaskEnvironment(p, sessionID, req.ProjectID, req.TaskID)
	if err != nil || req.ExpectedAttachmentRevision <= 0 || req.WorkspaceID == "" || s.deployments == nil || s.environments == nil || s.connections == nil {
		return nil, snap, errTaskBrowserUnavailable
	}
	for _, a := range task.EnvironmentAttachments {
		if a.ID == req.AttachmentID {
			snap.attachment = a
			break
		}
	}
	a := snap.attachment
	if a.ID == "" || a.AccountScopeID != p.AccountScopeID || a.ProjectID != req.ProjectID || a.TaskID != req.TaskID || a.Revision != req.ExpectedAttachmentRevision || a.Source.WorkspaceID != req.WorkspaceID || a.EffectiveState(task.ActiveAttemptID, time.Now().UnixMilli()) != "ready" {
		return nil, snap, errTaskBrowserUnavailable
	}
	if err := s.validateTaskEnvironmentSource(p, task, a.Source); err != nil {
		return nil, snap, errTaskBrowserUnavailable
	}
	d, found, err := s.deployments.GetDeployment(p.AccountScopeID, req.WorkspaceID, a.Source.DeploymentID)
	if err != nil || !found || !a.Source.Matches(d) || d.ReviewExpired(time.Now().UnixMilli()) || !d.IsRunning() {
		return nil, snap, errTaskBrowserUnavailable
	}
	e, found, err := s.environments.Get(p.AccountScopeID, req.WorkspaceID, d.EnvironmentID)
	if err != nil || !found || e.AccountScopeID != p.AccountScopeID || e.WorkspaceID != req.WorkspaceID || e.ID != d.EnvironmentID || e.Build == nil || e.Build.Digest() != a.Source.Build.DefinitionDigest || d.Frontend == nil || environments.ValidateFrontendEndpoints(d.Frontend.Endpoints, d.Frontend.Ports) != nil {
		return nil, snap, errTaskBrowserUnavailable
	}
	c, found, err := s.connections.Get(p.AccountScopeID, req.WorkspaceID, d.ConnectionID)
	if err != nil || !found || c.AccountScopeID != p.AccountScopeID || c.WorkspaceID != req.WorkspaceID || c.ID != d.ConnectionID || (c.Kind != environments.ConnectionKindLocalDocker && c.Kind != environments.ConnectionKindLocalPodman) {
		return nil, snap, errTaskBrowserUnavailable
	}
	if c.SSH != nil || (c.Kind == environments.ConnectionKindLocalPodman && c.LocalDocker != nil) || (c.LocalDocker != nil && c.LocalDocker.Host != "" && !strings.HasPrefix(c.LocalDocker.Host, "unix:///")) {
		return nil, snap, errTaskBrowserUnavailable
	}
	snap.deployment, snap.environment, snap.connection = *d.Clone(), *e.Clone(), *c.Clone()
	return task, snap, nil
}

func (s *Server) taskBrowserEndpoints(ctx context.Context, p identity.Principal, sessionID string, req tool.TaskEnvironmentRequest) (tool.TaskEnvironmentResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := tool.TaskEnvironmentResult{BrowserEndpoints: []tool.TaskBrowserEndpoint{}}
	_, before, err := s.taskBrowserSnapshot(p, sessionID, req)
	if err != nil {
		return result, err
	}
	if ctx.Err() != nil {
		return tool.TaskEnvironmentResult{}, errTaskBrowserUnavailable
	}
	// No ambient proxies, cookies, authentication or redirect following. HTTPS
	// retains the standard certificate/hostname verification.
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second, MaxResponseHeaderBytes: 16 << 10, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// Validation caps the snapshot at eight endpoints. Each independent probe has
	// a one-second budget; slow services cannot starve later healthy endpoints.
	endpoints := before.deployment.Frontend.Endpoints
	result.BrowserEndpoints = make([]tool.TaskBrowserEndpoint, len(endpoints))
	var probes sync.WaitGroup
	for i, endpoint := range endpoints {
		probes.Add(1)
		go func(i int, endpoint environments.FrontendEndpoint) {
			defer probes.Done()
			result.BrowserEndpoints[i] = probeTaskBrowserEndpoint(ctx, client, endpoint, before.deployment.Runtime.AssignedPorts)
		}(i, endpoint)
	}
	probes.Wait()
	task, after, err := s.taskBrowserSnapshot(p, sessionID, req)
	if err != nil || ctx.Err() != nil || !reflect.DeepEqual(before, after) {
		return tool.TaskEnvironmentResult{}, errTaskBrowserUnavailable
	}
	result.TaskRevision = task.Revision
	result.Attachments = s.taskEnvironmentProjection(ctx, p, task)
	if ctx.Err() != nil {
		return tool.TaskEnvironmentResult{}, errTaskBrowserUnavailable
	}
	return result, nil
}

func taskBrowserOrigin(ports []environments.AssignedPort, containerPort int, scheme string) (*url.URL, int, bool) {
	var selected environments.AssignedPort
	matches := 0
	for _, port := range ports {
		if port.ContainerPort == containerPort && (port.Protocol == "tcp" || port.Protocol == "") {
			selected, matches = port, matches+1
		}
	}
	if matches != 1 || len(selected.EndpointURL) > 2048 || selected.HostPort < 1 || selected.HostPort > 65535 || (scheme != "http" && scheme != "https") {
		return nil, 0, false
	}
	u, err := url.Parse(selected.EndpointURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "tcp") || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return nil, 0, false
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") || u.Port() != strconv.Itoa(selected.HostPort) {
		return nil, 0, false
	}
	return &url.URL{Scheme: scheme, Host: net.JoinHostPort(ip.String(), strconv.Itoa(selected.HostPort))}, selected.HostPort, true
}

func probeTaskBrowserEndpoint(ctx context.Context, client *http.Client, endpoint environments.FrontendEndpoint, ports []environments.AssignedPort) tool.TaskBrowserEndpoint {
	result := tool.TaskBrowserEndpoint{ID: endpoint.ID, Name: endpoint.Name, ContainerPort: endpoint.ContainerPort, Error: "No verified local TCP mapping; inspect deployment ports."}
	origin, hostPort, ok := taskBrowserOrigin(ports, endpoint.ContainerPort, endpoint.Scheme)
	path := endpoint.Path
	if path == "" {
		path = "/"
	}
	if !ok || !environments.SafeFrontendPath(path) || !environments.SafeFrontendPath(endpoint.HealthPath) {
		return result
	}
	result.HostPort = hostPort
	result.Error = "Frontend not ready; inspect service health and check again."
	origin.Path = endpoint.HealthPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.String(), nil)
	if err != nil {
		return result
	}
	response, err := client.Do(req)
	if err != nil {
		return result
	}
	// Do not read or disclose provider response bodies. Closing without draining
	// bounds body handling even for an infinite/chunked response.
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result
	}
	origin.Path = path
	result.URL, result.Ready, result.Error = origin.String(), true, ""
	return result
}
