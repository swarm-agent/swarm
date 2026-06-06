#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/ssh-session-db-inspect.sh <ssh-alias> [options]

Reusable remote Swarm session DB inspector. It can search sessions, dump recent
sessions, include messages/events, and optionally write large output to a file
on the remote host. By default it stops an active Swarm service before opening
the Pebble DB, then restores the service if it was active.

Common examples:
  scripts/ssh-session-db-inspect.sh <ssh-alias> --latest 5
  scripts/ssh-session-db-inspect.sh <ssh-alias> --session <session-id> --dump
  scripts/ssh-session-db-inspect.sh <ssh-alias> --query "finished testing" --json --out session-dump.json
  scripts/ssh-session-db-inspect.sh <ssh-alias> --query "session.assistant.completed" --events 40

Options:
  --remote-dir <path>   Remote swarm-go checkout path; auto-discovered by default.
  --service <unit>      Remote service unit. Default: swarm.service
  --db-path <path>      Pebble DB path. Default: /var/lib/swarmd/swarmd.pebble
  --no-stop             Do not stop/restart the service before inspecting.

Selection/search:
  --latest <n>          Inspect latest n sessions. Default: 5
  --session <id>        Inspect one exact session id.
  --query <text>        Search session id/title/workspace/metadata/messages/events.
  --all                 Inspect all sessions scanned by --scan-limit.
  --scan-limit <n>      Max sessions to scan from newest first. Default: 1000

Output shaping:
  --messages <n>        Tail messages per selected session. Default: 12
  --events <n>          Tail V3 events per selected session. Default: 24
  --dump                Dump all messages/events for selected sessions.
  --json                Emit JSON instead of human-readable text.
  --out <path>          Write full output to remote path instead of stdout.
  -h, --help            Show this help.
USAGE
}

fail() {
  printf 'ssh-session-db-inspect: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

b64() {
  printf '%s' "$1" | base64 | tr -d '\n'
}

if [[ $# -lt 1 ]]; then
  usage
  exit 2
fi

SSH_ALIAS="$1"
shift
REMOTE_DIR=""
SERVICE_UNIT="swarm.service"
DB_PATH="/var/lib/swarmd/swarmd.pebble"
STOP_SERVICE="true"
LATEST="5"
SESSION_ID=""
QUERY=""
ALL="false"
SCAN_LIMIT="1000"
MESSAGE_LIMIT="12"
EVENT_LIMIT="24"
DUMP="false"
JSON_OUTPUT="false"
OUT_PATH=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --remote-dir)
      [[ $# -ge 2 ]] || fail "--remote-dir requires a value"
      REMOTE_DIR="$2"
      shift 2
      ;;
    --service)
      [[ $# -ge 2 ]] || fail "--service requires a value"
      SERVICE_UNIT="$2"
      shift 2
      ;;
    --db-path)
      [[ $# -ge 2 ]] || fail "--db-path requires a value"
      DB_PATH="$2"
      shift 2
      ;;
    --no-stop)
      STOP_SERVICE="false"
      shift
      ;;
    --latest)
      [[ $# -ge 2 ]] || fail "--latest requires a value"
      LATEST="$2"
      shift 2
      ;;
    --session)
      [[ $# -ge 2 ]] || fail "--session requires a value"
      SESSION_ID="$2"
      shift 2
      ;;
    --query)
      [[ $# -ge 2 ]] || fail "--query requires a value"
      QUERY="$2"
      shift 2
      ;;
    --all)
      ALL="true"
      shift
      ;;
    --scan-limit)
      [[ $# -ge 2 ]] || fail "--scan-limit requires a value"
      SCAN_LIMIT="$2"
      shift 2
      ;;
    --messages)
      [[ $# -ge 2 ]] || fail "--messages requires a value"
      MESSAGE_LIMIT="$2"
      shift 2
      ;;
    --events)
      [[ $# -ge 2 ]] || fail "--events requires a value"
      EVENT_LIMIT="$2"
      shift 2
      ;;
    --dump)
      DUMP="true"
      shift
      ;;
    --json)
      JSON_OUTPUT="true"
      shift
      ;;
    --out)
      [[ $# -ge 2 ]] || fail "--out requires a value"
      OUT_PATH="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

for pair in "latest:${LATEST}" "scan-limit:${SCAN_LIMIT}" "messages:${MESSAGE_LIMIT}" "events:${EVENT_LIMIT}"; do
  name="${pair%%:*}"
  value="${pair#*:}"
  [[ "${value}" =~ ^[0-9]+$ ]] || fail "--${name} must be an integer"
done
[[ -n "${DB_PATH}" ]] || fail "empty database path"

require_command ssh

if [[ -z "${REMOTE_DIR}" ]]; then
  REMOTE_DIR="$(ssh "${SSH_ALIAS}" 'set -euo pipefail
for candidate in "$HOME/swarm-go" "$HOME/src/swarm-go" "$HOME/work/swarm-go"; do
  if [ -d "$candidate" ] && [ -f "$candidate/AGENTS.md" ] && [ -d "$candidate/swarmd" ]; then
    printf "%s\n" "$candidate"
    exit 0
  fi
done
find "$HOME" /opt /srv /tmp -maxdepth 4 -type d -name swarm-go 2>/dev/null \
  | while IFS= read -r candidate; do
      if [ -f "$candidate/AGENTS.md" ] && [ -d "$candidate/swarmd" ]; then
        printf "%s\n" "$candidate"
        exit 0
      fi
    done
exit 1' 2>/dev/null)" || fail "could not find remote swarm-go checkout on ${SSH_ALIAS}; pass --remote-dir"
fi

[[ -n "${REMOTE_DIR}" ]] || fail "empty remote checkout path"
printf 'ssh-session-db-inspect: remote=%s dir=%s service=%s db=%s\n' \
  "${SSH_ALIAS}" "${REMOTE_DIR}" "${SERVICE_UNIT}" "${DB_PATH}" >&2

ssh "${SSH_ALIAS}" 'bash -s' -- \
  "remote_dir_b64=$(b64 "${REMOTE_DIR}")" \
  "service_unit_b64=$(b64 "${SERVICE_UNIT}")" \
  "db_path_b64=$(b64 "${DB_PATH}")" \
  "stop_service=${STOP_SERVICE}" \
  "latest=${LATEST}" \
  "session_id_b64=$(b64 "${SESSION_ID}")" \
  "query_b64=$(b64 "${QUERY}")" \
  "all=${ALL}" \
  "scan_limit=${SCAN_LIMIT}" \
  "message_limit=${MESSAGE_LIMIT}" \
  "event_limit=${EVENT_LIMIT}" \
  "dump=${DUMP}" \
  "json_output=${JSON_OUTPUT}" \
  "out_path_b64=$(b64 "${OUT_PATH}")" <<'REMOTE_SESSION_DB_INSPECT'
set -euo pipefail

remote_dir=""
service_unit="swarm.service"
db_path="/var/lib/swarmd/swarmd.pebble"
stop_service="true"
latest="5"
session_id=""
query=""
all="false"
scan_limit="1000"
message_limit="12"
event_limit="24"
dump="false"
json_output="false"
out_path=""
decode_b64() {
  if [ -z "$1" ]; then
    return 0
  fi
  printf '%s' "$1" | base64 -d
}

for arg in "$@"; do
  case "$arg" in
    remote_dir_b64=*) remote_dir="$(decode_b64 "${arg#remote_dir_b64=}")" ;;
    service_unit_b64=*) service_unit="$(decode_b64 "${arg#service_unit_b64=}")" ;;
    db_path_b64=*) db_path="$(decode_b64 "${arg#db_path_b64=}")" ;;
    stop_service=*) stop_service="${arg#stop_service=}" ;;
    latest=*) latest="${arg#latest=}" ;;
    session_id_b64=*) session_id="$(decode_b64 "${arg#session_id_b64=}")" ;;
    query_b64=*) query="$(decode_b64 "${arg#query_b64=}")" ;;
    all=*) all="${arg#all=}" ;;
    scan_limit=*) scan_limit="${arg#scan_limit=}" ;;
    message_limit=*) message_limit="${arg#message_limit=}" ;;
    event_limit=*) event_limit="${arg#event_limit=}" ;;
    dump=*) dump="${arg#dump=}" ;;
    json_output=*) json_output="${arg#json_output=}" ;;
    out_path_b64=*) out_path="$(decode_b64 "${arg#out_path_b64=}")" ;;
  esac
done

service_was_active="false"
service_scope="none"
restored="false"

is_user_active() { systemctl --user is-active --quiet "$service_unit" >/dev/null 2>&1; }
is_system_active() { systemctl is-active --quiet "$service_unit" >/dev/null 2>&1; }

stop_remote_service() {
  if [ "$stop_service" != "true" ]; then
    printf 'ssh-session-db-inspect: service stop skipped\n' >&2
    return
  fi
  if is_user_active; then
    service_was_active="true"
    service_scope="user"
    printf 'ssh-session-db-inspect: stopping user service %s\n' "$service_unit" >&2
    systemctl --user stop "$service_unit"
    return
  fi
  if is_system_active; then
    service_was_active="true"
    service_scope="system"
    printf 'ssh-session-db-inspect: stopping system service %s\n' "$service_unit" >&2
    systemctl stop "$service_unit" 2>/dev/null || sudo -n systemctl stop "$service_unit"
    return
  fi
  printf 'ssh-session-db-inspect: service %s not active; inspecting without stop\n' "$service_unit" >&2
}

restore_remote_service() {
  if [ "$restored" = "true" ]; then
    return
  fi
  restored="true"
  if [ "$stop_service" != "true" ] || [ "$service_was_active" != "true" ]; then
    return
  fi
  printf 'ssh-session-db-inspect: restoring %s service %s\n' "$service_scope" "$service_unit" >&2
  if [ "$service_scope" = "user" ]; then
    systemctl --user start "$service_unit"
  elif [ "$service_scope" = "system" ]; then
    systemctl start "$service_unit" 2>/dev/null || sudo -n systemctl start "$service_unit"
  fi
}

stop_remote_service
trap restore_remote_service EXIT

if [ ! -d "$db_path" ]; then
  printf 'ssh-session-db-inspect: db path does not exist: %s\n' "$db_path" >&2
  exit 1
fi
if [ ! -d "$remote_dir/swarmd" ]; then
  printf 'ssh-session-db-inspect: remote swarmd dir does not exist: %s/swarmd\n' "$remote_dir" >&2
  exit 1
fi

go_bin=""
for candidate in "$remote_dir/.tools/go/bin/go" "$remote_dir/tools/go/bin/go"; do
  if [ -x "$candidate" ]; then
    go_bin="$candidate"
    break
  fi
done
if [ -z "$go_bin" ] && command -v go >/dev/null 2>&1; then
  go_bin="$(command -v go)"
fi
if [ -z "$go_bin" ]; then
  printf 'ssh-session-db-inspect: could not find go on remote host\n' >&2
  exit 1
fi

cd "$remote_dir/swarmd"
tmpdir="cmd/sessiondbinspect_tmp_$$"
mkdir -p "$tmpdir"
cat > "$tmpdir/main.go" <<'EOF_GO'
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type report struct {
	GeneratedAtUnixMs int64           `json:"generated_at_unix_ms"`
	DBPath            string          `json:"db_path"`
	SessionCount      int             `json:"session_count"`
	ScannedCount      int             `json:"scanned_count"`
	MatchedCount      int             `json:"matched_count"`
	Selection         selectionReport `json:"selection"`
	Sessions          []sessionDump   `json:"sessions"`
}

type selectionReport struct {
	Latest       int    `json:"latest"`
	SessionID    string `json:"session_id,omitempty"`
	Query        string `json:"query,omitempty"`
	All          bool   `json:"all"`
	ScanLimit    int    `json:"scan_limit"`
	MessageLimit int    `json:"message_limit"`
	EventLimit   int    `json:"event_limit"`
	Dump         bool   `json:"dump"`
}

type sessionDump struct {
	Session       pebblestore.SessionSnapshot       `json:"session"`
	LegacyMessages []pebblestore.MessageSnapshot    `json:"legacy_messages,omitempty"`
	V3Messages     []pebblestore.MessageSnapshot    `json:"v3_messages,omitempty"`
	V3Projection   *pebblestore.V3SessionProjection `json:"v3_projection,omitempty"`
	V3RunIntents   []pebblestore.V3SessionRunIntent `json:"v3_run_intents,omitempty"`
	V3Events       []pebblestore.V3SessionEvent     `json:"v3_events,omitempty"`
	V3Outbox       []pebblestore.V3RealtimeOutboxRecord `json:"v3_outbox,omitempty"`
	MatchedReason  string                           `json:"matched_reason,omitempty"`
}

func main() {
	dbPath := flag.String("db", "", "path to swarmd Pebble DB")
	latest := flag.Int("latest", 5, "latest sessions to inspect when no search is provided")
	sessionID := flag.String("session", "", "exact session id")
	query := flag.String("query", "", "case-insensitive search text")
	all := flag.Bool("all", false, "include all scanned sessions")
	scanLimit := flag.Int("scan-limit", 1000, "max newest sessions to scan")
	messageLimit := flag.Int("messages", 12, "tail messages per session")
	eventLimit := flag.Int("events", 24, "tail events per session")
	dump := flag.Bool("dump", false, "dump all messages and events for matched sessions")
	jsonOutput := flag.Bool("json", false, "emit json")
	flag.Parse()

	if strings.TrimSpace(*dbPath) == "" {
		fmt.Fprintln(os.Stderr, "--db is required")
		os.Exit(2)
	}

	store, err := pebblestore.Open(strings.TrimSpace(*dbPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	sessions := pebblestore.NewSessionStore(store)
	allSessions, err := sessions.ListSessions(10000)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list sessions: %v\n", err)
		os.Exit(1)
	}
	sort.Slice(allSessions, func(i, j int) bool { return allSessions[i].UpdatedAt > allSessions[j].UpdatedAt })

	scanned := bounded(allSessions, positive(*scanLimit, 1000))
	selected := make([]sessionDump, 0)
	needle := strings.ToLower(strings.TrimSpace(*query))
	exactSessionID := strings.TrimSpace(*sessionID)
	latestLimit := positive(*latest, 5)

	for index, session := range scanned {
		legacyMessages, _ := sessions.ListMessages(session.ID, 0, 100000)
		v3Messages, _ := sessions.ListV3SessionMessages(session.ID, 0, 100000)
		v3Events, _ := sessions.ListV3SessionEvents(session.ID, 0, 100000)
		v3Intents, _ := sessions.ListV3SessionRunIntents(session.ID, 0, 1000)
		v3Outbox, _ := sessions.ListV3RealtimeOutboxForSessionAfterSeq(session.ID, 0, 100000)
		projection, hasProjection, _ := sessions.GetV3SessionProjection(session.ID)

		candidate := sessionDump{
			Session:        session,
			LegacyMessages: trimMessages(legacyMessages, *messageLimit, *dump),
			V3Messages:     trimMessages(v3Messages, *messageLimit, *dump),
			V3RunIntents:   v3Intents,
			V3Events:       trimEvents(v3Events, *eventLimit, *dump),
			V3Outbox:       trimOutbox(v3Outbox, *eventLimit, *dump),
		}
		if hasProjection {
			p := projection
			candidate.V3Projection = &p
		}

		include, reason := shouldInclude(session, legacyMessages, v3Messages, v3Events, exactSessionID, needle, *all, index, latestLimit)
		if !include {
			continue
		}
		candidate.MatchedReason = reason
		selected = append(selected, candidate)
	}

	report := report{
		GeneratedAtUnixMs: time.Now().UnixMilli(),
		DBPath:            strings.TrimSpace(*dbPath),
		SessionCount:      len(allSessions),
		ScannedCount:      len(scanned),
		MatchedCount:      len(selected),
		Selection: selectionReport{
			Latest:       latestLimit,
			SessionID:    exactSessionID,
			Query:        strings.TrimSpace(*query),
			All:          *all,
			ScanLimit:    positive(*scanLimit, 1000),
			MessageLimit: positive(*messageLimit, 12),
			EventLimit:   positive(*eventLimit, 24),
			Dump:         *dump,
		},
		Sessions: selected,
	}

	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "encode json: %v\n", err)
			os.Exit(1)
		}
		return
	}

	printText(report)
}

func shouldInclude(session pebblestore.SessionSnapshot, legacyMessages, v3Messages []pebblestore.MessageSnapshot, v3Events []pebblestore.V3SessionEvent, sessionID, needle string, all bool, index, latest int) (bool, string) {
	if sessionID != "" {
		if session.ID == sessionID {
			return true, "session id"
		}
		return false, ""
	}
	if needle != "" {
		if sessionMatches(session, needle) {
			return true, "session fields"
		}
		for _, message := range append(append([]pebblestore.MessageSnapshot{}, legacyMessages...), v3Messages...) {
			if strings.Contains(strings.ToLower(message.ID), needle) || strings.Contains(strings.ToLower(message.Role), needle) || strings.Contains(strings.ToLower(message.Content), needle) {
				return true, "message"
			}
		}
		for _, event := range v3Events {
			if strings.Contains(strings.ToLower(event.EventType), needle) || strings.Contains(strings.ToLower(string(event.Payload)), needle) {
				return true, "v3 event"
			}
		}
		return false, ""
	}
	if all {
		return true, "all"
	}
	if index < latest {
		return true, "latest"
	}
	return false, ""
}

func sessionMatches(session pebblestore.SessionSnapshot, needle string) bool {
	metadata, _ := json.Marshal(session.Metadata)
	parts := []string{session.ID, session.Title, session.WorkspacePath, session.WorkspaceName, session.Mode, string(metadata)}
	for _, part := range parts {
		if strings.Contains(strings.ToLower(part), needle) {
			return true
		}
	}
	return false
}

func printText(r report) {
	fmt.Printf("generated_at=%s db=%s sessions=%d scanned=%d matched=%d\n", unixMilli(r.GeneratedAtUnixMs), r.DBPath, r.SessionCount, r.ScannedCount, r.MatchedCount)
	fmt.Printf("selection latest=%d session_id=%q query=%q all=%t dump=%t\n", r.Selection.Latest, r.Selection.SessionID, r.Selection.Query, r.Selection.All, r.Selection.Dump)
	for index, item := range r.Sessions {
		s := item.Session
		fmt.Printf("\n=== SESSION %d matched_by=%s ===\n", index+1, item.MatchedReason)
		fmt.Printf("id=%s title=%q workspace=%q mode=%s updated_at=%s message_count=%d\n", s.ID, s.Title, s.WorkspacePath, s.Mode, unixMilli(s.UpdatedAt), s.MessageCount)
		if item.V3Projection != nil {
			p := item.V3Projection
			fmt.Printf("v3_projection last_event_seq=%d high_watermark=%d updated_at=%s\n", p.LastEventSeq, p.ProjectionHighWatermarkSeq, unixMilli(p.UpdatedAt))
		}
		if len(item.V3RunIntents) > 0 {
			fmt.Printf("v3_run_intents=%d\n", len(item.V3RunIntents))
			for _, intent := range item.V3RunIntents {
				fmt.Printf("- run_id=%s status=%s updated_at=%s blocked_reason=%s\n", intent.RunID, intent.Status, unixMilli(intent.UpdatedAt), oneLine(intent.BlockedReason, 180))
			}
		}
		printMessages("legacy_messages", item.LegacyMessages)
		printMessages("v3_messages", item.V3Messages)
		if len(item.V3Events) > 0 {
			fmt.Printf("v3_events=%d\n", len(item.V3Events))
			for _, event := range item.V3Events {
				payload := map[string]any{}
				_ = json.Unmarshal(event.Payload, &payload)
				if isDiagnosticPayload(payload) {
					fmt.Printf("- DIAG seq=%d type=%s run_id=%s stage=%s source=%s sequence=%s payload=%s\n", event.Seq, event.EventType, stringField(payload, "run_id"), stringField(payload, "stage"), stringField(payload, "source"), stringField(payload, "sequence_label"), jsonOneLine(payload["payload"], 20000))
					continue
				}
				fmt.Printf("- event seq=%d type=%s run_id=%s delta=%q message=%s payload=%s\n", event.Seq, event.EventType, stringField(payload, "run_id"), oneLine(stringField(payload, "delta"), 100), messagePayloadSummary(payload), jsonOneLine(payload, 3000))
			}
		}
		if len(item.V3Outbox) > 0 {
			fmt.Printf("v3_outbox=%d\n", len(item.V3Outbox))
			for _, record := range item.V3Outbox {
				payload := map[string]any{}
				_ = json.Unmarshal(record.Event.Payload, &payload)
				fmt.Printf("- outbox endpoint_seq=%d cursor=%s event_seq=%d type=%s diagnostic=%t payload=%s\n", record.EndpointSeq, record.EndpointCursor, record.Event.Seq, record.Event.EventType, isDiagnosticPayload(payload), jsonOneLine(payload, 3000))
			}
		}
	}
}

func printMessages(label string, messages []pebblestore.MessageSnapshot) {
	if len(messages) == 0 {
		return
	}
	fmt.Printf("%s=%d\n", label, len(messages))
	for _, message := range messages {
		fmt.Printf("- msg seq=%d id=%s role=%s at=%s content=%s\n", message.GlobalSeq, message.ID, message.Role, unixMilli(message.CreatedAt), oneLine(message.Content, 260))
	}
}

func trimMessages(messages []pebblestore.MessageSnapshot, limit int, dump bool) []pebblestore.MessageSnapshot {
	if dump {
		return messages
	}
	limit = positive(limit, 12)
	if len(messages) <= limit {
		return messages
	}
	return messages[len(messages)-limit:]
}

func trimEvents(events []pebblestore.V3SessionEvent, limit int, dump bool) []pebblestore.V3SessionEvent {
	if dump {
		return events
	}
	limit = positive(limit, 24)
	if len(events) <= limit {
		return events
	}
	return events[len(events)-limit:]
}

func trimOutbox(records []pebblestore.V3RealtimeOutboxRecord, limit int, dump bool) []pebblestore.V3RealtimeOutboxRecord {
	if dump {
		return records
	}
	limit = positive(limit, 24)
	if len(records) <= limit {
		return records
	}
	return records[len(records)-limit:]
}

func bounded[T any](items []T, limit int) []T {
	limit = positive(limit, 1000)
	if len(items) <= limit {
		return items
	}
	return items[:limit]
}

func positive(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func stringField(record map[string]any, key string) string {
	if value, ok := record[key].(string); ok {
		return value
	}
	return ""
}

func messagePayloadSummary(payload map[string]any) string {
	message, ok := payload["message"]
	if !ok {
		return ""
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return ""
	}
	return oneLine(string(encoded), 220)
}

func isDiagnosticPayload(payload map[string]any) bool {
	value, ok := payload["diagnostic"].(bool)
	return ok && value
}

func jsonOneLine(value any, limit int) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return oneLine(string(encoded), limit)
}

func unixMilli(ms int64) string {
	if ms <= 0 {
		return "0"
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

func oneLine(value string, limit int) string {
	clean := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\n", " "), "\r", " "))
	if limit <= 0 || len(clean) <= limit {
		return clean
	}
	if limit <= 3 {
		return clean[:limit]
	}
	return clean[:limit-3] + "..."
}
EOF_GO

cmd=("$go_bin" run "./$tmpdir" --db "$db_path" --latest "$latest" --scan-limit "$scan_limit" --messages "$message_limit" --events "$event_limit")
if [ -n "$session_id" ]; then cmd+=(--session "$session_id"); fi
if [ -n "$query" ]; then cmd+=(--query "$query"); fi
if [ "$all" = "true" ]; then cmd+=(--all); fi
if [ "$dump" = "true" ]; then cmd+=(--dump); fi
if [ "$json_output" = "true" ]; then cmd+=(--json); fi

if [ -n "$out_path" ]; then
  mkdir -p "$(dirname "$out_path")"
  "${cmd[@]}" >"$out_path"
  bytes="$(wc -c <"$out_path" | tr -d ' ')"
  printf 'ssh-session-db-inspect: wrote remote output %s (%s bytes)\n' "$out_path" "$bytes" >&2
else
  "${cmd[@]}"
fi

rm -rf "$tmpdir"
restore_remote_service
REMOTE_SESSION_DB_INSPECT
