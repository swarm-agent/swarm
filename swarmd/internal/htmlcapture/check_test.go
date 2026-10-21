package htmlcapture

// Purpose: CheckStandalone returns the browser's exception and load-failure
// details so a design repair can target them. Threats: unbounded or
// private-URL-bearing diagnostics, lost line/column, and a blank page treated
// as a pass. Owner: checkCollector.observe/list and blankViewport. Synthetic
// CDP events are the narrowest layer that covers the mapping and bounds
// without launching Chrome; real Chrome is exercised by the live design test.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
)

func TestCheckCollectorMapsAndBoundsDiagnostics(t *testing.T) {
	var c checkCollector
	c.observe(&cdpruntime.EventExceptionThrown{ExceptionDetails: &cdpruntime.ExceptionDetails{Text: "Uncaught", LineNumber: 9, ColumnNumber: 4, Exception: &cdpruntime.RemoteObject{Description: "ReferenceError: boom is not defined\n    at index.html:10:5"}}})
	c.observe(&network.EventRequestWillBeSent{RequestID: "r1", Request: &network.Request{URL: "https://fonts.example/css?family=Inter&key=secret"}})
	c.observe(&network.EventLoadingFailed{RequestID: "r1", ErrorText: "net::ERR_BLOCKED_BY_CLIENT"})
	got := c.list()
	if len(got) != 2 {
		t.Fatalf("diagnostics: %+v", got)
	}
	if got[0].Kind != "page_error" || got[0].Message != "ReferenceError: boom is not defined" || got[0].Line != 10 || got[0].Column != 5 {
		t.Fatalf("exception mapping: %+v", got[0])
	}
	if got[1].Kind != "failed_load" || got[1].URL != "https://fonts.example/css" {
		t.Fatalf("load failure mapping (query must be stripped): %+v", got[1])
	}
	long := string(bytes.Repeat([]byte("x"), 4000))
	for i := 0; i < 40; i++ {
		c.observe(&cdpruntime.EventExceptionThrown{ExceptionDetails: &cdpruntime.ExceptionDetails{Text: long, LineNumber: int64(i)}})
	}
	got = c.list()
	if len(got) != maxCheckDiagnostics+1 || got[len(got)-1].Kind != "truncated" {
		t.Fatalf("bound: %d diagnostics", len(got))
	}
	for _, d := range got {
		if len(d.Message) > maxCheckText+len("…") {
			t.Fatalf("message not clipped: %d bytes", len(d.Message))
		}
	}
}

func TestBlankViewport(t *testing.T) {
	encode := func(img image.Image) []byte {
		var b bytes.Buffer
		_ = png.Encode(&b, img)
		return b.Bytes()
	}
	flat := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			flat.Set(x, y, color.White)
		}
	}
	if !blankViewport(encode(flat)) {
		t.Fatalf("flat white page not reported blank")
	}
	drawn := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			if x < 60 {
				drawn.Set(x, y, color.Black)
			} else {
				drawn.Set(x, y, color.White)
			}
		}
	}
	if blankViewport(encode(drawn)) {
		t.Fatalf("drawn page reported blank")
	}
	if blankViewport([]byte("not a png")) {
		t.Fatalf("undecodable image reported blank")
	}
}
