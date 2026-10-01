package api

import (
	"encoding/json"
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the registered independent HTTP route must authorize the principal,
// preserve exact bytes, reject stale refs/CAS without mutation and constrain HTML
// with an opaque-origin CSP. Handler tests are the narrowest transport proof;
// real browser isolation remains a separate parent-owned acceptance check.
func TestDesignHTTPExactBytesIsolationAndCAS(t *testing.T) {
	s, _ := newArtifactV3APITestServer(t)
	db := s.sessions.DesignStore()
	p := pebblestore.DesignPrincipal{AccountID: "account-1", PrincipalID: "user-1"}
	r, err := db.SubmitDesignRequest(p, pebblestore.DesignSubmit{RequestID: "request", IdempotencyKey: "request", ParentSessionID: "artifact-v3-api", ParentRunID: "parent-run", Candidates: []pebblestore.DesignCandidateSpec{{ArtifactID: "design", Kind: "html", Operation: "generate", Brief: "private brief"}}})
	if err != nil { t.Fatal(err) }
	r, err = db.RecordDesignAttempt(p, r.ID, pebblestore.DesignAttemptMutation{IdempotencyKey: "start", ExpectedRevision: r.Revision, State: "running", ChildSessionID: "child", RunID: "child-run"})
	if err != nil { t.Fatal(err) }
	content := "<!doctype html><html><body>original</body></html>"
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil { t.Fatal(err) }
	output := pebblestore.DesignOutputRef{RequestID: r.ID, Candidate: 0, Attempt: 1, ChildSessionID: "child", RunID: "child-run", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(content)))}
	r, err = db.RecordDesignResponse(p, pebblestore.DesignResponseMutation{IdempotencyKey: "response", ExpectedRevision: r.Revision, Response: pebblestore.DesignResponse{Ref: output, Provider: "test", Model: "test", Content: []byte(content)}})
	if err != nil { t.Fatal(err) }
	r, err = db.RecordDesignValidation(p, pebblestore.DesignValidationMutation{IdempotencyKey: "validation", ExpectedRevision: r.Revision, Output: output, Passed: true, Code: "renderable", PNG: pngBytes.Bytes()})
	if err != nil { t.Fatal(err) }
	r, err = db.PublishDesignRevision(p, r.ID, pebblestore.DesignPublication{IdempotencyKey: "publish", ExpectedRevision: r.Revision, ChildSessionID: "child", RunID: "child-run", Kind: "html", Content: []byte(content)})
	if err != nil { t.Fatal(err) }
	ref := *r.Candidates[0].Attempts[0].Result
	path := "/v3/sessions/artifact-v3-api/designs/artifacts/design"
	post := func(input any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(input)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, withTestPrincipal(httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))))
		return w
	}
	w := post(map[string]any{"action": "preview_html", "ref": ref})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "data:image/png;base64,") || strings.Contains(w.Body.String(), content) || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox;") || strings.Contains(w.Header().Get("Content-Security-Policy"), "allow-same-origin") || w.Header().Get("Cache-Control") != "no-store" { t.Fatal(w.Code, w.Header(), w.Body.String()) }
	w = post(map[string]any{"action": "download", "ref": ref})
	if w.Code != 200 || w.Body.String() != content || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") { t.Fatal(w.Code, w.Body.String()) }
	w = post(map[string]any{"action": "select", "ref": ref, "idempotency_key": "first", "expected_version": 0, "expected_current": nil})
	if w.Code != 200 { t.Fatal(w.Code, w.Body.String()) }
	w = post(map[string]any{"action": "select", "ref": ref, "idempotency_key": "stale", "expected_version": 0, "expected_current": nil})
	if w.Code != 409 { t.Fatal(w.Code, w.Body.String()) }
	bad := ref; bad.SHA256 = strings.Repeat("0", 64)
	w = post(map[string]any{"action": "read", "ref": bad})
	if w.Code != 409 || strings.Contains(w.Body.String(), content) { t.Fatal(w.Code, w.Body.String()) }
	w = post(map[string]any{"action": "edit", "ref": ref, "brief": "change", "run_id": "forged"})
	if w.Code != 400 { t.Fatal(w.Code) }
	for _, owner := range [][2]string{{"foreign", "user-1"}, {"account-1", "foreign"}} {
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, withAccountPrincipal(httptest.NewRequest(http.MethodGet, path, nil), owner[0], owner[1]))
		if w.Code != 404 { t.Fatal(w.Code, w.Body.String()) }
	}
	w = post(map[string]any{"action": "edit", "ref": ref, "brief": "Make the card clearer", "idempotency_key": "edit-message"})
	if w.Code != 200 { t.Fatal("canonical edit message", w.Code, w.Body.String()) }
	// Accepted user intent is not falsely reported as an admitted design request.
	requests, err := db.ListSessionDesignRequests(p, "artifact-v3-api", "", 20)
	if err != nil || len(requests) != 1 { t.Fatal("edit bypassed parent admission", requests, err) }
	messages := httptest.NewRecorder()
	s.Handler().ServeHTTP(messages, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/sessions/artifact-v3-api/messages", nil)))
	if messages.Code != 200 || !strings.Contains(messages.Body.String(), ref.SHA256) || !strings.Contains(messages.Body.String(), "Make the card clearer") { t.Fatal("exact edit intent not durable", messages.Code, messages.Body.String()) }
	w = post(map[string]any{"action": "edit", "ref": ref, "brief": "Make the card clearer", "idempotency_key": "edit-message"})
	if w.Code != 200 { t.Fatal("edit replay", w.Code, w.Body.String()) }
	a, err := db.GetDesignArtifact(p, "design")
	if err != nil || a.SelectionVersion != 1 || a.Selected == nil || *a.Selected != ref { t.Fatal(a, err) }
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/sessions/artifact-v3-api/designs", nil)))
	if w.Code != 200 || strings.Contains(w.Body.String(), "private brief") || !strings.Contains(w.Body.String(), "request") { t.Fatal(w.Code, w.Body.String()) }
}
