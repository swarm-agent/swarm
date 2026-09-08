// Package taskscope owns the lexical scope contract shared by task preflight
// and sparse worktree allocation. It does not grant filesystem access.
package taskscope

import (
	"fmt"
	"path"
	"strings"
	"unicode"
)

const Guidance = "Use exact workspace-relative file paths or directory paths, optionally ending in /**. Filename globs such as src/item*.go, brace expansion, ?, and character classes are unsupported; discover and list exact files instead. Do not broaden scope to bypass a rejection."

// InvalidError lets callers classify scope failures without inspecting filenames.
type InvalidError struct{ Scope string }

func (e *InvalidError) Error() string {
	return fmt.Sprintf("owned scope %q must be a clean workspace-relative path or a trailing /** scope; list exact files instead of filename globs", e.Scope)
}

// Canonical preserves the allocator's historical whole-worktree and directory
// aliases, but never expands a filename glob or interprets Git pattern input.
func Canonical(raw string) (scope string, whole bool, err error) {
	original := raw
	invalid := func() (string, bool, error) {
		return "", false, &InvalidError{Scope: original}
	}
	if strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return invalid()
	}
	raw = strings.TrimSpace(raw)
	if raw == "." || raw == "*" || raw == "**" || raw == "./**" {
		return "", true, nil
	}
	raw = strings.TrimPrefix(raw, "./")
	if strings.HasSuffix(raw, "/**") {
		raw = strings.TrimSuffix(raw, "/**")
	} else {
		raw = strings.TrimSuffix(raw, "/*")
	}
	if raw == "" || raw == "." || raw == ".." || strings.HasPrefix(raw, "../") || strings.HasPrefix(raw, "/") || path.Clean(raw) != raw || strings.ContainsAny(raw, "*?[]!\\:{}") {
		return invalid()
	}
	return raw, false, nil
}

// ValidateProgram requires a reviewable scope rather than a whole repository
// sentinel. Scope bytes remain unchanged in the approved definition.
func ValidateProgram(raw string) error {
	_, whole, err := Canonical(raw)
	if err != nil {
		return err
	}
	if whole {
		return fmt.Errorf("owned scope must identify explicit files or directories, not the whole workspace")
	}
	return nil
}
