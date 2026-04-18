package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	mcpTransportHTTP  = "http"
	mcpTransportSSE   = "sse"
	mcpTransportStdio = "stdio"
	mcpSourceDefault  = "default"
	mcpSourceUser     = "user"
)

type MCPServerRecord struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	URL       string            `json:"url,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Enabled   bool              `json:"enabled"`
	Source    string            `json:"source"`
	CreatedAt int64             `json:"created_at"`
	UpdatedAt int64             `json:"updated_at"`
}

type MCPStore struct {
	store *Store
}

func NewMCPStore(store *Store) *MCPStore {
	return &MCPStore{store: store}
}

func (s *MCPStore) Get(serverID string) (MCPServerRecord, bool, error) {
	if s == nil || s.store == nil {
		return MCPServerRecord{}, false, errors.New("mcp store is not configured")
	}
	serverID = normalizeMCPServerID(serverID)
	if serverID == "" {
		return MCPServerRecord{}, false, errors.New("server id is required")
	}

	var record MCPServerRecord
	ok, err := s.store.GetJSON(KeyMCPServer(serverID), &record)
	if err != nil {
		return MCPServerRecord{}, false, err
	}
	if !ok {
		return MCPServerRecord{}, false, nil
	}
	record.ID = normalizeMCPServerID(record.ID)
	if record.ID == "" {
		record.ID = serverID
	}
	record.Transport = normalizeMCPTransport(record.Transport)
	record.Source = normalizeMCPSource(record.Source)
	record.Name = strings.TrimSpace(record.Name)
	record.URL = strings.TrimSpace(record.URL)
	record.Command = strings.TrimSpace(record.Command)
	record.Args = normalizeMCPArgs(record.Args)
	record.Env = normalizeMCPStringMap(record.Env)
	record.Headers = normalizeMCPStringMap(record.Headers)
	if record.CreatedAt < 0 {
		record.CreatedAt = 0
	}
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record, true, nil
}

func (s *MCPStore) List(limit int) ([]MCPServerRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("mcp store is not configured")
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]MCPServerRecord, 0, limit)
	err := s.store.IteratePrefix(MCPServerPrefix(), 100000, func(_ string, value []byte) error {
		var record MCPServerRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		record.ID = normalizeMCPServerID(record.ID)
		if record.ID == "" {
			return nil
		}
		record.Transport = normalizeMCPTransport(record.Transport)
		record.Source = normalizeMCPSource(record.Source)
		record.Name = strings.TrimSpace(record.Name)
		record.URL = strings.TrimSpace(record.URL)
		record.Command = strings.TrimSpace(record.Command)
		record.Args = normalizeMCPArgs(record.Args)
		record.Env = normalizeMCPStringMap(record.Env)
		record.Headers = normalizeMCPStringMap(record.Headers)
		if record.CreatedAt < 0 {
			record.CreatedAt = 0
		}
		if record.UpdatedAt < 0 {
			record.UpdatedAt = 0
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(out[i].ID))
		right := strings.ToLower(strings.TrimSpace(out[j].ID))
		return left < right
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MCPStore) Upsert(input MCPServerRecord) (MCPServerRecord, error) {
	if s == nil || s.store == nil {
		return MCPServerRecord{}, errors.New("mcp store is not configured")
	}
	serverID := normalizeMCPServerID(input.ID)
	if serverID == "" {
		return MCPServerRecord{}, errors.New("server id is required")
	}

	existing, hasExisting, err := s.Get(serverID)
	if err != nil {
		return MCPServerRecord{}, err
	}

	now := time.Now().UnixMilli()
	record := MCPServerRecord{
		ID:        serverID,
		Name:      strings.TrimSpace(input.Name),
		Transport: normalizeMCPTransport(input.Transport),
		URL:       strings.TrimSpace(input.URL),
		Command:   strings.TrimSpace(input.Command),
		Args:      normalizeMCPArgs(input.Args),
		Env:       normalizeMCPStringMap(input.Env),
		Headers:   normalizeMCPStringMap(input.Headers),
		Enabled:   input.Enabled,
		Source:    normalizeMCPSource(input.Source),
		UpdatedAt: now,
	}
	if hasExisting {
		record.CreatedAt = existing.CreatedAt
		if record.CreatedAt <= 0 {
			record.CreatedAt = now
		}
	} else {
		record.CreatedAt = now
	}
	if record.Transport == "" {
		return MCPServerRecord{}, errors.New("transport must be one of: sse, http, stdio")
	}
	switch record.Transport {
	case mcpTransportStdio:
		if record.Command == "" {
			return MCPServerRecord{}, errors.New("command is required for stdio servers")
		}
		record.URL = ""
	case mcpTransportSSE, mcpTransportHTTP:
		if record.URL == "" {
			return MCPServerRecord{}, errors.New("url is required for remote MCP servers")
		}
		record.Command = ""
		record.Args = nil
		record.Env = nil
	default:
		return MCPServerRecord{}, fmt.Errorf("unsupported transport %q", record.Transport)
	}
	if record.Name == "" {
		record.Name = record.ID
	}
	if err := s.store.PutJSON(KeyMCPServer(serverID), record); err != nil {
		return MCPServerRecord{}, err
	}
	return record, nil
}

func (s *MCPStore) EnsureDefault(record MCPServerRecord) (MCPServerRecord, bool, error) {
	if s == nil || s.store == nil {
		return MCPServerRecord{}, false, errors.New("mcp store is not configured")
	}
	serverID := normalizeMCPServerID(record.ID)
	if serverID == "" {
		return MCPServerRecord{}, false, errors.New("server id is required")
	}
	existing, ok, err := s.Get(serverID)
	if err != nil {
		return MCPServerRecord{}, false, err
	}
	if ok {
		return existing, false, nil
	}
	record.ID = serverID
	if strings.TrimSpace(record.Source) == "" {
		record.Source = mcpSourceDefault
	}
	inserted, err := s.Upsert(record)
	if err != nil {
		return MCPServerRecord{}, false, err
	}
	return inserted, true, nil
}

func (s *MCPStore) Delete(serverID string) (bool, error) {
	if s == nil || s.store == nil {
		return false, errors.New("mcp store is not configured")
	}
	serverID = normalizeMCPServerID(serverID)
	if serverID == "" {
		return false, errors.New("server id is required")
	}
	if _, ok, err := s.Get(serverID); err != nil {
		return false, err
	} else if !ok {
		return false, nil
	}
	if err := s.store.Delete(KeyMCPServer(serverID)); err != nil {
		return false, err
	}
	return true, nil
}

func (s *MCPStore) SetEnabled(serverID string, enabled bool) (MCPServerRecord, error) {
	record, ok, err := s.Get(serverID)
	if err != nil {
		return MCPServerRecord{}, err
	}
	if !ok {
		return MCPServerRecord{}, fmt.Errorf("mcp server %q not found", normalizeMCPServerID(serverID))
	}
	record.Enabled = enabled
	record.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutJSON(KeyMCPServer(record.ID), record); err != nil {
		return MCPServerRecord{}, err
	}
	return record, nil
}

func normalizeMCPServerID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeMCPTransport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case mcpTransportHTTP:
		return mcpTransportHTTP
	case mcpTransportSSE:
		return mcpTransportSSE
	case mcpTransportStdio:
		return mcpTransportStdio
	default:
		return ""
	}
}

func normalizeMCPSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case mcpSourceDefault:
		return mcpSourceDefault
	default:
		return mcpSourceUser
	}
}

func normalizeMCPArgs(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeMCPStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
