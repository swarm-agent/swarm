#!/usr/bin/env bash
set -euo pipefail

EVIDENCE_DIR="${1:-.tmp/slice-b/peer-managed-workspace-db/$(date -u +%Y%m%dT%H%M%SZ)-vm-proof}"
HOST_EVIDENCE_DIR="${SWARM_HOST_EVIDENCE_DIR:-}"
mkdir -p "${EVIDENCE_DIR}"

COMMANDS_LOG="${EVIDENCE_DIR}/commands.log"
SUMMARY_JSON="${EVIDENCE_DIR}/summary.json"
: >"${COMMANDS_LOG}"

log_cmd() { printf '%s\n' "$*" | tee -a "${COMMANDS_LOG}" >&2; }
fail() {
  local msg="${1:-slice B peer-managed workspace DB proof failed}"
  jq -nc --arg status FAIL --arg error "${msg}" --arg evidence_dir "${EVIDENCE_DIR}" \
    '{status:$status,error:$error,evidence_dir:$evidence_dir,exit_code:1}' >"${SUMMARY_JSON}" 2>/dev/null || true
  printf 'error: %s\n' "${msg}" >&2
  exit 1
}
require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
[[ -f "swarmd/go.mod" ]] || fail "must run from swarm-go repo root"
if [[ -z "${SWARM_HARNESS_VM_GUEST:-}" ]]; then
  fail "this proof must run inside swarm-harness VM via scripts/swarm-harness-vm.sh run"
fi
if [[ -f "scripts/lib-go.sh" ]]; then
  # shellcheck disable=SC1091
  source "scripts/lib-go.sh"
  swarm_require_go "${ROOT_DIR}"
fi
require_command go
require_command jq
require_command awk

RUN_ROOT="$(mktemp -d -t swarm-slice-b-peer-db-XXXXXX)"
cleanup() { rm -rf -- "${RUN_ROOT}"; }
trap cleanup EXIT

DB_PATH="${RUN_ROOT}/peer-managed-workspace.pebble"
PROBE_DIR="swarmd/.cache/sliceb/peermanageddbprobe"
mkdir -p "${PROBE_DIR}"
cat >"${PROBE_DIR}/main.go" <<'GOEOF'
package main

import (
  "bytes"
  "encoding/json"
  "errors"
  "flag"
  "fmt"
  "net/http"
  "net/http/httptest"
  "os"
  "path/filepath"
  "strings"

  "swarm/packages/swarmd/internal/testdeps/startupconfig"
  "swarm/packages/swarmd/internal/api"
  "swarm/packages/swarmd/internal/identity"
  pebblestore "swarm/packages/swarmd/internal/store/pebble"
  "swarm/packages/swarmd/internal/stream"
  swarmruntime "swarm/packages/swarmd/internal/swarm"
  workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

type fakeSwarm struct{}

func (fakeSwarm) EnsureLocalState(swarmruntime.EnsureLocalStateInput) (swarmruntime.LocalState, error) {
  return swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "managed-swarm", Name: "managed-host", Role: "managed"}}, nil
}
func (fakeSwarm) RenameLocalSwarm(swarmruntime.RenameLocalSwarmInput) (swarmruntime.LocalState, error) { return swarmruntime.LocalState{}, errors.New("not used") }
func (fakeSwarm) ListGroupsForSwarm(string, int) ([]swarmruntime.GroupState, string, error) { return nil, "", nil }
func (fakeSwarm) UpsertGroup(swarmruntime.UpsertGroupInput) (swarmruntime.Group, error) { return swarmruntime.Group{}, nil }
func (fakeSwarm) DeleteGroup(string) error { return nil }
func (fakeSwarm) SetCurrentGroup(string, string) (swarmruntime.GroupState, error) { return swarmruntime.GroupState{}, nil }
func (fakeSwarm) OutgoingPeerAuthToken(string) (string, bool, error) { return "", false, nil }
func (fakeSwarm) ValidateIncomingPeerAuth(swarmID, token string) (bool, error) {
  return strings.TrimSpace(swarmID) == "manager-swarm" && strings.TrimSpace(token) == "manager-token", nil
}
func (fakeSwarm) UpsertGroupMember(swarmruntime.UpsertGroupMemberInput) (swarmruntime.GroupMember, error) { return swarmruntime.GroupMember{}, nil }
func (fakeSwarm) RemoveGroupMember(swarmruntime.RemoveGroupMemberInput) error { return nil }
func (fakeSwarm) CreateInvite(swarmruntime.CreateInviteInput) (swarmruntime.Invite, error) { return swarmruntime.Invite{}, nil }
func (fakeSwarm) SubmitEnrollment(swarmruntime.SubmitEnrollmentInput) (swarmruntime.Enrollment, error) { return swarmruntime.Enrollment{}, nil }
func (fakeSwarm) ListPendingEnrollments(int) ([]swarmruntime.Enrollment, error) { return nil, nil }
func (fakeSwarm) DecideEnrollment(swarmruntime.DecideEnrollmentInput) (swarmruntime.Enrollment, []swarmruntime.TrustedPeer, error) { return swarmruntime.Enrollment{}, nil, nil }
func (fakeSwarm) PrepareRemoteBootstrapParentPeer(swarmruntime.PrepareRemoteBootstrapParentPeerInput) error { return nil }
func (fakeSwarm) ApproveManagedPairing(swarmruntime.ApproveManagedPairingInput) (swarmruntime.PairingState, error) { return swarmruntime.PairingState{}, nil }
func (fakeSwarm) TrustManagedPeer(swarmruntime.TrustManagedPeerInput) (swarmruntime.TrustedPeer, error) { return swarmruntime.TrustedPeer{}, nil }
func (fakeSwarm) RemoveManagedPeer(swarmruntime.RemoveManagedPeerInput) (swarmruntime.RemoveManagedPeerResult, error) { return swarmruntime.RemoveManagedPeerResult{}, nil }
func (fakeSwarm) UpdateLocalPairingFromConfig(startupconfig.FileConfig, []swarmruntime.TransportSummary) (swarmruntime.PairingState, error) { return swarmruntime.PairingState{}, nil }
func (fakeSwarm) DetachToStandalone(string) error { return nil }

type workspaceEntryRaw struct {
  AccountScopeID string   `json:"account_scope_id"`
  Path           string   `json:"path"`
  Name           string   `json:"name"`
  Directories    []string `json:"directories"`
}

type output struct {
  DBPath                 string                     `json:"db_path"`
  DestinationPath        string                     `json:"destination_path"`
  Status                 int                        `json:"status"`
  Response               map[string]any             `json:"response"`
  AccountScopedKey       string                     `json:"account_scoped_key"`
  LegacyKey              string                     `json:"legacy_key"`
  CurrentByAccountKey    string                     `json:"current_by_account_key"`
  AccountScopedPersisted bool                       `json:"account_scoped_persisted"`
  LegacyPersisted        bool                       `json:"legacy_persisted"`
  CurrentPersisted       bool                       `json:"current_persisted"`
  StoredEntry            workspaceEntryRaw          `json:"stored_entry"`
  ListForPeerPrincipal   []workspaceruntime.Entry   `json:"list_for_peer_principal"`
  ListLegacy             []workspaceruntime.Entry   `json:"list_legacy"`
}

func main() {
  dbPath := flag.String("db", "", "path to swarmd pebble DB")
  root := flag.String("root", "", "destination root")
  flag.Parse()
  if strings.TrimSpace(*dbPath) == "" || strings.TrimSpace(*root) == "" {
    fmt.Fprintln(os.Stderr, "--db and --root are required")
    os.Exit(2)
  }
  if err := os.MkdirAll(*root, 0o755); err != nil { panic(err) }
  store, err := pebblestore.Open(*dbPath)
  if err != nil { panic(err) }
  defer store.Close()

  workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
  eventLog, err := pebblestore.NewEventLog(store)
  if err != nil { panic(err) }
  server := api.NewServer("test", nil, nil, nil, nil, nil, workspaceSvc, nil, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
  server.SetSwarmService(fakeSwarm{})
  startupPath := filepath.Join(*root, "swarm.conf")
  cfg := startupconfig.Default(startupPath)
  cfg.SwarmMode = true
  cfg.SwarmName = "managed-host"
  if err := startupconfig.Write(cfg); err != nil { panic(err) }
  server.SetStartupConfigPath(startupPath)

  payload := map[string]any{
    "destination_root": *root,
    "workspace_name": "persisted-peer-workspace",
    "source_workspace_path": "/manager/source/persisted-peer-workspace",
    "provision": true,
  }
  body, err := json.Marshal(payload)
  if err != nil { panic(err) }
  req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/managed-workspaces/ensure-link", bytes.NewReader(body))
  req.Header.Set("Content-Type", "application/json")
  req.Header.Set("X-Swarm-Peer-ID", "manager-swarm")
  req.Header.Set("X-Swarm-Peer-Token", "manager-token")
  rr := httptest.NewRecorder()
  server.Handler().ServeHTTP(rr, req)

  var response map[string]any
  if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil { panic(err) }
  destination, _ := response["destination_path"].(string)
  if rr.Code != http.StatusOK { panic(fmt.Errorf("ensure-link status=%d body=%s", rr.Code, rr.Body.String())) }
  if strings.TrimSpace(destination) == "" { panic("missing destination_path") }

  principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "peer-managed-workspace", AccountScopeID: "peer-managed-workspace", AccountScopeSource: identity.AccountScopeSourceServerState}
  accountKey := pebblestore.KeyWorkspaceEntryForAccount(principal.AccountScopeID, destination)
  legacyKey := pebblestore.KeyWorkspaceEntry(destination)
  currentKey := pebblestore.KeyWorkspaceCurrentForAccount(principal.AccountScopeID, principal.UserID)

  var stored workspaceEntryRaw
  accountOK, err := store.GetJSON(accountKey, &stored)
  if err != nil { panic(err) }
  var legacy workspaceEntryRaw
  legacyOK, err := store.GetJSON(legacyKey, &legacy)
  if err != nil { panic(err) }
  var current pebblestore.WorkspaceBinding
  currentOK, err := store.GetJSON(currentKey, &current)
  if err != nil { panic(err) }
  peerList, err := workspaceSvc.ListKnownForPrincipal(principal, 100)
  if err != nil { panic(err) }
  legacyList, err := workspaceSvc.ListKnown(100)
  if err != nil { panic(err) }

  out := output{
    DBPath: *dbPath, DestinationPath: destination, Status: rr.Code, Response: response,
    AccountScopedKey: accountKey, LegacyKey: legacyKey, CurrentByAccountKey: currentKey,
    AccountScopedPersisted: accountOK, LegacyPersisted: legacyOK, CurrentPersisted: currentOK,
    StoredEntry: stored, ListForPeerPrincipal: peerList, ListLegacy: legacyList,
  }
  enc := json.NewEncoder(os.Stdout)
  enc.SetIndent("", "  ")
  if err := enc.Encode(out); err != nil { panic(err) }
}
GOEOF

log_cmd "cd swarmd && go test ./internal/api -run TestPeerManagedWorkspace -count=1"
(cd swarmd && go test ./internal/api -run TestPeerManagedWorkspace -count=1)

log_cmd "cd swarmd && go run ./.cache/sliceb/peermanageddbprobe --db <tmp> --root <tmp>"
(cd swarmd && go run ./.cache/sliceb/peermanageddbprobe --db "${DB_PATH}" --root "${RUN_ROOT}/managed-root") >"${EVIDENCE_DIR}/db-proof.json"

jq -e '
  .status == 200 and
  .account_scoped_persisted == true and
  .legacy_persisted == false and
  .current_persisted == false and
  .stored_entry.account_scope_id == "peer-managed-workspace" and
  .stored_entry.name == "persisted-peer-workspace" and
  .stored_entry.path == .destination_path and
  (.destination_path as $destination | (.stored_entry.directories | index($destination))) and
  (.list_for_peer_principal | length) == 1 and
  .list_for_peer_principal[0].path == .destination_path and
  (.list_legacy | length) == 0
' "${EVIDENCE_DIR}/db-proof.json" >/dev/null || fail "peer-managed workspace DB persistence invariants failed"

jq -n \
  --arg status PASS \
  --arg evidence_dir "${EVIDENCE_DIR}" \
  --slurpfile proof "${EVIDENCE_DIR}/db-proof.json" \
  '{status:$status,exit_code:0,evidence_dir:$evidence_dir,checks:{targeted_go_test:true,account_scoped_workspace_key_persisted:$proof[0].account_scoped_persisted,legacy_workspace_key_absent:($proof[0].legacy_persisted == false),current_binding_absent:($proof[0].current_persisted == false),stored_account_scope_id:$proof[0].stored_entry.account_scope_id,destination_path:$proof[0].destination_path,account_scoped_key:$proof[0].account_scoped_key,legacy_key:$proof[0].legacy_key}}' >"${SUMMARY_JSON}"

if [[ -n "${HOST_EVIDENCE_DIR}" ]]; then
  mkdir -p "${HOST_EVIDENCE_DIR}"
  cp -a "${EVIDENCE_DIR}/." "${HOST_EVIDENCE_DIR}/"
fi
printf 'PASS slice B peer-managed workspace DB VM proof evidence_dir=%s\n' "${EVIDENCE_DIR}"
