package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// A held directory FD provides a short alias to a test-owned fixture even when
// TMPDIR is arbitrarily long. The final runtime directory is a real private
// directory, so ConfigureRuntimeDir still exercises all production checks and
// the <=50-byte limit. No live /run/user directory or global env is touched.
// Only injected runners use this alias; no engine inherits the descriptor.
func configureBuildRuntimeFixture(t *testing.T, p *LocalDockerProvider) {
	t.Helper()
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "r"), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	p.ConfigureRuntimeDir(fmt.Sprintf("/proc/self/fd/%d/r", file.Fd()))
}

func allocateBuildRuntimeFixture(t *testing.T, p *LocalDockerProvider, id string) string {
	t.Helper()
	runroot, err := p.buildRunroot(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runroot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := p.publishBuildOwnership(id, runroot); err != nil {
		t.Fatal(err)
	}
	return runroot
}

func assertBuildSentinel(t *testing.T, dir string) {
	t.Helper()
	if data, err := os.ReadFile(filepath.Join(dir, "sentinel")); err != nil || string(data) != "preserve" {
		t.Fatalf("foreign resource changed: %q %v", data, err)
	}
}

// Purpose: cleanupBuildFiles/verifyBuildOwnership must reject missing, malformed,
// redirected, cross-operation/account and replaced-directory evidence BEFORE
// issuing engine/unit commands or deleting resources. Real private filesystem
// fixtures and an injected runner prove rejection plus preservation, without a
// user session, privileges, Podman or ambient runtime state.
func TestManagedBuildOwnershipFailClosed(t *testing.T) {
	for _, mode := range []string{"missing-root", "missing-root-receipt", "missing-run-receipt", "receipt-directory", "symlink", "oversized", "operation", "account", "path", "inode", "root-symlink", "replaced-root", "runroot-symlink", "replaced-runroot", "allocation-only", "interrupted-publication"} {
		t.Run(mode, func(t *testing.T) {
			r := &imageBuildRunner{}
			p := NewLocalPodmanProvider(r)
			p.ConfigureBuildRoot(t.TempDir())
			configureBuildRuntimeFixture(t, p)
			id := "op_ownership"
			root, _ := p.buildDirectory(id)
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			runroot := allocateBuildRuntimeFixture(t, p, id)
			if err := os.WriteFile(filepath.Join(runroot, "sentinel"), []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			receipt := filepath.Join(root, buildOwnershipFile)
			switch mode {
			case "missing-root":
				if err := os.RemoveAll(root); err != nil {
					t.Fatal(err)
				}
			case "missing-root-receipt", "allocation-only", "interrupted-publication":
				if err := os.Remove(receipt); err != nil {
					t.Fatal(err)
				}
				if mode == "allocation-only" {
					if err := os.Remove(filepath.Join(runroot, buildOwnershipFile)); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "interrupted-publication" || mode == "allocation-only" {
					if err := os.WriteFile(filepath.Join(root, "allocation-intent"), []byte(id), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "missing-run-receipt":
				if err := os.Remove(filepath.Join(runroot, buildOwnershipFile)); err != nil {
					t.Fatal(err)
				}
			case "receipt-directory", "symlink":
				if err := os.Remove(receipt); err != nil {
					t.Fatal(err)
				}
				var err error
				if mode == "symlink" {
					err = os.Symlink(filepath.Join(runroot, buildOwnershipFile), receipt)
				} else {
					err = os.Mkdir(receipt, 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "root-symlink", "replaced-root":
				moved := root + "-old"
				if err := os.Rename(root, moved); err != nil {
					t.Fatal(err)
				}
				if mode == "root-symlink" {
					if err := os.Symlink(moved, root); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(root, 0700); err != nil {
						t.Fatal(err)
					}
					data, err := os.ReadFile(filepath.Join(moved, buildOwnershipFile))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(receipt, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "runroot-symlink", "replaced-runroot":
				moved := runroot + "-old"
				if err := os.Rename(runroot, moved); err != nil {
					t.Fatal(err)
				}
				if mode == "runroot-symlink" {
					if err := os.Symlink(moved, runroot); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(runroot, 0700); err != nil {
						t.Fatal(err)
					}
					for _, name := range []string{buildOwnershipFile, "sentinel"} {
						data, err := os.ReadFile(filepath.Join(moved, name))
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(runroot, name), data, 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			case "oversized":
				if err := os.WriteFile(receipt, []byte(strings.Repeat("x", 4097)), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				owner, err := readBuildOwnership(receipt)
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "operation":
					owner.Operation = "op_foreign"
				case "account":
					owner.BuildRoot = filepath.Join(p.buildRoot, "foreign-account")
				case "path":
					owner.Runroot = filepath.Join(p.runtimeDir, "foreign")
				case "inode":
					owner.RunInode++
				}
				data, err := json.Marshal(owner)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(receipt, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.cleanupBuildFiles(context.Background(), id); err == nil {
				t.Fatal("unowned cleanup accepted")
			}
			assertBuildSentinel(t, runroot)
			if len(r.calls) != 0 {
				t.Fatal("unowned cleanup issued commands")
			}
			if mode != "missing-root" {
				if _, err := os.Stat(root); err != nil {
					t.Fatal("scratch evidence lost")
				}
			}
		})
	}
}

// Purpose: BuildImage must surface cleanup errors as ErrOperationCleanupFailed,
// withhold an otherwise successful image, and retain scratch ownership for
// recovery. Injected unit failures prove error/postconditions; restoring inactive
// evidence on a fresh provider proves restart recovery and idempotent retry.
func TestManagedBuildCleanupRetainsRecoveryEvidence(t *testing.T) {
	r := &imageBuildRunner{t: t, definition: buildDefinitionFixture(), operation: "op_recovery", id: "sha256:" + strings.Repeat("c", 64), failCleanup: true}
	p := NewLocalPodmanProvider(r)
	p.ConfigureBuildRoot(t.TempDir())
	configureBuildRuntimeFixture(t, p)
	result, err := p.BuildImage(context.Background(), ImageBuildRequest{OperationID: r.operation, Connection: podmanConnectionForTest(environments.ConnectionKindLocalPodman), Definition: r.definition})
	if result != nil || !errors.Is(err, ErrOperationCleanupFailed) {
		t.Fatalf("cleanup failure hidden: %+v %v", result, err)
	}
	if _, err := p.verifyBuildOwnership(r.operation); err != nil {
		t.Fatalf("recovery evidence lost: %v", err)
	}
	r.failCleanup = false
	fresh := NewLocalPodmanProvider(r)
	fresh.ConfigureBuildRoot(p.buildRoot)
	fresh.ConfigureRuntimeDir(p.runtimeDir)
	for i := 0; i < 2; i++ {
		if err := fresh.CleanupBuild(context.Background(), r.operation); err != nil {
			t.Fatal(err)
		}
	}
}

// Purpose: two BuildImage invocations on one provider must actually overlap,
// allocate independent runtime/storage resources, and finish cleanup without
// mutating each other's sentinel. Channel barriers prove real concurrent
// allocation, not sequential path derivation; stateless per-call fixture runners
// avoid data races while the shared provider exercises its buildRuns registry.
func TestManagedBuildConcurrentIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan string, 2)
	releaseOne, releaseTwo := make(chan struct{}), make(chan struct{})
	p := NewLocalPodmanProvider(&parallelBuildRunner{t: t, entered: entered, releaseOne: releaseOne, releaseTwo: releaseTwo})
	p.ConfigureBuildRoot(t.TempDir())
	configureBuildRuntimeFixture(t, p)
	done := make(chan error, 2)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	for _, id := range []string{"op_parallel_one", "op_parallel_two"} {
		workers.Add(1)
		go func(id string) {
			defer workers.Done()
			_, err := p.BuildImage(ctx, ImageBuildRequest{OperationID: id, Connection: podmanConnectionForTest(environments.ConnectionKindLocalPodman), Definition: buildDefinitionFixture()})
			done <- err
		}(id)
	}
	paths := []string{}
	for i := 0; i < 2; i++ {
		select {
		case path := <-entered:
			paths = append(paths, path)
		case <-ctx.Done():
			t.Fatal("builds did not overlap")
		}
	}
	if paths[0] == paths[1] {
		t.Fatal("concurrent runroots alias")
	}
	for _, path := range paths {
		assertBuildSentinel(t, path)
	}
	close(releaseOne)
	select {
	case err := <-done:
		if err == nil || errors.Is(err, ErrOperationCleanupFailed) {
			t.Fatalf("unexpected first build failure: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("first cleanup did not finish")
	}
	remaining, err := p.buildRunroot("op_parallel_two")
	if err != nil {
		t.Fatal(err)
	}
	assertBuildSentinel(t, remaining)
	close(releaseTwo)
	select {
	case err := <-done:
		if err == nil || errors.Is(err, ErrOperationCleanupFailed) {
			t.Fatalf("unexpected build failure: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("concurrent cleanup did not finish")
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("runroot leaked")
		}
	}
}

type parallelBuildRunner struct {
	t                      *testing.T
	entered                chan<- string
	releaseOne, releaseTwo <-chan struct{}
}

func (r *parallelBuildRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return (&imageBuildRunner{definition: buildDefinitionFixture()}).Run(ctx, name, args...)
}
func (r *parallelBuildRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}
func (r *parallelBuildRunner) RunWithIO(ctx context.Context, in io.Reader, out, stderr io.Writer, name string, args ...string) error {
	if name != "systemd-run" {
		return (&imageBuildRunner{t: r.t, definition: buildDefinitionFixture()}).RunWithIO(ctx, in, out, stderr, name, args...)
	}
	var path string
	for i, arg := range args {
		if arg == "--runroot" {
			path = args[i+1]
		}
	}
	if err := os.WriteFile(filepath.Join(path, "sentinel"), []byte("preserve"), 0600); err != nil {
		return err
	}
	select {
	case r.entered <- path:
	case <-ctx.Done():
		return ctx.Err()
	}
	release := r.releaseTwo
	if strings.Contains(strings.Join(args, " "), "op_parallel_one") {
		release = r.releaseOne
	}
	select {
	case <-release:
		return buildTestExit(125)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Purpose: bounded scratch removal must report failure and keep the ownership
// receipt even after runroot removal succeeds. A deterministic over-bound
// inventory triggers the real cleanup failure path (also as root in containers),
// proving no accepted image and restart recovery rather than permission folklore.
func TestManagedBuildScratchRemovalFailure(t *testing.T) {
	for _, resource := range []string{"scratch", "runroot"} {
		t.Run(resource, func(t *testing.T) {
			r := &imageBuildRunner{t: t, definition: buildDefinitionFixture(), operation: "op_remove_failure", id: "sha256:" + strings.Repeat("c", 64)}
			p := NewLocalPodmanProvider(r)
			p.ConfigureBuildRoot(t.TempDir())
			configureBuildRuntimeFixture(t, p)
			root, _ := p.buildDirectory(r.operation)
			failureRoot := root
			if resource == "runroot" {
				var err error
				failureRoot, err = p.buildRunroot(r.operation)
				if err != nil {
					t.Fatal(err)
				}
			}
			r.onBuild = func(_ []string) error {
				for i := 0; i < 33; i++ {
					if err := os.WriteFile(filepath.Join(failureRoot, fmt.Sprintf("extra-%d", i)), []byte("private"), 0600); err != nil {
						return err
					}
				}
				return nil
			}
			result, err := p.BuildImage(context.Background(), ImageBuildRequest{OperationID: r.operation, Connection: podmanConnectionForTest(environments.ConnectionKindLocalPodman), Definition: r.definition})
			if result != nil || !errors.Is(err, ErrOperationCleanupFailed) {
				t.Fatalf("scratch removal uncertainty hidden: %+v %v", result, err)
			}
			if _, err := p.verifyBuildOwnership(r.operation); err != nil {
				t.Fatalf("retry receipt lost: %v", err)
			}
			for i := 0; i < 33; i++ {
				if err := os.Remove(filepath.Join(failureRoot, fmt.Sprintf("extra-%d", i))); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.CleanupBuild(context.Background(), r.operation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Purpose: interrupted publishBuildOwnership must not turn an allocation intent
// or single receipt into cleanup authority. Exclusive receipt failure exercises
// the real publication function and ensures runtime/scratch evidence survives.
func TestManagedBuildOwnershipPublicationFailure(t *testing.T) {
	p := NewLocalPodmanProvider(&imageBuildRunner{})
	p.ConfigureBuildRoot(t.TempDir())
	configureBuildRuntimeFixture(t, p)
	id := "op_publish_failure"
	root, _ := p.buildDirectory(id)
	runroot, err := p.buildRunroot(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, runroot, filepath.Join(root, buildOwnershipFile)} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "allocation-intent"), []byte(id), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runroot, "sentinel"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.publishBuildOwnership(id, runroot); err == nil {
		t.Fatal("failed publication accepted")
	}
	if err := p.cleanupBuildFiles(context.Background(), id); err == nil {
		t.Fatal("partial publication authorized deletion")
	}
	assertBuildSentinel(t, runroot)
	if _, err := readBuildOwnership(filepath.Join(runroot, buildOwnershipFile)); err != nil {
		t.Fatal("partial recovery receipt lost")
	}
	if _, err := os.Stat(filepath.Join(root, "allocation-intent")); err != nil {
		t.Fatal("allocation intent lost")
	}
}

// Purpose: ConfigureRuntimeDir must affect only the managed build resolver,
// leaving runner environment discovery fixtures unchanged; unsafe modes and
// redirected final directory components fail closed before allocation. A held
// private FD alias also proves long fixture paths do not disable the length guard.
func TestManagedBuildConfiguredRuntime(t *testing.T) {
	p := NewLocalPodmanProvider(&OSCommandRunner{})
	runner := p.runner.(*OSCommandRunner)
	runner.commandEnv = func() ([]string, error) {
		return []string{"XDG_RUNTIME_DIR=/unavailable", "DBUS_SESSION_BUS_ADDRESS=unix:abstract=fixture"}, nil
	}
	configureBuildRuntimeFixture(t, p)
	dir, bus, err := p.resolveSessionEnvironment()
	if err != nil || dir != p.runtimeDir || bus != "unix:abstract=fixture" {
		t.Fatalf("configured runtime not used: %q %q %v", dir, bus, err)
	}
	env, err := runner.commandEnv()
	if err != nil || env[0] != "XDG_RUNTIME_DIR=/unavailable" {
		t.Fatal("runtime override mutated runner discovery")
	}
	if err := os.Chmod(p.runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := p.resolveRuntimeDir(); err == nil {
		t.Fatal("public runtime accepted")
	}
	if err := os.Chmod(p.runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(p.runtimeDir), "link")
	if err := os.Symlink(p.runtimeDir, link); err != nil {
		t.Fatal(err)
	}
	p.ConfigureRuntimeDir(link)
	if _, err := p.resolveRuntimeDir(); err == nil {
		t.Fatal("symlink runtime accepted")
	}
}

// Purpose: verifyActiveBuildOwnership must refuse a missing runtime allocation
// even with a valid scratch receipt, while cleanup can idempotently finish after
// successful runtime removal. The helper/cleanup layer proves engine admission
// cannot recreate a runroot from incomplete evidence.
func TestManagedBuildMissingActiveRunroot(t *testing.T) {
	r := &imageBuildRunner{}
	p := NewLocalPodmanProvider(r)
	p.ConfigureBuildRoot(t.TempDir())
	configureBuildRuntimeFixture(t, p)
	id := "op_missing_active"
	root, _ := p.buildDirectory(id)
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	allocateBuildRuntimeFixture(t, p, id)
	if err := p.cleanupRunroot(id); err != nil {
		t.Fatal(err)
	}
	if _, err := p.verifyActiveBuildOwnership(id); err == nil {
		t.Fatal("missing runtime authorized engine")
	}
	if _, err := p.verifyBuildOwnership(id); err != nil {
		t.Fatal("idempotent cleanup evidence lost")
	}
	if err := p.cleanupBuildFiles(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}
