package environments

import (
	"strings"
	"testing"
)

// Purpose: Environment.Validate's frontend metadata boundary must reject URL
// injection, ambiguous identities and undeclared ports; the domain layer is the
// narrowest place to prove this independently of HTTP/provisioning side effects.
func TestFrontendEndpointValidation(t *testing.T) {
	valid := FrontendEndpoint{ID: "web", Name: "Web", ContainerPort: 8080, Scheme: "http", HealthPath: "/health"}
	ports := []PortMapping{{ContainerPort: 8080}}
	if err := ValidateFrontendEndpoints([]FrontendEndpoint{valid}, ports); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"https://example.invalid/", "//example.invalid", "/%2f%2fevil", "/a/../b", "/a?token=x", "/a#x", "/a\\b", "/a\n", "/user:password@host", strings.Repeat("/", 513)} {
		e := valid
		e.Path = bad
		if ValidateFrontendEndpoints([]FrontendEndpoint{e}, ports) == nil {
			t.Fatalf("unsafe path admitted: %q", bad)
		}
		e = valid
		e.HealthPath = bad
		if ValidateFrontendEndpoints([]FrontendEndpoint{e}, ports) == nil {
			t.Fatalf("unsafe health path admitted: %q", bad)
		}
	}
	for _, mutate := range []func(*FrontendEndpoint){
		func(e *FrontendEndpoint) { e.ID = "../id" },
		func(e *FrontendEndpoint) { e.Scheme = "javascript" },
		func(e *FrontendEndpoint) { e.HealthPath = "" },
		func(e *FrontendEndpoint) { e.Name = "\n" },
		func(e *FrontendEndpoint) { e.ContainerPort = 65536 },
		func(e *FrontendEndpoint) { e.ContainerPort = 9000 },
	} {
		e := valid
		mutate(&e)
		if ValidateFrontendEndpoints([]FrontendEndpoint{e}, ports) == nil {
			t.Fatalf("invalid metadata admitted: %+v", e)
		}
	}
	if ValidateFrontendEndpoints([]FrontendEndpoint{valid, valid}, ports) == nil || ValidateFrontendEndpoints(make([]FrontendEndpoint, 9), ports) == nil || ValidateFrontendEndpoints([]FrontendEndpoint{valid}, []PortMapping{{ContainerPort: 8080, Protocol: "udp"}}) == nil {
		t.Fatal("duplicate, excessive or UDP endpoints admitted")
	}
}

// Purpose: Environment.Clone must isolate static endpoint metadata from callers;
// a domain clone assertion proves edits cannot mutate stored definition slices.
func TestFrontendEndpointClone(t *testing.T) {
	e := &Environment{FrontendEndpoints: []FrontendEndpoint{{ID: "web", Path: "/"}}}
	clone := e.Clone()
	clone.FrontendEndpoints[0].Path = "/changed"
	if e.FrontendEndpoints[0].Path != "/" {
		t.Fatal("clone aliases frontend metadata")
	}
}
