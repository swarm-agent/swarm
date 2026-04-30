package session

import (
	"context"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	HostedSessionMetadataEnabled              = "swarm_routed_session"
	HostedSessionMetadataHostSwarmID          = "swarm_routed_host_swarm_id"
	HostedSessionMetadataHostBackendURL       = "swarm_routed_host_backend_url"
	HostedSessionMetadataHostWorkspacePath    = "swarm_routed_host_workspace_path"
	HostedSessionMetadataRuntimeWorkspacePath = "swarm_routed_runtime_workspace_path"
	HostedSessionMetadataChildSwarmID         = "swarm_routed_child_swarm_id"
)

type HostedSessionDescriptor struct {
	HostSwarmID          string
	HostBackendURL       string
	HostWorkspacePath    string
	RuntimeWorkspacePath string
	ChildSwarmID         string
}

type HostedSessionSync interface {
	AppendMessage(ctx context.Context, descriptor HostedSessionDescriptor, sessionID, role, content string, metadata map[string]any) (pebblestore.MessageSnapshot, pebblestore.SessionSnapshot, error)
	SetMode(ctx context.Context, descriptor HostedSessionDescriptor, sessionID, mode string) (pebblestore.SessionSnapshot, error)
	SetTitle(ctx context.Context, descriptor HostedSessionDescriptor, sessionID, title string) (pebblestore.SessionSnapshot, error)
	UpdateMetadata(ctx context.Context, descriptor HostedSessionDescriptor, sessionID string, metadata map[string]any) (pebblestore.SessionSnapshot, error)
	UpsertLifecycle(ctx context.Context, descriptor HostedSessionDescriptor, snapshot pebblestore.SessionLifecycleSnapshot) error
	PublishEvent(ctx context.Context, descriptor HostedSessionDescriptor, sessionID, eventType string, payload map[string]any, causationID, correlationID string) (pebblestore.EventEnvelope, error)
}

func HostedSessionFromMetadata(metadata map[string]any) (HostedSessionDescriptor, bool) {
	if len(metadata) == 0 {
		return HostedSessionDescriptor{}, false
	}
	enabled, ok := metadata[HostedSessionMetadataEnabled]
	if ok {
		switch typed := enabled.(type) {
		case bool:
			if !typed {
				return HostedSessionDescriptor{}, false
			}
		case string:
			if strings.TrimSpace(strings.ToLower(typed)) != "true" {
				return HostedSessionDescriptor{}, false
			}
		}
	}
	hostSwarmID := stringMetadataValue(metadata, HostedSessionMetadataHostSwarmID)
	hostBackendURL := stringMetadataValue(metadata, HostedSessionMetadataHostBackendURL)
	hostWorkspacePath := stringMetadataValue(metadata, HostedSessionMetadataHostWorkspacePath)
	runtimeWorkspacePath := stringMetadataValue(metadata, HostedSessionMetadataRuntimeWorkspacePath)
	childSwarmID := stringMetadataValue(metadata, HostedSessionMetadataChildSwarmID)
	if hostSwarmID == "" {
		return HostedSessionDescriptor{}, false
	}
	return HostedSessionDescriptor{
		HostSwarmID:          hostSwarmID,
		HostBackendURL:       hostBackendURL,
		HostWorkspacePath:    hostWorkspacePath,
		RuntimeWorkspacePath: runtimeWorkspacePath,
		ChildSwarmID:         childSwarmID,
	}, true
}

func HostedSessionFromMetadataForLocal(metadata map[string]any, localSwarmID string) (HostedSessionDescriptor, bool) {
	descriptor, hosted := HostedSessionFromMetadata(metadata)
	if !hosted {
		return HostedSessionDescriptor{}, false
	}
	localSwarmID = strings.TrimSpace(localSwarmID)
	if localSwarmID != "" && strings.EqualFold(strings.TrimSpace(descriptor.HostSwarmID), localSwarmID) {
		return HostedSessionDescriptor{}, false
	}
	return descriptor, true
}

func (d HostedSessionDescriptor) WithMetadata(metadata map[string]any) map[string]any {
	cloned := cloneSessionMetadataMap(metadata)
	if cloned == nil {
		cloned = make(map[string]any, 6)
	}
	cloned[HostedSessionMetadataEnabled] = true
	cloned[HostedSessionMetadataHostSwarmID] = strings.TrimSpace(d.HostSwarmID)
	cloned[HostedSessionMetadataHostBackendURL] = strings.TrimSpace(d.HostBackendURL)
	if strings.TrimSpace(d.HostWorkspacePath) != "" {
		cloned[HostedSessionMetadataHostWorkspacePath] = strings.TrimSpace(d.HostWorkspacePath)
	}
	if strings.TrimSpace(d.RuntimeWorkspacePath) != "" {
		cloned[HostedSessionMetadataRuntimeWorkspacePath] = strings.TrimSpace(d.RuntimeWorkspacePath)
	}
	if strings.TrimSpace(d.ChildSwarmID) != "" {
		cloned[HostedSessionMetadataChildSwarmID] = strings.TrimSpace(d.ChildSwarmID)
	}
	return cloned
}

func stringMetadataValue(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func adaptHostedSessionForRuntime(session pebblestore.SessionSnapshot) pebblestore.SessionSnapshot {
	descriptor, ok := HostedSessionFromMetadata(session.Metadata)
	if !ok {
		return session
	}
	if runtimeWorkspacePath := strings.TrimSpace(descriptor.RuntimeWorkspacePath); runtimeWorkspacePath != "" {
		session.WorkspacePath = runtimeWorkspacePath
	}
	return session
}

func adaptHostedSessionForLocalRuntime(session pebblestore.SessionSnapshot, localSwarmID string) pebblestore.SessionSnapshot {
	descriptor, ok := HostedSessionFromMetadata(session.Metadata)
	if !ok {
		return session
	}
	hostWorkspacePath := strings.TrimSpace(descriptor.HostWorkspacePath)
	runtimeWorkspacePath := strings.TrimSpace(descriptor.RuntimeWorkspacePath)
	localSwarmID = strings.TrimSpace(localSwarmID)
	if localSwarmID != "" && strings.EqualFold(strings.TrimSpace(descriptor.HostSwarmID), localSwarmID) {
		if hostWorkspacePath != "" {
			session.WorkspacePath = hostWorkspacePath
		}
		return session
	}
	if localSwarmID == "" && hostWorkspacePath != "" && strings.TrimSpace(session.WorkspacePath) == hostWorkspacePath {
		return session
	}
	if runtimeWorkspacePath != "" {
		session.WorkspacePath = runtimeWorkspacePath
	}
	return session
}
