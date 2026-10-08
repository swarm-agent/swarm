//go:build linux

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type metadataCleanupRunner struct {
	imageBuildRunner
	afterChild bool
	onStopped  func()
}

func (r *metadataCleanupRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.afterChild && name == "systemctl" && strings.Contains(strings.Join(args, " "), "show swarm-build-cleanup-") && r.onStopped != nil {
		r.onStopped()
	}
	return r.imageBuildRunner.Run(ctx, name, args...)
}

func (r *metadataCleanupRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}

func writeShutdownMetadata(t *testing.T, storage string) {
	t.Helper()
	for _, dir := range []string{"vfs/dir", "vfs-layers"} {
		if err := os.MkdirAll(filepath.Join(storage, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"storage.lock": "lock generation", "vfs-layers/layers.lock": "lock generation", "vfs-layers/layers.json": "[]\n"} {
		if err := os.WriteFile(filepath.Join(storage, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// Purpose: cleanupBuildFiles/removeStoppedBuildMetadata must handle the real
// command ordering missed by the old fake: the child removes read-only layers,
// then Podman Shutdown reopens graph/layer metadata before unit stop is proved.
// Execute the fixed script on test-owned files and inject only the engine's
// shutdown epilogue; assert filesystem identity, cleanup, and retained evidence
// at cancellation/stop/substitution boundaries. No real engine, UID mapping,
// service or live host resource is involved in this narrow provider test.
func TestManagedBuildShutdownMetadataCleanup(t *testing.T) {
	for _, mode := range []string{"success", "active-unit", "cancel-after-child", "receipt-change", "root-change", "runroot-change", "storage-change", "storage-symlink", "layer-leftover"} {
		t.Run(mode, func(t *testing.T) {
			r := &metadataCleanupRunner{}
			p := NewLocalPodmanProvider(r)
			p.ConfigureBuildRoot(t.TempDir())
			configureBuildRuntimeFixture(t, p)
			id := "op_shutdown_metadata"
			root, _ := p.buildDirectory(id)
			storage := filepath.Join(root, "storage")
			layer := filepath.Join(storage, "vfs", "dir", "layer", "rootfs")
			if err := os.MkdirAll(layer, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(layer, "sentinel"), []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(layer, 0555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(layer, 0700) })
			runroot := allocateBuildRuntimeFixture(t, p, id)
			original, err := os.Lstat(storage)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			r.onCleanup = func(commandCtx context.Context, phase string, _ []string) error {
				if phase != "cleanup-storage" {
					return nil
				}
				if mode != "layer-leftover" {
					cmd := exec.CommandContext(commandCtx, "sh", "-c", buildStorageRemovalScript, "swarm-build-cleanup", storage)
					cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
					if err := cmd.Run(); err != nil {
						return err
					}
				}
				current, err := os.Lstat(storage)
				if err != nil || !os.SameFile(original, current) {
					t.Fatal("child removed graph-root inode")
				}
				writeShutdownMetadata(t, storage)
				r.afterChild = true
				return nil
			}
			r.onStopped = func() {
				// Metadata must still be present until this explicit stop proof.
				if _, err := os.Lstat(filepath.Join(storage, "storage.lock")); err != nil {
					t.Fatal("metadata removed before stop proof")
				}
				switch mode {
				case "active-unit":
					r.cleanupUnitState = "LoadState=loaded\nActiveState=active\n"
				case "cancel-after-child":
					cancel()
				case "receipt-change":
					owner, err := readBuildOwnership(filepath.Join(root, buildOwnershipFile))
					if err != nil {
						t.Fatal(err)
					}
					owner.Operation = "foreign"
					data, err := json.Marshal(owner)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, buildOwnershipFile), data, 0600); err != nil {
						t.Fatal(err)
					}
				case "runroot-change":
					if err := os.Rename(runroot, runroot+"-old"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(runroot, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(runroot, "sentinel"), []byte("preserve"), 0600); err != nil {
						t.Fatal(err)
					}
				case "root-change":
					if err := os.Rename(root, root+"-old"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(root, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, "sentinel"), []byte("preserve"), 0600); err != nil {
						t.Fatal(err)
					}
				case "storage-change", "storage-symlink":
					if err := os.Rename(storage, storage+"-old"); err != nil {
						t.Fatal(err)
					}
					if mode == "storage-symlink" {
						if err := os.Symlink(outside, storage); err != nil {
							t.Fatal(err)
						}
					} else {
						writeShutdownMetadata(t, storage)
					}
				}
			}
			err = p.cleanupBuildFiles(ctx, id)
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{root, runroot} {
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("successful cleanup retained resource")
					}
				}
				// A fresh provider can repeat cleanup without recreating an engine.
				fresh := NewLocalPodmanProvider(r)
				fresh.ConfigureBuildRoot(p.buildRoot)
				fresh.ConfigureRuntimeDir(p.runtimeDir)
				calls := len(r.calls)
				if err := fresh.cleanupBuildFiles(context.Background(), id); err != nil {
					t.Fatal(err)
				}
				if len(r.calls) != calls {
					t.Fatal("idempotent absent cleanup ran engine")
				}
			} else {
				if err == nil {
					t.Fatal("unsafe cleanup accepted")
				}
				if mode == "cancel-after-child" && !errors.Is(err, context.Canceled) {
					t.Fatal("lost cancellation identity")
				}
				evidenceRoot := root
				if mode == "root-change" {
					evidenceRoot += "-old"
					assertBuildSentinel(t, root)
				}
				evidenceRunroot := runroot
				if mode == "runroot-change" {
					evidenceRunroot += "-old"
					assertBuildSentinel(t, runroot)
				}
				for _, dir := range []string{evidenceRoot, evidenceRunroot} {
					if _, err := readBuildOwnership(filepath.Join(dir, buildOwnershipFile)); err != nil {
						t.Fatal("lost recovery evidence")
					}
				}
				if mode == "layer-leftover" {
					assertBuildSentinel(t, layer)
				}
				if mode == "storage-change" {
					if _, err := os.Lstat(filepath.Join(storage, "storage.lock")); err != nil {
						t.Fatal("foreign storage metadata deleted")
					}
				}
				if mode == "active-unit" || mode == "cancel-after-child" {
					if _, err := os.Lstat(filepath.Join(storage, "storage.lock")); err != nil {
						t.Fatal("unconfirmed cleanup erased metadata")
					}
					// Fresh retry uses unchanged receipts and reruns namespace cleanup.
					r.afterChild = false
					r.onStopped = nil
					r.cleanupUnitState = ""
					fresh := NewLocalPodmanProvider(r)
					fresh.ConfigureBuildRoot(p.buildRoot)
					fresh.ConfigureRuntimeDir(p.runtimeDir)
					if err := fresh.cleanupBuildFiles(context.Background(), id); err != nil {
						t.Fatalf("retry: %v", err)
					}
				}
			}
			assertBuildSentinel(t, outside)
		})
	}
}

// Purpose: the host epilogue must validate the complete bounded metadata tree
// before deleting anything. Unknown/real layers, populated indexes, links,
// special files, excessive output, writable or mapped-owner-like inaccessible
// resources must not be treated as harmless empty metadata. Direct helper tests
// assert zero metadata deletion and no diagnostic disclosure, at the narrowest
// filesystem boundary; real subordinate UID/mount behavior needs parent testing.
func TestManagedBuildStoppedMetadataRejectsUnsafeTree(t *testing.T) {
	for _, mode := range []string{"layer", "index", "unknown-secret", "symlink", "hardlink", "fifo", "oversize", "too-many", "public-mode", "readonly-directory", "replace-file"} {
		t.Run(mode, func(t *testing.T) {
			r := &imageBuildRunner{}
			p := NewLocalPodmanProvider(r)
			p.ConfigureBuildRoot(t.TempDir())
			configureBuildRuntimeFixture(t, p)
			id := "op_unsafe_metadata"
			root, _ := p.buildDirectory(id)
			storage := filepath.Join(root, "storage")
			writeShutdownMetadata(t, storage)
			allocateBuildRuntimeFixture(t, p, id)
			owner, err := p.verifyActiveBuildOwnership(id)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.Lstat(storage)
			if err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(storage, "storage.lock")
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "layer":
				err = os.Mkdir(filepath.Join(storage, "vfs", "dir", "layer"), 0700)
			case "index":
				err = os.WriteFile(filepath.Join(storage, "vfs-layers", "layers.json"), []byte(`[{"id":"PRIVATE_LAYER_SECRET"}]`), 0600)
			case "unknown-secret":
				err = os.WriteFile(filepath.Join(storage, "PRIVATE_RECIPE_SECRET"), nil, 0600)
			case "symlink", "hardlink", "fifo":
				if err := os.Remove(lock); err != nil {
					t.Fatal(err)
				}
				if mode == "symlink" {
					err = os.Symlink(filepath.Join(outside, "sentinel"), lock)
				}
				if mode == "hardlink" {
					err = os.Link(filepath.Join(outside, "sentinel"), lock)
				}
				if mode == "fifo" {
					err = syscall.Mkfifo(lock, 0600)
				}
			case "oversize":
				err = os.WriteFile(lock, []byte(strings.Repeat("PRIVATE", 1000)), 0600)
			case "too-many":
				for i := 0; i < 33; i++ {
					if err := os.WriteFile(filepath.Join(storage, strings.Repeat("x", i+1)), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "public-mode":
				err = os.Chmod(lock, 0666)
			case "readonly-directory":
				err = os.Chmod(filepath.Join(storage, "vfs-layers"), 0000)
				t.Cleanup(func() { _ = os.Chmod(filepath.Join(storage, "vfs-layers"), 0700) })
			case "replace-file":
				// Test the FD-pinned inspection/removal boundary without a race.
				store, err := os.Open(storage)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				budget := 32
				n, err := inspectBuildMetadata(context.Background(), int(store.Fd()), "storage.lock", "storage.lock", &budget)
				if err != nil {
					t.Fatal(err)
				}
				defer n.close()
				if err := os.Rename(lock, lock+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "sentinel"), lock); err != nil {
					t.Fatal(err)
				}
				if err := n.remove(context.Background(), func() error { return nil }); err == nil {
					t.Fatal("substituted metadata removed")
				}
				if info, err := os.Lstat(lock); err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("foreign symlink removed")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			err = p.removeStoppedBuildMetadata(context.Background(), id, root, owner, original)
			if err == nil || strings.Contains(err.Error(), "PRIVATE") || len(err.Error()) > 256 {
				t.Fatalf("unsafe tree accepted or leaked: %v", err)
			}
			if _, err := os.Lstat(lock); err != nil {
				t.Fatal("failed preflight removed metadata")
			}
			if _, err := p.verifyActiveBuildOwnership(id); err != nil {
				t.Fatal("failed preflight lost ownership")
			}
			assertBuildSentinel(t, outside)
		})
	}
}

// Purpose: the stopped epilogue's actual owner predicate rejects subordinate or
// foreign UID/GID metadata without a privileged chown fixture. This predicate
// layer is the narrowest hermetic proof of mapped-owner admission; integration
// fixtures above prove that unsafe admission retains receipts and files.
func TestManagedBuildMetadataMappedOwnerRejected(t *testing.T) {
	stat := unix.Stat_t{Uid: uint32(os.Geteuid()), Gid: uint32(os.Getegid()), Mode: unix.S_IFREG | 0600}
	if !safeBuildMetadataOwner(stat) {
		t.Fatal("current owner rejected")
	}
	foreign := stat
	foreign.Uid++
	if safeBuildMetadataOwner(foreign) {
		t.Fatal("foreign/mapped UID accepted")
	}
	foreign = stat
	foreign.Gid++
	if safeBuildMetadataOwner(foreign) {
		t.Fatal("foreign/mapped GID accepted")
	}
}

// Purpose: openBuildMetadata's production openat2 flags must reject real kernel
// mount boundaries, even same-device bind mounts. The hermetic mount target is
// the already-existing proc mount (read only; no mount or privilege operations).
// Absence of openat2 is a failure requiring parent environment review, not a
// permission-weakening fallback. Ordinary child access remains usable.
func TestManagedBuildMetadataMountBoundary(t *testing.T) {
	parent, err := os.Open("/")
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	file, err := openBuildMetadata(int(parent.Fd()), "proc")
	if file != nil {
		file.Close()
	}
	if !errors.Is(err, unix.EXDEV) {
		t.Fatalf("mount boundary accepted or unsupported kernel: %v", err)
	}
	private := t.TempDir()
	if err := os.WriteFile(filepath.Join(private, "storage.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(private)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	file, err = openBuildMetadata(int(dir.Fd()), "storage.lock")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
}
