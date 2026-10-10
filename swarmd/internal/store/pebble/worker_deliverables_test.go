package pebblestore

import (
	"strings"
	"testing"
)

// Requirement: a completed worker run turns the agent's checkpoint result into
// a pending_review social_post deliverable whose post text is the only
// agent-chosen field; the action contract is copied from the operator's
// requirement. Threat: an agent result that injects action, target URL or
// secret fields, oversized or control-character text, or a contract that
// aliases the stored worker definition. Boundary: ParseWorkerSocialPostResult
// and BuildWorkerSocialPostDeliverable, pure functions that own the
// conversion; no store or provider is needed to prove it.
func testSocialPostFixture() (WorkerRecord, WorkerAutomationDefinition, WorkerRunRecord, WorkerDeliverableRequirement) {
	req := WorkerDeliverableRequirement{Name: "tweet", Kind: "social_post", Required: true, ActionContract: &DeliverableActionContract{Action: "publish_x_post", TargetSecretRef: "env:TWITTER"}}
	auto := WorkerAutomationDefinition{ID: "wauto_1", Name: "Daily post", Revision: 2, DeliverableRequirements: []WorkerDeliverableRequirement{req}}
	w := WorkerRecord{ID: "wkr_1", AccountScopeID: "account", Name: "Poster", LocalBindings: map[string]string{"primary": "ws_1"}, Automations: []WorkerAutomationDefinition{auto}}
	run := WorkerRunRecord{ID: "wrun_abc", AccountScopeID: "account", WorkerID: "wkr_1", AutomationID: "wauto_1", AutomationRevision: 2, SessionID: "sess_1"}
	return w, auto, run, req
}

func TestBuildWorkerSocialPostDeliverable(t *testing.T) {
	w, auto, run, req := testSocialPostFixture()
	art := SessionPlanArtifactReference{ArtifactID: "art_1", Role: "deliverable", MediaType: "image/png"}
	cp := SessionPlanCheckpoint{ID: "cp-1", Status: "completed", Result: "  {\"tweet_text\": \"  hello\\nworld  \"}\n", Artifacts: []SessionPlanArtifactReference{art}}
	rec, err := BuildWorkerSocialPostDeliverable(w, auto, run, req, cp)
	if err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	if rec.ID != "deliv_wrun_abc_tweet" || rec.Kind != "social_post" || rec.Status != "pending_review" || rec.AccountID != "account" || rec.WorkspaceID != "ws_1" || rec.WorkerID != "wkr_1" || rec.OccurrenceID != "wrun_abc" || rec.SessionID != "sess_1" || rec.Title != "Daily post: tweet" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if len(rec.Payload) != 1 || rec.Payload["tweet_text"] != "hello\nworld" {
		t.Fatalf("payload: %+v", rec.Payload)
	}
	if len(rec.MediaRefs) != 1 || rec.MediaRefs[0] != art {
		t.Fatalf("media refs: %+v", rec.MediaRefs)
	}
	if rec.ActionContract == nil || rec.ActionContract == req.ActionContract || rec.ActionContract.Action != "publish_x_post" || rec.ActionContract.TargetSecretRef != "env:TWITTER" || rec.ActionContract.TargetURL != "" || len(rec.ActionContract.Parameters) != 0 {
		t.Fatalf("contract not an unaliased operator copy: %+v", rec.ActionContract)
	}
	again, err := BuildWorkerSocialPostDeliverable(w, auto, run, req, cp)
	if err != nil || again.ID != rec.ID {
		t.Fatalf("ID not deterministic: %q %v", again.ID, err)
	}

	fenced := cp
	fenced.Result = "```json\n{\"tweet_text\":\"fenced\"}\n```"
	if rec, err := BuildWorkerSocialPostDeliverable(w, auto, run, req, fenced); err != nil || rec.Payload["tweet_text"] != "fenced" {
		t.Fatalf("trivial fence rejected: %+v %v", rec.Payload, err)
	}
	exact := cp
	exact.Result = `{"tweet_text":"` + strings.Repeat("é", 280) + `"}`
	if _, err := BuildWorkerSocialPostDeliverable(w, auto, run, req, exact); err != nil {
		t.Fatalf("280 runes rejected: %v", err)
	}

	rejected := map[string]string{
		"empty result":       "",
		"empty text":         `{"tweet_text":"   "}`,
		"281 runes":          `{"tweet_text":"` + strings.Repeat("é", 281) + `"}`,
		"action key":         `{"tweet_text":"hi","action":"execute_webhook"}`,
		"target secret key":  `{"tweet_text":"hi","target_secret_ref":"env:OTHER"}`,
		"target url key":     `{"tweet_text":"hi","target_url":"https://example.invalid"}`,
		"case variant key":   `{"Tweet_Text":"hi"}`,
		"missing key":        `{"text":"hi"}`,
		"null text":          `{"tweet_text":null}`,
		"non-string text":    `{"tweet_text":5}`,
		"control char":       `{"tweet_text":"hi\u0007there"}`,
		"tab char":           `{"tweet_text":"hi\tthere"}`,
		"carriage return":    `{"tweet_text":"hi\rthere"}`,
		"array":              `[{"tweet_text":"hi"}]`,
		"plain text":         `hi there`,
		"two objects":        `{"tweet_text":"a"}{"tweet_text":"b"}`,
		"trailing garbage":   `{"tweet_text":"a"} extra`,
		"fence with prose":   "```json\n{\"tweet_text\":\"a\"}\n``` and more ```",
		"unterminated fence": "```json\n{\"tweet_text\":\"a\"}",
	}
	for name, result := range rejected {
		bad := cp
		bad.Result = result
		if rec, err := BuildWorkerSocialPostDeliverable(w, auto, run, req, bad); err == nil {
			t.Fatalf("%s: accepted %+v", name, rec)
		}
	}

	notDone := cp
	notDone.Status = "in_progress"
	if _, err := BuildWorkerSocialPostDeliverable(w, auto, run, req, notDone); err == nil {
		t.Fatal("incomplete checkpoint accepted")
	}
	otherRun := run
	otherRun.AccountScopeID = "other"
	if _, err := BuildWorkerSocialPostDeliverable(w, auto, otherRun, req, cp); err == nil {
		t.Fatal("cross-account run accepted")
	}
	badReq := req
	badReq.ActionContract = &DeliverableActionContract{Action: "execute_webhook", TargetURL: "https://example.invalid"}
	if _, err := BuildWorkerSocialPostDeliverable(w, auto, run, badReq, cp); err == nil {
		t.Fatal("non-X contract accepted at build time")
	}
}

func TestWorkerSocialPostDeliverableIDDisambiguates(t *testing.T) {
	a, err := WorkerSocialPostDeliverableID("wrun_1", "a b")
	if err != nil {
		t.Fatal(err)
	}
	b, err := WorkerSocialPostDeliverableID("wrun_1", "a_b")
	if err != nil {
		t.Fatal(err)
	}
	c, err := WorkerSocialPostDeliverableID("wrun_1", "A_B")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || b == c || a == c || b != "deliv_wrun_1_a_b" {
		t.Fatalf("ids collide or changed: %q %q %q", a, b, c)
	}
	if _, err := WorkerSocialPostDeliverableID("wrun_1", "  "); err == nil {
		t.Fatal("empty name accepted")
	}
}
