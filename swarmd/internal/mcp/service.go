package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	DefaultExaServerID  = "exa-public"
	DefaultExaServerURL = "https://mcp.exa.ai/mcp"
)

type Server struct {
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

type UpsertInput struct {
	ID        string
	Name      string
	Transport string
	URL       string
	Command   string
	Args      []string
	Env       map[string]string
	Headers   map[string]string
	Enabled   *bool
	Source    string
}

type ExaRuntimeConfig struct {
	Enabled bool
	URL     string
}

type Service struct {
	store  *pebblestore.MCPStore
	events *pebblestore.EventLog
}

func NewService(store *pebblestore.MCPStore, events *pebblestore.EventLog) *Service {
	return &Service{store: store, events: events}
}

func (s *Service) EnsureDefaults() error {
	// Generic MCP server management is deferred until it can be integrated
	// with Swarm Sync. Do not persist a default MCP server record here; Exa
	// search resolves the built-in public MCP endpoint directly when no user
	// override exists.
	return nil
}

func (s *Service) List(limit int) ([]Server, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("mcp service is not configured")
	}
	records, err := s.store.List(limit)
	if err != nil {
		return nil, err
	}
	out := make([]Server, 0, len(records))
	for _, record := range records {
		out = append(out, toServer(record))
	}
	return out, nil
}

func (s *Service) Get(id string) (Server, bool, error) {
	if s == nil || s.store == nil {
		return Server{}, false, errors.New("mcp service is not configured")
	}
	record, ok, err := s.store.Get(id)
	if err != nil {
		return Server{}, false, err
	}
	if !ok {
		return Server{}, false, nil
	}
	return toServer(record), true, nil
}

func (s *Service) Upsert(input UpsertInput) (Server, *pebblestore.EventEnvelope, error) {
	if s == nil || s.store == nil {
		return Server{}, nil, errors.New("mcp service is not configured")
	}
	id := strings.ToLower(strings.TrimSpace(input.ID))
	if id == "" {
		return Server{}, nil, errors.New("id is required")
	}
	current, hasCurrent, err := s.store.Get(id)
	if err != nil {
		return Server{}, nil, err
	}

	enabled := true
	if hasCurrent {
		enabled = current.Enabled
	}
	if input.Enabled != nil {
		enabled = *input.Enabled
	}

	transport := strings.ToLower(strings.TrimSpace(input.Transport))
	if transport == "" && hasCurrent {
		transport = strings.ToLower(strings.TrimSpace(current.Transport))
	}

	name := strings.TrimSpace(input.Name)
	if name == "" && hasCurrent {
		name = strings.TrimSpace(current.Name)
	}

	url := strings.TrimSpace(input.URL)
	if url == "" && hasCurrent {
		url = strings.TrimSpace(current.URL)
	}

	command := strings.TrimSpace(input.Command)
	if command == "" && hasCurrent {
		command = strings.TrimSpace(current.Command)
	}

	args := append([]string(nil), input.Args...)
	if len(args) == 0 && hasCurrent {
		args = append([]string(nil), current.Args...)
	}

	env := cloneStringMap(input.Env)
	if len(env) == 0 && hasCurrent {
		env = cloneStringMap(current.Env)
	}

	headers := cloneStringMap(input.Headers)
	if len(headers) == 0 && hasCurrent {
		headers = cloneStringMap(current.Headers)
	}

	source := strings.ToLower(strings.TrimSpace(input.Source))
	if source == "" && hasCurrent {
		source = strings.ToLower(strings.TrimSpace(current.Source))
	}
	if source == "" {
		source = "user"
	}

	record, err := s.store.Upsert(pebblestore.MCPServerRecord{
		ID:        id,
		Name:      name,
		Transport: transport,
		URL:       url,
		Command:   command,
		Args:      args,
		Env:       env,
		Headers:   headers,
		Enabled:   enabled,
		Source:    source,
	})
	if err != nil {
		return Server{}, nil, err
	}
	server := toServer(record)
	event, err := s.appendEvent("mcp.server.upserted", server)
	if err != nil {
		return Server{}, nil, err
	}
	return server, event, nil
}

func (s *Service) Delete(id string) (bool, *pebblestore.EventEnvelope, error) {
	if s == nil || s.store == nil {
		return false, nil, errors.New("mcp service is not configured")
	}
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return false, nil, errors.New("id is required")
	}
	deleted, err := s.store.Delete(id)
	if err != nil {
		return false, nil, err
	}
	if !deleted {
		return false, nil, nil
	}
	event, err := s.appendEvent("mcp.server.deleted", map[string]any{"id": id})
	if err != nil {
		return false, nil, err
	}
	return true, event, nil
}

func (s *Service) SetEnabled(id string, enabled bool) (Server, *pebblestore.EventEnvelope, error) {
	if s == nil || s.store == nil {
		return Server{}, nil, errors.New("mcp service is not configured")
	}
	record, err := s.store.SetEnabled(id, enabled)
	if err != nil {
		return Server{}, nil, err
	}
	server := toServer(record)
	event, err := s.appendEvent("mcp.server.updated", server)
	if err != nil {
		return Server{}, nil, err
	}
	return server, event, nil
}

func (s *Service) ResolveExaRuntimeConfig() (ExaRuntimeConfig, error) {
	// Generic MCP management is deferred, so Exa fallback must not depend on
	// persisted MCP records. Old local records may exist from earlier builds;
	// ignore them so free users always get the hosted Exa bridge when no API key
	// is configured.
	return ExaRuntimeConfig{
		Enabled: true,
		URL:     DefaultExaServerURL,
	}, nil
}

func (s *Service) appendEvent(eventType string, payload any) (*pebblestore.EventEnvelope, error) {
	if s == nil || s.events == nil {
		return nil, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal mcp event payload: %w", err)
	}
	env, err := s.events.Append("system:mcp", strings.TrimSpace(eventType), "global", raw, "", "")
	if err != nil {
		return nil, err
	}
	return &env, nil
}

func toServer(record pebblestore.MCPServerRecord) Server {
	return Server{
		ID:        strings.TrimSpace(record.ID),
		Name:      strings.TrimSpace(record.Name),
		Transport: strings.TrimSpace(record.Transport),
		URL:       strings.TrimSpace(record.URL),
		Command:   strings.TrimSpace(record.Command),
		Args:      append([]string(nil), record.Args...),
		Env:       cloneStringMap(record.Env),
		Headers:   cloneStringMap(record.Headers),
		Enabled:   record.Enabled,
		Source:    strings.TrimSpace(record.Source),
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
