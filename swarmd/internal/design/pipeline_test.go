package design

// Purpose: swarm_design promises one model call per design, one browser check,
// and at most one repair that applies exact edits, with results kept honest.
// Threats: hidden retry loops that spend tokens, a repair that silently
// rewrites or half-applies, an unavailable check reported as a pass, an edit
// that overwrites a file changed underneath it, and invented usage numbers.
// Owner: design.Pipeline (Run, createOne, repair, editOne, finish) and
// ApplyEdits. The pipeline is the narrowest layer that owns these decisions;
// the model, browser and filesystem are injected, so fakes prove the control
// flow without a provider or Chrome.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const goodHTML = "<!doctype html><html><head><title>t</title></head><body><main id=\"a\">ok</main></body></html>"
const brokenHTML = "<!doctype html><html><head><title>t</title></head><body><main id=\"a\">ok</main><script>boom()</script></body></html>"

type fakeModel struct {
	mu       sync.Mutex
	calls    []ModelCall
	reply    func(ModelCall) (string, error)
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	delay    time.Duration
}

func (m *fakeModel) Info() ModelInfo { return ModelInfo{Provider: "fake", Model: "fake-designer"} }

func (m *fakeModel) Complete(ctx context.Context, c ModelCall) (ModelReply, error) {
	n := m.inFlight.Add(1)
	defer m.inFlight.Add(-1)
	for {
		seen := m.maxSeen.Load()
		if n <= seen || m.maxSeen.CompareAndSwap(seen, n) {
			break
		}
	}
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	m.mu.Lock()
	m.calls = append(m.calls, c)
	m.mu.Unlock()
	text, err := m.reply(c)
	if err != nil {
		return ModelReply{}, err
	}
	cost := 0.001
	return ModelReply{Text: text, Usage: CallUsage{Total: 100, Input: 80, Output: 20, CostUSD: &cost, Reported: true}}, nil
}

func (m *fakeModel) phases() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for _, c := range m.calls {
		out[c.Phase]++
	}
	return out
}

// fakeChecker reports an error for any document containing boom().
type fakeChecker struct {
	calls       atomic.Int32
	unavailable bool
}

func (c *fakeChecker) Check(_ context.Context, html []byte) CheckReport {
	c.calls.Add(1)
	if c.unavailable {
		return CheckReport{Status: CheckUnavailable, Code: "browser_unavailable"}
	}
	if strings.Contains(string(html), "boom()") {
		return CheckReport{Status: CheckErrors, Diagnostics: []Diagnostic{{Kind: "page_error", Message: "ReferenceError: boom is not defined", Line: 1}}}
	}
	return CheckReport{Status: CheckOK}
}

type fakeFiles struct {
	mu       sync.Mutex
	files    map[string][]byte
	replaces int
	// beforeReplace runs just before ReplaceIfDigest compares digests.
	beforeReplace func(map[string][]byte)
}

func newFakeFiles() *fakeFiles { return &fakeFiles{files: map[string][]byte{}} }

func (f *fakeFiles) Read(_ context.Context, rel string) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[rel]
	if !ok {
		return nil, "", errors.New("not found")
	}
	return append([]byte(nil), b...), digest(b), nil
}

func (f *fakeFiles) CreateExclusive(_ context.Context, rel string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.files[rel]; ok {
		return errors.New("exists")
	}
	f.files[rel] = append([]byte(nil), data...)
	return nil
}

func (f *fakeFiles) WriteResults(_ context.Context, rel string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[rel] = append([]byte(nil), data...)
	return nil
}

func (f *fakeFiles) ReplaceIfDigest(_ context.Context, rel, want string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeReplace != nil {
		f.beforeReplace(f.files)
	}
	if digest(f.files[rel]) != want {
		return ErrStaleBase
	}
	f.replaces++
	f.files[rel] = append([]byte(nil), data...)
	return nil
}

func fenced(html string) string { return "Here:\n```html\n" + html + "\n```" }

func editsJSON(edits ...Edit) string {
	b, _ := json.Marshal(map[string]any{"edits": edits})
	return "```json\n" + string(b) + "\n```"
}

func run(t *testing.T, p *Pipeline, req Request) BatchResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := p.Run(ctx, req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestApplyEditsRequiresUniqueNonOverlappingMatches(t *testing.T) {
	src := "<p>a</p><p>a</p><p>b</p>"
	cases := []struct {
		name  string
		edits []Edit
		want  error
	}{
		{"missing", []Edit{{Old: "<p>z</p>", New: "x"}}, ErrEditNotFound},
		{"ambiguous", []Edit{{Old: "<p>a</p>", New: "x"}}, ErrEditAmbiguous},
		{"overlap", []Edit{{Old: "<p>a</p><p>b", New: "x"}, {Old: "a</p><p>b</p>", New: "y"}}, ErrEditOverlap},
	}
	for _, tc := range cases {
		out, err := ApplyEdits(src, tc.edits)
		if !errors.Is(err, tc.want) || out != "" {
			t.Fatalf("%s: got %q, %v; want %v and no output", tc.name, out, err, tc.want)
		}
	}
	out, err := ApplyEdits(src, []Edit{{Old: "<p>b</p>", New: "<p>c</p>"}, {Old: "<p>a</p><p>a", New: "<p>x</p><p>a"}})
	if err != nil || out != "<p>x</p><p>a</p><p>c</p>" {
		t.Fatalf("valid edits: got %q, %v", out, err)
	}
	if _, err := ParseEdits(`{"edits":[]}`); !errors.Is(err, ErrEditsEmpty) {
		t.Fatalf("empty edits accepted: %v", err)
	}
	if _, err := ParseEdits("not json"); !errors.Is(err, ErrEditsMalformed) {
		t.Fatalf("malformed edits accepted: %v", err)
	}
}

func TestItemLifecycleExactCallCounts(t *testing.T) {
	// passed: 1 generate + 1 check, no repair.
	m := &fakeModel{reply: func(ModelCall) (string, error) { return fenced(goodHTML), nil }}
	c := &fakeChecker{}
	f := newFakeFiles()
	res := run(t, &Pipeline{Model: m, Checker: c, Files: f}, Request{Brief: "x", Count: 1, Folder: "designs/b"})
	if res.Items[0].Status != StatusPassed || m.phases()["generate"] != 1 || m.phases()["repair"] != 0 || c.calls.Load() != 1 {
		t.Fatalf("passed: %+v phases=%v checks=%d", res.Items[0], m.phases(), c.calls.Load())
	}
	if string(f.files["designs/b/01.html"]) != goodHTML || res.Items[0].SHA256 != digest([]byte(goodHTML)) {
		t.Fatalf("passed file not written exactly")
	}

	// repaired: 1 generate + 1 repair, 2 checks.
	m = &fakeModel{reply: func(c ModelCall) (string, error) {
		if c.Phase == "repair" {
			return editsJSON(Edit{Old: "<script>boom()</script>", New: ""}), nil
		}
		return fenced(brokenHTML), nil
	}}
	c, f = &fakeChecker{}, newFakeFiles()
	res = run(t, &Pipeline{Model: m, Checker: c, Files: f}, Request{Brief: "x", Count: 1, Folder: "d"})
	if res.Items[0].Status != StatusRepaired || m.phases()["repair"] != 1 || c.calls.Load() != 2 || string(f.files["d/01.html"]) != goodHTML {
		t.Fatalf("repaired: %+v phases=%v checks=%d file=%q", res.Items[0], m.phases(), c.calls.Load(), f.files["d/01.html"])
	}

	// still failing after the one repair: failed, file kept, no third call.
	m = &fakeModel{reply: func(c ModelCall) (string, error) {
		if c.Phase == "repair" {
			return editsJSON(Edit{Old: "<title>t</title>", New: "<title>u</title>"}), nil
		}
		return fenced(brokenHTML), nil
	}}
	c, f = &fakeChecker{}, newFakeFiles()
	res = run(t, &Pipeline{Model: m, Checker: c, Files: f}, Request{Brief: "x", Count: 1, Folder: "d"})
	item := res.Items[0]
	if item.Status != StatusFailed || len(m.calls) != 2 || c.calls.Load() != 2 || item.CheckAfter == nil || item.CheckAfter.Status != CheckErrors {
		t.Fatalf("failed: %+v calls=%d checks=%d", item, len(m.calls), c.calls.Load())
	}
	if !strings.Contains(string(f.files["d/01.html"]), "boom()") {
		t.Fatalf("failed design was not kept with its errors")
	}
}

func TestRepairAmbiguousEditRefusedWithoutPartialApply(t *testing.T) {
	doubled := strings.Replace(brokenHTML, "<main id=\"a\">ok</main>", "<p>x</p><p>x</p>", 1)
	m := &fakeModel{reply: func(c ModelCall) (string, error) {
		if c.Phase == "repair" {
			return editsJSON(Edit{Old: "<script>boom()</script>", New: ""}, Edit{Old: "<p>x</p>", New: "<p>y</p>"}), nil
		}
		return fenced(doubled), nil
	}}
	c, f := &fakeChecker{}, newFakeFiles()
	res := run(t, &Pipeline{Model: m, Checker: c, Files: f}, Request{Brief: "x", Count: 1, Folder: "d"})
	item := res.Items[0]
	if item.Status != StatusFailed || item.Repair == nil || !strings.Contains(item.Repair.Refused, "more than once") {
		t.Fatalf("ambiguous repair not refused: %+v", item)
	}
	if string(f.files["d/01.html"]) != doubled || c.calls.Load() != 1 || item.CheckAfter != nil {
		t.Fatalf("refused repair changed the document or re-checked: checks=%d", c.calls.Load())
	}
}

func TestCheckUnavailableNeverPasses(t *testing.T) {
	m := &fakeModel{reply: func(ModelCall) (string, error) { return fenced(goodHTML), nil }}
	c, f := &fakeChecker{unavailable: true}, newFakeFiles()
	res := run(t, &Pipeline{Model: m, Checker: c, Files: f}, Request{Brief: "x", Count: 1, Folder: "d"})
	if res.Items[0].Status != StatusCheckUnavailable || m.phases()["repair"] != 0 || res.Counts[StatusPassed] != 0 {
		t.Fatalf("unavailable check: %+v counts=%v", res.Items[0], res.Counts)
	}

	// Edit mode: an unverified edit never replaces the file.
	m = &fakeModel{reply: func(ModelCall) (string, error) {
		return editsJSON(Edit{Old: "<title>t</title>", New: "<title>new</title>"}), nil
	}}
	f = newFakeFiles()
	f.files["site/index.html"] = []byte(goodHTML)
	res = run(t, &Pipeline{Model: m, Checker: c, Files: f}, Request{Brief: "rename", EditPath: "site/index.html"})
	if res.Items[0].Status != StatusCheckUnavailable || f.replaces != 0 || string(f.files["site/index.html"]) != goodHTML {
		t.Fatalf("unverified edit replaced the file: %+v", res.Items[0])
	}
}

func TestEditStaleBaseRefused(t *testing.T) {
	m := &fakeModel{reply: func(ModelCall) (string, error) {
		return editsJSON(Edit{Old: "<title>t</title>", New: "<title>new</title>"}), nil
	}}
	f := newFakeFiles()
	f.files["a.html"] = []byte(goodHTML)
	changed := []byte(strings.Replace(goodHTML, "ok", "changed by someone", 1))
	f.beforeReplace = func(files map[string][]byte) { files["a.html"] = changed }
	res := run(t, &Pipeline{Model: m, Checker: &fakeChecker{}, Files: f}, Request{Brief: "rename", EditPath: "a.html"})
	if res.Items[0].Status != StatusStaleBase || string(f.files["a.html"]) != string(changed) || res.EditFinalSHA256 != "" {
		t.Fatalf("stale base not refused: %+v", res.Items[0])
	}
}

func TestEditFailureLeavesOriginalByteIdentical(t *testing.T) {
	original := goodHTML + "\n<!-- keep me exactly -->"
	for _, tc := range []struct {
		name  string
		reply func(ModelCall) (string, error)
		want  Status
	}{
		{"edit not found", func(ModelCall) (string, error) { return editsJSON(Edit{Old: "absent", New: "x"}), nil }, StatusEditRefused},
		{"repair still failing", func(c ModelCall) (string, error) {
			if c.Phase == "repair" {
				return editsJSON(Edit{Old: "<title>t</title>", New: "<title>u</title>"}), nil
			}
			return editsJSON(Edit{Old: "</body>", New: "<script>boom()</script></body>"}), nil
		}, StatusFailed},
		{"model error", func(ModelCall) (string, error) { return "", errors.New("provider down") }, StatusGenerationFailed},
	} {
		f := newFakeFiles()
		f.files["p.html"] = []byte(original)
		res := run(t, &Pipeline{Model: &fakeModel{reply: tc.reply}, Checker: &fakeChecker{}, Files: f}, Request{Brief: "change", EditPath: "p.html"})
		if res.Items[0].Status != tc.want || f.replaces != 0 || string(f.files["p.html"]) != original {
			t.Fatalf("%s: status=%s replaces=%d", tc.name, res.Items[0].Status, f.replaces)
		}
	}
}

func TestBudgetRefusalStopsSchedulingAndConcurrencyCap(t *testing.T) {
	var generated atomic.Int32
	m := &fakeModel{delay: 5 * time.Millisecond, reply: func(c ModelCall) (string, error) {
		if generated.Add(1) > 3 {
			return "", ErrBudgetExceeded
		}
		return fenced(goodHTML), nil
	}}
	f := newFakeFiles()
	res := run(t, &Pipeline{Model: m, Checker: &fakeChecker{}, Files: f, Concurrency: 2}, Request{Brief: "x", Count: 12, Folder: "d"})
	if m.maxSeen.Load() > 2 {
		t.Fatalf("concurrency cap exceeded: %d", m.maxSeen.Load())
	}
	if res.Counts[StatusPassed] != 3 || res.Counts[StatusSkippedBudget] != 9 {
		t.Fatalf("counts after budget refusal: %v", res.Counts)
	}
	// The refusal stops scheduling: at most the in-flight calls run after it.
	if calls := len(m.calls); calls > 3+2 {
		t.Fatalf("model called %d times after the budget refused", calls)
	}
	for _, item := range res.Items {
		if item.Status == StatusSkippedBudget && item.Path != "" {
			t.Fatalf("skipped design wrote a file: %+v", item)
		}
	}
}

func TestTotalsOnlyFromReportedUsage(t *testing.T) {
	m := &fakeModel{reply: func(ModelCall) (string, error) { return fenced(goodHTML), nil }}
	res := run(t, &Pipeline{Model: m, Checker: &fakeChecker{}, Files: newFakeFiles()}, Request{Brief: "x", Count: 2, Folder: "d"})
	if res.Totals.Calls != 2 || res.Totals.Tokens != 200 || !res.Totals.CostComplete {
		t.Fatalf("totals: %+v", res.Totals)
	}
	unreported := &unreportedModel{}
	res = run(t, &Pipeline{Model: unreported, Checker: &fakeChecker{}, Files: newFakeFiles()}, Request{Brief: "x", Count: 1, Folder: "d"})
	if res.Totals.CostComplete || res.Totals.CostUSD != 0 || res.Totals.Tokens != 0 {
		t.Fatalf("unreported usage was invented: %+v", res.Totals)
	}
}

type unreportedModel struct{}

func (unreportedModel) Info() ModelInfo { return ModelInfo{} }
func (unreportedModel) Complete(context.Context, ModelCall) (ModelReply, error) {
	return ModelReply{Text: fenced(goodHTML)}, nil
}

func TestCreateWritesResultsAndRefusesBadRequests(t *testing.T) {
	m := &fakeModel{reply: func(ModelCall) (string, error) { return "no document here", nil }}
	f := newFakeFiles()
	res := run(t, &Pipeline{Model: m, Checker: &fakeChecker{}, Files: f}, Request{Brief: "x", Count: 1, Folder: "d"})
	if res.Items[0].Status != StatusGenerationFailed || res.Items[0].Path != "" {
		t.Fatalf("reply without a document: %+v", res.Items[0])
	}
	var saved BatchResult
	if err := json.Unmarshal(f.files["d/results.json"], &saved); err != nil || saved.FinishedAt == 0 || saved.Counts[StatusGenerationFailed] != 1 {
		t.Fatalf("results.json: %v %+v", err, saved)
	}
	p := &Pipeline{Model: m, Checker: &fakeChecker{}, Files: newFakeFiles()}
	for _, bad := range []Request{{Brief: "", Count: 1, Folder: "d"}, {Brief: "x", Count: 51, Folder: "d"}, {Brief: "x", Count: 0, Folder: "d"}, {Brief: "x", EditPath: "a.txt"}, {Brief: "x", Count: 2, EditPath: "a.html"}} {
		if _, err := p.Run(context.Background(), bad); err == nil {
			t.Fatalf("bad request accepted: %+v", bad)
		}
	}
	if len(m.calls) != 1 {
		t.Fatalf("bad requests spent model calls: %d", len(m.calls))
	}
}

func TestExtractHTML(t *testing.T) {
	if got, err := ExtractHTML(fenced(goodHTML)); err != nil || got != goodHTML {
		t.Fatalf("fenced: %q %v", got, err)
	}
	if got, err := ExtractHTML("intro " + goodHTML + " outro"); err != nil || got != goodHTML {
		t.Fatalf("bare: %q %v", got, err)
	}
	if _, err := ExtractHTML("<div>half"); err == nil {
		t.Fatalf("incomplete reply accepted")
	}
}

func TestItemDurationIsRecorded(t *testing.T) {
	// Purpose: results.json reports how long each design took; a deferred
	// assignment to a non-named return silently reported 0.
	clock := time.Unix(0, 0)
	var mu sync.Mutex
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); clock = clock.Add(time.Second); return clock }
	m := &fakeModel{reply: func(ModelCall) (string, error) { return fenced(goodHTML), nil }}
	res := run(t, &Pipeline{Model: m, Checker: &fakeChecker{}, Files: newFakeFiles(), Now: now}, Request{Brief: "x", Count: 1, Folder: "d"})
	if res.Items[0].DurationMS <= 0 {
		t.Fatalf("item duration not recorded: %+v", res.Items[0])
	}
}
