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
			r := &imageBuildRunner{t: t, definition: buildDefinitionFixture(), operation: "op_test", id: "sha256:" + strings.Repeat("c", 64), failBuild: mode == "failure" || mode == "both-fail", failCleanup: mode == "cleanup-failure" || mode == "both-fail", cancelBuild: mode == "cancel", cancel: cancel}
			p := NewLocalPodmanProvider(r)
			root := t.TempDir()
			p.ConfigureBuildRoot(root)
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
			} else if !errors.Is(err, ErrOperationCleanupFailed) {
				t.Fatalf("cleanup uncertainty hidden: %v", err)
			}
			var build string
			for _, c := range r.calls {
				if c.Name == "systemd-run" {
					build = strings.Join(c.Args, " ")
				}
			}
			for _, want := range []string{"--jobs=1", "--ignorefile", "--network=slirp4netns:allow_host_loopback=false", "--cgroupns=private", "--property=TasksMax=1024", "--property=KillMode=control-group", "env -i", "--http-proxy=false", "--authfile", "--storage-driver=vfs", "--cgroup-manager=cgroupfs", "--property=Delegate=yes", "SWARM_BUILD_SHA=" + r.definition.Product.Commit} {
				if !strings.Contains(build, want) {
					t.Fatalf("missing %s", want)
				}
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
		owned := filepath.Join(root, "op_partial")
		if err := os.MkdirAll(filepath.Join(owned, "storage"), 0700); err != nil {
			t.Fatal(err)
		}
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
		owned := filepath.Join(root, "op_storage")
		if err := os.Mkdir(owned, 0700); err != nil {
			t.Fatal(err)
		}
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
