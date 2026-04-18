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

func main() {
	dbPath := flag.String("db", "", "path to swarmd pebble DB")
	title := flag.String("title", "", "exact session title to match")
	contains := flag.String("contains", "", "fallback contains match for title")
	messages := flag.Int("messages", 8, "number of tail messages to print")
	flag.Parse()

	if strings.TrimSpace(*dbPath) == "" {
		fmt.Fprintln(os.Stderr, "--db is required")
		os.Exit(2)
	}
	if strings.TrimSpace(*title) == "" && strings.TrimSpace(*contains) == "" {
		fmt.Fprintln(os.Stderr, "--title or --contains is required")
		os.Exit(2)
	}

	store, err := pebblestore.Open(strings.TrimSpace(*dbPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	sessions := pebblestore.NewSessionStore(store)
	perms := pebblestore.NewPermissionStore(store)

	all, err := sessions.ListSessions(10000)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list sessions: %v\n", err)
		os.Exit(1)
	}

	exact := strings.TrimSpace(*title)
	containsNeedle := strings.ToLower(strings.TrimSpace(*contains))
	matches := make([]pebblestore.SessionSnapshot, 0, 4)
	for _, s := range all {
		t := strings.TrimSpace(s.Title)
		if exact != "" && t == exact {
			matches = append(matches, s)
			continue
		}
		if containsNeedle != "" && strings.Contains(strings.ToLower(t), containsNeedle) {
			matches = append(matches, s)
		}
	}

	fmt.Printf("session_count=%d matched=%d\n", len(all), len(matches))
	if len(matches) == 0 {
		fmt.Println("no matching session found")
		return
	}

	sort.Slice(matches, func(i, j int) bool { return matches[i].UpdatedAt > matches[j].UpdatedAt })

	for i, s := range matches {
		fmt.Printf("\n=== MATCH %d ===\n", i+1)
		prettyJSON("session", s)
		fmt.Printf("session_updated_at=%s\n", unixMilli(s.UpdatedAt))

		usage, ok, err := sessions.GetUsageSummary(s.ID)
		if err != nil {
			fmt.Printf("usage_summary_error=%v\n", err)
		} else if ok {
			prettyJSON("usage_summary", usage)
		} else {
			fmt.Println("usage_summary=none")
		}

		pending, err := perms.ListPendingPermissions(s.ID, 500)
		if err != nil {
			fmt.Printf("pending_permissions_error=%v\n", err)
		} else {
			fmt.Printf("pending_permissions=%d\n", len(pending))
			for _, p := range pending {
				fmt.Printf("- permission id=%s run_id=%s tool=%s mode=%s requirement=%s status=%s created_at=%s updated_at=%s\n",
					p.ID, p.RunID, p.ToolName, p.Mode, p.Requirement, p.Status, unixMilli(p.CreatedAt), unixMilli(p.UpdatedAt))
				if strings.TrimSpace(p.Reason) != "" {
					fmt.Printf("  reason=%s\n", trimOneLine(p.Reason, 240))
				}
			}
		}

		runWaitPrefix := pebblestore.RunWaitPrefix(s.ID)
		runWaitCount := 0
		err = store.IteratePrefix(runWaitPrefix, 2000, func(key string, value []byte) error {
			runWaitCount++
			var rw pebblestore.RunWaitState
			if err := json.Unmarshal(value, &rw); err != nil {
				fmt.Printf("run_wait key=%s decode_error=%v\n", key, err)
				return nil
			}
			fmt.Printf("run_wait key=%s run_id=%s pending_permission_ids=%d updated_at=%s\n",
				key, rw.RunID, len(rw.PendingPermissionIDs), unixMilli(rw.UpdatedAt))
			runPerms, err := perms.ListRunPermissions(s.ID, rw.RunID, 500)
			if err != nil {
				fmt.Printf("run_permissions_error run_id=%s err=%v\n", rw.RunID, err)
				return nil
			}
			fmt.Printf("run_permissions run_id=%s count=%d\n", rw.RunID, len(runPerms))
			for _, rp := range runPerms {
				fmt.Printf("  - id=%s status=%s tool=%s requirement=%s created_at=%s updated_at=%s\n",
					rp.ID, rp.Status, rp.ToolName, rp.Requirement, unixMilli(rp.CreatedAt), unixMilli(rp.UpdatedAt))
			}
			return nil
		})
		if err != nil {
			fmt.Printf("run_wait_iter_error=%v\n", err)
		} else if runWaitCount == 0 {
			fmt.Println("run_wait=none")
		}

		turns, err := sessions.ListTurnUsage(s.ID, 10)
		if err != nil {
			fmt.Printf("turn_usage_error=%v\n", err)
		} else {
			fmt.Printf("turn_usage_count=%d\n", len(turns))
			for _, tu := range turns {
				fmt.Printf("- run_id=%s total_tokens=%d context_window=%d updated_at=%s provider=%s model=%s transport=%s connected_via_websocket=%s\n",
					tu.RunID,
					tu.TotalTokens,
					tu.ContextWindow,
					unixMilli(tu.UpdatedAt),
					emptyIfBlank(tu.Provider, "n/a"),
					emptyIfBlank(tu.Model, "n/a"),
					emptyIfBlank(tu.Transport, "n/a"),
					formatOptionalBool(tu.ConnectedViaWS))
			}
		}

		msgs, err := sessions.ListMessages(s.ID, 0, 5000)
		if err != nil {
			fmt.Printf("messages_error=%v\n", err)
			continue
		}
		fmt.Printf("messages_total=%d\n", len(msgs))
		if len(msgs) == 0 {
			continue
		}
		tail := *messages
		if tail < 1 {
			tail = 1
		}
		start := len(msgs) - tail
		if start < 0 {
			start = 0
		}
		for _, m := range msgs[start:] {
			fmt.Printf("- seq=%d role=%s at=%s content=%s\n",
				m.GlobalSeq, m.Role, unixMilli(m.CreatedAt), trimOneLine(m.Content, 260))
		}
	}
}

func prettyJSON(label string, v any) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Printf("%s=<marshal error: %v>\n", label, err)
		return
	}
	fmt.Printf("%s=%s\n", label, string(raw))
}

func unixMilli(ms int64) string {
	if ms <= 0 {
		return "0"
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func trimOneLine(s string, max int) string {
	clean := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " "))
	if max <= 0 || len(clean) <= max {
		return clean
	}
	if max <= 3 {
		return clean[:max]
	}
	return clean[:max-3] + "..."
}

func formatOptionalBool(value *bool) string {
	if value == nil {
		return "n/a"
	}
	if *value {
		return "true"
	}
	return "false"
}

func emptyIfBlank(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
