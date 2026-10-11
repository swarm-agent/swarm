package worktree

import (
	"fmt"
	"strings"
)

// maxDescribedStatusEntries bounds how many paths a blocking message names.
const maxDescribedStatusEntries = 10

// untrackedCollisionCode marks an untracked target file the task would write.
const untrackedCollisionCode = "?!"

// statusEntry is one record of `git status --porcelain=v1`.
type statusEntry struct {
	Code string // two-letter XY status
	Path string
}

func (e statusEntry) untracked() bool { return e.Code == "??" }

// describe names the path and what kind of uncommitted change it is.
func (e statusEntry) describe() string {
	kind := "modified"
	switch {
	case e.untracked():
		kind = "untracked"
	case e.Code == untrackedCollisionCode:
		kind = "untracked, and the task writes this path"
	case strings.ContainsRune(e.Code, 'U') || e.Code == "AA" || e.Code == "DD":
		kind = "conflict"
	case strings.ContainsRune(e.Code, 'D'):
		kind = "deleted"
	case e.Code[1] == ' ':
		kind = "staged"
	}
	return e.Path + " (" + kind + ")"
}

// parsePorcelainStatusZ parses `git status --porcelain=v1 -z` output. Rename and
// copy records carry their original path as a following field, which is skipped.
func parsePorcelainStatusZ(raw string) []statusEntry {
	var entries []statusEntry
	fields := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		if len(field) < 4 {
			continue
		}
		entry := statusEntry{Code: field[:2], Path: field[3:]}
		if entry.Code[0] == 'R' || entry.Code[0] == 'C' {
			i++
		}
		entries = append(entries, entry)
	}
	return entries
}

// untrackedPathCollides reports whether an untracked file sits where a changed
// path would be written: the same path, a directory the change needs, or a
// file below a path the change turns into a file.
func untrackedPathCollides(untracked string, changed []string) bool {
	untracked = strings.TrimSuffix(untracked, "/")
	for _, path := range changed {
		if path == untracked || strings.HasPrefix(path, untracked+"/") || strings.HasPrefix(untracked, path+"/") {
			return true
		}
	}
	return false
}

// UntrackedCollisions returns the untracked paths that integrating the changed
// paths would overwrite or replace.
func UntrackedCollisions(untracked, changed []string) []string {
	var out []string
	for _, path := range untracked {
		if untrackedPathCollides(path, changed) {
			out = append(out, path)
		}
	}
	return out
}

// describeStatusEntries renders at most maxDescribedStatusEntries entries and
// says how many more were omitted.
func describeStatusEntries(entries []statusEntry) []string {
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(entries), maxDescribedStatusEntries+1))
	for i, entry := range entries {
		if i == maxDescribedStatusEntries {
			out = append(out, fmt.Sprintf("and %d more", len(entries)-i))
			break
		}
		out = append(out, entry.describe())
	}
	return out
}

// DescribePaths joins at most maxDescribedStatusEntries paths for a message.
func DescribePaths(paths []string) string {
	if len(paths) > maxDescribedStatusEntries {
		return strings.Join(paths[:maxDescribedStatusEntries], ", ") + fmt.Sprintf(", and %d more", len(paths)-maxDescribedStatusEntries)
	}
	return strings.Join(paths, ", ")
}
