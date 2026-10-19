package htmlcapture

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
)

// Bounds for what a check returns.
const (
	maxCheckDiagnostics = 20
	maxCheckText        = 512
)

// CheckDiagnostic is one problem the browser reported while loading a page.
type CheckDiagnostic struct {
	Kind    string // page_error, failed_load, security, blank_page
	Message string
	URL     string
	Line    int // 1-based; 0 when unknown
	Column  int // 1-based; 0 when unknown
}

// CheckResult is one standalone check.
type CheckResult struct {
	PNG         []byte
	Diagnostics []CheckDiagnostic
}

// CheckStandalone loads one standalone document exactly like CaptureStandalone
// and also returns what went wrong: exception messages with their line and
// column, failed or blocked loads with their URL, and a blank first viewport.
//
// Unlike CaptureStandalone, authored exception text is returned. It is meant
// only for the same account's design repair and the user's own results; never
// log it or put it in events. A *StandaloneError with FailureClass
// "infrastructure" means the check could not judge the page.
func (r *ChromedpRenderer) CheckStandalone(ctx context.Context, req StandaloneRequest) (CheckResult, error) {
	if len(req.HTML) == 0 || len(req.HTML) > MaxStandaloneHTMLBytes || !utf8.Valid(req.HTML) || bytes.IndexByte(req.HTML, 0) >= 0 {
		return CheckResult{}, &StandaloneError{Code: "standalone_input_invalid", FailureClass: "input"}
	}
	var collect checkCollector
	results, err := r.captureObserved(ctx, Request{Entry: "index.html", Files: map[string][]byte{"index.html": bytes.Clone(req.HTML)}, StateIDs: []string{"standalone"}, ViewportWidth: req.ViewportWidth, ViewportHeight: req.ViewportHeight}, true, collect.observe)
	out := CheckResult{Diagnostics: collect.list()}
	if err != nil {
		failure := standaloneFailure(ctx, err)
		var se *StandaloneError
		if errors.As(failure, &se) && se.FailureClass == "content" && len(out.Diagnostics) == 0 {
			out.Diagnostics = []CheckDiagnostic{{Kind: strings.TrimPrefix(se.Code, "standalone_"), Message: checkCodeMessage(se.Code)}}
		}
		return out, failure
	}
	out.PNG = results[0].PNG
	if blankViewport(out.PNG) {
		out.Diagnostics = append(out.Diagnostics, CheckDiagnostic{Kind: "blank_page", Message: "the first screen renders as a single flat color: nothing visible was drawn"})
	}
	return out, nil
}

func checkCodeMessage(code string) string {
	switch code {
	case "standalone_not_ready":
		return "the page never finished loading: no <body>, or fonts or images failed to load"
	case "standalone_security_violation":
		return "the page made a prohibited request (external resource, navigation, popup or download)"
	case "standalone_runtime_exception":
		return "the page threw a JavaScript error"
	}
	return code
}

// checkCollector records bounded diagnostics from CDP target events.
type checkCollector struct {
	mu       sync.Mutex
	urls     map[network.RequestID]string
	items    []CheckDiagnostic
	overflow bool
}

func (c *checkCollector) add(d CheckDiagnostic) {
	if len(c.items) >= maxCheckDiagnostics {
		c.overflow = true
		return
	}
	d.Message = clip(d.Message)
	d.URL = clip(stripQuery(d.URL))
	for _, have := range c.items {
		if have == d {
			return
		}
	}
	c.items = append(c.items, d)
}

func (c *checkCollector) observe(ev any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch event := ev.(type) {
	case *cdpruntime.EventExceptionThrown:
		details := event.ExceptionDetails
		if details == nil {
			return
		}
		message := details.Text
		if details.Exception != nil && details.Exception.Description != "" {
			message = firstLine(details.Exception.Description)
		}
		c.add(CheckDiagnostic{Kind: "page_error", Message: message, Line: int(details.LineNumber) + 1, Column: int(details.ColumnNumber) + 1})
	case *network.EventRequestWillBeSent:
		if event.Request != nil {
			if c.urls == nil {
				c.urls = map[network.RequestID]string{}
			}
			if len(c.urls) < 1024 {
				c.urls[event.RequestID] = event.Request.URL
			}
		}
	case *network.EventLoadingFailed:
		reason := event.ErrorText
		if event.BlockedReason != "" {
			reason = "blocked: " + string(event.BlockedReason)
		}
		c.add(CheckDiagnostic{Kind: "failed_load", Message: "a resource failed to load (" + reason + "); the page must not load anything external", URL: c.urls[event.RequestID]})
	case *log.EventEntryAdded:
		if event.Entry != nil && event.Entry.Source == log.SourceSecurity {
			c.add(CheckDiagnostic{Kind: "security", Message: event.Entry.Text, URL: event.Entry.URL})
		}
	}
}

func (c *checkCollector) list() []CheckDiagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]CheckDiagnostic(nil), c.items...)
	if c.overflow {
		out = append(out, CheckDiagnostic{Kind: "truncated", Message: "more problems were found than are listed"})
	}
	return out
}

// blankViewport reports a first screen that is at least 99.8% one color.
func blankViewport(data []byte) bool {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return false
	}
	bounds := img.Bounds()
	total := bounds.Dx() * bounds.Dy()
	if total == 0 {
		return false
	}
	ref := img.At(bounds.Min.X, bounds.Min.Y)
	differ := 0
	limit := total / 500 // 0.2%
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 2 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 2 {
			if !sameColor(img, x, y, ref) {
				differ += 4
				if differ > limit {
					return false
				}
			}
		}
	}
	return true
}

func sameColor(img image.Image, x, y int, ref interface{ RGBA() (r, g, b, a uint32) }) bool {
	r1, g1, b1, a1 := img.At(x, y).RGBA()
	r2, g2, b2, a2 := ref.RGBA()
	return r1 == r2 && g1 == g2 && b1 == b2 && a1 == a2
}

func clip(s string) string {
	if len(s) <= maxCheckText {
		return s
	}
	cut := maxCheckText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func stripQuery(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i]
	}
	return u
}
