package privacy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var credentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*bearer\s+)([^\s"'\\]+)`),
	regexp.MustCompile(`(?i)([?&](?:key|api[_-]?key|google[_-]?api[_-]?key|x-goog-api-key|access[_-]?token|refresh[_-]?token|id[_-]?token|token)=)([^&\s"'\\]+)`),
	regexp.MustCompile(`(?i)\b((?:api[_-]?key|google[_-]?api[_-]?key|x-goog-api-key|access[_-]?token|refresh[_-]?token|id[_-]?token|token)["']?\s*[:=]\s*["']?)([^"',\s}\\]+)`),
	regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{20,}\b`),
}
var diagnosticURL = regexp.MustCompile(`(?i)https?://[^\s"'<>\\]+`)

// SanitizeDiagnostic is for errors and diagnostics, not resource URLs. Drop entire
// URLs: signed credentials can occur in paths as well as arbitrary query fields.
func SanitizeDiagnostic(raw string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, value := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret), url.QueryEscape(url.QueryEscape(secret))} {
			raw = strings.ReplaceAll(raw, value, "[REDACTED]")
		}
	}
	raw = diagnosticURL.ReplaceAllString(raw, "[REDACTED URL]")
	for _, pattern := range credentialPatterns {
		if pattern.NumSubexp() > 0 {
			raw = pattern.ReplaceAllString(raw, `${1}[REDACTED]`)
		} else {
			raw = pattern.ReplaceAllString(raw, `[REDACTED]`)
		}
	}
	return raw
}

// SafeError deliberately retains no original error or Unwrap path. Only safe
// cancellation/deadline and network retry classification survive the boundary.
func SafeError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	safe := &diagnosticError{message: SanitizeDiagnostic(err.Error(), secrets...), canceled: errors.Is(err, context.Canceled), deadline: errors.Is(err, context.DeadlineExceeded)}
	var network net.Error
	if errors.As(err, &network) {
		safe.timeout = network.Timeout()
		safe.temporary = network.Temporary()
	}
	return safe
}

type diagnosticError struct {
	message                                string
	canceled, deadline, timeout, temporary bool
}

func (e *diagnosticError) Error() string { return e.message }
func (e *diagnosticError) Is(target error) bool {
	return (target == context.Canceled && e.canceled) || (target == context.DeadlineExceeded && e.deadline)
}
func (e *diagnosticError) Timeout() bool   { return e.timeout || e.deadline }
func (e *diagnosticError) Temporary() bool { return e.temporary }

// GoogleMediaClient refuses redirects rather than allowing net/http to forward
// the nonstandard API-key header (or an upload body) to a different origin.
func GoogleMediaClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	copy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 || len(via) >= 10 || !sameOrigin(req.URL, via[0].URL) {
			return errors.New("Google media redirect rejected")
		}
		if client.CheckRedirect != nil {
			if err := client.CheckRedirect(req, via); err != nil {
				return err
			}
			if !sameOrigin(req.URL, via[0].URL) {
				return errors.New("Google media redirect rejected")
			}
		}
		return nil
	}
	return &copy
}

func sameOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && a.User == nil && b.User == nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// GoogleMediaURL restricts provider-returned download/upload URLs to the exact
// configured API origin. A configured loopback test origin is not a global allowlist.
func GoogleMediaURL(base, reference string) (string, error) {
	origin, err := url.Parse(base)
	if err != nil || origin.Host == "" || origin.User != nil {
		return "", errors.New("invalid Google media origin")
	}
	target, err := url.Parse(reference)
	if err != nil {
		return "", errors.New("invalid Google media resource URL")
	}
	target = origin.ResolveReference(target)
	if !sameOrigin(target, origin) {
		return "", errors.New("untrusted Google media resource origin")
	}
	return target.String(), nil
}
