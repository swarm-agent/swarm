package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/tool/searchipc"
)

func TestResidentWorkerServesDistinctRequestsAndWatchesMutations(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "alpha\n")

	reader, writer := io.Pipe()
	outReader, outWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- runStream(context.Background(), reader, outWriter) }()
	enc := json.NewEncoder(writer)
	dec := json.NewDecoder(bufio.NewReader(outReader))

	first := sendRequest(t, enc, dec, searchipc.Request{ProtocolVersion: searchipc.ProtocolVersion, RequestID: "one", IndexRoot: root, TargetPath: root, Operation: "content", Queries: []string{"alpha"}, MaxResults: 20, TimeoutMillis: 10000})
	if first.RequestID != "one" || !first.Completed || first.Diagnostics.ColdStartCount != 1 || !first.Diagnostics.WatcherReady {
		t.Fatalf("unexpected first response: %+v", first)
	}

	writeTestFile(t, filepath.Join(root, "b.txt"), "bravo\n")
	var second searchipc.Response
	deadline := time.Now().Add(5 * time.Second)
	for {
		second = sendRequest(t, enc, dec, searchipc.Request{ProtocolVersion: searchipc.ProtocolVersion, RequestID: "two", IndexRoot: root, TargetPath: root, Operation: "content", Queries: []string{"bravo"}, MaxResults: 20, TimeoutMillis: 10000})
		if responseHasMatch(second, "b.txt") || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !responseHasMatch(second, "b.txt") {
		t.Fatalf("watcher did not expose created file: %+v", second)
	}
	if second.Diagnostics.ColdStartCount != 1 {
		t.Fatalf("expected one cold start, got %+v", second.Diagnostics)
	}

	writeTestFile(t, filepath.Join(root, "b.txt"), "charlie\n")
	waitForMatch(t, enc, dec, root, "charlie", "b.txt")
	if err := os.Rename(filepath.Join(root, "b.txt"), filepath.Join(root, "c.txt")); err != nil {
		t.Fatal(err)
	}
	waitForMatch(t, enc, dec, root, "charlie", "c.txt")
	if err := os.Remove(filepath.Join(root, "c.txt")); err != nil {
		t.Fatal(err)
	}
	waitForNoMatch(t, enc, dec, root, "charlie")

	_ = writer.Close()
	_ = outReader.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not shut down on EOF")
	}
}

func TestWorkerCorrelatesAndRejectsRootMismatch(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "alpha")
	input := strings.NewReader(mustJSON(t, searchipc.Request{RequestID: "one", IndexRoot: root, TargetPath: root, Queries: []string{"alpha"}, TimeoutMillis: 10000}) + "\n" + mustJSON(t, searchipc.Request{RequestID: "two", IndexRoot: other, TargetPath: other, Queries: []string{"alpha"}, TimeoutMillis: 10000}) + "\n")
	var output strings.Builder
	if err := runStream(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(output.String()))
	var first, second searchipc.Response
	if err := dec.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&second); err != nil {
		t.Fatal(err)
	}
	if first.RequestID != "one" || !first.Completed {
		t.Fatalf("bad first response: %+v", first)
	}
	if second.RequestID != "two" || second.Completed || second.ErrorCode != "root_mismatch" {
		t.Fatalf("bad mismatch response: %+v", second)
	}
}

func TestNarrowTargetNeverLeaksSiblingResults(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	sibling := filepath.Join(root, "sibling")
	writeTestFile(t, filepath.Join(target, "inside.txt"), "needle")
	writeTestFile(t, filepath.Join(sibling, "outside.txt"), "needle")
	var output strings.Builder
	request := mustJSON(t, searchipc.Request{RequestID: "scope", IndexRoot: root, TargetPath: target, Operation: "content", Queries: []string{"needle"}, MaxResults: 50, PageLimit: 50, TimeoutMillis: 10000})
	if err := runStream(context.Background(), strings.NewReader(request+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	var resp searchipc.Response
	if err := json.Unmarshal([]byte(strings.TrimSpace(output.String())), &resp); err != nil {
		t.Fatal(err)
	}
	if !responseHasMatch(resp, "inside.txt") {
		t.Fatalf("missing target result: %+v", resp)
	}
	if responseHasMatch(resp, "outside.txt") {
		t.Fatalf("leaked sibling result: %+v", resp)
	}
}

func TestWorkerMalformedInputAndTimeoutFailExplicitly(t *testing.T) {
	var malformed strings.Builder
	if err := runStream(context.Background(), strings.NewReader("{bad\n"), &malformed); err == nil {
		t.Fatal("expected malformed stream error")
	}
	var malformedResp searchipc.Response
	if err := json.Unmarshal([]byte(strings.TrimSpace(malformed.String())), &malformedResp); err != nil {
		t.Fatal(err)
	}
	if malformedResp.Completed || malformedResp.ErrorCode != "malformed_request" {
		t.Fatalf("bad malformed response: %+v", malformedResp)
	}

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "alpha")
	var timeout strings.Builder
	request := mustJSON(t, searchipc.Request{RequestID: "timeout", IndexRoot: root, TargetPath: root, Queries: []string{"alpha"}, TimeoutMillis: 1})
	if err := runStream(context.Background(), strings.NewReader(request+"\n"), &timeout); err != nil {
		t.Fatal(err)
	}
	var timeoutResp searchipc.Response
	if err := json.Unmarshal([]byte(strings.TrimSpace(timeout.String())), &timeoutResp); err != nil {
		t.Fatal(err)
	}
	if timeoutResp.Completed || timeoutResp.HelperError == "" {
		t.Fatalf("timeout looked successful: %+v", timeoutResp)
	}
}

func sendRequest(t *testing.T, enc *json.Encoder, dec *json.Decoder, req searchipc.Request) searchipc.Response {
	t.Helper()
	if err := enc.Encode(req); err != nil {
		t.Fatal(err)
	}
	var resp searchipc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp
}
func waitForMatch(t *testing.T, enc *json.Encoder, dec *json.Decoder, root, query, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := sendRequest(t, enc, dec, searchipc.Request{RequestID: query, IndexRoot: root, TargetPath: root, Queries: []string{query}, MaxResults: 20, TimeoutMillis: 10000})
		if responseHasMatch(resp, path) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("watcher did not expose %s in %s: %+v", query, path, resp)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
func waitForNoMatch(t *testing.T, enc *json.Encoder, dec *json.Decoder, root, query string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := sendRequest(t, enc, dec, searchipc.Request{RequestID: query, IndexRoot: root, TargetPath: root, Queries: []string{query}, MaxResults: 20, TimeoutMillis: 10000})
		if !responseHasAnyMatch(resp) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("watcher retained deleted match: %+v", resp)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
func responseHasAnyMatch(resp searchipc.Response) bool {
	for _, r := range resp.ContentResults {
		if len(r.Matches) > 0 {
			return true
		}
	}
	return false
}
func responseHasMatch(resp searchipc.Response, name string) bool {
	for _, r := range resp.ContentResults {
		for _, m := range r.Matches {
			if strings.HasSuffix(filepath.ToSlash(m.RelativePath), name) {
				return true
			}
		}
	}
	return false
}
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
