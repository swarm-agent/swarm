package design

// Purpose: the browser check decides whether a design passes, gets its one
// repair, or is reported as unverified. Threat: a browser that cannot run, or
// an unclassified failure, turning into a pass or a token-spending repair.
// Owner: ChromeChecker.Check maps htmlcapture.CheckStandalone outcomes; a fake
// browser is the narrowest layer that covers every failure class without
// Chrome (live Chrome behavior is proven in the live design test).

import (
	"context"
	"errors"
	"testing"

	"swarm/packages/swarmd/internal/htmlcapture"
)

type fakeBrowser struct {
	result htmlcapture.CheckResult
	err    error
}

func (f fakeBrowser) CheckStandalone(context.Context, htmlcapture.StandaloneRequest) (htmlcapture.CheckResult, error) {
	return f.result, f.err
}

func TestChromeCheckerClassifiesEveryOutcome(t *testing.T) {
	pageError := []htmlcapture.CheckDiagnostic{{Kind: "page_error", Message: "ReferenceError: x is not defined", Line: 3, Column: 5}}
	cases := []struct {
		name    string
		browser StandaloneChecker
		want    CheckStatus
		diags   int
	}{
		{"clean", fakeBrowser{}, CheckOK, 0},
		{"blank", fakeBrowser{result: htmlcapture.CheckResult{Diagnostics: []htmlcapture.CheckDiagnostic{{Kind: "blank_page"}}}}, CheckErrors, 1},
		{"content error", fakeBrowser{result: htmlcapture.CheckResult{Diagnostics: pageError}, err: &htmlcapture.StandaloneError{Code: "standalone_runtime_exception", FailureClass: "content"}}, CheckErrors, 1},
		{"timeout is repairable", fakeBrowser{err: &htmlcapture.StandaloneError{Code: "standalone_timeout", FailureClass: "infrastructure"}}, CheckErrors, 1},
		{"browser unavailable", fakeBrowser{err: &htmlcapture.StandaloneError{Code: "standalone_browser_unavailable", FailureClass: "infrastructure"}}, CheckUnavailable, 0},
		{"browser failed", fakeBrowser{err: &htmlcapture.StandaloneError{Code: "standalone_browser_failed", FailureClass: "infrastructure"}}, CheckUnavailable, 0},
		{"cancelled", fakeBrowser{err: &htmlcapture.StandaloneError{Code: "standalone_cancelled", FailureClass: "infrastructure"}}, CheckUnavailable, 0},
		{"unclassified error", fakeBrowser{err: errors.New("boom")}, CheckUnavailable, 0},
		{"no browser", nil, CheckUnavailable, 0},
	}
	for _, tc := range cases {
		got := ChromeChecker{Browser: tc.browser}.Check(context.Background(), []byte(goodHTML))
		if got.Status != tc.want || len(got.Diagnostics) != tc.diags {
			t.Fatalf("%s: got %s with %d diagnostics, want %s with %d", tc.name, got.Status, len(got.Diagnostics), tc.want, tc.diags)
		}
	}
	got := ChromeChecker{Browser: fakeBrowser{result: htmlcapture.CheckResult{Diagnostics: pageError}, err: &htmlcapture.StandaloneError{Code: "standalone_runtime_exception", FailureClass: "content"}}}.Check(context.Background(), []byte(goodHTML))
	if d := got.Diagnostics[0]; d.Message != pageError[0].Message || d.Line != 3 || d.Column != 5 {
		t.Fatalf("diagnostic detail lost: %+v", d)
	}
}
