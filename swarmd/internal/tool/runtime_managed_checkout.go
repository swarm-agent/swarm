package tool

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/worktree"
)

func managedCheckoutName(lease environments.DeploymentLease) string {
	sum := sha256.Sum256([]byte(lease.AccountScopeID + "\x00" + lease.ID + "\x00" + lease.ConsumerID))
	return ".swarm-task-" + hex.EncodeToString(sum[:16])
}
func managedQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func managedBashCommand(lease environments.DeploymentLease, command string) []string {
	if lease.TaskBinding != nil && lease.TaskBinding.ParentLeaseID != "" {
		// The receipt-derived path is never accepted from model arguments. No local
		// or parent-checkout fallback when preparation is missing or invalid.
		command = "test ! -L " + managedQuote(managedCheckoutName(lease)) + " || exit 125; cd -- " + managedQuote(managedCheckoutName(lease)) + " || exit 125; test -f .git/swarm-ready || exit 125; bash -c " + managedQuote(command)
	}
	return []string{"bash", "-c", sandboxBashScript, "swarm-managed-bash", command}
}
func (r *Runtime) managedChildScope(id string) (WorkspaceScope, ManagedExecutionBinding, error) {
	s, found, err := r.sessions.GetSession(id)
	if err != nil || !found || !s.WorktreeEnabled || s.WorktreeRootPath == "" || s.Metadata["swarm_v3_worktree_owner_session_id"] != id || s.Metadata["swarm_v3_runtime_workspace_path"] != s.WorktreeRootPath {
		return WorkspaceScope{}, ManagedExecutionBinding{}, errors.New("managed child worktree ownership unavailable")
	}
	scope := WorkspaceScope{SessionID: id, Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: s.UserID, AccountScopeID: s.AccountScopeID, SessionID: id}, PrimaryPath: s.WorktreeRootPath, Roots: []string{s.WorktreeRootPath}, WorktreeEnabled: true, WorktreeRootPath: s.WorktreeRootPath, WorktreeBranch: s.WorktreeBranch, WorktreeBaseCommit: asString(s.Metadata["base_commit"]), MutationScopes: asStringSlice(s.Metadata["owned_scope"])}
	b, err := r.managedExecutionBinding(scope)
	if err != nil {
		return scope, ManagedExecutionBinding{}, err
	}
	if b == nil {
		return scope, ManagedExecutionBinding{}, errors.New("managed selection missing")
	}
	return scope, *b, nil
}
func (r *Runtime) managedCommand(ctx context.Context, scope WorkspaceScope, b ManagedExecutionBinding, script string, inCheckout bool) (*provider.ExecResult, error) {
	lease, err := r.managedReceipt(scope, b)
	if err != nil {
		return nil, err
	}
	if inCheckout && (lease.TaskBinding.ParentLeaseID == "" || !scope.WorktreeEnabled) {
		return nil, errors.New("managed file/Git checkout requires an admitted delegated Coder")
	}
	command := []string{"bash", "-c", "set -euo pipefail\n" + script}
	if inCheckout {
		command = managedBashCommand(lease, "bash -c "+managedQuote("set -euo pipefail\n"+script))
	}
	service := r.deploymentManager.(managedLeaseExecutor)
	result, err := service.ExecTaskLease(ctx, scope.Principal.AccountScopeID, b.WorkspaceID, b.LeaseID, environments.OperationAttribution{Actor: scope.Principal.UserID, SessionID: scope.SessionID}, provider.ExecRequest{Command: command, Timeout: time.Minute, MaxOutput: environments.MaxOperationOutputBytes})
	if err != nil {
		return result, err
	}
	if result == nil {
		return nil, errors.New("managed operation returned no receipt")
	}
	if result.ExitCode != 0 || result.Truncated {
		return result, fmt.Errorf("managed operation %s failed (exit %d, truncated %t): %s", result.OperationID, result.ExitCode, result.Truncated, result.CombinedOutput())
	}
	return result, nil
}

// managedCommandOutput runs a checkout command whose stdout is parsed. The
// managed wrapper merges stderr into stdout and runs a login shell, so profile,
// logout, locale or other shell-startup output can surround the payload. The
// script's output is framed by per-call random markers and only the framed
// region is returned; a missing or repeated frame fails closed. The script's own
// stderr (for example mktemp, git, stat or base64 diagnostics) is captured in a
// private file outside the frame and is reported only when the script fails.
func (r *Runtime) managedCommandOutput(ctx context.Context, scope WorkspaceScope, b ManagedExecutionBinding, script string) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	begin, end := managedFrameMarkers(hex.EncodeToString(nonce[:]))
	// The payload runs in a subshell with errexit re-enabled; errexit is off only
	// around the subshell itself so a failing payload command still aborts it.
	framed := "e=$(mktemp)\ntrap 'rm -f -- \"$e\"' EXIT\n" +
		"printf '\\n%s\\n' " + managedQuote(begin) + "\n" +
		"set +e\n(\nset -euo pipefail\n" + script + "\n) 2>\"$e\"\nrc=$?\nset -e\n" +
		"if test \"$rc\" -ne 0; then cat -- \"$e\" >&2; exit \"$rc\"; fi\n" +
		"printf '\\n%s\\n' " + managedQuote(end)
	result, err := r.managedCommand(ctx, scope, b, framed, true)
	if err != nil {
		return "", err
	}
	return managedFramedOutput(result.Stdout, begin, end)
}

func managedFrameMarkers(nonce string) (string, string) {
	return "swarm-managed-begin-" + nonce, "swarm-managed-end-" + nonce
}

func managedFramedOutput(stdout, begin, end string) (string, error) {
	open, closing := "\n"+begin+"\n", "\n"+end+"\n"
	if strings.Count(stdout, open) != 1 || strings.Count(stdout, closing) != 1 {
		return "", errors.New("managed operation returned no framed result")
	}
	_, rest, _ := strings.Cut(stdout, open)
	payload, _, found := strings.Cut(rest, closing)
	if !found {
		return "", errors.New("managed operation returned no framed result")
	}
	return payload, nil
}

func (r *Runtime) prepareManagedCheckout(ctx context.Context, childID string) error {
	scope, b, err := r.managedChildScope(childID)
	if err != nil {
		return err
	}
	lease, err := r.managedReceipt(scope, b)
	if err != nil {
		return err
	}
	if lease.TaskBinding.ParentLeaseID == "" || scope.WorktreeBaseCommit != lease.PreparedSource.Build.Product.Commit {
		return errors.New("managed child base differs from prepared source")
	}
	pack, err := worktree.ExportManagedCheckout(ctx, scope.PrimaryPath, scope.WorktreeBranch, scope.WorktreeBaseCommit)
	if err != nil {
		return err
	}
	name := managedQuote(managedCheckoutName(lease))
	// An incomplete preparation is never reused. Its unique directory remains
	// available for diagnosis until the leased deployment is released.
	if _, err = r.managedCommand(ctx, scope, b, "umask 077; mkdir -- "+name+"; cd -- "+name+"; git init --quiet --initial-branch=managed --template=; : > .git/swarm-pack", false); err != nil {
		return err
	}
	for offset := 0; offset < len(pack); offset += 32768 {
		end := offset + 32768
		if end > len(pack) {
			end = len(pack)
		}
		chunk := base64.StdEncoding.EncodeToString(pack[offset:end])
		if _, err = r.managedCommand(ctx, scope, b, "cd -- "+name+"; printf %s "+managedQuote(chunk)+" | base64 -d >> .git/swarm-pack", false); err != nil {
			return err
		}
	}
	sum := sha256.Sum256(pack)
	finish := "cd -- " + name + "; test \"$(sha256sum .git/swarm-pack | cut -d' ' -f1)\" = " + managedQuote(hex.EncodeToString(sum[:])) + "; git unpack-objects < .git/swarm-pack; printf '%s\\n' " + managedQuote(scope.WorktreeBaseCommit) + " > .git/shallow; git -c core.hooksPath=/dev/null checkout --quiet -B managed " + managedQuote(scope.WorktreeBaseCommit) + "; rm .git/swarm-pack; printf '%s\\n' " + managedQuote(scope.WorktreeBaseCommit) + " > .git/swarm-ready"
	_, err = r.managedCommand(ctx, scope, b, finish, false)
	return err
}

// CompleteManagedCheckout is called by trusted orchestration before its normal
// Coder Git inspection. It never integrates or pushes the parent repository.
func (r *Runtime) CompleteManagedCheckout(ctx context.Context, childID string) error {
	scope, b, err := r.managedChildScope(childID)
	if err != nil {
		return err
	}
	base := scope.WorktreeBaseCommit
	output, err := r.managedCommandOutput(ctx, scope, b, "test -z \"$(git status --porcelain --untracked-files=all)\"; git merge-base --is-ancestor "+managedQuote(base)+" HEAD; git rev-parse HEAD; git rev-list --objects --no-object-names "+managedQuote(base+"..HEAD")+" | git pack-objects --stdout | base64 -w0")
	if err != nil {
		return err
	}
	head, encoded, found := strings.Cut(output, "\n")
	head = strings.TrimSpace(head)
	if !found || !validCommitID(head) || head == base {
		return errors.New("managed Coder completed without a new commit")
	}
	pack, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(pack) > worktree.MaxManagedPackBytes {
		return errors.New("managed Git handoff invalid or exceeds transfer limit")
	}
	// Waiting for a sibling's import must not extend an expired/revoked receipt.
	// Revalidate through the same durable provider boundary under the repository
	// lock, including immediately before the allocated child ref can advance.
	fence := func() error {
		_, err := r.managedCommand(ctx, scope, b, "test \"$(git rev-parse HEAD)\" = "+managedQuote(head)+"; test -z \"$(git status --porcelain --untracked-files=all)\"", true)
		return err
	}
	return worktree.ImportManagedCheckout(ctx, scope.PrimaryPath, scope.WorktreeBranch, base, head, pack, scope.MutationScopes, fence)
}

func managedRelativePath(scope WorkspaceScope, raw string, mutate bool) (string, error) {
	if filepath.IsAbs(raw) {
		rel, err := filepath.Rel(scope.PrimaryPath, raw)
		if err != nil {
			return "", err
		}
		raw = rel
	}
	if raw == "" || raw == "." || filepath.Clean(raw) != raw || strings.HasPrefix(raw, "../") || raw == ".." || strings.ContainsAny(raw, "\x00\r\n") {
		return "", errors.New("managed tool requires a checkout-relative file path")
	}
	for _, part := range strings.Split(raw, "/") {
		if strings.EqualFold(part, ".git") {
			return "", errors.New("managed file tools cannot access Git metadata")
		}
	}
	if mutate && !workspaceMutationAllowed(scope, filepath.Join(scope.PrimaryPath, raw)) {
		return "", errors.New("managed file is outside owned scope")
	}
	return raw, nil
}

// The lease is the security boundary. File tools additionally refuse symlinks
// and use compare-and-swap content hashes so an edit cannot overwrite a change
// made between its read and write. Temporary decoding never targets host source.
func managedFileGuard(name string) string {
	return "p=" + managedQuote(name) + "; q=$p; while test \"$q\" != .; do test ! -L \"$q\"; q=$(dirname -- \"$q\"); done; "
}
func (r *Runtime) executeManagedFile(ctx context.Context, scope WorkspaceScope, b ManagedExecutionBinding, name string, args map[string]any) (string, error) {
	rel, err := managedRelativePath(scope, asString(args["path"]), name != "read")
	if err != nil {
		return "", err
	}
	for _, key := range []string{"workspace_path", "committed_source", "lease_id", "deployment_id", "working_dir"} {
		if _, ok := args[key]; ok {
			return "", errors.New("managed file overrides are forbidden")
		}
	}
	script := managedFileGuard(rel) + "if test -e \"$p\"; then test -f \"$p\"; test \"$(stat -c %s -- \"$p\")\" -le 1048576; printf 'present\\n'; base64 -w0 -- \"$p\"; else printf 'absent\\n'; fi"
	receipt, err := r.managedCommandOutput(ctx, scope, b, script)
	if err != nil {
		return "", err
	}
	state, encoded, ok := strings.Cut(receipt, "\n")
	if !ok || (state != "present" && state != "absent") {
		return "", errors.New("invalid managed file receipt")
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", err
	}
	temp, err := os.MkdirTemp("", "swarm-managed-file-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temp)
	local := filepath.Join(temp, "content")
	if state == "present" {
		if err = os.WriteFile(local, data, 0600); err != nil {
			return "", err
		}
	}
	copied := map[string]any{}
	for k, v := range args {
		copied[k] = v
	}
	copied["path"] = "content"
	localScope := WorkspaceScope{PrimaryPath: temp, Roots: []string{temp}}
	var output string
	switch name {
	case "read":
		output, err = executeRead(localScope, copied)
	case "write":
		output, err = executeWrite(localScope, copied)
	case "edit":
		output, err = executeEdit(localScope, copied)
	default:
		return "", errors.New("unsupported managed file tool")
	}
	if err != nil {
		return "", err
	}
	if name != "read" {
		after, readErr := os.ReadFile(local)
		if readErr != nil {
			return "", readErr
		}
		if len(after) > 1<<20 {
			return "", errors.New("managed file exceeds 1 MiB limit")
		}
		sum := sha256.Sum256(data)
		guard := "test ! -e \"$p\"; "
		if state == "present" {
			guard = "test -f \"$p\"; test \"$(sha256sum -- \"$p\" | cut -d' ' -f1)\" = " + managedQuote(hex.EncodeToString(sum[:])) + "; "
		}
		// Bound each command argument below the OS argv string limit. The staging
		// file is unique; traps remove it on any failed write or CAS.
		staged, stageErr := r.managedCommandOutput(ctx, scope, b, "mktemp .git/swarm-file.XXXXXXXX")
		if stageErr != nil {
			return "", stageErr
		}
		staging := strings.TrimSpace(staged)
		if filepath.Dir(staging) != ".git" || !strings.HasPrefix(staging, ".git/swarm-file.") || strings.ContainsAny(staging, "\r\n ") {
			return "", errors.New("invalid managed staging path")
		}
		for off := 0; off < len(after); off += 32768 {
			end := off + 32768
			if end > len(after) {
				end = len(after)
			}
			if _, err = r.managedCommand(ctx, scope, b, "printf %s "+managedQuote(base64.StdEncoding.EncodeToString(after[off:end]))+" | base64 -d >> "+managedQuote(staging), true); err != nil {
				return "", err
			}
		}
		script = managedFileGuard(rel) + "staged=" + managedQuote(staging) + "; trap 'rm -f -- \"$staged\"' EXIT; " + guard + "mkdir -p -- \"$(dirname -- \"$p\")\"; if test -e \"$p\"; then chmod --reference=\"$p\" \"$staged\"; else chmod 644 \"$staged\"; fi; mv -T -- \"$staged\" \"$p\""
		if _, err = r.managedCommand(ctx, scope, b, script, true); err != nil {
			return "", err
		}
	}
	var response map[string]any
	if json.Unmarshal([]byte(output), &response) != nil {
		return "", errors.New("invalid file tool result")
	}
	response["path"], response["execution_mode"], response["lease_id"] = rel, "managed", b.LeaseID
	response["summary"] = strings.ReplaceAll(asString(response["summary"]), local, rel)
	encodedResult, err := json.Marshal(response)
	return string(encodedResult), err
}

func (r *Runtime) executeManagedGit(ctx context.Context, scope WorkspaceScope, b ManagedExecutionBinding, name string, args map[string]any) (string, error) {
	for _, key := range []string{"workspace_path", "committed_source", "lease_id", "deployment_id", "working_dir"} {
		if _, ok := args[key]; ok {
			return "", errors.New("managed Git overrides are forbidden")
		}
	}
	argv := []string{"git", "--no-replace-objects", "--literal-pathspecs", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}
	switch name {
	case "git_status":
		argv = append(argv, "status")
		if asBool(args["short"]) {
			argv = append(argv, "--short")
		}
		if asBool(args["branch"]) {
			argv = append(argv, "--branch")
		}
	case "git_diff":
		argv = append(argv, "diff")
		if asBool(args["staged"]) {
			argv = append(argv, "--staged")
		}
	case "git_add":
		argv = append(argv, "add")
		if asBool(args["all"]) {
			argv = append(argv, "--all")
		}
	case "git_commit":
		message := strings.TrimSpace(asString(args["message"]))
		if message == "" {
			return "", errors.New("git_commit requires message")
		}
		argv = append(argv, "commit", "-m", message)
		if asBool(args["all"]) {
			return "", errors.New("managed git_commit requires explicit staging; use git_add")
		}
	default:
		return "", errors.New("managed Git tool unsupported")
	}
	if name == "git_add" || name == "git_diff" {
		paths := asStringSlice(args["pathspec"])
		if len(paths) == 0 {
			if p := asString(args["pathspec"]); p != "" {
				paths = []string{p}
			}
		}
		if name == "git_add" && len(paths) == 0 {
			if !asBool(args["all"]) {
				return "", errors.New("git_add requires pathspec or all=true")
			}
			paths = scope.MutationScopes
			if len(paths) == 0 {
				paths = []string{"."}
			}
		}
		if len(paths) > 0 {
			argv = append(argv, "--")
		}
		for _, p := range paths {
			if p == "." && (len(scope.MutationScopes) == 0 || (len(scope.MutationScopes) == 1 && scope.MutationScopes[0] == ".")) {
				argv = append(argv, p)
				continue
			}
			clean, err := managedRelativePath(scope, p, name == "git_add")
			if err != nil {
				return "", err
			}
			argv = append(argv, clean)
		}
	}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = managedQuote(a)
	}
	result, err := r.managedCommand(ctx, scope, b, strings.Join(quoted, " "), true)
	if err != nil {
		return "", err
	}
	response := map[string]any{"execution_mode": "managed", "lease_id": b.LeaseID, "operation_id": result.OperationID, "argv": argv, "output": result.CombinedOutput(), "exit_code": result.ExitCode, "prompt_injection_tag": "tool_output_untrusted"}
	encoded, err := json.Marshal(response)
	return string(encoded), err
}

// ReleaseManagedCheckout releases only the child's receipt, including failed or
// cancelled runs. Deployment retention/destruction remains the owner's policy.
func (r *Runtime) ReleaseManagedCheckout(ctx context.Context, childID string) error {
	scope, b, err := r.managedChildScope(childID)
	if err != nil {
		return err
	}
	service, ok := r.deploymentManager.(interface {
		ReleaseDeployment(context.Context, lifecycle.ReleaseDeploymentRequest) (*lifecycle.ReleaseDeploymentResult, error)
	})
	if !ok {
		return errors.New("managed receipt cleanup unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err = service.ReleaseDeployment(ctx, lifecycle.ReleaseDeploymentRequest{AccountScopeID: scope.Principal.AccountScopeID, WorkspaceID: b.WorkspaceID, LeaseID: b.LeaseID, Attribution: environments.OperationAttribution{Actor: scope.Principal.UserID, SessionID: childID}, Reason: "delegated_coder_finished"})
	if err != nil {
		return fmt.Errorf("managed child receipt cleanup failed: %w", err)
	}
	return nil
}
