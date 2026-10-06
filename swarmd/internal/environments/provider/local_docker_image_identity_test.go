package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

const deploymentImageDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func deploymentImageInspect(t *testing.T, kind environments.ConnectionKind, image string) string {
	t.Helper()
	data := []byte(podmanInspectFixture)
	if kind == environments.ConnectionKindLocalDocker {
		data = makeInspectJSON("owned-container", "running", 0, "18080", "healthy")
	}
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatal(err)
	}
	records[0]["Image"] = image
	// Config.Image is a mutable name, not authority for the observed image ID.
	records[0]["Config"].(map[string]any)["Image"] = "localhost/untrusted:tag"
	out, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// This runner models only the lifecycle boundary under test. Removal changes
// owned state and cannot remove the independent sentinel container.
type deploymentImageRunner struct {
	kind       environments.ConnectionKind
	inspect    string
	calls      []mockCall
	owned      bool
	unrelated  bool
	cleanupErr error
}

func (r *deploymentImageRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, mockCall{Name: name, Args: append([]string(nil), args...)})
	if name == "systemctl" && r.kind == environments.ConnectionKindLocalPodman {
		return []byte("256"), nil
	}
	engine := "docker"
	command := args
	if r.kind == environments.ConnectionKindLocalPodman {
		engine = "podman"
		if len(args) < 3 || args[0] != "--remote=false" || args[1] != "--cgroup-manager=systemd" {
			return nil, errors.New("unexpected Podman transport")
		}
		command = args[2:]
	}
	if name != engine || len(command) == 0 {
		return nil, errors.New("unexpected engine command")
	}
	switch command[0] {
	case "info":
		return []byte(strings.Replace(podmanInfoFixture, "5.2.0", "4.9.3", 1)), nil
	case "inspect":
		if !r.owned {
			return nil, errors.New("no such container")
		}
		return []byte(r.inspect), nil
	case "run":
		r.owned = true
		return []byte("owned-container\n"), nil
	case "exec":
		if !r.owned {
			return nil, errors.New("setup reached removed container")
		}
		return []byte("setup complete"), nil
	case "rm":
		if !reflect.DeepEqual(command, []string{"rm", "-f", "-v", "owned-container"}) {
			return nil, errors.New("cleanup targeted an unrelated resource")
		}
		if r.cleanupErr != nil {
			return nil, r.cleanupErr
		}
		r.owned = false
		return nil, nil
	default:
		return nil, errors.New("unexpected lifecycle mutation")
	}
}

func (r *deploymentImageRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}

func (r *deploymentImageRunner) RunWithIO(ctx context.Context, _ io.Reader, _, _ io.Writer, name string, args ...string) error {
	_, err := r.Run(ctx, name, args...)
	return err
}

func deploymentImageProvider(r *deploymentImageRunner) *LocalDockerProvider {
	if r.kind == environments.ConnectionKindLocalPodman {
		return NewLocalPodmanProvider(r)
	}
	return NewLocalDockerProvider(r)
}

func deploymentImageRequest(kind environments.ConnectionKind, image string) DeployRequest {
	q := podmanDeployForTest()
	q.Connection.Kind = kind
	if kind == environments.ConnectionKindLocalDocker {
		q.Environment.Container.RootlessSystemd = nil
	}
	q.Environment.Container.Image = "sha256:" + deploymentImageDigest
	q.Environment.Provisioning.Strategy.RegistryImage.Image = q.Environment.Container.Image
	q.Environment.Container.SetupCommands = []string{"echo setup"}
	q.Deployment.Build = &environments.ImageBuildResult{
		OperationID: "build-operation", ConnectionID: q.Connection.ID,
		ImageID: image, DefinitionDigest: strings.Repeat("a", 64), ContextDigest: strings.Repeat("b", 64),
		Product: environments.CommittedBuildSource{WorkspaceID: "product", WorkspaceGeneration: 1, Commit: strings.Repeat("c", 40)},
		Recipe: environments.CommittedBuildSource{WorkspaceID: "recipe", WorkspaceGeneration: 1, Commit: strings.Repeat("d", 40)},
		RecipeFile: "scripts/Containerfile", ProductResult: "authenticated-product-result",
	}
	return q
}

// Purpose: LocalDockerProvider.Inspect owns build-bound observed image admission
// for Docker and Podman. Prefix differences must not reject a full SHA-256 match,
// while names, truncated/malformed IDs and two equally invalid strings must not
// authorize runtime access. Runner-backed Inspect is the narrowest layer proving
// parsing, identity rejection, nil results and absence of lifecycle mutations.
func TestLocalDeploymentInspectBuildImageIdentity(t *testing.T) {
	for _, kind := range []environments.ConnectionKind{environments.ConnectionKindLocalPodman, environments.ConnectionKindLocalDocker} {
		t.Run(string(kind), func(t *testing.T) {
			type identityCase struct {
				name, receipt, observed string
				accept                  bool
			}
			cases := []identityCase{
				{"prefixed", "sha256:" + deploymentImageDigest, "sha256:" + deploymentImageDigest, true},
				{"bare-inspect", "sha256:" + deploymentImageDigest, deploymentImageDigest, true},
				{"bare-receipt", deploymentImageDigest, "sha256:" + deploymentImageDigest, true},
				{"both-bare", deploymentImageDigest, deploymentImageDigest, true},
				{"different-full-digest", "sha256:" + deploymentImageDigest, strings.Repeat("f", 64), false},
			}
			invalid := []string{"", deploymentImageDigest[:12], "sha256:" + deploymentImageDigest[:12], "localhost/untrusted:tag", "sha512:" + deploymentImageDigest, strings.Repeat("g", 64), strings.ToUpper(deploymentImageDigest), "sha256:sha256:" + deploymentImageDigest, " " + deploymentImageDigest, deploymentImageDigest + "\n"}
			for _, id := range invalid {
				cases = append(cases,
					identityCase{"invalid-inspect-" + id, "sha256:" + deploymentImageDigest, id, false},
					identityCase{"invalid-receipt-" + id, id, deploymentImageDigest, false},
					identityCase{"equally-invalid-" + id, id, id, false},
				)
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					r := &deploymentImageRunner{kind: kind, inspect: deploymentImageInspect(t, kind, tc.observed), owned: true, unrelated: true}
					q := deploymentImageRequest(kind, tc.receipt)
					q.Deployment.Runtime.ContainerID = "owned-container"
					before := *q.Deployment.Build
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					res, err := deploymentImageProvider(r).Inspect(ctx, q.Connection, q.Deployment)
					if tc.accept {
						if err != nil || res == nil || res.Status != environments.DeploymentStatusRunning || res.Runtime.ContainerID != "owned-container" || res.Runtime.Endpoint != "http://127.0.0.1:18080" {
							t.Fatalf("full identity match rejected: %+v %v", res, err)
						}
					} else if err == nil || !strings.Contains(err.Error(), "deployment image does not match authenticated build result") || res != nil {
						t.Fatalf("invalid identity admitted: %+v %v", res, err)
					}
					wantArgs := []string{"inspect", "owned-container"}
					if kind == environments.ConnectionKindLocalPodman {
						wantArgs = append([]string{"--remote=false", "--cgroup-manager=systemd"}, wantArgs...)
					}
					if len(r.calls) != 1 || !reflect.DeepEqual(r.calls[0].Args, wantArgs) || !r.owned || !r.unrelated || *q.Deployment.Build != before {
						t.Fatalf("inspection mutated state/provenance: %+v", r.calls)
					}
				})
			}
		})
	}
}

// Purpose: Deploy must feed the authenticated receipt through both post-run
// Inspect calls before accepting Podman or Docker runtime state. For Podman,
// identity/isolation rejection must prevent setup and remove only its new owned
// container. This provider-level integration test proves ordering and cleanup
// postconditions without a daemon, real engine, or external recipe.
func TestLocalDeploymentDeployBuildImageIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, receipt, observed, old, replacement, wantError string
		cleanupFailure                                     bool
	}{
		{name: "bare-inspect", receipt: "sha256:" + deploymentImageDigest, observed: deploymentImageDigest},
		{name: "prefixed-inspect", receipt: "sha256:" + deploymentImageDigest, observed: "sha256:" + deploymentImageDigest},
		{name: "bare-receipt", receipt: deploymentImageDigest, observed: "sha256:" + deploymentImageDigest},
		{name: "different-image", receipt: "sha256:" + deploymentImageDigest, observed: strings.Repeat("f", 64), wantError: "deployment image does not match authenticated build result"},
		{name: "empty-receipt", receipt: "", observed: deploymentImageDigest, wantError: "deployment image does not match authenticated build result"},
		{name: "malformed-receipt", receipt: "invalid", observed: deploymentImageDigest, wantError: "deployment image does not match authenticated build result"},
		{name: "short-inspect", receipt: "sha256:" + deploymentImageDigest, observed: deploymentImageDigest[:12], wantError: "deployment image does not match authenticated build result"},
		{name: "empty-inspect", receipt: "sha256:" + deploymentImageDigest, observed: "", wantError: "deployment image does not match authenticated build result"},
		{name: "tag-inspect", receipt: "sha256:" + deploymentImageDigest, observed: "localhost/untrusted:tag", wantError: "deployment image does not match authenticated build result"},
		{name: "equal-invalid", receipt: "invalid", observed: "invalid", wantError: "deployment image does not match authenticated build result"},
		{name: "public-port", receipt: "sha256:" + deploymentImageDigest, observed: deploymentImageDigest, old: "127.0.0.1", replacement: "0.0.0.0", wantError: "non-loopback"},
		{name: "host-cgroup", receipt: "sha256:" + deploymentImageDigest, observed: deploymentImageDigest, old: "private", replacement: "host", wantError: "required rootless_systemd isolation"},
		{name: "host-network", receipt: "sha256:" + deploymentImageDigest, observed: deploymentImageDigest, old: "slirp4netns", replacement: "host", wantError: "required rootless_systemd isolation"},
		{name: "privileged", receipt: "sha256:" + deploymentImageDigest, observed: deploymentImageDigest, old: `"Privileged":false`, replacement: `"Privileged":true`, wantError: "required rootless_systemd isolation"},
		{name: "host-mount", receipt: "sha256:" + deploymentImageDigest, observed: deploymentImageDigest, old: `"HostConfig":`, replacement: `"Mounts":[{"Type":"bind"}],"HostConfig":`, wantError: "unexpected host bind mount"},
		{name: "unbounded-pids", receipt: "sha256:" + deploymentImageDigest, observed: deploymentImageDigest, old: `"PidsLimit":1024`, replacement: `"PidsLimit":0`, wantError: "required rootless_systemd isolation"},
		{name: "no-systemd", receipt: "sha256:" + deploymentImageDigest, observed: deploymentImageDigest, old: `"SystemdMode":true`, replacement: `"SystemdMode":false`, wantError: "required rootless_systemd isolation"},
		{name: "cleanup-failure", receipt: "sha256:" + deploymentImageDigest, observed: strings.Repeat("f", 64), wantError: "owned container cleanup failed", cleanupFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind := environments.ConnectionKindLocalPodman
			inspect := deploymentImageInspect(t, kind, tc.observed)
			if tc.old != "" {
				inspect = strings.Replace(inspect, tc.old, tc.replacement, 1)
			}
			r := &deploymentImageRunner{kind: kind, inspect: inspect, unrelated: true}
			if tc.cleanupFailure {
				r.cleanupErr = errors.New("cleanup unavailable")
			}
			q := deploymentImageRequest(kind, tc.receipt)
			before := *q.Deployment.Build
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			res, err := deploymentImageProvider(r).Deploy(ctx, q)
			wantCommands := []string{"info", "systemctl", "inspect", "run", "inspect", "exec", "inspect"}
			if tc.wantError == "" {
				if err != nil || res == nil || res.Status != environments.DeploymentStatusRunning || res.Runtime.Endpoint != "http://127.0.0.1:18080" || !r.owned {
					t.Fatalf("matching managed deploy rejected: %+v %v", res, err)
				}
			} else {
				wantCommands = []string{"info", "systemctl", "inspect", "run", "inspect", "rm"}
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || res != nil || r.owned != tc.cleanupFailure {
					t.Fatalf("failed deploy accepted or cleanup untruthful: %+v %v owned=%v", res, err, r.owned)
				}
				if !tc.cleanupFailure && !strings.Contains(err.Error(), "owned container removed") {
					t.Fatalf("missing confirmed cleanup: %v", err)
				}
				if tc.cleanupFailure && strings.Contains(err.Error(), "owned container removed") {
					t.Fatalf("unconfirmed cleanup reported as removal: %v", err)
				}
			}
			var commands []string
			for _, c := range r.calls {
				if c.Name == "systemctl" {
					commands = append(commands, c.Name)
				} else {
					commands = append(commands, c.Args[2])
					if c.Args[2] == "run" {
						joined := strings.Join(c.Args, " ")
						for _, want := range []string{"--systemd=always", "--cgroupns=private", "--network=slirp4netns:allow_host_loopback=false", "--pids-limit 1024", "--http-proxy=false", "--pull=never", "--runtime=crun", q.Environment.Container.Image} {
							if !strings.Contains(joined, want) {
								t.Fatalf("lost runtime isolation/image argument %q: %v", want, c.Args)
							}
						}
						for _, bad := range []string{"--privileged", " -v ", "--network=host"} {
							if strings.Contains(joined, bad) {
								t.Fatalf("unsafe run arguments: %v", c.Args)
							}
						}
					}
				}
			}
			if !reflect.DeepEqual(commands, wantCommands) || !r.unrelated || *q.Deployment.Build != before || q.Deployment.Runtime.ContainerID != "" {
				t.Fatalf("unexpected lifecycle/provenance mutation: %v", commands)
			}
		})
	}
}

// Purpose: Deploy's existing-container reuse path must carry the receipt to
// Inspect, not bypass image identity by synthesizing a build-free deployment.
// A matching full ID reuses without setup; a mismatch is rejected without
// deleting a pre-existing Podman container. The provider runner is the narrowest
// layer exercising this branch and its no-mutation postcondition.
func TestLocalPodmanReuseBuildImageIdentity(t *testing.T) {
	for _, match := range []bool{true, false} {
		t.Run(map[bool]string{true: "match", false: "mismatch"}[match], func(t *testing.T) {
			kind := environments.ConnectionKindLocalPodman
			observed := deploymentImageDigest
			if !match {
				observed = strings.Repeat("f", 64)
			}
			r := &deploymentImageRunner{kind: kind, inspect: deploymentImageInspect(t, kind, observed), owned: true, unrelated: true}
			q := deploymentImageRequest(kind, "sha256:"+deploymentImageDigest)
			q.Environment.DeploymentPolicy.Reuse = true
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			res, err := deploymentImageProvider(r).Deploy(ctx, q)
			if match {
				if err != nil || res == nil || res.Runtime.ContainerID != "owned-container" {
					t.Fatalf("matching reuse rejected: %+v %v", res, err)
				}
			} else if err == nil || res != nil {
				t.Fatalf("mismatched reuse admitted: %+v %v", res, err)
			}
			for _, c := range r.calls {
				if c.Name != "systemctl" && c.Args[2] != "info" && c.Args[2] != "inspect" {
					t.Fatalf("reuse mutated existing resources: %+v", c)
				}
			}
			if !r.owned || !r.unrelated {
				t.Fatal("reuse removed existing containers")
			}
		})
	}
}

// Purpose: Docker shares Inspect identity validation but must not acquire the
// Podman rootless-systemd admission/transport contract. Real Deploy calls with
// Docker JSON verify unchanged setup/runtime behavior for a managed full digest,
// rejection of a different digest and the existing build-free image contract.
func TestLocalDockerDeployBuildImageIdentity(t *testing.T) {
	for _, name := range []string{"managed", "mismatch", "unmanaged", "reuse"} {
		t.Run(name, func(t *testing.T) {
			kind := environments.ConnectionKindLocalDocker
			observed := "sha256:" + deploymentImageDigest
			if name == "mismatch" {
				observed = "sha256:" + strings.Repeat("f", 64)
			}
			r := &deploymentImageRunner{kind: kind, inspect: deploymentImageInspect(t, kind, observed), unrelated: true, owned: name == "reuse"}
			q := deploymentImageRequest(kind, "sha256:"+deploymentImageDigest)
			if name == "unmanaged" {
				q.Deployment.Build = nil
				q.Environment.Container.Image = "alpine:fixed"
				q.Environment.Provisioning.Strategy.RegistryImage.Image = "alpine:fixed"
			}
			q.Environment.DeploymentPolicy.Reuse = name == "reuse"
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			res, err := deploymentImageProvider(r).Deploy(ctx, q)
			if name == "mismatch" {
				if err == nil || res != nil || !strings.Contains(err.Error(), "deployment image does not match authenticated build result") {
					t.Fatalf("Docker mismatch admitted: %+v %v", res, err)
				}
			} else if err != nil || res == nil || res.Runtime.Endpoint != "http://127.0.0.1:18080" {
				t.Fatalf("Docker lifecycle changed: %+v %v", res, err)
			}
			want := []string{"inspect", "run", "exec", "inspect"}
			if name == "reuse" {
				want = []string{"inspect", "inspect"}
			}
			var commands []string
			for _, c := range r.calls {
				if c.Name != "docker" || strings.Contains(strings.Join(c.Args, " "), "--systemd") {
					t.Fatalf("Docker used Podman contract: %+v", c)
				}
				commands = append(commands, c.Args[0])
			}
			if !reflect.DeepEqual(commands, want) || !r.owned || !r.unrelated {
				t.Fatalf("Docker lifecycle changed: %v", commands)
			}
		})
	}
}
