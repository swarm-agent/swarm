package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
)

const podmanInfoFixture = `{"version":{"Version":"5.2.0"},"host":{"os":"linux","arch":"amd64","security":{"rootless":true},"cgroupVersion":"v2","cgroupManager":"systemd","cgroupControllers":["cpu","memory","pids"],"ociRuntime":{"name":"crun"},"slirp4netns":{"executable":"/usr/bin/slirp4netns"}}}`
const podmanInspectFixture = `[{"Id":"owned-container","State":{"Running":true},"Config":{"SystemdMode":true},"HostConfig":{"Privileged":false,"CgroupMode":"private","CgroupManager":"systemd","NetworkMode":"slirp4netns","PidsLimit":1024},"NetworkSettings":{"Ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"18080"}]}}}]`

type podmanRunner struct {
	calls   []mockCall
	info    string
	inspect string
	run     bool
}

func (r *podmanRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, mockCall{name, append([]string(nil), args...)})
	if name == "systemctl" {
		return []byte("256"), nil
	}
	if name != "podman" || len(args) < 3 || args[0] != "--remote=false" || args[1] != "--cgroup-manager=systemd" {
		return nil, errors.New("wrong engine/transport")
	}
	switch args[2] {
	case "info":
		return []byte(r.info), nil
	case "inspect":
		if !r.run {
			return nil, errors.New("no such container")
		}
		return []byte(r.inspect), nil
	case "run":
		r.run = true
		return []byte("owned-container"), nil
	default:
		return nil, nil
	}
}
func (r *podmanRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}
func (r *podmanRunner) RunWithIO(ctx context.Context, _ io.Reader, _, _ io.Writer, name string, args ...string) error {
	_, err := r.Run(ctx, name, args...)
	return err
}
func podmanConnectionForTest(kind environments.ConnectionKind) *environments.Connection {
	return &environments.Connection{ID: "conn", AccountScopeID: "account", WorkspaceID: "workspace", Name: "Local engine", Kind: kind}
}
func podmanDeployForTest() DeployRequest {
	return DeployRequest{
		Connection: podmanConnectionForTest(environments.ConnectionKindLocalPodman),
		Environment: &environments.Environment{ID: "env", AccountScopeID: "account", WorkspaceID: "workspace", Name: "Systemd", Mode: environments.EnvironmentModeDeployable, Role: environments.EnvironmentRoleTesting,
			Container:    environments.ContainerDefinition{Image: "localhost/test:fixed", Command: []string{"/sbin/init"}, RootlessSystemd: &environments.RootlessSystemd{CgroupNamespace: "private", Network: "slirp4netns", PidsLimit: 1024}, ExposedPorts: []environments.PortMapping{{ContainerPort: 8080, HostPort: 18080}}, EnvVars: map[string]string{"LITERAL": "${HOST_SECRET}"}},
			Provisioning: environments.WorkspaceProvisioning{Strategy: environments.SourceStrategy{Kind: environments.SourceStrategyKindRegistryImage, RegistryImage: &environments.RegistryImageConfig{Image: "localhost/test:fixed", PullPolicy: "never"}}}},
		Deployment: &environments.Deployment{ID: "dep", Name: "Deployment", EnvironmentID: "env", ConnectionID: "conn", AccountScopeID: "account", WorkspaceID: "workspace", Status: environments.DeploymentStatusPending},
	}
}

// Purpose: LocalPodmanProvider capability discovery must reject unsupported,
// rootful, remote, malformed or incomplete hosts before mutation. The injected
// command runner proves the admission boundary without changing a real host.
func TestLocalPodmanCapabilityAdmission(t *testing.T) {
	for _, tc := range []struct{ name, old, new string }{
		{"valid", "", ""}, {"rootful", "\"rootless\":true", "\"rootless\":false"},
		{"remote", "\"os\":\"linux\"", "\"os\":\"linux\",\"serviceIsRemote\":true"},
		{"cgroup-v1", "\"v2\"", "\"v1\""}, {"cgroupfs", "\"systemd\"", "\"cgroupfs\""},
		{"no-pids", "\"pids\"", "\"io\""}, {"no-cpu", "\"cpu\"", "\"io\""}, {"no-memory", "\"memory\"", "\"io\""},
		{"no-runtime", "\"crun\"", "\"\""}, {"no-network", "/usr/bin/slirp4netns", ""}, {"no-version", "5.2.0", ""},
		{"malformed", podmanInfoFixture, "{}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &podmanRunner{info: strings.Replace(podmanInfoFixture, tc.old, tc.new, 1)}
			p := NewLocalPodmanProvider(r)
			caps, err := p.Capabilities(context.Background(), podmanConnectionForTest(environments.ConnectionKindLocalPodman))
			if tc.name == "valid" {
				if err != nil || !caps.SupportsPodman || !caps.RootlessSystemd || caps.SupportsDocker {
					t.Fatalf("%+v %v", caps, err)
				}
			} else if err == nil || caps != (environments.ConnectionCapabilities{}) {
				t.Fatalf("unsupported host accepted: %+v %v", caps, err)
			}
			wantCalls := 1
			if tc.name == "valid" {
				wantCalls = 2
			}
			if len(r.calls) != wantCalls || r.calls[0].Args[2] != "info" {
				t.Fatalf("probe mutated host: %#v", r.calls)
			}
		})
	}
}

// Purpose: Deploy must emit only the closed isolation configuration and preserve
// literal container environment values and loopback publication. Local provider
// argv plus observed inspect postconditions prove the narrow launch contract.
func TestLocalPodmanDeployIsolation(t *testing.T) {
	t.Setenv("HOST_SECRET", "must-not-import")
	r := &podmanRunner{info: podmanInfoFixture, inspect: podmanInspectFixture}
	p := NewLocalPodmanProvider(r)
	result, err := p.Deploy(context.Background(), podmanDeployForTest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Runtime.Endpoint != "http://127.0.0.1:18080" {
		t.Fatalf("%+v", result)
	}
	var args []string
	for _, c := range r.calls {
		if c.Args[2] == "run" {
			args = c.Args
		}
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--remote=false", "--cgroup-manager=systemd", "--systemd=always", "--cgroupns=private", "--network=slirp4netns:allow_host_loopback=false", "--pids-limit 1024", "--http-proxy=false", "--pull=never", "-p 127.0.0.1:18080:8080/tcp", "LITERAL=${HOST_SECRET}"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
	for _, bad := range []string{"--privileged", " -v ", "must-not-import", "--network=host"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("unsafe args: %s", joined)
		}
	}
}

// Purpose: Invalid runtime combinations must fail before even probing or touching
// containers; cached capability claims cannot authorize a different engine.
func TestLocalPodmanRejectsUnsafeDefinition(t *testing.T) {
	for _, mutate := range []func(*DeployRequest){
		func(q *DeployRequest) { q.Environment.Container.Privileged = true },
		func(q *DeployRequest) { q.Environment.Container.RootlessSystemd.PidsLimit = 0 },
		func(q *DeployRequest) { q.Environment.Container.RootlessSystemd.Network = "host" },
		func(q *DeployRequest) { q.Environment.Container.RootlessSystemd.CgroupNamespace = "host" },
		func(q *DeployRequest) {
			q.Environment.Provisioning.Mounts = []environments.AdditionalMount{{HostPath: "/host", ContainerPath: "/host"}}
		},
		func(q *DeployRequest) { q.Environment.Container.RootlessSystemd = nil },
		func(q *DeployRequest) {
			q.Connection.LocalDocker = &environments.LocalDockerConfig{Host: "tcp://remote:2375"}
		},
		func(q *DeployRequest) { q.Connection.Kind = environments.ConnectionKindLocalDocker },
		func(q *DeployRequest) { q.Environment.Provisioning.Strategy.RegistryImage.PullPolicy = "always" },
	} {
		q := podmanDeployForTest()
		mutate(&q)
		r := &podmanRunner{info: podmanInfoFixture}
		_, err := NewLocalPodmanProvider(r).Deploy(context.Background(), q)
		if err == nil || len(r.calls) != 0 {
			t.Fatalf("unsafe request reached engine: %v %#v", err, r.calls)
		}
	}
}

// Purpose: Inspect and reuse must reject unexpected public networking, host
// mounts and missing cgroup/PID isolation instead of returning cached access.
func TestLocalPodmanObservedIsolation(t *testing.T) {
	for _, tc := range []struct{ old, new string }{
		{"127.0.0.1", "0.0.0.0"}, {"private", "host"}, {"slirp4netns", "host"}, {"1024", "0"}, {"\"Privileged\":false", "\"Privileged\":true"},
		{"\"HostConfig\"", "\"Mounts\":[{\"Type\":\"bind\"}],\"HostConfig\""},
	} {
		r := &podmanRunner{info: podmanInfoFixture, inspect: strings.Replace(podmanInspectFixture, tc.old, tc.new, 1), run: true}
		q := podmanDeployForTest()
		q.Deployment.Runtime.ContainerID = "owned-container"
		p := NewLocalPodmanProvider(r)
		if _, err := p.ResolveAccess(context.Background(), q.Connection, q.Deployment); err == nil {
			t.Fatal("unsafe cached access accepted")
		}
		if err := p.Start(context.Background(), q.Connection, q.Deployment); err == nil {
			t.Fatal("unsafe start accepted")
		}
		for _, c := range r.calls {
			if c.Name != "systemctl" && c.Args[2] != "info" && c.Args[2] != "inspect" {
				t.Fatalf("unsafe mutation: %#v", c)
			}
		}
	}
}

// Purpose: the shared DTO JSON/clone contract must retain all explicit runtime
// controls independently; validation must not silently discard unknown values.
func TestRootlessSystemdRoundTrip(t *testing.T) {
	q := podmanDeployForTest()
	b, err := json.Marshal(q.Environment)
	if err != nil {
		t.Fatal(err)
	}
	var got environments.Environment
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&got, q.Environment) {
		t.Fatal("round trip lost runtime")
	}
	clone := got.Clone()
	clone.Container.RootlessSystemd.PidsLimit = 12
	if got.Container.RootlessSystemd.PidsLimit != 1024 {
		t.Fatal("clone aliases runtime")
	}
}

// Purpose: failed observed isolation after container creation must clean up only
// the new container and never run setup. The provider runner records actual
// command ordering; no live engine or host configuration is involved.
func TestLocalPodmanFailedIsolationCleanup(t *testing.T) {
	r := &podmanRunner{info: podmanInfoFixture, inspect: strings.Replace(podmanInspectFixture, "127.0.0.1", "0.0.0.0", 1)}
	q := podmanDeployForTest()
	q.Environment.Container.SetupCommands = []string{"echo setup"}
	if _, err := NewLocalPodmanProvider(r).Deploy(context.Background(), q); err == nil {
		t.Fatal("unsafe deploy accepted")
	}
	removed := false
	for _, c := range r.calls {
		if c.Name != "podman" {
			continue
		}
		if c.Args[2] == "exec" {
			t.Fatal("setup ran before verification")
		}
		if c.Args[2] == "rm" {
			removed = true
			if c.Args[len(c.Args)-1] != "owned-container" {
				t.Fatalf("removed unrelated resource: %v", c)
			}
		}
	}
	if !removed {
		t.Fatal("failed container not cleaned up")
	}
}
