package api

import (
	"fmt"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
)

// boldLoopFindings is the FINDINGS body of the forge reviewer's bold-heading
// report: three numbered findings with bold titles and detail lines.
func boldLoopFindings() string {
	var b strings.Builder
	for i, severity := range []string{"Medium", "Medium", "High"} {
		fmt.Fprintf(&b, "%d. **%s: internal/upload/upload.go:%d the limit is not enforced on this path**\n", i+1, severity, 10*(i+1))
		for j := 0; j < 12; j++ {
			fmt.Fprintf(&b, "   Detail %d.%d: the change does not cover this case, and the report claims it does.\n", i+1, j)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// boldLoopMessage is the forge reviewer's final message: progress notes,
// then the report with Markdown-bold headings at the start of their lines.
func boldLoopMessage(verdict, findings string) string {
	var b strings.Builder
	for i := 1; i <= 16; i++ {
		fmt.Fprintf(&b, "I'm checking whether step %d of the brief is covered by the diff.\n", i)
	}
	b.WriteString("\n**VERDICT: " + verdict + "**\n\n**SUMMARY:**\nThe change is reviewed against its brief; see the findings.\n\n**FINDINGS:**\n\n" + findings + "\n\n**TESTS RUN:** none (review is read-only)\n")
	return b.String()
}

// Requirement: a reviewer's BLOCK report with bold headings after progress
// notes is recorded as block with its full FINDINGS section, the agent
// step's outcome carries those findings, and the reopen step sends exactly
// them into the coder job's own session.
// Threat (forge run): the bold report was recorded as malformed and the coder
// was sent "missing or malformed review verdict" instead of three findings.
// Owners: pebblestore.ParseJobReview, WorkerStore.RecordJobReview,
// Server.automationAgentReview via jobAutomationAgents.Wait, Runner.runAgent
// and runReopen, jobAutomationReopener.Reopen over the real session routes.
// Only the model provider is replaced (the final message is recorded).
func TestReviewLoopReadsABoldBlockReportAndSendsItsFindings(t *testing.T) {
	findings := boldLoopFindings()
	loop := newReviewLoop(t, boldLoopMessage("BLOCK", findings))
	before := len(loop.f.userMessages(t, loop.f.session.ID))
	d := loop.process(t)
	review := d.Steps[0]
	if review.Outcome["verdict"] != jobautomation.VerdictBlock || review.Outcome["findings"] != findings {
		t.Fatalf("review step outcome = %+v", review.Outcome)
	}
	if _, unreadable := review.Outcome["review_unreadable"]; unreadable {
		t.Fatalf("a readable review was marked unreadable: %+v", review.Outcome)
	}
	got := loop.f.userMessages(t, loop.f.session.ID)
	if len(got) != before+1 {
		t.Fatalf("reopen sent %d messages (steps %+v)", len(got)-before, d.Steps)
	}
	if want := "The reviewer blocked your work. Its findings:\n\n" + findings + "\n\n" + jobautomation.ReopenClosing; got[len(got)-1] != want {
		t.Fatalf("reopen message = %.400q", got[len(got)-1])
	}
}

// Requirement: a bold `**VERDICT: OK**` report is recorded as ok, so a later
// `verdict: ok` condition holds and nothing is reopened.
// Threat: a loop whose reviewer bolds its headings could never pass.
// Owners: as above.
func TestReviewLoopBoldOkSatisfiesAnOkCondition(t *testing.T) {
	loop := newReviewLoop(t, boldLoopMessage("OK", "None"))
	before := len(loop.f.userMessages(t, loop.f.session.ID))
	d := loop.process(t)
	if d.Steps[0].Outcome["verdict"] != jobautomation.VerdictOK {
		t.Fatalf("review step outcome = %+v", d.Steps[0].Outcome)
	}
	if d.Steps[1].State != jobautomation.StateSkipped {
		t.Fatalf("an ok review reopened the job: %+v", d.Steps[1])
	}
	if d.Steps[2].State == jobautomation.StateSkipped || len(loop.agents.started) != 2 || loop.agents.started[1] != "finder" {
		t.Fatalf("the verdict: ok condition did not hold: step %+v, started %v", d.Steps[2], loop.agents.started)
	}
	if n := len(loop.f.userMessages(t, loop.f.session.ID)); n != before {
		t.Fatalf("an ok review sent %d follow-ups", n-before)
	}
}

// Requirement: a review the parser could not read (record reason "missing or
// malformed review verdict", no parsed verdict) is marked review_unreadable
// on the agent step's outcome; the reopen step then sends nothing into the
// coder's session, uses no round, fails with "review unreadable: <reviewer
// job>" and the delivery ends blocked with that reason.
// Threat (forge run): the coder was sent the malformed reason as findings,
// the round counted, and a second unreadable review exhausted the loop.
// Owners: Server.automationAgentReview, Runner.runAgent, Runner.runReopen,
// Runner.runDelivery/finish over the real store, runner and session routes.
func TestReviewLoopUnreadableReviewNeverReopensTheCoder(t *testing.T) {
	notes := "I'm checking whether the limit is enforced.\n"
	report := "**VERDICT: BLOCK**\n**SUMMARY:** bad\n**FINDINGS:** 1. high a.go:1 broken\n**TESTS RUN:** none"
	for name, message := range map[string]string{
		"two verdict headings":       notes + "**VERDICT: BLOCK**\n**SUMMARY:** bad\n**FINDINGS:** 1. high a.go:1 broken\nVERDICT: OK\n**TESTS RUN:** none",
		"bold heading in the notes":  notes + "**SUMMARY:** early\n" + report,
		"quoted bold verdict":        notes + "> " + report,
		"bold verdict in a list":     notes + "- " + report,
		"bold report in a fence":     notes + "```\n" + report + "\n```",
		"bold sections out of order": notes + "**VERDICT: BLOCK**\n**FINDINGS:** 1. high a.go:1 broken\n**SUMMARY:** bad\n**TESTS RUN:** none",
	} {
		t.Run(name, func(t *testing.T) {
			loop := newReviewLoop(t, message)
			before := len(loop.f.userMessages(t, loop.f.session.ID))
			d := loop.process(t)
			review := d.Steps[0]
			if review.Outcome["verdict"] != jobautomation.VerdictBlock || review.Outcome["review_unreadable"] != true {
				t.Fatalf("review step outcome = %+v", review.Outcome)
			}
			if f, _ := review.Outcome["findings"].(string); f != "" {
				t.Fatalf("an unreadable review carried findings %q", f)
			}
			if n := len(loop.f.userMessages(t, loop.f.session.ID)); n != before {
				t.Fatalf("an unreadable review sent %d follow-ups into the coder's session", n-before)
			}
			want := "review unreadable: " + loop.agents.reviewer
			fix := d.Steps[1]
			if fix.State != jobautomation.StateFailed || fix.Reason != want {
				t.Fatalf("reopen step = %+v", fix)
			}
			if _, sent := fix.Outcome["followup"]; sent {
				t.Fatalf("reopen step recorded a follow-up: %+v", fix.Outcome)
			}
			if d.State != jobautomation.StateBlocked || d.BlockedReason != want || d.Round != 0 {
				t.Fatalf("delivery = state %s reason %q round %d", d.State, d.BlockedReason, d.Round)
			}
			if d.Steps[2].State != jobautomation.StateSkipped {
				t.Fatalf("a later step ran after an unreadable review: %+v", d.Steps[2])
			}
		})
	}
}

// Requirement: a real BLOCK whose FINDINGS section is empty ("None") still
// sends the record's reason (the structured report), as before.
// Threat: the unreadable rule swallowing a readable BLOCK.
// Owners: Server.automationAgentReview, Runner.runReopen.
func TestReviewLoopReadableBlockWithoutFindingsSendsTheReason(t *testing.T) {
	report := "VERDICT: BLOCK\nSUMMARY: the brief's second requirement is not done\nFINDINGS: None\nTESTS RUN: none (review is read-only)"
	loop := newReviewLoop(t, "I'm checking.\n"+report)
	before := len(loop.f.userMessages(t, loop.f.session.ID))
	d := loop.process(t)
	if d.Steps[0].Outcome["verdict"] != jobautomation.VerdictBlock || d.Steps[0].Outcome["findings"] != report {
		t.Fatalf("review step outcome = %+v", d.Steps[0].Outcome)
	}
	if _, unreadable := d.Steps[0].Outcome["review_unreadable"]; unreadable {
		t.Fatalf("a readable block was marked unreadable: %+v", d.Steps[0].Outcome)
	}
	got := loop.f.userMessages(t, loop.f.session.ID)
	if len(got) != before+1 {
		t.Fatalf("reopen sent %d messages (steps %+v)", len(got)-before, d.Steps)
	}
	if want := "The reviewer blocked your work. Its findings:\n\n" + report + "\n\n" + jobautomation.ReopenClosing; got[len(got)-1] != want {
		t.Fatalf("reopen message = %q", got[len(got)-1])
	}
}
