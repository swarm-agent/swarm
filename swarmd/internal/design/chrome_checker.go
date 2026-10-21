package design

import (
	"context"
	"errors"
	"time"

	"swarm/packages/swarmd/internal/htmlcapture"
)

// StandaloneChecker is the browser check the Chrome checker uses;
// *htmlcapture.ChromedpRenderer implements it.
type StandaloneChecker interface {
	CheckStandalone(ctx context.Context, req htmlcapture.StandaloneRequest) (htmlcapture.CheckResult, error)
}

// ChromeChecker checks a design in the daemon's sandboxed headless Chrome.
type ChromeChecker struct {
	Browser StandaloneChecker
	Now     func() time.Time
}

// Check loads the page once. A page problem is CheckErrors with its
// diagnostics; a browser that cannot run is CheckUnavailable, never a pass.
func (c ChromeChecker) Check(ctx context.Context, html []byte) CheckReport {
	now := c.Now
	if now == nil {
		now = time.Now
	}
	started := now()
	report := func(status CheckStatus, code string, diags []Diagnostic) CheckReport {
		return CheckReport{Status: status, Code: code, Diagnostics: diags, DurationMS: now().Sub(started).Milliseconds()}
	}
	if c.Browser == nil {
		return report(CheckUnavailable, "browser_not_configured", nil)
	}
	result, err := c.Browser.CheckStandalone(ctx, htmlcapture.StandaloneRequest{HTML: html})
	diags := convertDiagnostics(result.Diagnostics)
	if err != nil {
		var se *htmlcapture.StandaloneError
		if !errors.As(err, &se) {
			return report(CheckUnavailable, "browser_failed", nil)
		}
		switch {
		case se.FailureClass == "content":
			return report(CheckErrors, se.Code, diags)
		case se.Code == "standalone_timeout":
			// A page that never finishes loading is something a repair can fix.
			return report(CheckErrors, se.Code, append(diags, Diagnostic{Kind: "load_timeout", Message: "the page did not finish loading in time; look for endless loops or work that blocks loading"}))
		case se.Code == "standalone_input_invalid":
			return report(CheckErrors, se.Code, []Diagnostic{{Kind: "invalid_document", Message: "the document is empty, not UTF-8, contains NUL bytes or is over 4 MiB"}})
		default:
			return report(CheckUnavailable, se.Code, nil)
		}
	}
	for _, d := range diags {
		if d.Kind == "blank_page" {
			return report(CheckErrors, "blank_page", diags)
		}
	}
	return report(CheckOK, "", nil)
}

func convertDiagnostics(in []htmlcapture.CheckDiagnostic) []Diagnostic {
	out := make([]Diagnostic, 0, len(in))
	for _, d := range in {
		out = append(out, Diagnostic{Kind: d.Kind, Message: d.Message, URL: d.URL, Line: d.Line, Column: d.Column})
	}
	return out
}
