package provider

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

func buildDefinitionFixture() environments.ImageBuildDefinition {
	return environments.ImageBuildDefinition{Product: environments.CommittedBuildSource{WorkspaceID: "product", WorkspaceGeneration: 1, Commit: strings.Repeat("a", 40)}, Recipe: environments.CommittedBuildSource{WorkspaceID: "recipe", WorkspaceGeneration: 1, Commit: strings.Repeat("b", 40)}, RecipeDirectory: "recipe", RecipeFile: "recipe/Containerfile"}
}
func buildTar(t *testing.T, headers []*tar.Header, bodies []string) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for i, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if len(bodies) > i {
			if _, err := tw.Write([]byte(bodies[i])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Purpose: ExportCommittedBuild/extractBuildArchive own the only build context
// writes. Adversarial tar headers must not escape, follow links or overwrite
// files; this parser layer proves filesystem postconditions without an engine.
func TestManagedBuildArchiveConfinement(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind byte
		size int64
	}{
		{"../escape", tar.TypeReg, 1}, {"/escape", tar.TypeReg, 1}, {"link", tar.TypeSymlink, 0}, {"hard", tar.TypeLink, 0}, {"device", tar.TypeChar, 0}, {".swarm-recipe/inject", tar.TypeReg, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dest := filepath.Join(root, "context")
			if err := os.Mkdir(dest, 0700); err != nil {
				t.Fatal(err)
			}
			body := ""
			if tc.size == 1 {
				body = "x"
			}
			raw := buildTar(t, []*tar.Header{{Name: tc.name, Typeflag: tc.kind, Size: tc.size, Linkname: "../escape", Mode: 0600}}, []string{body})
			var total int64
			count := 0
			if err := extractBuildArchive(context.Background(), bytes.NewReader(raw), dest, "", io.Discard, &total, &count); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			entries, _ := os.ReadDir(dest)
			if len(entries) != 0 {
				t.Fatal("rejected archive wrote files")
			}
			if _, err := os.Stat(filepath.Join(root, "escape")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("escaped context")
			}
		})
	}
	dest := t.TempDir()
	raw := buildTar(t, []*tar.Header{{Name: ".env", Typeflag: tar.TypeReg, Size: 6, Mode: 0600}, {Name: "source.go", Typeflag: tar.TypeReg, Size: 4, Mode: 0644}}, []string{"secret", "code"})
	var total int64
	count := 0
	var digest bytes.Buffer
	if err := extractBuildArchive(context.Background(), bytes.NewReader(raw), dest, "", &digest, &total, &count); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("credential included")
	}
	if bytes.Contains(digest.Bytes(), []byte("secret")) {
		t.Fatal("credential in digest input")
	}
	if err := extractBuildArchive(context.Background(), bytes.NewReader(raw), dest, "", io.Discard, &total, &count); err == nil {
		t.Fatal("duplicate file overwritten")
	}
	got, _ := os.ReadFile(filepath.Join(dest, "source.go"))
	if string(got) != "code" {
		t.Fatal("original changed")
	}
}

type imageBuildRunner struct {
	t                                   *testing.T
	definition                          environments.ImageBuildDefinition
	operation, id                       string
	calls                               []mockCall
	failBuild, failCleanup, cancelBuild bool
	cancel                              context.CancelFunc
	unitState                           string
	unitErr                             error
	failurePhase                        string
	commandErr                          error
	commandOutput                       string
	onBuild                             func([]string) error
}

func (r *imageBuildRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, mockCall{name, append([]string(nil), args...)})
	joined := strings.Join(args, " ")
	if name == "env" && strings.Contains(joined, "rev-parse") {
		if strings.Contains(joined, r.definition.Product.Commit) {
			return []byte(r.definition.Product.Commit), nil
		}
		return []byte(r.definition.Recipe.Commit), nil
	}
	if name == "systemctl" {
		if strings.Contains(joined, "Version") {
			return []byte("256"), nil
		}
		if r.failCleanup {
			return nil, errors.New("injected cleanup failure")
		}
		if r.unitState != "" || r.unitErr != nil {
			return []byte(r.unitState), r.unitErr
		}
		return []byte("LoadState=loaded\nActiveState=inactive\n"), nil
	}
	if strings.Contains(joined, "info --format=json") {
		return []byte(podmanInfoFixture), nil
	}
	if strings.Contains(joined, "image inspect") {
		data, _ := json.Marshal([]any{map[string]any{"Id": r.id, "Labels": map[string]string{"io.swarm.build.operation": r.operation, "io.swarm.build.inputs": r.definition.Digest(), "org.opencontainers.image.revision": r.definition.Product.Commit}}})
		return data, nil
	}
	return nil, nil
}
func (r *imageBuildRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}
func (r *imageBuildRunner) RunWithIO(ctx context.Context, _ io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	r.calls = append(r.calls, mockCall{name, append([]string(nil), args...)})
	joined := strings.Join(args, " ")
	phase := ""
	if name == "systemd-run" {
		phase = "build"
	} else if strings.Contains(joined, " save ") {
		phase = "export"
	} else if name == "podman" && strings.Contains(joined, " load ") {
		phase = "import"
	}
	if phase != "" && phase == r.failurePhase {
		_, _ = io.WriteString(stdout, "PRIVATE_STDOUT")
		_, _ = io.WriteString(stderr, r.commandOutput)
		return r.commandErr
	}
	if strings.Contains(joined, "archive --format=tar") {
		file, body := "source.go", "committed product"
		if strings.Contains(joined, r.definition.Recipe.Commit) {
			file, body = "recipe/Containerfile", "FROM scratch"
		}
		_, err := stdout.Write(buildTar(r.t, []*tar.Header{{Name: file, Typeflag: tar.TypeReg, Size: int64(len(body)), Mode: 0644}}, []string{body}))
		return err
	}
	if name == "systemd-run" {
		if r.onBuild != nil {
			if err := r.onBuild(args); err != nil {
				return err
			}
		}
		if r.cancelBuild {
			r.cancel()
			return ctx.Err()
		}
		if r.failBuild {
			_, _ = stderr.Write([]byte("PRIVATE_RECIPE_SECRET"))
			return errors.New("PRIVATE_RECIPE_SECRET")
		}
		for i, a := range args {
			if a == "--iidfile" {
				return os.WriteFile(args[i+1], []byte(r.id), 0600)
			}
		}
		return errors.New("missing image receipt")
	}
	if strings.Contains(joined, " save ") {
		_, err := stdout.Write([]byte("opaque archive"))
		return err
	}
	return nil
}

// Purpose: LocalPodmanProvider.BuildImage must isolate credentials/context,
// bound descendants, retain exact image provenance and remove owned scratch on
// success, failure and cancellation. The injected engine proves emitted argv
// and postconditions, not actual Podman/systemd host compatibility.
func TestManagedBuildProviderContract(t *testing.T) {
	for _, mode := range []string{"success", "failure", "cancel", "cleanup-failure", "both-fail"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opID := "op_" + strings.ReplaceAll(mode, "-", "_")
			r := &imageBuildRunner{t: t, definition: buildDefinitionFixture(), operation: opID, id: "sha256:" + strings.Repeat("c", 64), failBuild: mode == "failure" || mode == "both-fail", failCleanup: mode == "cleanup-failure" || mode == "both-fail", cancelBuild: mode == "cancel", cancel: cancel}
			p := NewLocalPodmanProvider(r)
			root := t.TempDir()
			p.ConfigureBuildRoot(root)
			configureBuildRuntimeFixture(t, p)
			runroot, rErr := p.buildRunroot(opID)
			if rErr != nil {
				t.Fatalf("buildRunroot: %v", rErr)
			}

			result, err := p.BuildImage(ctx, ImageBuildRequest{OperationID: r.operation, Connection: podmanConnectionForTest(environments.ConnectionKindLocalPodman), Definition: r.definition, ProductRoot: filepath.Join(root, "product"), RecipeRoot: filepath.Join(root, "recipe")})
			if mode == "success" {
				if err != nil || result == nil || result.ImageID != r.id || result.Product != r.definition.Product || len(result.ContextDigest) != 64 {
					t.Fatalf("%+v %v", result, err)
				}
			} else if err == nil || result != nil || strings.Contains(err.Error(), "PRIVATE_RECIPE_SECRET") {
				t.Fatalf("failure not closed/redacted: %+v %v", result, err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation identity lost: %v", err)
			}
			if mode == "both-fail" && (!strings.Contains(err.Error(), "isolated image build failed") || !strings.Contains(err.Error(), "build unit termination unconfirmed")) {
				t.Fatalf("combined failure lost: %v", err)
			}
			if mode != "cleanup-failure" && mode != "both-fail" {
				if _, err := os.Stat(filepath.Join(root, r.operation)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("owned scratch retained")
				}
				if _, err := os.Stat(runroot); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("owned runroot retained")
				}
			} else if !errors.Is(err, ErrOperationCleanupFailed) {
				t.Fatalf("cleanup uncertainty hidden: %v", err)
			}
			var build string
			for _, c := range r.calls {
				if c.Name == "systemd-run" {
					build = strings.Join(c.Args, " ")
				}
			}
			for _, want := range []string{"--jobs=1", "--ignorefile", "--network=slirp4netns:allow_host_loopback=false", "--cgroupns=private", "--property=TasksMax=1024", "--property=KillMode=control-group", "env -i", "--http-proxy=false", "--authfile", "--storage-driver=vfs", "--cgroup-manager=cgroupfs", "--property=Delegate=yes", "SWARM_BUILD_SHA=" + r.definition.Product.Commit, "--runroot " + runroot} {
				if !strings.Contains(build, want) {
					t.Fatalf("missing %s", want)
				}
			}
			if len(runroot) > 50 {
				t.Fatalf("runroot %q length %d exceeds 50", runroot, len(runroot))
			}
			for _, bad := range []string{"--privileged", "--network=host", "--volume", "--secret", "--ssh"} {
				if strings.Contains(build, bad) {
					t.Fatalf("unsafe build: %s", bad)
				}
			}
			if _, ok := p.buildRuns.Load(r.operation); ok {
				t.Fatal("completed build leaked live registry entry")
			}
		})
	}
}

// Purpose: bounded archive and image-ID reads prevent attacker-sized engine
// output and symlink receipts from becoming accepted provenance. These helpers
// are the narrowest layer proving cancellation and zero oversized writes.
func TestManagedBuildOutputBounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var b bytes.Buffer
	w := &buildArchiveWriter{writer: &b, remaining: 2, cancel: cancel}
	if _, err := w.Write([]byte("big")); err == nil || ctx.Err() == nil || b.Len() != 0 {
		t.Fatal("archive limit not enforced")
	}
	root := t.TempDir()
	file := filepath.Join(root, "id")
	if err := os.WriteFile(file, bytes.Repeat([]byte("x"), 129), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBuildImageID(file); err == nil {
		t.Fatal("oversized receipt accepted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readBuildImageID(link); err == nil {
		t.Fatal("symlink receipt accepted")
	}
}

// Purpose: ExportCommittedBuild must export the selected commit, not ambient
// dirty/untracked files or a moved branch. Two hermetic Git repositories prove
// the actual archive command semantics without any engine, network or provider.
func TestManagedBuildExactCommittedExport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	git := func(root string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	product, recipe := t.TempDir(), t.TempDir()
	git(product, "init")
	git(recipe, "init")
	if err := os.WriteFile(filepath.Join(product, "source.go"), []byte("selected"), 0600); err != nil {
		t.Fatal(err)
	}
	git(product, "add", "source.go")
	git(product, "commit", "-m", "selected")
	b := buildDefinitionFixture()
	b.Product.Commit = git(product, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(product, "source.go"), []byte("newer"), 0600); err != nil {
		t.Fatal(err)
	}
	git(product, "add", "source.go")
	git(product, "commit", "-m", "newer")
	if err := os.WriteFile(filepath.Join(product, "untracked"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(recipe, "recipe"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recipe, "recipe", "Containerfile"), []byte("FROM scratch"), 0600); err != nil {
		t.Fatal(err)
	}
	git(recipe, "add", "recipe")
	git(recipe, "commit", "-m", "recipe")
	b.Recipe.Commit = git(recipe, "rev-parse", "HEAD")
	dest := t.TempDir()
	digest, err := ExportCommittedBuild(ctx, &OSCommandRunner{}, product, recipe, dest, b)
	if err != nil || len(digest) != 64 {
		t.Fatalf("archive: %s %v", digest, err)
	}
	data, _ := os.ReadFile(filepath.Join(dest, "source.go"))
	if string(data) != "selected" {
		t.Fatal("wrong commit exported")
	}
	if _, err := os.Stat(filepath.Join(dest, "untracked")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("live file exported")
	}
	if data, err := os.ReadFile(filepath.Join(dest, ".swarm-recipe", "recipe", "Containerfile")); err != nil || string(data) != "FROM scratch" {
		t.Fatal("recipe missing")
	}
	b.Product.Commit = strings.Repeat("f", 40)
	empty := t.TempDir()
	if _, err := ExportCommittedBuild(ctx, &OSCommandRunner{}, product, recipe, empty, b); err == nil {
		t.Fatal("missing commit accepted")
	}
	entries, _ := os.ReadDir(empty)
	if len(entries) != 0 {
		t.Fatal("missing commit partially wrote context")
	}
}

// Purpose: cleanupBuildFiles must handle pre-launch/collected units and empty
// storage idempotently, but retain scratch on uncertain termination. Injected
// systemd responses prove this failure boundary without host services.
func TestManagedBuildPartialCleanup(t *testing.T) {
	for _, state := range []string{"LoadState=not-found\nActiveState=inactive", "LoadState=loaded\nActiveState=active", ""} {
		root := t.TempDir()
		r := &imageBuildRunner{unitState: state, unitErr: errors.New("unit query failed")}
		p := NewLocalPodmanProvider(r)
		p.ConfigureBuildRoot(root)
		configureBuildRuntimeFixture(t, p)
		owned := filepath.Join(root, "op_partial")
		if err := os.MkdirAll(filepath.Join(owned, "storage"), 0700); err != nil {
			t.Fatal(err)
		}
		allocateBuildRuntimeFixture(t, p, "op_partial")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := p.CleanupBuild(ctx, "op_partial")
		confirmed := strings.Contains(state, "not-found")
		if (err == nil) != confirmed {
			t.Fatalf("state %q: %v", state, err)
		}
		_, statErr := os.Stat(owned)
		if errors.Is(statErr, os.ErrNotExist) != confirmed {
			t.Fatalf("scratch removal disagrees with termination evidence: %v", statErr)
		}
		inventory := false
		for _, call := range r.calls {
			args := strings.Join(call.Args, " ")
			inventory = inventory || strings.Contains(args, "images --filter label=io.swarm.build.operation=op_partial")
			if strings.Contains(args, "unmount") {
				t.Fatal("empty storage invoked engine initialization")
			}
		}
		if !inventory {
			t.Fatal("scratch failure prevented independent image cleanup")
		}
		r.unitState, r.unitErr = "LoadState=not-found\nActiveState=inactive", errors.New("collected")
		for i := 0; i < 2; i++ {
			if err := p.CleanupBuild(ctx, "op_partial"); err != nil {
				t.Fatalf("cleanup retry: %v", err)
			}
		}
		cancel()
	}
}

// Purpose: BuildCleanupError must not persist credentials or unbounded cleanup
// output while preserving primary error identity and cleanup classification.
// The diagnostic helper is the narrowest layer proving these postconditions.
func TestManagedBuildCleanupDiagnosticBounds(t *testing.T) {
	primary := errors.New("committed source unavailable")
	err := BuildCleanupError(primary, errors.New("password=PRIVATE_VALUE\n"+strings.Repeat("x", 4096)))
	if !errors.Is(err, primary) || !errors.Is(err, ErrOperationCleanupFailed) || strings.Contains(err.Error(), "PRIVATE_VALUE") || len(err.Error()) > 1300 {
		t.Fatalf("unsafe cleanup diagnostic: %v", err)
	}
	if BuildCleanupError(primary, nil) != primary {
		t.Fatal("confirmed cleanup replaced original error")
	}
}

// Purpose: cleanupBuildFiles must never follow redirected storage, and must
// unmount populated owned storage before removing it. The filesystem and command
// boundary test proves confinement without invoking a real rootless engine.
func TestManagedBuildCleanupStorageOwnership(t *testing.T) {
	for _, redirected := range []bool{false, true} {
		root, outside := t.TempDir(), t.TempDir()
		r := &imageBuildRunner{}
		p := NewLocalPodmanProvider(r)
		p.ConfigureBuildRoot(root)
		configureBuildRuntimeFixture(t, p)
		owned := filepath.Join(root, "op_storage")
		if err := os.Mkdir(owned, 0700); err != nil {
			t.Fatal(err)
		}
		allocateBuildRuntimeFixture(t, p, "op_storage")
		storage := filepath.Join(owned, "storage")
		if redirected {
			if err := os.Symlink(outside, storage); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Mkdir(storage, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(storage, "sentinel"), []byte("owned"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := p.cleanupBuildFiles(ctx, "op_storage")
		cancel()
		if (err != nil) != redirected {
			t.Fatalf("redirect=%t cleanup=%v", redirected, err)
		}
		unmounted := false
		for _, call := range r.calls {
			if strings.Contains(strings.Join(call.Args, " "), "unmount --all --force") {
				unmounted = true
				if !strings.Contains(strings.Join(call.Args, " "), "--root "+storage) {
					t.Fatal("unmount escaped owned storage")
				}
			}
		}
		if unmounted == redirected {
			t.Fatal("unmount did not respect storage ownership")
		}
		if redirected {
			if data, err := os.ReadFile(filepath.Join(outside, "sentinel")); err != nil || string(data) != "owned" {
				t.Fatal("redirected storage mutated")
			}
		} else if _, err := os.Stat(owned); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("owned scratch retained after confirmed cleanup")
		}
	}
}

// Purpose: BuildImage must retain failure phase/status without admitting an image
// or persisting arbitrary recipe/runner output. Injected command failures are the
// narrowest layer proving exit propagation, bounded hints and owned cleanup.
func TestManagedBuildCommandFailures(t *testing.T) {
	for _, phase := range []string{"build", "export", "import"} {
		t.Run(phase, func(t *testing.T) {
			r := &imageBuildRunner{t: t, definition: buildDefinitionFixture(), operation: "op_failure", id: "sha256:" + strings.Repeat("c", 64), failurePhase: phase, commandErr: buildTestExit(125), commandOutput: "no space left on device: PRIVATE_SECRET\n" + strings.Repeat("PRIVATE_SECRET", 10000)}
			p := NewLocalPodmanProvider(r)
			root := t.TempDir()
			p.ConfigureBuildRoot(root)
			configureBuildRuntimeFixture(t, p)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := p.BuildImage(ctx, ImageBuildRequest{OperationID: r.operation, Connection: podmanConnectionForTest(environments.ConnectionKindLocalPodman), Definition: r.definition, ProductRoot: filepath.Join(root, "product"), RecipeRoot: filepath.Join(root, "recipe")})
			var failure *BuildCommandError
			if result != nil || !errors.As(err, &failure) || failure.Phase != phase || failure.Code != 125 || failure.Kind != "exit" {
				t.Fatalf("incorrect failure: %+v %v", result, err)
			}
			if strings.Contains(err.Error(), "PRIVATE") || !strings.Contains(err.Error(), "storage capacity") || len(err.Error()) > 512 || errors.Unwrap(failure) != nil {
				t.Fatalf("unsafe/unhelpful diagnostic: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, r.operation)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed operation scratch retained")
			}
			inventory := false
			for _, call := range r.calls {
				args := strings.Join(call.Args, " ")
				inventory = inventory || strings.Contains(args, "images --filter label=io.swarm.build.operation="+r.operation)
				if strings.Contains(args, "image inspect") {
					t.Fatal("failed command reached admission inspection")
				}
			}
			if !inventory {
				t.Fatal("owned image cleanup not attempted")
			}
		})
	}
}

type buildTestExit int

func (e buildTestExit) Error() string { return "PRIVATE_RUNNER_SECRET" }
func (e buildTestExit) ExitCode() int { return int(e) }

// Purpose: runBuildCommand must classify cancellation, deadlines and missing
// executables independently of untrusted error text, and stop inspecting output
// at its byte budget. This helper-level test avoids host runtime dependencies.
func TestManagedBuildDiagnosticClassification(t *testing.T) {
	for _, tc := range []struct {
		err  error
		kind string
		code int
	}{
		{context.Canceled, "cancelled", 130},
		{context.DeadlineExceeded, "deadline", 124},
		{exec.ErrNotFound, "executable-unavailable", 127},
		{buildTestExit(-1), "signal", -1},
		{errors.New("PRIVATE_RUNNER_SECRET"), "runner", 1},
	} {
		r := &imageBuildRunner{failurePhase: "build", commandErr: tc.err, commandOutput: strings.Repeat("x", 16*1024) + "no space left on device PRIVATE_SECRET"}
		p := NewLocalPodmanProvider(r)
		err := p.runBuildCommand(context.Background(), "build", io.Discard, "systemd-run")
		var failure *BuildCommandError
		if !errors.As(err, &failure) || failure.Kind != tc.kind || failure.Code != tc.code || strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "storage capacity") {
			t.Fatalf("classification/bound: %v", err)
		}
		if (tc.kind == "cancelled" || tc.kind == "deadline") && !errors.Is(err, tc.err) {
			t.Fatal("lost context sentinel")
		}
	}
}

// Purpose: runBuildCommand must extract a real os/exec status while withholding
// unlabelled secrets and forged diagnostics. A bounded local shell process proves
// the pipe boundary; it is not a Podman compatibility or live workload test.
func TestManagedBuildRealCommandDiagnostic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := NewLocalPodmanProvider(&OSCommandRunner{})
	// This is a shell pipe/status test, not a rootless session probe.
	p.runner.(*OSCommandRunner).commandEnv = func() ([]string, error) {
		return []string{"PATH=" + os.Getenv("PATH")}, nil
	}
	err := p.runBuildCommand(ctx, "build", io.Discard, "sh", "-c", `printf 'PRIVATE_STDOUT'; printf 'failed to connect to bus PRIVATE_UNLABELLED_SECRET\n' >&2; exit 125`)
	var failure *BuildCommandError
	if !errors.As(err, &failure) || failure.Code != 125 || failure.Kind != "exit" || !strings.Contains(err.Error(), "user session bus") || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("unsafe or missing real command diagnostic: %v", err)
	}
	if err := p.runBuildCommand(ctx, "build", io.Discard, "sh", "-c", "exit 0"); err != nil {
		t.Fatalf("successful command rejected: %v", err)
	}
	cancel()
	err = p.runBuildCommand(ctx, "build", io.Discard, "sh", "-c", "exit 0")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled runner lost context: %v", err)
	}
}

// Purpose: buildRunrootPath and buildRunroot must guarantee that any allocated
// runroot is an absolute, clean path directly beneath the verified user runtime
// directory, with total path length <= 50 characters (as enforced by Podman 4.9.3).
// If a runtime directory would cause the runroot to exceed 50 characters, or is
// un-clean/relative, it must fail closed before executing commands.
func TestManagedBuildRunrootLengthAndFormat(t *testing.T) {
	for _, tc := range []struct {
		name       string
		runtimeDir string
		opID       string
		wantErr    bool
		maxLen     int
	}{
		{"standard-uid-1000", "/run/user/1000", "op_e5b458d5f2ac455a9775d51ff1220ab5", false, 50},
		{"large-uid-100000", "/run/user/100000", "op_test_123", false, 50},
		{"max-uid-4294967295", "/run/user/4294967295", "op_max_uid", false, 50},
		{"short-runtime-custom", "/run/user/500", "op_abc", false, 50},
		{"excessively-long-runtime", "/run/user/this_path_is_far_too_long_to_ever_fit_within_fifty_chars", "op_toolong", true, 0},
		{"relative-runtime", "run/user/1000", "op_relative", true, 0},
		{"dirty-runtime", "/run/user/1000/../1000", "op_dirty", true, 0},
		{"invalid-op-id", "/run/user/1000", "invalid_no_op_prefix", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildRunrootPath(tc.runtimeDir, "/build/account", tc.opID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %s, got %q", tc.name, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", tc.name, err)
			}
			if len(got) > tc.maxLen {
				t.Fatalf("runroot length %d exceeds max %d: %q", len(got), tc.maxLen, got)
			}
			if filepath.Dir(got) != tc.runtimeDir {
				t.Fatalf("runroot %q not direct child of %q", got, tc.runtimeDir)
			}
			base := filepath.Base(got)
			if !strings.HasPrefix(base, buildRunrootPrefix) {
				t.Fatalf("runroot base %q missing prefix %q", base, buildRunrootPrefix)
			}
			if len(base) != len(buildRunrootPrefix)+24 {
				t.Fatalf("runroot base %q invalid length %d", base, len(base))
			}
		})
	}
	first, err := buildRunrootPath("/run/user/4294967295", "/build/account-one", "op_same")
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildRunrootPath("/run/user/4294967295", "/build/account-two", "op_same")
	if err != nil || first == second {
		t.Fatalf("account build roots alias: %q %q %v", first, second, err)
	}
}

// Purpose: BuildImage, image save (export), and cleanupBuildFiles (unmount) must
// use the exact same per-operation runroot. An inconsistency between phases
// would leave mounts uncleaned or export from the wrong storage driver state.
// The runner call inspection proves argument consistency across all phases.
func TestManagedBuildRunrootConsistency(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opID := "op_consistency_test"
	r := &imageBuildRunner{t: t, definition: buildDefinitionFixture(), operation: opID, id: "sha256:" + strings.Repeat("d", 64)}
	p := NewLocalPodmanProvider(r)
	root := t.TempDir()
	p.ConfigureBuildRoot(root)
	configureBuildRuntimeFixture(t, p)
	expectedRunroot, err := p.buildRunroot(opID)
	if err != nil {
		t.Fatalf("buildRunroot: %v", err)
	}

	result, err := p.BuildImage(ctx, ImageBuildRequest{
		OperationID: opID,
		Connection:  podmanConnectionForTest(environments.ConnectionKindLocalPodman),
		Definition:  r.definition,
		ProductRoot: filepath.Join(root, "product"),
		RecipeRoot:  filepath.Join(root, "recipe"),
	})
	if err != nil || result == nil {
		t.Fatalf("BuildImage failed: %v", err)
	}

	// Verify build phase systemd-run received the runroot
	var buildRunroot, saveRunroot string
	for _, c := range r.calls {
		if c.Name == "systemd-run" {
			for i, a := range c.Args {
				if a == "--runroot" && i+1 < len(c.Args) {
					buildRunroot = c.Args[i+1]
				}
			}
		}
		if c.Name == "env" && len(c.Args) > 1 && c.Args[len(c.Args)-1] != "" {
			for i, a := range c.Args {
				if a == "--runroot" && i+1 < len(c.Args) {
					saveRunroot = c.Args[i+1]
				}
			}
		}
	}
	if buildRunroot != expectedRunroot {
		t.Fatalf("build runroot mismatch: got %q, want %q", buildRunroot, expectedRunroot)
	}
	if saveRunroot != expectedRunroot {
		t.Fatalf("save runroot mismatch: got %q, want %q", saveRunroot, expectedRunroot)
	}

	// Verify unmount call in cleanup received the identical runroot
	cleanupRoot := filepath.Join(root, opID)
	// Recreate mock populated storage so cleanup invokes unmount
	if err := os.MkdirAll(filepath.Join(cleanupRoot, "storage"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cleanupRoot, "storage", "sentinel"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	allocateBuildRuntimeFixture(t, p, opID)
	r.calls = nil
	if err := p.cleanupBuildFiles(context.Background(), opID); err != nil {
		t.Fatalf("cleanupBuildFiles: %v", err)
	}
	var unmountRunroot string
	for _, c := range r.calls {
		if strings.Contains(strings.Join(c.Args, " "), "unmount") {
			for i, a := range c.Args {
				if a == "--runroot" && i+1 < len(c.Args) {
					unmountRunroot = c.Args[i+1]
				}
			}
		}
	}
	if unmountRunroot != expectedRunroot {
		t.Fatalf("unmount runroot mismatch: got %q, want %q", unmountRunroot, expectedRunroot)
	}

	// Verify runroot directory was cleaned up on disk
	if _, err := os.Stat(expectedRunroot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runroot %q was not removed after cleanup", expectedRunroot)
	}
}

// Purpose: BuildImage must preserve a pre-existing runroot and its contents
// after exclusive allocation rejects a collision. cleanupBuildFiles must also
// refuse to delete it when the scratch root is absent. Private filesystem
// fixtures prove both rejection and zero mutations, not just an error string.
func TestManagedBuildRunrootCollisionAndConcurrency(t *testing.T) {
	r := &imageBuildRunner{t: t, definition: buildDefinitionFixture()}
	p := NewLocalPodmanProvider(r)
	p.ConfigureBuildRoot(t.TempDir())
	configureBuildRuntimeFixture(t, p)
	opID := "op_collision"
	runroot, err := p.buildRunroot(opID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runroot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runroot, "sentinel"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := p.BuildImage(context.Background(), ImageBuildRequest{OperationID: opID, Connection: podmanConnectionForTest(environments.ConnectionKindLocalPodman), Definition: r.definition})
	if result != nil || err == nil || !strings.Contains(err.Error(), "runroot already exists") {
		t.Fatalf("collision accepted: %+v %v", result, err)
	}
	assertBuildSentinel(t, runroot)
	root, _ := p.buildDirectory(opID)
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected invocation scratch leaked")
	}
	r.calls = nil
	if err := p.cleanupBuildFiles(context.Background(), opID); err == nil {
		t.Fatal("missing root authorized foreign cleanup")
	}
	assertBuildSentinel(t, runroot)
	if len(r.calls) != 0 {
		t.Fatal("collision cleanup issued engine commands")
	}
}

// Purpose: When Podman fails with engine exit 125 and the exact stderr
// 'Error: the specified runroot is longer than 50 characters', buildInfrastructureHint
// must return the actionable fixed troubleshooting hint without leaking secrets or paths.
func TestManagedBuildRunrootDiagnosticHint(t *testing.T) {
	exactStderr := "Error: the specified runroot is longer than 50 characters"
	hint := buildInfrastructureHint(exactStderr)
	if !strings.Contains(hint, "untrusted output hint:") || !strings.Contains(hint, "runroot path length") {
		t.Fatalf("unexpected hint for exact stderr: %q", hint)
	}

	adversarialStderr := "Error: the specified runroot is longer than 50 characters: PRIVATE_SECRET_TOKEN=xyz123"
	advHint := buildInfrastructureHint(adversarialStderr)
	if strings.Contains(advHint, "PRIVATE_SECRET_TOKEN") || strings.Contains(advHint, "xyz123") {
		t.Fatalf("secret leaked in diagnostic hint: %q", advHint)
	}
	if advHint != hint {
		t.Fatalf("hint varied for adversarial input: got %q, want %q", advHint, hint)
	}
}

// Purpose: Lifecycle cancellation during an active build and crash recovery on a fresh
// provider instance must identify the identical runroot resource, unmount storage using
// that runroot, and clean up without orphaned state or affecting sibling operations.
func TestManagedBuildRunrootLifecycleCancellationAndCrashCleanup(t *testing.T) {
	// Subtest 1: Lifecycle cancellation during active build
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		opID := "op_cancel_runroot_test"
		r := &imageBuildRunner{
			t:           t,
			definition:  buildDefinitionFixture(),
			operation:   opID,
			id:          "sha256:" + strings.Repeat("e", 64),
			cancelBuild: true,
			cancel:      cancel,
		}
		p := NewLocalPodmanProvider(r)
		root := t.TempDir()
		p.ConfigureBuildRoot(root)
		configureBuildRuntimeFixture(t, p)
		expectedRunroot, err := p.buildRunroot(opID)
		if err != nil {
			t.Fatal(err)
		}

		_, err = p.BuildImage(ctx, ImageBuildRequest{
			OperationID: opID,
			Connection:  podmanConnectionForTest(environments.ConnectionKindLocalPodman),
			Definition:  r.definition,
			ProductRoot: filepath.Join(root, "product"),
			RecipeRoot:  filepath.Join(root, "recipe"),
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if _, err := os.Stat(expectedRunroot); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("runroot %q was not removed after cancellation", expectedRunroot)
		}
		if _, err := os.Stat(filepath.Join(root, opID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("operation scratch was not removed after cancellation")
		}
	})

	// Subtest 2: Crash recovery cleanup on fresh provider instance
	t.Run("crash-recovery", func(t *testing.T) {
		opID := "op_crash_recovery_test"
		r := &imageBuildRunner{t: t, definition: buildDefinitionFixture(), operation: opID}
		pFresh := NewLocalPodmanProvider(r)
		root := t.TempDir()
		pFresh.ConfigureBuildRoot(root)
		configureBuildRuntimeFixture(t, pFresh)
		expectedRunroot, err := pFresh.buildRunroot(opID)
		if err != nil {
			t.Fatal(err)
		}

		// Simulate crashed state on disk: root and runroot exist, storage populated
		opRoot := filepath.Join(root, opID)
		if err := os.MkdirAll(filepath.Join(opRoot, "storage"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(opRoot, "storage", "sentinel"), []byte("crashed"), 0600); err != nil {
			t.Fatal(err)
		}
		allocateBuildRuntimeFixture(t, pFresh, opID)

		// Fresh provider instance calls CleanupBuild (as crash recovery would)
		if err := pFresh.CleanupBuild(context.Background(), opID); err != nil {
			t.Fatalf("CleanupBuild crash recovery failed: %v", err)
		}

		// Verify unmount called with exact expectedRunroot
		unmounted := false
		for _, c := range r.calls {
			if strings.Contains(strings.Join(c.Args, " "), "unmount") {
				for i, a := range c.Args {
					if a == "--runroot" && i+1 < len(c.Args) && c.Args[i+1] == expectedRunroot {
						unmounted = true
					}
				}
			}
		}
		if !unmounted {
			t.Fatalf("unmount was not called with expected runroot %q", expectedRunroot)
		}

		// Verify both runroot and opRoot removed
		if _, err := os.Stat(expectedRunroot); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("runroot was not cleaned up during crash recovery")
		}
		if _, err := os.Stat(opRoot); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("operation scratch was not cleaned up during crash recovery")
		}
	})
}
