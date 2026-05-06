package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

type SwarmNodeRecord struct {
	SwarmID      string `json:"swarm_id"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	Kind         string `json:"kind"`
	Transport    string `json:"transport"`
	BackendURL   string `json:"backend_url"`
	DesktopURL   string `json:"desktop_url,omitempty"`
	MagicDNSName string `json:"magic_dns_name,omitempty"`
	TailnetFQDN  string `json:"tailnet_fqdn,omitempty"`
	TailscaleIP  string `json:"tailscale_ip,omitempty"`
	DeploymentID string `json:"deployment_id,omitempty"`
	Source       string `json:"source,omitempty"`
	Status       string `json:"status"`
	LastSeenAt   int64  `json:"last_seen_at,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

type SwarmNodeStore struct {
	store *Store
}

func NewSwarmNodeStore(store *Store) *SwarmNodeStore {
	return &SwarmNodeStore{store: store}
}

func (s *SwarmNodeStore) Get(swarmID string) (SwarmNodeRecord, bool, error) {
	if s == nil || s.store == nil {
		return SwarmNodeRecord{}, false, nil
	}
	swarmID = normalizeSwarmNodeSwarmID(swarmID)
	if swarmID == "" {
		return SwarmNodeRecord{}, false, errors.New("swarm node swarm id is required")
	}
	if isForbiddenSwarmNodeSwarmID(swarmID) {
		return SwarmNodeRecord{}, false, nil
	}
	var record SwarmNodeRecord
	ok, err := s.store.GetJSON(KeySwarmNode(swarmID), &record)
	if err != nil {
		return SwarmNodeRecord{}, false, err
	}
	if !ok {
		return SwarmNodeRecord{}, false, nil
	}
	record = normalizeSwarmNodeRecord(record)
	if record.SwarmID == "" {
		record.SwarmID = swarmID
	}
	return record, true, nil
}

func (s *SwarmNodeStore) Put(record SwarmNodeRecord) (SwarmNodeRecord, error) {
	if s == nil || s.store == nil {
		return SwarmNodeRecord{}, errors.New("swarm node store is not configured")
	}
	record = normalizeSwarmNodeRecord(record)
	if record.SwarmID == "" {
		return SwarmNodeRecord{}, errors.New("swarm node swarm id is required")
	}
	if isForbiddenSwarmNodeSwarmID(record.SwarmID) {
		return SwarmNodeRecord{}, errors.New("swarm node swarm id must be the real child swarm id; remote-deploy fallback ids are forbidden")
	}
	if record.Name == "" {
		record.Name = record.SwarmID
	}
	if record.BackendURL == "" {
		return SwarmNodeRecord{}, errors.New("swarm node backend url is required")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeySwarmNode(record.SwarmID), record); err != nil {
		return SwarmNodeRecord{}, err
	}
	return record, nil
}

func (s *SwarmNodeStore) Delete(swarmID string) error {
	if s == nil || s.store == nil {
		return errors.New("swarm node store is not configured")
	}
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		return errors.New("swarm node swarm id is required")
	}
	return s.store.Delete(KeySwarmNode(swarmID))
}

func (s *SwarmNodeStore) List(limit int) ([]SwarmNodeRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]SwarmNodeRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(SwarmNodePrefix(), limit, func(key string, value []byte) error {
		var record SwarmNodeRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode swarm node: %w", err)
		}
		record = normalizeSwarmNodeRecord(record)
		if record.SwarmID == "" {
			record.SwarmID = decodeSwarmNodeSwarmIDFromKey(key)
		}
		if record.SwarmID == "" || isForbiddenSwarmNodeSwarmID(record.SwarmID) || record.Name == "" || record.BackendURL == "" {
			return nil
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func normalizeSwarmNodeRecord(record SwarmNodeRecord) SwarmNodeRecord {
	record.SwarmID = normalizeSwarmNodeSwarmID(record.SwarmID)
	record.Name = strings.TrimSpace(record.Name)
	record.Role = normalizeSwarmNodeRole(record.Role)
	record.Kind = normalizeSwarmNodeKind(record.Kind)
	record.Transport = normalizeSwarmNodeTransport(record.Transport)
	record.BackendURL = normalizeSwarmNodeURL(record.BackendURL)
	record.DesktopURL = normalizeSwarmNodeURL(record.DesktopURL)
	record.MagicDNSName = strings.TrimSpace(record.MagicDNSName)
	record.TailnetFQDN = strings.TrimSpace(record.TailnetFQDN)
	record.TailscaleIP = strings.TrimSpace(record.TailscaleIP)
	record.DeploymentID = strings.TrimSpace(record.DeploymentID)
	record.Source = normalizeSwarmNodeSource(record.Source)
	record.Status = normalizeSwarmNodeStatus(record.Status)
	record.LastError = strings.TrimSpace(record.LastError)
	if record.LastSeenAt < 0 {
		record.LastSeenAt = 0
	}
	if record.CreatedAt < 0 {
		record.CreatedAt = 0
	}
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record
}

func normalizeSwarmNodeSwarmID(value string) string {
	return strings.TrimSpace(value)
}

func isForbiddenSwarmNodeSwarmID(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "remote-deploy:")
}

func normalizeSwarmNodeRole(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "master", "controller":
		return "controller"
	case "child":
		return "child"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeSwarmNodeKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "self", "local", "container", "remote", "manual":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "manual"
	}
}

func normalizeSwarmNodeTransport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "local", "tailscale", "lan":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "tailscale"
	}
}

func normalizeSwarmNodeStatus(value string) string {
	status := strings.ToLower(strings.TrimSpace(value))
	switch status {
	case "online", "ready", "attached", "registered", "offline", "error", "failed", "pending", "provisioning", "checking":
		return status
	default:
		return "unknown"
	}
}

func normalizeSwarmNodeSource(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	return value
}

func normalizeSwarmNodeURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		if parsed.Path == "/" {
			parsed.Path = ""
		}
		return strings.TrimRight(parsed.String(), "/")
	}
	return strings.TrimRight(value, "/")
}

func decodeSwarmNodeSwarmIDFromKey(key string) string {
	if !strings.HasPrefix(key, SwarmNodePrefix()) {
		return ""
	}
	raw := strings.TrimPrefix(key, SwarmNodePrefix())
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return ""
	}
	return normalizeSwarmNodeSwarmID(decoded)
}
