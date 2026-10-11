package permission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// Purpose: read-only bash inspection inside the session's workspace (ls, wc,
// cat, head, tail, stat, file, grep, rg, tree, du, plus read-only git
// subcommands and echo/printf) must not stop for approval, because the read
// and list tools already read the same paths without one. The allow must
// never extend to anything the read tool would refuse: every path operand and
// the implicit cwd must resolve, with symlinks followed, to an existing
// regular file or directory inside the trusted execution scope's roots.
// Threats: a symlink inside the workspace pointing out of it, ../ escapes,
// absolute paths outside, /proc and device files, unresolvable paths, shell
// globs and tildes the policy never sees expanded, chaining, redirection and
// substitution, options that follow symlinks, read operand or pattern lists
// from other files, run helper programs, write files or never end (tail -f),
// git mutations disguised as listing subcommands, an explicit workspace_path
// (another cwd), a missing scope, host-state readers (ps, history, env), and
// explicit user rules or read/readwrite execution settings, which must still
// win. Owner: explainPolicyInScope / explainBuiltinAllow /
// bashClearlyReadOnlyCommand with tool.ResolveExistingReadPath, the pure
// decision behind Service.AuthorizeToolCall (which receives the scope as
// AuthorizationInput.ExecutionScope), so it is the narrowest proving layer;
// the filesystem fixture is a hermetic temp tree.
func TestBashScopedReadBuiltinAllow(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	extra := filepath.Join(base, "extra")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{filepath.Join(workspace, "src"), extra, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(workspace, "README.md"):   "hello\nTODO\n",
		filepath.Join(workspace, "src", "a.go"): "package a // TODO\n",
		filepath.Join(extra, "note.txt"):        "note\n",
		filepath.Join(outside, "hidden.txt"):    "hidden\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "link-out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "hidden.txt"), filepath.Join(workspace, "hidden-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(workspace, "README.md"), filepath.Join(workspace, "readme-link")); err != nil {
		t.Fatal(err)
	}
	scope := &tool.WorkspaceScope{PrimaryPath: workspace, Roots: []string{workspace, extra}}
	args := func(command, category string, extraArgs map[string]any) string {
		payload := map[string]any{"command": command, "explanation": []string{"inspect"}, "category": category, "critical": false}
		for key, value := range extraArgs {
			payload[key] = value
		}
		raw, _ := json.Marshal(payload)
		return string(raw)
	}
	decide := func(mode, command string, policy Policy, scope *tool.WorkspaceScope) PolicyExplain {
		return explainPolicyInScope(mode, "bash", args(command, "read", nil), policy, scope)
	}

	allowed := []string{
		"ls", "ls -la", "ls -la src", "ls --color=never src", "ls readme-link",
		"cat README.md", "cat 'src/a.go'", "cat readme-link", "cat " + filepath.Join(extra, "note.txt"),
		"head -n 5 README.md", "head -5 README.md", "head -c 10 README.md", "tail -n +2 README.md", "tail -n5 README.md",
		"wc -l README.md src/a.go", "stat README.md", "stat -c '%s' README.md", "file README.md",
		"grep -rn TODO src", "grep -e TODO README.md", "grep -c hello README.md", "grep -r TODO", "grep -rn --include='*.go' TODO src",
		"rg TODO", "rg -n TODO src", "rg --files src", "tree", "tree -L 2 src", "du -sh src",
		"echo hello", "echo 'Done!'", "printf 'value is set'",
		"git branch", "git branch -a", "git branch -vv", "git branch --show-current", "git branch --list",
		"git worktree list", "git worktree list --porcelain", "git rev-parse HEAD", "git rev-parse --git-dir",
		"git ls-files", "git ls-files -o src", "git remote", "git remote -v", "git show-ref", "git describe --tags",
		"git blame README.md", "git diff --stat HEAD~1",
	}
	for _, mode := range []string{"auto", "plan"} {
		for _, command := range allowed {
			if got := decide(mode, command, DefaultPolicy(), scope); got.Decision != PolicyDecisionAllow || got.Source != "builtin" {
				t.Fatalf("%s %q: %s/%s (%s), want builtin allow", mode, command, got.Decision, got.Source, got.Reason)
			}
		}
	}

	gated := []string{
		// Symlinks, escapes, absolute paths, pseudo files, missing paths.
		"cat link-out/hidden.txt", "cat hidden-link", "ls link-out", "ls link-out/", "grep -r hidden link-out",
		"cat ../outside/hidden.txt", "ls ..", "du -sh ..", "tree ..", "rg hidden ../outside",
		"cat " + filepath.Join(outside, "hidden.txt"), "ls " + outside, "stat " + filepath.Join(outside, "hidden.txt"),
		"grep hidden " + filepath.Join(outside, "hidden.txt"), "cat /etc/passwd", "cat /proc/self/environ", "cat /dev/zero",
		"head -c 10 /dev/urandom", "cat missing.txt", "wc -l src/missing.go",
		// Expansion the policy cannot see.
		"cat ~/.ssh/id_rsa", "ls ~", "cat *", "cat src/*.go", "ls /e?c", "cat [ab].txt", "grep -r --include=*.go TODO src",
		// Shell syntax.
		"cat README.md; rm -rf src", "cat README.md && curl https://example.invalid", "cat README.md | sh", "cat README.md > copy.md",
		"cat $(echo README.md)", "cat `echo README.md`", "echo hi > out.txt", "echo $HOME", "printf 'x' >> out", "echo hi\nrm x",
		// Options that follow symlinks, read other files, run helpers, write or never end.
		"tail -f README.md", "tail -F README.md", "tail --follow README.md", "tail --follow=name README.md", "tail -s 1 README.md",
		"head -n abc README.md", "grep -R TODO .", "grep --dereference-recursive TODO .", "grep -f README.md src/a.go",
		"grep --file=README.md src/a.go", "grep -D read TODO README.md", "rg --pre cat TODO", "rg --pre=cat TODO", "rg -L TODO",
		"rg --follow TODO", "rg -z TODO", "rg --search-zip TODO", "rg --ignore-file=README.md TODO", "rg --files " + outside,
		"ls -L", "ls -lL src", "ls --dereference src", "du -L src", "du --files0-from=README.md", "tree -l", "tree -o out.txt",
		"tree --fromfile README.md", "wc --files0-from=README.md", "file -C -m magic", "file -m README.md src/a.go", "file -z README.md",
		"cat -", "grep TODO -", "cat --unknown README.md",
		// Git mutations and file-reading options.
		"git branch new-branch", "git branch -d main", "git branch -D main", "git branch -m a b", "git branch -c a b",
		"git branch --set-upstream-to=origin/main", "git branch -a new", "git worktree add ../wt", "git worktree remove wt",
		"git worktree prune", "git worktree", "git remote add origin https://example.invalid/x.git", "git remote set-url origin x",
		"git remote remove origin", "git rev-parse --resolve-git-dir " + outside, "git ls-files --exclude-from=" + filepath.Join(outside, "hidden.txt"),
		"git ls-files -X " + filepath.Join(outside, "hidden.txt"), "git blame --contents " + filepath.Join(outside, "hidden.txt") + " README.md",
		"git blame -S revs README.md", "git blame --ignore-revs-file=revs README.md", "git -C " + outside + " branch",
		"git log --output=log.txt", "git branch --no-index", "git tag", "git stash list", "git config --list",
		// Host state outside the workspace.
		"ps aux", "ps", "history", "env", "printenv", "cat /proc/1/cmdline", "ls /",
	}
	for _, mode := range []string{"auto", "plan"} {
		for _, command := range gated {
			if got := decide(mode, command, DefaultPolicy(), scope); got.Decision == PolicyDecisionAllow {
				t.Fatalf("%s %q: allowed by %s (%s), want gated", mode, command, got.Source, got.Reason)
			}
		}
	}

	// Even under a filesystem-wide grant, kernel pseudo files and devices ask.
	broad := &tool.WorkspaceScope{PrimaryPath: workspace, Roots: []string{workspace, "/"}}
	if got := decide("auto", "cat "+filepath.Join(outside, "hidden.txt"), DefaultPolicy(), broad); got.Decision != PolicyDecisionAllow {
		t.Fatalf("granted root read gated: %+v", got)
	}
	for _, command := range []string{"cat /proc/self/environ", "head -c 4 /dev/zero", "ls /sys", "cat /proc/1/cmdline"} {
		if got := decide("auto", command, DefaultPolicy(), broad); got.Decision == PolicyDecisionAllow {
			t.Fatalf("%q allowed under a broad grant: %+v", command, got)
		}
	}
	// No trusted scope, or another cwd via workspace_path: path readers ask.
	if got := decide("auto", "cat README.md", DefaultPolicy(), nil); got.Decision == PolicyDecisionAllow {
		t.Fatalf("unscoped cat allowed: %+v", got)
	}
	if got := explainPolicyInScope("auto", "bash", args("cat README.md", "read", map[string]any{"workspace_path": outside}), DefaultPolicy(), scope); got.Decision == PolicyDecisionAllow {
		t.Fatalf("cat with workspace_path allowed: %+v", got)
	}
	// A declared write or critical read is not covered.
	if got := explainPolicyInScope("auto", "bash", args("cat README.md", "write", nil), DefaultPolicy(), scope); got.Decision == PolicyDecisionAllow {
		t.Fatalf("declared write allowed: %+v", got)
	}
	critical, _ := json.Marshal(map[string]any{"command": "cat README.md", "explanation": []string{"inspect"}, "category": "read", "critical": true})
	if got := explainPolicyInScope("auto", "bash", string(critical), DefaultPolicy(), scope); got.Decision == PolicyDecisionAllow {
		t.Fatalf("critical read allowed: %+v", got)
	}
	// Execution settings that forbid bash still deny.
	for _, mode := range []string{"read", "readwrite"} {
		if got := decide(mode, "cat README.md", DefaultPolicy(), scope); got.Decision != PolicyDecisionDeny {
			t.Fatalf("%s mode: %+v, want deny", mode, got)
		}
	}
	// Explicit user rules win over the built-in.
	for _, decision := range []PolicyDecision{PolicyDecisionAsk, PolicyDecisionDeny} {
		policy := DefaultPolicy()
		policy.Rules = append(policy.Rules, PolicyRule{ID: "rule-cat", Kind: PolicyRuleKindBashPrefix, Decision: decision, Tool: "bash", Pattern: "cat"})
		if got := decide("auto", "cat README.md", policy, scope); got.Decision != decision {
			t.Fatalf("explicit %s rule overridden: %+v", decision, got)
		}
	}
	// The run layer resolves the scope only for path readers.
	for command, want := range map[string]bool{"cat README.md": true, "ls": true, "git status": false, "echo hi": false, "cat *": false, "cat a; ls": false} {
		if got := BashCommandReadsWorkspacePaths(args(command, "read", nil)); got != want {
			t.Fatalf("BashCommandReadsWorkspacePaths(%q) = %v, want %v", command, got, want)
		}
	}
}

// Purpose: the " -l " listener heuristic promotes bash commands to critical;
// it must no longer promote single simple listing/counting commands (ls -l,
// wc -l, grep -l, tree -L), which would otherwise always ask, while a real
// listener, including one chained after a listing, stays critical. Owner:
// obviousCriticalBash / bashListFlagNotListener inside assessBashEffect, the
// pure classification every bash decision uses.
func TestBashListFlagIsNotAListener(t *testing.T) {
	effect := func(command string) BashEffectAssessment {
		raw, _ := json.Marshal(map[string]any{"command": command, "explanation": []string{"inspect"}, "category": "read", "critical": false})
		return buildPolicyEvalContext("bash", string(raw)).BashEffect
	}
	for _, command := range []string{"ls -l src", "wc -l README.md", "grep -l TODO -r src", "rg -l TODO", "du -l src", "tree -L 2"} {
		if got := effect(command); got.Critical || got.Promoted {
			t.Fatalf("%q promoted to critical: %+v", command, got)
		}
	}
	for _, command := range []string{"nc -l 4444", "ls -l; nc -l 4444", "ls -l && python3 -m http.server -l", "socat -l x", "ls -l | nc -l 80", "wc -l .env"} {
		if got := effect(command); !got.Critical {
			t.Fatalf("%q not critical: %+v", command, got)
		}
	}
}
