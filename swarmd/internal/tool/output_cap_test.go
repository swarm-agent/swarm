package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/sandbox"
)

// hostBashFixture runs real Bash on the host (explicit sandbox off) in a
// temporary workspace.
func hostBashFixture(t *testing.T) string {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeOff, Backend: sandbox.BackendBubblewrap}, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := sandbox.Default()
	sandbox.SetDefault(m)
	t.Cleanup(func() {
		sandbox.SetDefault(old)
		m.Close()
	})
	return workspace
}

func runHostBash(t *testing.T, workspace, command string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	response, err := executeBash(ctx, WorkspaceScope{PrimaryPath: workspace}, map[string]any{"command": command, "category": "read", "critical": false, "explanation": []any{"Print fixture output in the temporary workspace"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(response), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestBashOutputOverCapKeepsHeadTailAndSavesFullOutput(t *testing.T) {
	// Purpose: a command whose output exceeds the tool output cap returns only
	// its head and tail around a marker naming the omitted bytes and the file
	// the full output was saved to; the file holds every byte and is ignored
	// by Git. Threat: a long test run (observed live: a full package test)
	// feeds its whole output back to the model turn after turn, or the cut
	// loses the tail where failures are, or saved output lands in a commit.
	// Owners: bashResponse, newBashCapture, capToolOutputText, saveToolOutput.
	// Layer: the bash tool boundary with a real host command; the cap is a
	// property of the tool result, so a lower layer cannot show it.
	workspace := hostBashFixture(t)
	const lines = 20000 // ~ 600 KB, well over the default cap
	payload := runHostBash(t, workspace, fmt.Sprintf("echo HEAD-MARKER; for i in $(seq 1 %d); do echo \"line $i of the fixture output\"; done; echo TAIL-MARKER", lines))
	output, _ := payload["output"].(string)
	if len(output) > ToolOutputCapBytes() {
		t.Fatalf("output %d bytes is over the cap %d", len(output), ToolOutputCapBytes())
	}
	if !strings.HasPrefix(output, "HEAD-MARKER") || !strings.HasSuffix(strings.TrimSpace(output), "TAIL-MARKER") {
		t.Fatalf("head or tail not kept: %q ... %q", output[:40], output[len(output)-40:])
	}
	if !strings.Contains(output, "bytes omitted; full output saved to ") || !strings.Contains(output, "; read it with read/search]") {
		t.Fatalf("marker missing: %q", output)
	}
	if payload["truncated"] != true || payload["output_capped"] != true {
		t.Fatalf("cap not reported: %#v", payload)
	}
	saved, _ := payload["full_output_path"].(string)
	wantDir := filepath.Join(workspace, ".swarm", "tool-output")
	if filepath.Dir(saved) != wantDir {
		t.Fatalf("full output saved at %q, want inside %q", saved, wantDir)
	}
	if !strings.Contains(output, saved) {
		t.Fatalf("marker does not name the saved file %q", saved)
	}
	data, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	full := string(data)
	if !strings.HasPrefix(full, "HEAD-MARKER") || !strings.Contains(full, fmt.Sprintf("line %d of the fixture output", lines/2)) || !strings.HasSuffix(strings.TrimSpace(full), "TAIL-MARKER") {
		t.Fatalf("saved output is not the full output (%d bytes)", len(full))
	}
	omitted, _ := payload["output_omitted_bytes"].(float64)
	if payload["output_original_bytes"] != float64(len(full)) || omitted <= 0 || int(omitted) >= len(full) {
		t.Fatalf("sizes not reported: original=%v omitted=%v full=%d", payload["output_original_bytes"], omitted, len(full))
	}
	if info, err := os.Stat(saved); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved output is not private: %v %v", info, err)
	}
	ignore, err := os.ReadFile(filepath.Join(wantDir, ".gitignore"))
	if err != nil || string(ignore) != "*\n" {
		t.Fatalf("tool-output directory does not ignore itself: %q %v", ignore, err)
	}
}

func TestBashSmallOutputUnchanged(t *testing.T) {
	// Purpose: output within the cap is returned exactly as before, with no
	// cap fields and nothing written to the workspace.
	// Threat: the cap rewrites or litters ordinary small results.
	// Owners: bashResponse, capToolOutputText. Layer: bash tool boundary.
	workspace := hostBashFixture(t)
	payload := runHostBash(t, workspace, "printf 'small output\\nsecond line\\n'")
	if payload["output"] != "small output\nsecond line\n" {
		t.Fatalf("small output changed: %q", payload["output"])
	}
	for _, key := range []string{"output_capped", "full_output_path", "output_omitted_bytes"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("small output carries %s: %#v", key, payload)
		}
	}
	if payload["truncated"] != false {
		t.Fatalf("small output marked truncated: %#v", payload)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".swarm")); !os.IsNotExist(err) {
		t.Fatalf("small output wrote to the workspace: %v", err)
	}
}

func TestBashCaptureKeepsRealTailPastViewerBuffer(t *testing.T) {
	// Purpose: past the in-memory capture limit the end of the output is
	// still kept, so the capped result shows how a very long command ended.
	// Threat: head-only capture loses the final failure summary of a run
	// whose output exceeds maxBashOutputViewerBytes.
	// Owners: cappedBuffer.keepTail, newBashCapture. Layer: unit (buffer).
	capture := newBashCapture()
	_, _ = capture.Write([]byte("START\n"))
	_, _ = capture.Write([]byte(strings.Repeat("x", maxBashOutputViewerBytes+2*ToolOutputCapBytes())))
	_, _ = capture.Write([]byte("\nFINAL-SUMMARY\n"))
	text := capture.String()
	if !capture.Truncated() || !strings.HasPrefix(text, "START") || !strings.HasSuffix(text, "FINAL-SUMMARY\n") || !strings.Contains(text, "bytes not retained") {
		t.Fatalf("capture lost its tail or marker: truncated=%v len=%d end=%q", capture.Truncated(), len(text), text[len(text)-40:])
	}
}

func TestCapToolOutputRefusesSymlinkedOutputDirectory(t *testing.T) {
	// Purpose: the full output is only ever written to a real directory
	// inside the workspace; a .swarm symlink planted by an agent is refused
	// and the result says the output could not be saved.
	// Threat: an agent-writable workspace redirects daemon writes outside
	// the workspace (for example into another checkout or daemon storage).
	// Owners: saveToolOutput, ensureRealDirectory. Layer: unit with a real
	// temporary filesystem.
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{workspace, outside} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(workspace, ".swarm")); err != nil {
		t.Fatal(err)
	}
	capped := capToolOutputText(WorkspaceScope{PrimaryPath: workspace}, "bash", strings.Repeat("y\n", ToolOutputCapBytes()), true)
	if !capped.Capped || capped.SavedPath != "" || capped.SaveError == "" || !strings.Contains(capped.Text, "full output could not be saved") {
		t.Fatalf("symlinked directory not refused: %+v", capped.SaveError)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("output written outside the workspace: %v", entries)
	}
}

func TestCapToolOutputKeepsUTF8AndPrunesOldOutputs(t *testing.T) {
	// Purpose: the cut never splits a UTF-8 sequence, and saved outputs are
	// bounded per workspace (oldest removed first).
	// Threat: invalid UTF-8 in model input; unbounded disk use from saved
	// outputs. Owners: headAtBoundary, tailAtBoundary, pruneSavedToolOutputs.
	// Layer: unit with a real temporary filesystem.
	workspace := t.TempDir()
	text := strings.Repeat("é漢🙂", ToolOutputCapBytes())
	capped := capToolOutputText(WorkspaceScope{PrimaryPath: workspace}, "bash", text, true)
	if !capped.Capped || !utf8.ValidString(capped.Text) || len(capped.Text) > ToolOutputCapBytes() {
		t.Fatalf("cut is not valid UTF-8 within the cap: valid=%v len=%d", utf8.ValidString(capped.Text), len(capped.Text))
	}
	for i := 0; i < maxSavedToolOutputs+5; i++ {
		if _, err := saveToolOutput(WorkspaceScope{PrimaryPath: workspace}, "bash", "x"); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(workspace, ".swarm", "tool-output"))
	if err != nil {
		t.Fatal(err)
	}
	saved := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".txt") {
			saved++
		}
	}
	if saved != maxSavedToolOutputs {
		t.Fatalf("saved outputs %d, want %d", saved, maxSavedToolOutputs)
	}
}

func TestToolOutputCapSettingDefaultAndBounds(t *testing.T) {
	// Purpose: the cap is one setting: 0 selects the default, values outside
	// the accepted range are refused without changing the cap in effect.
	// Threat: a typo silently disables the cap or makes it uselessly small.
	// Owners: SetToolOutputCapBytes, ToolOutputCapBytes. Layer: unit.
	t.Cleanup(func() { _ = SetToolOutputCapBytes(0) })
	if err := SetToolOutputCapBytes(0); err != nil || ToolOutputCapBytes() != DefaultToolOutputCapBytes {
		t.Fatalf("default cap: %v %d", err, ToolOutputCapBytes())
	}
	if err := SetToolOutputCapBytes(8192); err != nil || ToolOutputCapBytes() != 8192 {
		t.Fatalf("set cap: %v %d", err, ToolOutputCapBytes())
	}
	for _, bad := range []int{-1, 100, MaxToolOutputCapBytes + 1} {
		if err := SetToolOutputCapBytes(bad); err == nil || ToolOutputCapBytes() != 8192 {
			t.Fatalf("cap %d accepted or changed the cap: %v %d", bad, err, ToolOutputCapBytes())
		}
	}
}

func TestReadWithoutMaxLinesStopsAtCap(t *testing.T) {
	// Purpose: read without max_lines on a huge file returns whole lines up
	// to the cap with next_line_start to continue; an explicit max_lines is
	// the caller's own bound. Threat: one read of a large file feeding
	// hundreds of KB to the model. Owners: executeRead. Layer: read tool.
	workspace := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 5000; i++ {
		fmt.Fprintf(&b, "line %05d %s\n", i, strings.Repeat("z", 60))
	}
	if err := os.WriteFile(filepath.Join(workspace, "big.txt"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	scope := WorkspaceScope{PrimaryPath: workspace, Roots: []string{workspace}}
	read := func(args map[string]any) map[string]any {
		out, err := executeRead(scope, args)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(out), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	capped := read(map[string]any{"path": "big.txt"})
	count := int(capped["count"].(float64))
	if capped["output_capped"] != true || capped["truncated"] != true || int(capped["bytes"].(float64)) > ToolOutputCapBytes() || count == 0 || int(capped["next_line_start"].(float64)) != count+1 {
		t.Fatalf("read without max_lines not capped: count=%d bytes=%v capped=%v", count, capped["bytes"], capped["output_capped"])
	}
	explicit := read(map[string]any{"path": "big.txt", "max_lines": 2000})
	if explicit["count"] != float64(2000) || explicit["output_capped"] != nil {
		t.Fatalf("explicit max_lines not honoured: count=%v capped=%v", explicit["count"], explicit["output_capped"])
	}
}

func TestEncodeCappedResultsPayloadDropsTrailingResultsAndSaves(t *testing.T) {
	// Purpose: a search answer over the cap keeps its leading results within
	// the cap, says how many were omitted and saves the full answer; a small
	// answer is unchanged. Threat: unbounded search results to the model.
	// Owners: encodeCappedResultsPayload. Layer: unit (the encoder search uses).
	workspace := t.TempDir()
	scope := WorkspaceScope{PrimaryPath: workspace}
	results := make([]map[string]any, 600)
	for i := range results {
		results[i] = map[string]any{"path": fmt.Sprintf("file%03d.go", i), "text": strings.Repeat("m", 200)}
	}
	out, err := encodeCappedResultsPayload(scope, "search", map[string]any{"count": len(results), "results": results}, "results")
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	kept := len(payload["results"].([]any))
	if len(out) > ToolOutputCapBytes() || kept == 0 || int(payload["output_omitted_results"].(float64)) != len(results)-kept || payload["count"] != float64(kept) {
		t.Fatalf("not capped: len=%d kept=%d omitted=%v", len(out), kept, payload["output_omitted_results"])
	}
	data, err := os.ReadFile(payload["full_output_path"].(string))
	if err != nil || !strings.Contains(string(data), "file599.go") {
		t.Fatalf("full answer not saved: %v", err)
	}
	small, err := encodeCappedResultsPayload(scope, "search", map[string]any{"count": 1, "results": results[:1]}, "results")
	if err != nil || strings.Contains(small, "output_capped") {
		t.Fatalf("small answer changed: %s %v", small, err)
	}
}

// exaContentsFixture serves Exa /contents: it records each request body and
// answers every URL with pageText.
func exaContentsFixture(t *testing.T, pageText string) (*Runtime, *[]map[string]any) {
	t.Helper()
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		requests = append(requests, body)
		var results []map[string]any
		for _, u := range body["urls"].([]any) {
			results = append(results, map[string]any{"id": u, "url": u, "title": "Fixture", "text": pageText})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"requestId": "fixture", "results": results})
	}))
	t.Cleanup(server.Close)
	r := NewRuntime(1)
	t.Cleanup(func() { _ = r.Close() })
	r.SetExaConfigResolver(func(context.Context) (ExaRuntimeConfig, error) {
		return ExaRuntimeConfig{Enabled: true, APIKey: "fixture-key", SearchURL: server.URL + "/search", ContentsURL: server.URL + "/contents"}, nil
	})
	return r, &requests
}

func webfetchRecords(t *testing.T, out string) []map[string]any {
	t.Helper()
	var payload struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Results
}

func TestWebFetchHonoursMaxCharacters(t *testing.T) {
	// Purpose: webfetch with text.maxCharacters (Exa's own spelling, as
	// observed live) sends the limit to Exa and returns at most that many
	// characters of text even when the page comes back whole.
	// Threat: the limit is silently ignored and a whole page (~22 KB
	// observed) is returned. Owners: parseExaTextOption,
	// normalizeExaOptionKeys, applyWebTextCharLimit, clipWebText,
	// Runtime.executeWebFetch. Layer: tool boundary against a local Exa
	// fixture (no network).
	r, requests := exaContentsFixture(t, strings.Repeat("word ", 4500))
	for _, text := range []map[string]any{{"maxCharacters": 300}, {"max_characters": 300}} {
		*requests = nil
		out, err := r.executeWebFetch(context.Background(), map[string]any{"url": "https://example.com/page", "text": text})
		if err != nil {
			t.Fatal(err)
		}
		sent, _ := (*requests)[0]["text"].(map[string]any)
		if sent["maxCharacters"] != float64(300) {
			t.Fatalf("limit not sent to Exa for %v: %#v", text, (*requests)[0]["text"])
		}
		records := webfetchRecords(t, out)
		got, _ := records[0]["text"].(string)
		if n := utf8.RuneCountInString(got); n == 0 || n > 300 || records[0]["text_truncated"] != true {
			t.Fatalf("text not limited to 300 characters for %v: %d", text, n)
		}
	}
	if _, err := r.executeWebFetch(context.Background(), map[string]any{"url": "https://example.com/page", "text": map[string]any{"maxCharacters": 300, "max_characters": 500}}); err == nil {
		t.Fatal("conflicting spellings were accepted")
	}
}

func TestWebFetchDefaultPerURLCap(t *testing.T) {
	// Purpose: without a limit, each URL gets an equal share of the tool
	// output cap, sent to Exa and applied locally.
	// Threat: an unbounded page reaches the model when no limit is given.
	// Owners: applyWebTextCharLimit, Runtime.executeWebFetch. Layer: tool
	// boundary against a local Exa fixture.
	r, requests := exaContentsFixture(t, strings.Repeat("a", 3*ToolOutputCapBytes()))
	out, err := r.executeWebFetch(context.Background(), map[string]any{"urls": []any{"https://example.com/a", "https://example.com/b"}})
	if err != nil {
		t.Fatal(err)
	}
	want := ToolOutputCapBytes() / 2
	sent, _ := (*requests)[0]["text"].(map[string]any)
	if sent["maxCharacters"] != float64(want) {
		t.Fatalf("default limit not sent to Exa: %#v", (*requests)[0]["text"])
	}
	for _, record := range webfetchRecords(t, out) {
		if n := utf8.RuneCountInString(record["text"].(string)); n > want {
			t.Fatalf("page text %d over the default per-URL cap %d", n, want)
		}
	}
}

func TestSaveToolOutputSurvivesDirectorySwappedForSymlink(t *testing.T) {
	// Purpose: a .swarm or .swarm/tool-output replaced by a symlink after the
	// save opened it (the window an agent's background Bash loop can race)
	// cannot redirect the write or the pruning: nothing is created or
	// deleted in the symlink's target, the save fails instead of reporting a
	// path that now leads elsewhere, and the file written into the original
	// directory is removed. Threat: the host daemon writing agent-controlled
	// output outside the workspace (possibly into daemon storage) and
	// pruning deleting .txt files there.
	// Owners: saveToolOutput, openRealSubdirectories,
	// confirmRealSubdirectories, writeRootedExclusive, pruneSavedToolOutputs.
	// Layer: unit with a real temporary filesystem; toolOutputSaveTestHook
	// makes the race window deterministic.
	for _, swapped := range []string{filepath.Join(".swarm", "tool-output"), ".swarm"} {
		t.Run(swapped, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "workspace")
			outside := filepath.Join(root, "outside")
			for _, dir := range []string{workspace, outside, filepath.Join(outside, "tool-output")} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			// Seed the target with more than maxSavedToolOutputs .txt files, so
			// pruning through the symlink would delete some of them.
			seed := func(dir string) {
				for i := 0; i < maxSavedToolOutputs+10; i++ {
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("0000-victim-%03d.txt", i)), []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			seed(outside)
			seed(filepath.Join(outside, "tool-output"))
			snapshot := func() []string {
				var names []string
				_ = filepath.WalkDir(outside, func(path string, _ os.DirEntry, _ error) error {
					names = append(names, path)
					return nil
				})
				return names
			}
			before := snapshot()
			target := filepath.Join(workspace, swapped)
			// For ".swarm", outside/tool-output then stands in for
			// .swarm/tool-output; both are seeded.
			linkTo := outside
			toolOutputSaveTestHook = func() {
				if err := os.Rename(target, target+".moved"); err != nil {
					t.Error(err)
				}
				if err := os.Symlink(linkTo, target); err != nil {
					t.Error(err)
				}
			}
			t.Cleanup(func() { toolOutputSaveTestHook = nil })
			capped := capToolOutputText(WorkspaceScope{PrimaryPath: workspace}, "bash", strings.Repeat("secret\n", ToolOutputCapBytes()), true)
			if !capped.Capped || capped.SavedPath != "" || !strings.Contains(capped.SaveError, "replaced during the save") || !strings.Contains(capped.Text, "full output could not be saved") {
				t.Fatalf("swap not detected: path=%q err=%q", capped.SavedPath, capped.SaveError)
			}
			after := snapshot()
			if strings.Join(before, "\n") != strings.Join(after, "\n") {
				t.Fatalf("files outside the workspace changed:\nbefore %d\nafter %d", len(before), len(after))
			}
			// The write went to the original (moved) directory and was removed.
			movedDir := target + ".moved"
			if swapped == ".swarm" {
				movedDir = filepath.Join(movedDir, "tool-output")
			}
			entries, err := os.ReadDir(movedDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".txt") {
					t.Fatalf("output left behind after a failed save: %s", entry.Name())
				}
			}
		})
	}
}
