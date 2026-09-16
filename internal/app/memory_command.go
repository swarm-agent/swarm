package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

const memoryHelp = `/memory or /memory refresh — list saved objects and current revision
/memory edit <id> <replacement content> — replace selected content, preserving scope and pin
/memory forget <id> — show permanent-forget warning
/memory forget <id> confirm — permanently forget and redact history (cannot undo)
/memory draft — show the last failed edit command
Refresh after a revision conflict, inspect the current object, then resubmit your edit.`

func (a *App) memoryOutput(text string) {
	a.home.SetCommandOverlay(strings.Split(text, "\n"))
	a.home.SetStatus("Memory")
	if a.route == "v3chat" && a.v3Chat != nil {
		a.v3Chat.SetCommandEmission(text)
	}
}

func (a *App) handleMemoryCommand(raw string) {
	fields := strings.Fields(raw)
	if len(fields) == 1 || (len(fields) == 2 && fields[1] == "refresh") {
		a.refreshMemory()
		return
	}
	if len(fields) == 2 && fields[1] == "draft" {
		a.memoryOutput("Saved edit draft:\n" + a.memoryDraft)
		return
	}
	if len(fields) < 3 || (fields[1] != "edit" && fields[1] != "forget") {
		a.memoryOutput(memoryHelp)
		return
	}
	if a.memoryDocument == nil {
		a.memoryOutput("Run /memory first to inspect the current objects and revision.\n" + memoryHelp)
		return
	}
	id := fields[2]
	var entry client.MemoryEntry
	for _, candidate := range a.memoryDocument.Entries {
		if candidate["id"] == id {
			entry = candidate
			break
		}
	}
	if entry == nil {
		a.memoryOutput("Memory object not found. Run /memory refresh and select an existing ID.")
		return
	}
	action := "forget"
	var updated client.MemoryEntry
	if fields[1] == "forget" {
		if len(fields) != 4 || fields[3] != "confirm" {
			a.memoryOutput(fmt.Sprintf("Permanently forget %s and redact its history? This cannot be restored.\nTo confirm: /memory forget %s confirm\nNo mutation performed.", id, id))
			return
		}
	} else {
		// Consume only the command/verb/ID tokens, preserving internal whitespace in content.
		content := strings.TrimSpace(raw)
		for i := 0; i < 3; i++ {
			n := strings.IndexAny(content, " \t\r\n")
			if n < 0 {
				content = ""
				break
			}
			content = strings.TrimLeft(content[n:], " \t\r\n")
		}
		if strings.TrimSpace(content) == "" {
			a.memoryOutput(memoryHelp)
			return
		}
		a.memoryDraft = raw
		updated = make(client.MemoryEntry, len(entry))
		for key, value := range entry {
			updated[key] = value
		}
		updated["content"] = content
		// Explicit user edits are not automated source provenance.
		delete(updated, "sources")
		action = "remember"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.api.MutateMemory(ctx, a.memoryDocument.Revision, action, "Explicit TUI memory "+fields[1], id, updated); err != nil {
		a.memoryOutput("Memory mutation failed: " + err.Error() + "\nYour edit draft is preserved (/memory draft). Refresh before retrying a stale revision.")
		return
	}
	a.memoryDraft = ""
	a.memoryDocument = nil // Never reuse a pre-mutation revision if refresh fails.
	a.refreshMemory()
}

func (a *App) refreshMemory() {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	doc, err := a.api.GetMemory(ctx)
	if err != nil {
		a.memoryOutput("Memory refresh failed: " + err.Error() + "\nRetry /memory refresh. Any edit draft is preserved.")
		return
	}
	a.memoryDocument = &doc
	lines := []string{fmt.Sprintf("Memory revision %d", doc.Revision), memoryHelp}
	if len(doc.Entries) == 0 {
		lines = append(lines, "No saved memories.")
	}
	for _, entry := range doc.Entries {
		lines = append(lines, fmt.Sprintf("\n%s · %s · pinned=%v\n%s", entry["id"], entry["kind"], entry["pinned"], entry["content"]))
	}
	a.memoryOutput(strings.Join(lines, "\n"))
}
