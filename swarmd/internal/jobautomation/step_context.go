package jobautomation

import (
	"fmt"
	"strings"
)

const MaxHandoffBytes = 4000

// briefContext survives delivery persistence so only definition references are
// interpreted on execution, never placeholder-looking input data.
type briefContext struct {
	Template string            `json:"template"`
	Inputs   map[string]string `json:"inputs"`
}

func (s Step) briefReferences() string {
	if s.BriefContext != nil {
		return s.BriefContext.Template
	}
	return s.Brief
}

func renderAgentBrief(d Delivery, s Step) string {
	if s.BriefContext == nil {
		return agentBrief(d, s.Brief)
	}
	// Resolve definition step references first, then insert inputs in the
	// original template's literal segments, not in findings returned by steps.
	template := s.BriefContext.Template
	var out strings.Builder
	end := 0
	for _, loc := range stepOutputPlaceholder.FindAllStringIndex(template, -1) {
		out.WriteString(renderBriefInputs(template[end:loc[0]], s.BriefContext.Inputs))
		out.WriteString(agentBrief(d, template[loc[0]:loc[1]]))
		end = loc[1]
	}
	out.WriteString(renderBriefInputs(template[end:], s.BriefContext.Inputs))
	return truncate(out.String(), maxBriefRunes)
}

func renderBriefInputs(text string, values map[string]string) string {
	return inputPlaceholder.ReplaceAllStringFunc(text, func(m string) string {
		return values[inputPlaceholder.FindStringSubmatch(m)[1]]
	})
}

func validateStepPlaceholders(id, field, text string, earlier map[string]string) error {
	for _, m := range stepOutputPlaceholder.FindAllStringSubmatch(text, -1) {
		if _, ok := earlier[m[1]]; !ok {
			return invalid("step %s: %s placeholder %s names %q, which is not an earlier step", id, field, m[0], m[1])
		}
	}
	return nil
}

// agentBrief renders once, preserving findings' line structure. Reopens retain
// their historical one-line, 500-byte substitution behavior.
func agentBrief(d Delivery, brief string) string {
	return truncate(stepOutputPlaceholder.ReplaceAllStringFunc(brief, func(m string) string {
		match := stepOutputPlaceholder.FindStringSubmatch(m)
		for _, rec := range d.Steps {
			if rec.ID != match[1] {
				continue
			}
			text := rec.Output
			if match[2] != "output" {
				text, _ = rec.Outcome[match[2]].(string)
			}
			if strings.TrimSpace(text) != "" {
				return truncate(text, maxFindingsBytes)
			}
		}
		return "(no output)"
	}), maxBriefRunes)
}

func sinceContext(d Delivery, id string) (string, string, error) {
	for _, rec := range d.Steps {
		if rec.ID != id {
			continue
		}
		head, _ := rec.Outcome["head"].(string)
		verdict, _ := rec.Outcome["verdict"].(string)
		unreadable, _ := rec.Outcome["review_unreadable"].(bool)
		if rec.State != StateSucceeded || head == "" || unreadable || (verdict != VerdictOK && verdict != VerdictBlock) {
			return "", "", fmt.Errorf("since step %s has no readable completed review and head", id)
		}
		findings, _ := rec.Outcome["findings"].(string)
		return head, truncate(findings, maxFindingsBytes), nil
	}
	return "", "", fmt.Errorf("since step %s is missing", id)
}

// reserveFreshFix bounds fresh correction jobs per (account, automation, job).
// Reservation precedes dispatch and is idempotent across retries and restart.
func (s *Store) reserveFreshFix(d Delivery, step string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "job_automation/fresh_fix/" + hexKey(d.Account) + "/" + hexKey(d.AutomationID) + "/" + hexKey(d.JobID)
	var reservations map[string]bool
	_, err := s.db.GetJSON(key, &reservations)
	if err != nil {
		return false, err
	}
	if reservations == nil {
		reservations = map[string]bool{}
	}
	token := d.ID + "/" + step
	if reservations[token] {
		return true, nil
	}
	if len(reservations) >= MaxRounds {
		return false, nil
	}
	reservations[token] = true
	return true, s.db.PutJSON(key, reservations)
}

func followsReview(d Delivery, step Step) bool {
	for _, rec := range d.Steps {
		if rec.ID == step.ID {
			break
		}
		if verdict, _ := rec.Outcome["verdict"].(string); verdict != "" {
			return true
		}
	}
	return false
}
