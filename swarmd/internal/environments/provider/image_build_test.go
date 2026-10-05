package provider

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		return []byte("inactive"), nil
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
	for _, mode := range []string{"success", "failure", "cancel", "cleanup-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := &imageBuildRunner{t: t, definition: buildDefinitionFixture(), operation: "op_test", id: "sha256:" + strings.Repeat("c", 64), failBuild: mode == "failure", failCleanup: mode == "cleanup-failure", cancelBuild: mode == "cancel", cancel: cancel}
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
			if mode != "cleanup-failure" {
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
			for _, want := range []string{"--jobs=1", "--ignorefile", "--network=slirp4netns:allow_host_loopback=false", "--cgroupns=private", "--property=TasksMax=1024", "--property=KillMode=control-group", "env -i", "--http-proxy=false", "--authfile", "--storage-driver=vfs", "SWARM_BUILD_SHA=" + r.definition.Product.Commit} {
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
