package environments

import (
	"errors"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// FrontendEndpoint explicitly declares HTTP intent; exposed TCP ports alone do not.
type FrontendEndpoint struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContainerPort int    `json:"container_port"`
	Scheme        string `json:"scheme"`
	Path          string `json:"path,omitempty"`
	HealthPath    string `json:"health_path"`
}

var frontendID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// SafeFrontendPath deliberately excludes encoded/ambiguous URL syntax.
func SafeFrontendPath(p string) bool {
	return len(p) > 0 && len(p) <= 512 && strings.HasPrefix(p, "/") &&
		!strings.HasPrefix(p, "//") && !strings.ContainsAny(p, "%\\?#:@") &&
		utf8.ValidString(p) && strings.IndexFunc(p, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0 &&
		(path.Clean(p) == strings.TrimSuffix(p, "/") || p == "/")
}

func ValidateFrontendEndpoints(endpoints []FrontendEndpoint, ports []PortMapping) error {
	if len(endpoints) > 8 {
		return errors.New("at most eight frontend endpoints are allowed")
	}
	ids, usedPorts := map[string]bool{}, map[int]bool{}
	for _, e := range endpoints {
		p := e.Path
		if p == "" {
			p = "/"
		}
		if !frontendID.MatchString(e.ID) || ids[e.ID] || usedPorts[e.ContainerPort] ||
			e.ContainerPort < 1 || e.ContainerPort > 65535 ||
			strings.TrimSpace(e.Name) == "" || len(e.Name) > 128 || !utf8.ValidString(e.Name) ||
			strings.IndexFunc(e.Name, unicode.IsControl) >= 0 ||
			(e.Scheme != "http" && e.Scheme != "https") || !SafeFrontendPath(p) || !SafeFrontendPath(e.HealthPath) {
			return errors.New("invalid frontend endpoint identity, port, scheme or path")
		}
		matches := 0
		for _, port := range ports {
			if port.ContainerPort == e.ContainerPort && (port.Protocol == "" || port.Protocol == "tcp") {
				matches++
			}
		}
		if matches != 1 {
			return errors.New("frontend endpoint requires one declared TCP port")
		}
		ids[e.ID], usedPorts[e.ContainerPort] = true, true
	}
	return nil
}
