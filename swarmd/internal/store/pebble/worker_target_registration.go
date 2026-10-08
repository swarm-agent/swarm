package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
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
	KnownHostsFile string `json:"known_hosts_file,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

// RegisterWorkerSSHConnection writes the existing canonical connection record,
// with no private key, command, network check or ready capability. Stable IDs
// make retries inspect the exact original input instead of creating duplicates.
func (ws *WorkerStore) RegisterWorkerSSHConnection(account string, req WorkerSSHRegistration) (environments.Connection, error) {
	if account == "" || !validWorkerControlKey(req.IdempotencyKey) || !validWorkerControlKey(req.WorkspaceID) || strings.TrimSpace(req.Host) != req.Host || strings.TrimSpace(req.User) != req.User || strings.HasPrefix(req.Host, "-") || strings.HasPrefix(req.User, "-") || strings.ContainsAny(req.Host+req.User, "\r\n\x00 \t/\\;|&`$<>\"'") {
		return environments.Connection{}, ErrWorkerInvalid
	}
	// A trust-store reference is not host-key enrollment and does not assert
	// connectivity. Runtime SSH must verify the key against this explicit file.
	if req.KnownHostsFile != "" && (!filepath.IsAbs(req.KnownHostsFile) || filepath.Clean(req.KnownHostsFile) != req.KnownHostsFile || strings.ContainsAny(req.KnownHostsFile, "\r\n\x00")) {
		return environments.Connection{}, ErrWorkerInvalid
	}
	digest := sha256.Sum256([]byte(account + "\x00" + req.WorkspaceID + "\x00" + req.IdempotencyKey))
	conn := environments.Connection{ID: "connection_" + hex.EncodeToString(digest[:16]), AccountScopeID: account, WorkspaceID: req.WorkspaceID, Name: req.Name, Kind: environments.ConnectionKindSSH, SSH: &environments.SSHConfig{Host: req.Host, User: req.User, Port: req.Port, KnownHostsFile: req.KnownHostsFile}}
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

type WorkerGCPRegistration struct {
	Name           string `json:"name"`
	RuntimeID      string `json:"runtime_id"`
	DesktopURL     string `json:"desktop_url,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

// RegisterWorkerGCPRuntime registers an authorized GCP compute runtime target reference
// into the account's canonical TopologyStore without duplicating credentials.
func (ws *WorkerStore) RegisterWorkerGCPRuntime(account, user string, req WorkerGCPRegistration) (TopologyRuntimeRecord, error) {
	if account == "" || !validWorkerControlKey(req.IdempotencyKey) || !validWorkerControlKey(req.RuntimeID) || strings.TrimSpace(req.Name) == "" {
		return TopologyRuntimeRecord{}, ErrWorkerInvalid
	}
	if req.DesktopURL != "" && (strings.ContainsAny(req.DesktopURL, "\r\n\x00") || len(req.DesktopURL) > 2048) {
		return TopologyRuntimeRecord{}, ErrWorkerInvalid
	}
	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()
	ts := NewTopologyStore(ws.store)
	existing, found, err := ts.GetRuntimeForAccount(account, req.RuntimeID)
	if err != nil {
		return TopologyRuntimeRecord{}, err
	}
	if found {
		if existing.Transport != "gcp" || existing.Name != req.Name || existing.DesktopURL != req.DesktopURL {
			return TopologyRuntimeRecord{}, fmt.Errorf("%w: GCP runtime retry differs", ErrWorkerConflict)
		}
		return existing, nil
	}
	now := time.Now().UnixMilli()
	rec := TopologyRuntimeRecord{
		SwarmID:        req.RuntimeID,
		UserID:         user,
		AccountScopeID: account,
		Name:           req.Name,
		Transport:      "gcp",
		Status:         "registered",
		DesktopURL:     req.DesktopURL,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	return ts.PutRuntimeForAccount(account, rec)
}
