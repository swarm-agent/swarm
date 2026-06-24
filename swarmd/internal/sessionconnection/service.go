package sessionconnection

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	Protocol              = "swarm.session-stream.v1"
	ContractVersion       = 1
	DefaultReadyTimeoutMS = 10000
	TokenTTL              = 24 * time.Hour

	tokenPrefix     = "v3sc1."
	defaultTokenKID = "dev-session-connection"
)

var defaultTokenKey = []byte("swarm-session-connection-default-non-secret-test-key-v1")

type Service struct {
	mu          sync.Mutex
	keyID       string
	key         []byte
	connections map[string]Connection
}

type Options struct {
	DataDir string
}

type ConnectInput struct {
	Principal   identity.Principal
	SessionID   string
	ClientID    string
	RequestID   string
	ResumeToken string
	Store       *sessionruntime.Service
	Pending     func(sessionID string, limit int) ([]pebblestore.PermissionRecord, error)
	StreamPath  func(connectionID, token string) string
}

type ConnectResult struct {
	SessionID   string
	Snapshot    Snapshot
	Connection  Connection
	StreamURL   string
	ResumeToken string
}

type Snapshot struct {
	EventSeq           uint64
	EndpointSeq        uint64
	Session            json.RawMessage
	Messages           []json.RawMessage
	CurrentRun         *CurrentRun
	PendingPermissions []json.RawMessage
	ActivePlan         *json.RawMessage
	Usage              *json.RawMessage
}

type CurrentRun struct {
	RunID  string
	Phase  string
	Reason *RunPhaseReason
}

type RunPhaseReason struct {
	Code      string
	Message   string
	Retryable bool
}

type Connection struct {
	ConnectionID string
	SessionID    string
	ClientID     string
	RequestID    string
	UserID       string
	AccountID    string
	EventSeq     uint64
	EndpointSeq  uint64
	IssuedAt     int64
	ExpiresAt    int64
}

type tokenPayload struct {
	Version      int    `json:"version"`
	KID          string `json:"kid"`
	ConnectionID string `json:"connection_id"`
	SessionID    string `json:"session_id"`
	ClientID     string `json:"client_id"`
	RequestID    string `json:"request_id"`
	UserID       string `json:"user_id"`
	AccountID    string `json:"account_id"`
	EventSeq     uint64 `json:"event_seq"`
	EndpointSeq  uint64 `json:"endpoint_seq"`
	IssuedAt     int64  `json:"issued_at"`
	ExpiresAt    int64  `json:"expires_at"`
}

func NewService(options Options) (*Service, error) {
	key := append([]byte(nil), defaultTokenKey...)
	kid := defaultTokenKID
	dataDir := strings.TrimSpace(options.DataDir)
	if dataDir != "" {
		loaded, err := loadOrCreateTokenKey(dataDir)
		if err != nil {
			return nil, fmt.Errorf("load session connection token key: %w", err)
		}
		if len(loaded) < 32 {
			return nil, fmt.Errorf("session connection token key is too short: %d bytes", len(loaded))
		}
		key = loaded
		sum := sha256.Sum256(loaded)
		kid = "v3sessionconn-" + hex.EncodeToString(sum[:6])
	}
	return &Service{keyID: kid, key: key, connections: map[string]Connection{}}, nil
}

func loadOrCreateTokenKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, "session-connection.key")
	if raw, err := os.ReadFile(path); err == nil {
		return base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(raw)))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(base64.RawURLEncoding.EncodeToString(key)), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func (s *Service) Connect(input ConnectInput) (ConnectResult, error) {
	if s == nil {
		return ConnectResult{}, errors.New("session connection service is not configured")
	}
	if input.Store == nil {
		return ConnectResult{}, errors.New("session store is not configured")
	}
	principal := input.Principal
	if !principal.Valid() {
		return ConnectResult{}, identity.ErrPrincipalRequired
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return ConnectResult{}, errors.New("session_id is required")
	}
	clientID := strings.TrimSpace(input.ClientID)
	if clientID == "" {
		return ConnectResult{}, errors.New("client_id is required")
	}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return ConnectResult{}, errors.New("request_id is required")
	}
	if resume := strings.TrimSpace(input.ResumeToken); resume != "" {
		if _, err := s.ValidateToken(resume, principal, sessionID); err != nil {
			return ConnectResult{}, fmt.Errorf("resume_token invalid: %w", err)
		}
	}
	session, ok, err := input.Store.GetSession(sessionID)
	if err != nil || !ok {
		return ConnectResult{}, errSessionNotFound(ok, err)
	}
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) || strings.TrimSpace(session.UserID) == "" || strings.TrimSpace(session.UserID) != strings.TrimSpace(principal.UserID) {
		return ConnectResult{}, ErrUnauthorized
	}
	hydrated, ok, err := input.Store.HydrateSessionSnapshot(sessionID, 500, 0)
	if err != nil || !ok {
		return ConnectResult{}, errSessionNotFound(ok, err)
	}
	snapshot, err := buildSnapshot(input, hydrated)
	if err != nil {
		return ConnectResult{}, err
	}
	connection := Connection{
		ConnectionID: "conn-" + uuid.NewString(),
		SessionID:    sessionID,
		ClientID:     clientID,
		RequestID:    requestID,
		UserID:       strings.TrimSpace(principal.UserID),
		AccountID:    strings.TrimSpace(principal.AccountScopeID),
		EventSeq:     snapshot.EventSeq,
		EndpointSeq:  snapshot.EndpointSeq,
		IssuedAt:     time.Now().Unix(),
		ExpiresAt:    time.Now().Add(TokenTTL).Unix(),
	}
	resumeToken, err := s.StoreConnection(connection)
	if err != nil {
		return ConnectResult{}, err
	}
	streamURL := ""
	if input.StreamPath != nil {
		streamURL = input.StreamPath(connection.ConnectionID, resumeToken)
	}
	return ConnectResult{SessionID: sessionID, Snapshot: snapshot, Connection: connection, StreamURL: streamURL, ResumeToken: resumeToken}, nil
}

var ErrUnauthorized = errors.New("session is not visible to principal")

func errSessionNotFound(ok bool, err error) error {
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("session not found")
	}
	return nil
}

func buildSnapshot(input ConnectInput, hydrated pebblestore.V3SessionHydration) (Snapshot, error) {
	sessionRaw, err := json.Marshal(hydrated.Session)
	if err != nil {
		return Snapshot{}, err
	}
	messages := make([]json.RawMessage, 0, len(hydrated.Messages))
	for _, message := range hydrated.Messages {
		raw, err := json.Marshal(message)
		if err != nil {
			return Snapshot{}, err
		}
		messages = append(messages, raw)
	}
	var currentRun *CurrentRun
	if state, ok, err := input.Store.GetSessionRunState(hydrated.Session.ID); err != nil {
		return Snapshot{}, err
	} else if ok && strings.TrimSpace(state.RunID) != "" {
		currentRun = &CurrentRun{RunID: strings.TrimSpace(state.RunID), Phase: runPhaseFromLegacyStatus(state.Status, state.BlockedReason), Reason: runReasonFromState(state)}
	}
	pending := []json.RawMessage{}
	if input.Pending != nil {
		records, err := input.Pending(hydrated.Session.ID, 200)
		if err != nil {
			return Snapshot{}, err
		}
		for _, record := range records {
			raw, err := json.Marshal(record)
			if err != nil {
				return Snapshot{}, err
			}
			pending = append(pending, raw)
		}
	}
	var activePlan *json.RawMessage
	if plan, ok, err := input.Store.GetActivePlan(hydrated.Session.ID); err != nil {
		return Snapshot{}, err
	} else if ok {
		rawBytes, err := json.Marshal(plan)
		if err != nil {
			return Snapshot{}, err
		}
		raw := json.RawMessage(rawBytes)
		activePlan = &raw
	}
	var usage *json.RawMessage
	if summary, ok, err := input.Store.GetUsageSummary(hydrated.Session.ID); err != nil {
		return Snapshot{}, err
	} else if ok {
		rawBytes, err := json.Marshal(summary)
		if err != nil {
			return Snapshot{}, err
		}
		raw := json.RawMessage(rawBytes)
		usage = &raw
	}
	return Snapshot{EventSeq: hydrated.Projection.LastEventSeq, EndpointSeq: parseSnapshotEndpointSeq(hydrated.SnapshotEndpointCursor), Session: sessionRaw, Messages: messages, CurrentRun: currentRun, PendingPermissions: pending, ActivePlan: activePlan, Usage: usage}, nil
}

func parseSnapshotEndpointSeq(cursor string) uint64 {
	cursor = strings.TrimSpace(cursor)
	cursor = strings.TrimPrefix(cursor, "cursor-")
	var seq uint64
	_, _ = fmt.Sscanf(cursor, "%d", &seq)
	return seq
}

func runPhaseFromLegacyStatus(status, blockedReason string) string {
	switch strings.TrimSpace(status) {
	case sessionruntime.RunIntentPendingExecutor:
		return "pending_executor"
	case sessionruntime.RunIntentRunning:
		return "executor_started"
	case sessionruntime.RunIntentCompleted:
		return "completed"
	case sessionruntime.RunIntentFailed:
		return "failed"
	case sessionruntime.RunIntentCancelled:
		return "cancelled"
	case sessionruntime.RunIntentInterrupted:
		return "interrupted"
	case sessionruntime.RunIntentDispatchBlocked:
		return "waiting_permission"
	default:
		if strings.TrimSpace(blockedReason) != "" {
			return "waiting_permission"
		}
		return "accepted"
	}
}

func runReasonFromState(state sessionruntime.SessionRunState) *RunPhaseReason {
	reason := strings.TrimSpace(state.BlockedReason)
	if reason == "" {
		return nil
	}
	return &RunPhaseReason{Code: strings.TrimSpace(state.Status), Message: reason}
}

func (s *Service) StoreConnection(connection Connection) (string, error) {
	if s == nil {
		return "", errors.New("session connection service is not configured")
	}
	connection.ConnectionID = strings.TrimSpace(connection.ConnectionID)
	connection.SessionID = strings.TrimSpace(connection.SessionID)
	connection.UserID = strings.TrimSpace(connection.UserID)
	connection.AccountID = strings.TrimSpace(connection.AccountID)
	if connection.ConnectionID == "" || connection.SessionID == "" || connection.UserID == "" || connection.AccountID == "" {
		return "", errors.New("connection identity is incomplete")
	}
	if connection.IssuedAt == 0 {
		connection.IssuedAt = time.Now().Unix()
	}
	if connection.ExpiresAt == 0 {
		connection.ExpiresAt = time.Now().Add(TokenTTL).Unix()
	}
	token, err := s.sign(connection)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connections == nil {
		s.connections = map[string]Connection{}
	}
	s.connections[connection.ConnectionID] = connection
	return token, nil
}

func (s *Service) Connection(connectionID string) (Connection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conn, ok := s.connections[strings.TrimSpace(connectionID)]
	if !ok || conn.ExpiresAt <= time.Now().Unix() {
		return Connection{}, false
	}
	return conn, true
}

func (s *Service) ValidateToken(raw string, principal identity.Principal, sessionID string) (Connection, error) {
	payload, err := s.verify(raw)
	if err != nil {
		return Connection{}, err
	}
	if payload.ExpiresAt <= time.Now().Unix() {
		return Connection{}, errors.New("resume token expired")
	}
	if strings.TrimSpace(payload.SessionID) != strings.TrimSpace(sessionID) || strings.TrimSpace(payload.UserID) != strings.TrimSpace(principal.UserID) || strings.TrimSpace(payload.AccountID) != strings.TrimSpace(principal.AccountScopeID) {
		return Connection{}, errors.New("resume token scope mismatch")
	}
	return Connection{ConnectionID: payload.ConnectionID, SessionID: payload.SessionID, ClientID: payload.ClientID, RequestID: payload.RequestID, UserID: payload.UserID, AccountID: payload.AccountID, EventSeq: payload.EventSeq, EndpointSeq: payload.EndpointSeq, IssuedAt: payload.IssuedAt, ExpiresAt: payload.ExpiresAt}, nil
}

func (s *Service) ValidateStreamToken(raw string, connectionID string) (Connection, error) {
	payload, err := s.verify(raw)
	if err != nil {
		return Connection{}, err
	}
	if payload.ExpiresAt <= time.Now().Unix() {
		return Connection{}, errors.New("connection token expired")
	}
	if strings.TrimSpace(payload.ConnectionID) != strings.TrimSpace(connectionID) {
		return Connection{}, errors.New("connection token scope mismatch")
	}
	if conn, ok := s.Connection(connectionID); ok {
		return conn, nil
	}
	return Connection{ConnectionID: payload.ConnectionID, SessionID: payload.SessionID, ClientID: payload.ClientID, RequestID: payload.RequestID, UserID: payload.UserID, AccountID: payload.AccountID, EventSeq: payload.EventSeq, EndpointSeq: payload.EndpointSeq, IssuedAt: payload.IssuedAt, ExpiresAt: payload.ExpiresAt}, nil
}

func (s *Service) AdvanceToken(connection Connection, eventSeq, endpointSeq uint64) (string, error) {
	connection.EventSeq = eventSeq
	connection.EndpointSeq = endpointSeq
	connection.IssuedAt = time.Now().Unix()
	connection.ExpiresAt = time.Now().Add(TokenTTL).Unix()
	return s.StoreConnection(connection)
}

func (s *Service) sign(conn Connection) (string, error) {
	payload := tokenPayload{Version: 1, KID: s.keyID, ConnectionID: conn.ConnectionID, SessionID: conn.SessionID, ClientID: conn.ClientID, RequestID: conn.RequestID, UserID: conn.UserID, AccountID: conn.AccountID, EventSeq: conn.EventSeq, EndpointSeq: conn.EndpointSeq, IssuedAt: conn.IssuedAt, ExpiresAt: conn.ExpiresAt}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	return tokenPrefix + body + "." + sign(body, s.key), nil
}

func (s *Service) verify(raw string) (tokenPayload, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, tokenPrefix) {
		return tokenPayload{}, errors.New("malformed token")
	}
	parts := strings.Split(strings.TrimPrefix(raw, tokenPrefix), ".")
	if len(parts) != 2 {
		return tokenPayload{}, errors.New("malformed token")
	}
	if !hmac.Equal([]byte(parts[1]), []byte(sign(parts[0], s.key))) {
		return tokenPayload{}, errors.New("token signature mismatch")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return tokenPayload{}, err
	}
	var payload tokenPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return tokenPayload{}, err
	}
	if payload.Version != 1 || strings.TrimSpace(payload.KID) == "" {
		return tokenPayload{}, errors.New("unsupported token")
	}
	return payload, nil
}

func sign(body string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
