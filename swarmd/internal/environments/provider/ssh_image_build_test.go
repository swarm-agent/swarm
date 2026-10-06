package provider

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

type sshBuildRunner struct {
	t                                     *testing.T
	definition                            environments.ImageBuildDefinition
	op                                    string
	contextDigest                         string
	unavailable, transferFail, wrongImage bool
	remoteBuilds                          int
	archiveFiles                          map[string]string
}

func (r *sshBuildRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "env" {
		return []byte(strings.TrimSuffix(args[len(args)-1], "^{commit}") + "\n"), nil
	}
	joined := strings.Join(args, " ")
	if name != "ssh" {
		return nil, errors.New("unexpected transport")
	}
	if _, ok := ctx.Deadline(); !ok {
		r.t.Fatal("unbounded SSH command")
	}
	if r.unavailable {
		return nil, errors.New("access unavailable")
	}
	if strings.Contains(joined, "image inspect") {
		out, _ := json.Marshal(map[string]any{"Id": "sha256:" + strings.Repeat("c", 64), "Config": map[string]any{"Labels": map[string]string{"swarm.build_operation": r.op, "swarm.build_context": r.contextDigest}}})
		if r.wrongImage {
			out = []byte(`{"Id":"mutable:tag"}`)
		}
		return out, nil
	}
	return nil, nil
}
func (r *sshBuildRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}
func (r *sshBuildRunner) RunWithIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	if name == "env" {
		body, file := "package exact", "source.go"
		if strings.Contains(strings.Join(args, " "), r.definition.Recipe.Commit+"^{tree}") {
			body, file = "FROM generic-test-image\nCOPY . /workspace\n", r.definition.RecipeFile
		}
		return writeTestArchive(stdout, file, body)
	}
	if name != "ssh" {
		return errors.New("unexpected IO transport")
	}
	r.remoteBuilds++
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--network=none") || !strings.Contains(joined, "--kill-after=10s") {
		r.t.Fatal("unbounded remote build")
	}
	for _, a := range args {
		if strings.HasPrefix(a, "swarm.build_context=") {
			r.contextDigest = strings.TrimPrefix(a, "swarm.build_context=")
		}
	}
	if r.transferFail {
		return context.DeadlineExceeded
	}
	r.archiveFiles = map[string]string{}
	tr := tar.NewReader(stdin)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg {
			b, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			r.archiveFiles[h.Name] = string(b)
		}
	}
	return nil
}
func writeTestArchive(w io.Writer, name, body string) error {
	tw := tar.NewWriter(w)
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(body))}); err != nil {
		return err
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		return err
	}
	return tw.Close()
}

// Purpose: SSHDockerProvider.BuildImage must transfer exact sanitized committed
// trees, not live files, and admit only remote-observed immutable image identity.
// A transport fixture is the narrowest hermetic proof of bytes, bounds, preflight
// rejection, cancellation uncertainty and local owned scratch removal.
func TestSSHExactBuildTransfer(t *testing.T) {
	for _, failure := range []string{"", "access", "transfer", "image"} {
		t.Run(failure, func(t *testing.T) {
			scratch := t.TempDir()
			t.Setenv("TMPDIR", scratch)
			r := &sshBuildRunner{t: t, definition: buildDefinitionFixture(), op: "op_exact", unavailable: failure == "access", transferFail: failure == "transfer", wrongImage: failure == "image"}
			p := NewSSHDockerProvider(r)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := p.BuildImage(ctx, ImageBuildRequest{OperationID: r.op, Connection: testSSHConnection(), Definition: r.definition, ProductRoot: t.TempDir(), RecipeRoot: t.TempDir()})
			if failure == "" {
				if err != nil || result == nil || result.ImageID != "sha256:"+strings.Repeat("c", 64) || result.ConnectionDigest != environments.ConnectionTransportDigest(testSSHConnection()) || r.archiveFiles["source.go"] != "package exact" || !strings.Contains(r.archiveFiles[".swarm-recipe/recipe/Containerfile"], "COPY") {
					t.Fatalf("result=%+v err=%v files=%v", result, err, r.archiveFiles)
				}
			} else if err == nil || result != nil {
				t.Fatal("failure admitted a receipt")
			}
			if failure == "access" {
				var noEffects *BuildNoEffectsError
				if r.remoteBuilds != 0 || !errors.As(err, &noEffects) {
					t.Fatal("unavailable access reached build or fabricated cleanup uncertainty")
				}
			}
			if failure == "transfer" && !errors.Is(err, ErrOperationNotConfirmed) {
				t.Fatal("remote cleanup fabricated")
			}
			entries, _ := os.ReadDir(scratch)
			if len(entries) != 0 {
				t.Fatal("owned scratch leaked")
			}
		})
	}
}

// Purpose: Inspect/Exec/Destroy must fence edited connections, wrong images and
// unrelated containers before access or deletion. Runner-bound identity checks
// prove both rejection and preservation of the unrelated sentinel resource.
func TestSSHExactRuntimeIdentity(t *testing.T) {
	q := deploymentImageRequest(environments.ConnectionKindLocalDocker, "sha256:"+deploymentImageDigest)
	q.Connection.Kind = environments.ConnectionKindSSH
	q.Connection.SSH = &environments.SSHConfig{Host: "example.invalid", User: "tester", Port: 22}
	q.Deployment.ConnectionID = q.Connection.ID
	q.Deployment.Build.ConnectionDigest = environments.ConnectionTransportDigest(q.Connection)
	q.Deployment.Runtime.ContainerID = "owned-container"
	r := newMockSSHRunner()
	good := deploymentImageInspect(t, environments.ConnectionKindLocalDocker, q.Deployment.Build.ImageID)
	var records []map[string]any
	_ = json.Unmarshal([]byte(good), &records)
	records[0]["Config"].(map[string]any)["Labels"] = map[string]string{"swarm.build_receipt": q.Deployment.Build.OperationID, "swarm.deployment_id": q.Deployment.ID, "swarm.account_scope_id": q.Deployment.AccountScopeID}
	observed, _ := json.Marshal(records)
	r.handlers["inspect"] = func(string, []string) ([]byte, error) { return observed, nil }
	p := NewSSHDockerProvider(r)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := p.Inspect(ctx, q.Connection, q.Deployment); err != nil {
		t.Fatal(err)
	}
	before := len(r.Calls())
	q.Connection.SSH.Host = "changed.invalid"
	if _, err := p.Inspect(ctx, q.Connection, q.Deployment); err == nil || len(r.Calls()) != before {
		t.Fatal("edited connection reached transport")
	}
	q.Connection.SSH.Host = "example.invalid"
	records[0]["Image"] = "sha256:" + strings.Repeat("f", 64)
	observed, _ = json.Marshal(records)
	if _, err := p.Inspect(ctx, q.Connection, q.Deployment); err == nil {
		t.Fatal("wrong image admitted")
	}
	records[0]["Config"].(map[string]any)["Labels"] = map[string]string{"swarm.deployment_id": "unrelated"}
	observed, _ = json.Marshal(records)
	if err := p.Destroy(ctx, q.Connection, q.Deployment); err == nil {
		t.Fatal("unrelated cleanup admitted")
	}
	for _, c := range r.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), "docker rm") {
			t.Fatal("unrelated resource deleted")
		}
	}
}

// Purpose: Deploy owns receipt-bound allocation and rollback; exact image checks
// must precede setup and failures must delete only the owned container. The
// runner fixture proves loopback mapping, access rejection, partial-create
// cleanup and preservation of an unrelated container without contacting SSH.
func TestSSHExactDeploymentCleanup(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "partial"}[fail], func(t *testing.T) {
			q := deploymentImageRequest(environments.ConnectionKindLocalDocker, "sha256:"+deploymentImageDigest)
			q.Connection.Kind = environments.ConnectionKindSSH
			q.Connection.SSH = &environments.SSHConfig{Host: "example.invalid", User: "tester", Port: 22}
			q.Deployment.Build.ConnectionDigest = environments.ConnectionTransportDigest(q.Connection)
			q.Environment.Container.SetupCommands = nil
			r := newMockSSHRunner()
			r.handlers["container"] = func(string, []string) ([]byte, error) { return nil, nil }
			name := containerName(q.Environment.ID, q.Deployment.ID)
			allocated := false
			removed := false
			good := deploymentImageInspect(t, environments.ConnectionKindLocalDocker, q.Deployment.Build.ImageID)
			var records []map[string]any
			_ = json.Unmarshal([]byte(good), &records)
			records[0]["Config"].(map[string]any)["Labels"] = map[string]string{"swarm.build_receipt": q.Deployment.Build.OperationID, "swarm.deployment_id": q.Deployment.ID, "swarm.account_scope_id": q.Deployment.AccountScopeID}
			observed, _ := json.Marshal(records)
			r.handlers["inspect"] = func(string, []string) ([]byte, error) {
				if !allocated {
					return nil, errors.New("absent")
				}
				return observed, nil
			}
			r.handlers["run"] = func(_ string, args []string) ([]byte, error) {
				if !strings.Contains(strings.Join(args, " "), "127.0.0.1:18080:8080/tcp") {
					t.Fatal("public managed port")
				}
				allocated = true
				if fail {
					return nil, context.DeadlineExceeded
				}
				return []byte("owned-container"), nil
			}
			r.handlers["rm"] = func(_ string, args []string) ([]byte, error) {
				if args[len(args)-1] != "owned-container" {
					t.Fatal("unowned removal")
				}
				removed = true
				return nil, nil
			}
			p := NewSSHDockerProvider(r)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			out, err := p.Deploy(ctx, q)
			if fail {
				if err == nil || out != nil || !removed {
					t.Fatal("partial allocation not cleaned")
				}
			} else {
				if err != nil || out == nil || removed {
					t.Fatalf("deployment: %+v %v", out, err)
				}
				q.Deployment.Runtime = out.Runtime
				if err := p.Destroy(ctx, q.Connection, q.Deployment); err != nil || !removed {
					t.Fatal("owned destroy failed")
				}
			}
			for _, c := range r.Calls() {
				if strings.Contains(strings.Join(c.Args, " "), "docker rm") && strings.Contains(strings.Join(c.Args, " "), name) {
					t.Fatal("removed name rather than observed immutable ID")
				}
			}
		})
	}
}

// Purpose: SSH preflight must not report available capabilities or launch work
// when external access is absent; unsafe destination flags are rejected before
// runner invocation. The runner boundary proves no retries or local fallback.
func TestSSHExactPreflightUnavailable(t *testing.T) {
	r := newMockSSHRunner()
	r.failSSH = true
	p := NewSSHDockerProvider(r)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	caps, err := p.Capabilities(ctx, testSSHConnection())
	if err == nil || caps.SupportsDocker || len(r.Calls()) != 1 {
		t.Fatal("unavailable access claimed ready or retried")
	}
	conn := testSSHConnection()
	conn.SSH.Host = "-oProxyCommand=untrusted"
	before := len(r.Calls())
	if err := p.ValidateConnection(ctx, conn); err == nil || len(r.Calls()) != before {
		t.Fatal("unsafe SSH destination reached transport")
	}
}
