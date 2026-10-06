package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// Purpose: BuildImage owns imported-image admission. Podman 4.x inspect returns
// bare full SHA-256 IDs and top-level Labels; prefix spelling must not reject the
// same image or admit shortened/malformed IDs, other digests or altered labels.
// Injected realistic inspect JSON proves admission and cleanup postconditions at
// the narrow provider boundary, not compatibility with an installed engine.
func TestManagedBuildImportedImageIdentity(t *testing.T) {
	digest := strings.Repeat("c", 64)
	for _, mode := range []string{"bare", "prefixed", "bare-receipt", "digest", "short", "algorithm", "nonhex", "whitespace", "operation", "inputs", "revision", "empty", "multiple", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			r := &imageBuildRunner{t: t, definition: buildDefinitionFixture(), operation: "op_import_identity", id: "sha256:" + digest}
			image := map[string]any{
				"Id": digest, "Digest": "sha256:" + strings.Repeat("d", 64),
				"RepoTags": []string{}, "Architecture": "amd64", "Os": "linux",
				"Labels": map[string]string{"io.swarm.build.operation": r.operation, "io.swarm.build.inputs": r.definition.Digest(), "org.opencontainers.image.revision": r.definition.Product.Commit},
			}
			switch mode {
			case "prefixed":
				image["Id"] = r.id
			case "bare-receipt":
				r.id = digest
			case "digest":
				image["Id"] = strings.Repeat("e", 64)
			case "short":
				image["Id"] = digest[:12]
			case "algorithm":
				image["Id"] = "sha512:" + digest
			case "nonhex":
				image["Id"] = strings.Repeat("z", 64)
			case "whitespace":
				image["Id"] = digest + "\n"
			case "operation", "inputs", "revision":
				key := map[string]string{"operation": "io.swarm.build.operation", "inputs": "io.swarm.build.inputs", "revision": "org.opencontainers.image.revision"}[mode]
				image["Labels"].(map[string]string)[key] = "foreign"
			}
			images := []any{image}
			if mode == "empty" {
				images = nil
			} else if mode == "multiple" {
				images = append(images, image)
			}
			var err error
			r.inspectJSON, err = json.Marshal(images)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "malformed" {
				r.inspectJSON = []byte("PRIVATE_INVALID_JSON")
			}
			p := NewLocalPodmanProvider(r)
			p.ConfigureBuildRoot(t.TempDir())
			configureBuildRuntimeFixture(t, p)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn := podmanConnectionForTest(environments.ConnectionKindLocalPodman)
			result, err := p.BuildImage(ctx, ImageBuildRequest{OperationID: r.operation, Connection: conn, Definition: r.definition})
			success := mode == "bare" || mode == "prefixed" || mode == "bare-receipt"
			if success {
				if err != nil || result == nil || result.ImageID != "sha256:"+digest || result.OperationID != r.operation || result.ConnectionID != conn.ID || result.DefinitionDigest != r.definition.Digest() || result.Product != r.definition.Product || result.Recipe != r.definition.Recipe {
					t.Fatalf("valid image rejected or provenance changed: %+v %v", result, err)
				}
			} else if err == nil || result != nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("invalid provenance admitted/leaked: %+v %v", result, err)
			}
			root, _ := p.buildDirectory(r.operation)
			if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("owned scratch was not cleaned")
			}
			inventory := false
			for _, call := range r.calls {
				inventory = inventory || strings.Contains(strings.Join(call.Args, " "), "images --filter label=io.swarm.build.operation="+r.operation)
			}
			if inventory == success {
				t.Fatal("failed provenance must attempt independent owned-image cleanup; success must retain image")
			}
		})
	}
}

// Purpose: cleanupBuildFiles must use rootless namespace removal for read-only
// VFS/mapped-owner layers, with exactly the build's sanitized environment/store,
// after termination/unmount and ownership revalidation. Failures, cancellation,
// lying success and partial deletion must retain both receipts for crash retry.
// A fixture runner verifies actual argv and filesystem postconditions; it models
// namespace deletion (no chown/privileges) and cannot prove kernel UID mappings.
func TestManagedBuildNamespaceStorageCleanup(t *testing.T) {
	for _, mode := range []string{"success", "unmount-failure", "remove-failure", "cancel", "unconfirmed", "replaced-storage"} {
		t.Run(mode, func(t *testing.T) {
			r := &imageBuildRunner{}
			p := NewLocalPodmanProvider(r)
			p.ConfigureBuildRoot(t.TempDir())
			configureBuildRuntimeFixture(t, p)
			id := "op_namespace_cleanup"
			root, _ := p.buildDirectory(id)
			storage := filepath.Join(root, "storage")
			layer := filepath.Join(storage, "vfs", "dir", "layer", "rootfs")
			if err := os.MkdirAll(layer, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(layer, "sentinel"), []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(layer, 0555); err != nil {
				t.Fatal(err)
			}
			// Restore fixture permissions only for test-owned TempDir teardown.
			t.Cleanup(func() {
				_ = os.Chmod(layer, 0700)
				_ = os.Chmod(filepath.Join(storage+"-old", "vfs", "dir", "layer", "rootfs"), 0700)
			})
			runroot := allocateBuildRuntimeFixture(t, p, id)
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(storage, "foreign-link")); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, bus, err := p.resolveSessionEnvironment()
			if err != nil {
				t.Fatal(err)
			}
			prefix := []string{
				"-i", "PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(root, "home"),
				"XDG_RUNTIME_DIR=" + p.runtimeDir, "DBUS_SESSION_BUS_ADDRESS=" + bus,
				"TMPDIR=" + filepath.Join(root, "tmp"), "CONTAINERS_CONF=" + filepath.Join(root, "containers.conf"),
				"CONTAINERS_REGISTRIES_CONF=" + filepath.Join(root, "registries.conf"), "CONTAINERS_MOUNTS_CONF=" + filepath.Join(root, "mounts.conf"),
				"podman", "--remote=false", "--root", storage, "--runroot", runroot,
				"--storage-driver=vfs", "--cgroup-manager=cgroupfs", "--runtime=crun",
			}
			phases := []string{}
			r.onCleanup = func(commandCtx context.Context, phase string, args []string) error {
				phases = append(phases, phase)
				if _, ok := commandCtx.Deadline(); !ok {
					t.Fatal("cleanup command lacks deadline")
				}
				want := append([]string{}, prefix...)
				if phase == "cleanup-unmount" {
					want = append(want, "unmount", "--all", "--force")
				} else {
					want = append(want, "unshare", "sh", "-c", buildStorageRemovalScript, "swarm-build-cleanup", storage)
					launcher := []string{"--user", "--wait", "--pipe", "--collect", "--unit=swarm-build-cleanup-" + id + ".service", "--property=KillMode=control-group", "--property=Delegate=yes", "--property=RuntimeMaxSec=15", "--property=TimeoutStopSec=5", "--property=TasksMax=64", "--", "env"}
					want = append(launcher, want...)
				}
				if !reflect.DeepEqual(args, want) {
					t.Fatalf("cleanup escaped exact private engine identity: %v", args)
				}
				if _, err := p.verifyActiveBuildOwnership(id); err != nil {
					t.Fatalf("command issued without active ownership: %v", err)
				}
				if phase == "cleanup-storage" {
					info, err := os.Lstat(layer)
					if err != nil || (mode != "success" && info.Mode().Perm() != 0555) {
						t.Fatal("host cleanup altered read-only layers before namespace command")
					}
				}
				if phase == "cleanup-unmount" {
					if mode == "unmount-failure" {
						return buildTestExit(125)
					}
					if mode == "replaced-storage" {
						if err := os.Rename(storage, storage+"-old"); err != nil {
							return err
						}
						return os.Symlink(outside, storage)
					}
					return nil
				}
				if mode == "cancel" {
					cancel()
					return commandCtx.Err()
				}
				if mode == "unconfirmed" {
					return nil
				}
				// Model the namespace's mapped-owner/DAC capabilities without
				// ambient subordinate IDs or a privileged fixture dependency.
				if err := os.Chmod(layer, 0700); err != nil {
					return err
				}
				if mode == "remove-failure" {
					if err := os.Remove(filepath.Join(layer, "sentinel")); err != nil {
						return err
					}
					return buildTestExit(125)
				}
				entries, err := os.ReadDir(storage)
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if err := os.RemoveAll(filepath.Join(storage, entry.Name())); err != nil {
						return err
					}
				}
				// Preserve graph-root identity and model the shutdown lock epilogue.
				return os.WriteFile(filepath.Join(storage, "storage.lock"), []byte("lock"), 0600)
			}
			err = p.cleanupBuildFiles(ctx, id)
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{root, runroot} {
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("confirmed cleanup retained resource")
					}
				}
			} else {
				if err == nil || strings.Contains(err.Error(), "PRIVATE") {
					t.Fatalf("cleanup failure hidden/leaked: %v", err)
				}
				if _, err := p.verifyActiveBuildOwnership(id); err != nil {
					t.Fatalf("failed cleanup lost recovery evidence: %v", err)
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal("lost cleanup cancellation identity")
				}
			}
			if len(phases) == 0 || phases[0] != "cleanup-unmount" || ((mode == "unmount-failure" || mode == "replaced-storage") && len(phases) != 1) {
				t.Fatalf("removal preceded unmount/ownership validation: %v", phases)
			}
			assertBuildSentinel(t, outside)
			if mode == "remove-failure" {
				// A fresh provider retries partial deletion using unchanged
				// evidence/configuration, then absent resources are idempotent.
				fresh := NewLocalPodmanProvider(r)
				fresh.ConfigureBuildRoot(p.buildRoot)
				fresh.ConfigureRuntimeDir(p.runtimeDir)
				mode = "success"
				for i := 0; i < 2; i++ {
					if err := fresh.cleanupBuildFiles(context.Background(), id); err != nil {
						t.Fatalf("restart cleanup retry: %v", err)
					}
				}
			}
		})
	}
}

// Purpose: runBuildCommand's cleanup phases must keep engine/namespace exit
// structure without retaining arbitrary recipe-layer/command secrets, and a
// cancelled cleanup must not issue commands or delete recovery evidence.
// These bounded provider/helper fixtures need no engine or host configuration.
func TestManagedBuildNamespaceCleanupDiagnosticsAndCancellation(t *testing.T) {
	for _, phase := range []string{"cleanup-unmount", "cleanup-storage"} {
		r := &imageBuildRunner{failurePhase: phase, commandErr: buildTestExit(125), commandOutput: "newuidmap PRIVATE_LAYER_SECRET\n" + strings.Repeat("PRIVATE", 10000)}
		p := NewLocalPodmanProvider(r)
		verb := "unmount"
		if phase == "cleanup-storage" {
			verb = "unshare"
		}
		err := p.runBuildCommand(context.Background(), phase, io.Discard, "env", "-i", "podman", verb, "--all")
		var failure *BuildCommandError
		if !errors.As(err, &failure) || failure.Code != 125 || failure.Phase != phase || strings.Contains(err.Error(), "PRIVATE") || len(err.Error()) > 512 || !strings.Contains(err.Error(), "UID mapping") {
			t.Fatalf("unsafe cleanup diagnostic: %v", err)
		}
	}
	r := &imageBuildRunner{}
	p := NewLocalPodmanProvider(r)
	p.ConfigureBuildRoot(t.TempDir())
	configureBuildRuntimeFixture(t, p)
	root, _ := p.buildDirectory("op_cancel_cleanup")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	allocateBuildRuntimeFixture(t, p, "op_cancel_cleanup")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.cleanupBuildFiles(ctx, "op_cancel_cleanup"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cleanup accepted: %v", err)
	}
	if len(r.calls) != 0 {
		t.Fatal("cancelled cleanup issued commands")
	}
	if _, err := p.verifyActiveBuildOwnership("op_cancel_cleanup"); err != nil {
		t.Fatal("cancelled cleanup lost evidence")
	}
}

// Purpose: cleanupBuildImages consumes Podman's --no-trunc inventory, which can
// also use bare full IDs. It must target the operation filter, normalize only a
// validated full digest, and reject malformed/short identities before image rm.
// The injected inventory boundary proves argv and zero invalid removals.
func TestManagedBuildCleanupImageIdentity(t *testing.T) {
	digest := strings.Repeat("c", 64)
	for _, image := range []string{digest, "sha256:" + digest, digest[:12], strings.Repeat("z", 64), "sha512:" + digest} {
		r := &imageBuildRunner{imagesOutput: image + "\n"}
		p := NewLocalPodmanProvider(r)
		p.ConfigureBuildRoot(t.TempDir())
		err := p.cleanupBuildImages(context.Background(), "op_image_cleanup")
		valid := image == digest || image == "sha256:"+digest
		if (err == nil) != valid {
			t.Fatalf("image %q cleanup: %v", image, err)
		}
		if len(r.calls) != 1 && !valid {
			t.Fatal("invalid inventory issued removal")
		}
		if !strings.Contains(strings.Join(r.calls[0].Args, " "), "images --filter label=io.swarm.build.operation=op_image_cleanup") {
			t.Fatal("inventory lost operation scope")
		}
		if valid && (len(r.calls) != 2 || !reflect.DeepEqual(r.calls[1].Args, []string{"--remote=false", "--cgroup-manager=systemd", "image", "rm", "sha256:" + digest})) {
			t.Fatal("removal lost exact normalized image identity")
		}
	}
}

// Purpose: the fixed namespace script is the smallest executable layer proving
// pre-order permission repair, non-following links, option-safe storage paths
// and preservation of the active graph-root inode for Podman Shutdown.
// This hermetic shell fixture uses only test-owned files, not Podman, live
// containers, root privileges or actual subordinate-ID ownership.
func TestManagedBuildStorageRemovalScript(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	storage := filepath.Join(base, "store with spaces")
	layer := filepath.Join(storage, "vfs", "layer", "rootfs")
	if err := os.MkdirAll(layer, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layer, "file"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(layer, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(layer, 0700) })
	if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(storage, "link")); err != nil {
		t.Fatal(err)
	}
	original, err := os.Lstat(storage)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", buildStorageRemovalScript, "swarm-build-cleanup", storage)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	if err := cmd.Run(); err != nil {
		t.Fatalf("fixed storage script failed: %v", err)
	}
	current, err := os.Lstat(storage)
	if err != nil || !os.SameFile(original, current) {
		t.Fatal("namespace child deleted/replaced active graph root")
	}
	entries, err := os.ReadDir(storage)
	if err != nil || len(entries) != 0 {
		t.Fatal("read-only layers retained")
	}
	assertBuildSentinel(t, outside)
}

// Purpose: cleanupBuildFiles must prove a crashed/active cleanup unit stopped
// before a new storage command, and prove its newly launched unit stopped before
// erasing receipts. Injected unit state and successful storage deletion exercise
// both failure boundaries without host services; neither may claim cleanup done.
func TestManagedBuildCleanupUnitTerminationEvidence(t *testing.T) {
	for _, afterRemoval := range []bool{false, true} {
		r := &imageBuildRunner{}
		p := NewLocalPodmanProvider(r)
		p.ConfigureBuildRoot(t.TempDir())
		configureBuildRuntimeFixture(t, p)
		id := "op_cleanup_unit"
		root, _ := p.buildDirectory(id)
		storage := filepath.Join(root, "storage")
		if err := os.MkdirAll(storage, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(storage, "sentinel"), []byte("preserve"), 0600); err != nil {
			t.Fatal(err)
		}
		allocateBuildRuntimeFixture(t, p, id)
		if afterRemoval {
			r.onCleanup = func(_ context.Context, phase string, _ []string) error {
				if phase == "cleanup-storage" {
					r.cleanupUnitState = "LoadState=loaded\nActiveState=active\n"
					return os.RemoveAll(storage)
				}
				return nil
			}
		} else {
			r.cleanupUnitState = "LoadState=loaded\nActiveState=active\n"
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := p.cleanupBuildFiles(ctx, id)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "termination unconfirmed") {
			t.Fatalf("active cleanup unit accepted: %v", err)
		}
		if _, err := p.verifyActiveBuildOwnership(id); err != nil {
			t.Fatal("active cleanup unit lost receipts")
		}
		if !afterRemoval {
			assertBuildSentinel(t, storage)
			for _, call := range r.calls {
				if call.Name == "env" || call.Name == "systemd-run" {
					t.Fatal("active old cleanup unit allowed storage commands")
				}
			}
		}
	}
}
