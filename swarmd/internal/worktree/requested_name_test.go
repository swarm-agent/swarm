package worktree

import (
	"strings"
	"testing"
)

func TestCanonicalizeRequestedWorktreeNameNormalizesRouterName(t *testing.T) {
	got, err := CanonicalizeRequestedWorktreeName("  Fix   Desktop_settings (Now)  ", "")
	if err != nil {
		t.Fatalf("CanonicalizeRequestedWorktreeName: %v", err)
	}
	if got != "agent/fix-desktop-settings-now" {
		t.Fatalf("canonical name = %q, want %q", got, "agent/fix-desktop-settings-now")
	}
}

func TestCanonicalizeRequestedWorktreeNameAppliesConfiguredPrefix(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		want       string
	}{
		{name: "plain prefix", configured: "router", want: "router/fix-api"},
		{name: "template prefix", configured: "router/tasks/<id>", want: "router/tasks/fix-api"},
		{name: "trim surrounding slash", configured: "/router/tasks/", want: "router/tasks/fix-api"},
		{name: "default template", configured: "agent/<id>", want: "agent/fix-api"},
		{name: "placeholder only", configured: "<id>", want: "agent/fix-api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalizeRequestedWorktreeName("Fix API", tt.configured)
			if err != nil {
				t.Fatalf("CanonicalizeRequestedWorktreeName: %v", err)
			}
			if got != tt.want {
				t.Fatalf("canonical name = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCanonicalizeRequestedWorktreeNameRetryUsesExactOneSuffix(t *testing.T) {
	got, err := CanonicalizeRequestedWorktreeNameRetry("Fix API", "router/<id>")
	if err != nil {
		t.Fatalf("CanonicalizeRequestedWorktreeNameRetry: %v", err)
	}
	if got != "router/fix-api-1" {
		t.Fatalf("retry name = %q, want %q", got, "router/fix-api-1")
	}
	first, err := CanonicalizeRequestedWorktreeName("Fix API", "router/<id>")
	if err != nil {
		t.Fatal(err)
	}
	if got == first || strings.Contains(got, "-2") {
		t.Fatalf("retry %q is not the exact one-suffix candidate after %q", got, first)
	}
}

func TestCanonicalizeRequestedWorktreeNameRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name       string
		requested  string
		configured string
	}{
		{name: "empty", requested: "  "},
		{name: "no alphanumeric", requested: "---"},
		{name: "path slash", requested: "../escape"},
		{name: "path backslash", requested: `escape\branch`},
		{name: "ref traversal", requested: "fix..branch"},
		{name: "reflog selector", requested: "fix@{one}"},
		{name: "leading dot", requested: ".hidden"},
		{name: "leading dash", requested: "-option"},
		{name: "lock component", requested: "branch.lock"},
		{name: "unsupported character", requested: "fix🔥"},
		{name: "invalid prefix traversal", requested: "fix", configured: "../agent"},
		{name: "invalid prefix ref character", requested: "fix", configured: "agent~bad"},
		{name: "empty prefix", requested: "fix", configured: "///"},
		{name: "prefix double slash", requested: "fix", configured: "agent//tasks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := CanonicalizeRequestedWorktreeName(tt.requested, tt.configured); err == nil {
				t.Fatalf("CanonicalizeRequestedWorktreeName(%q, %q) = %q, want error", tt.requested, tt.configured, got)
			}
		})
	}
}

func TestCanonicalizeRequestedWorktreeNameRejectsOverlongBranch(t *testing.T) {
	requested := strings.Repeat("a", maxRequestedWorktreeBranchNameBytes)
	if got, err := CanonicalizeRequestedWorktreeName(requested, "agent"); err == nil {
		t.Fatalf("overlong branch accepted as %q", got)
	}
}
