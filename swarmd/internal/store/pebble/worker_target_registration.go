package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

type WorkerSSHRegistration struct {
	WorkspaceID    string `json:"workspace_id"`
	Name           string `json:"name"`
	Host           string `json:"host"`
	User           string `json:"user"`
	Port           int    `json:"port"`
	IdempotencyKey string `json:"idempotency_key"`
}

// RegisterWorkerSSHConnection writes the existing canonical connection record,
// with no private key, command, network check or ready capability. Stable IDs
// make retries inspect the exact original input instead of creating duplicates.
func (ws *WorkerStore) RegisterWorkerSSHConnection(account string, req WorkerSSHRegistration) (environments.Connection, error) {
	if account == "" || !validWorkerControlKey(req.IdempotencyKey) || !validWorkerControlKey(req.WorkspaceID) || strings.TrimSpace(req.Host) != req.Host || strings.TrimSpace(req.User) != req.User || strings.HasPrefix(req.Host, "-") || strings.HasPrefix(req.User, "-") || strings.ContainsAny(req.Host+req.User, "\r\n\x00 \t/\\;|&`$<>\"'") {
		return environments.Connection{}, ErrWorkerInvalid
	}
	digest := sha256.Sum256([]byte(account + "\x00" + req.WorkspaceID + "\x00" + req.IdempotencyKey))
	conn := environments.Connection{ID: "connection_" + hex.EncodeToString(digest[:16]), AccountScopeID: account, WorkspaceID: req.WorkspaceID, Name: req.Name, Kind: environments.ConnectionKindSSH, SSH: &environments.SSHConfig{Host: req.Host, User: req.User, Port: req.Port}}
	if err := conn.Validate(); err != nil {
		return environments.Connection{}, err
	}
	raw, err := json.Marshal(conn)
	if err != nil {
		return environments.Connection{}, err
	}
	if err = environments.AssertNoSecretsRaw(raw); err != nil {
		return environments.Connection{}, err
	}
	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()
	existing, found, err := NewConnectionStore(ws.store).Get(account, req.WorkspaceID, conn.ID)
	if err != nil {
		return environments.Connection{}, err
	}
	if found {
		comparison := existing
		comparison.CreatedAt = 0
		comparison.UpdatedAt = 0
		if !reflect.DeepEqual(comparison, conn) {
			return environments.Connection{}, fmt.Errorf("%w: connection retry differs", ErrWorkerConflict)
		}
		return existing, nil
	}
	conn.CreatedAt = time.Now().UnixMilli()
	conn.UpdatedAt = conn.CreatedAt
	if err = ws.store.PutJSON(KeyConnectionForAccount(account, req.WorkspaceID, conn.ID), conn); err != nil {
		return environments.Connection{}, err
	}
	return conn, nil
}
