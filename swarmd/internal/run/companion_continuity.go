package run

import (
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Continuity maintenance (see pebblestore companion_continuity.go) runs inside
// the state maintainer: each meaningful event of the Companion's log adds one
// bounded extractive item to the open draft, and the draft is sealed into a
// segment at a boundary that depends only on the event sequence. It makes no
// model calls, so it uses no provider quota, needs no model assignment and
// cannot loop: its work per maintenance page is bounded by the page.

// companionItemText flattens text to one bounded line.
func companionItemText(text string) string {
	return companionOneLine(text, pebblestore.CompanionSegmentItemTextBytes)
}

func companionItemKeywords(text string) []string {
	keywords := pebblestore.CompanionContextKeywords(text)
	if len(keywords) > 6 {
		keywords = keywords[:6]
	}
	return keywords
}

// companionContinuityItems is what one classified event adds to continuity.
// topic is the request's topic record (nil when the message made none),
// answered the topic record IDs of the requests a reply resolved. Only the
// person's substantive requests, the Companion's replies, recorded decisions
// and work, task reports, device results and the person's approval and
// confirmation decisions are kept; a device result keeps its tool and status,
// never its output.
func companionContinuityItems(ev CompanionEvent, topic *pebblestore.CompanionContextRecord, answered []string) []pebblestore.CompanionSegmentItem {
	item := pebblestore.CompanionSegmentItem{Source: ev.Source}
	switch ev.Kind {
	case CompanionEventPersonMessage:
		if topic == nil {
			return nil
		}
		item.Kind, item.Authority = pebblestore.CompanionItemAsk, pebblestore.CompanionAuthorityPerson
		item.Text, item.Keywords = companionItemText(topic.Title), append([]string(nil), topic.Keywords...)
		item.RecordID, item.RecordRevision, item.ContentSHA256 = topic.ID, topic.Revision, topic.Source.ContentSHA256
	case CompanionEventReply:
		if ev.Message == nil || shouldDropSensitiveConversationMessage(*ev.Message) {
			return nil
		}
		item.Kind, item.Authority = pebblestore.CompanionItemReply, pebblestore.CompanionAuthorityAssistant
		item.Text = companionItemText(ev.Message.Content)
		item.Keywords = companionItemKeywords(item.Text)
		item.ContentSHA256 = pebblestore.CompanionMessageDigest(*ev.Message)
		if len(answered) > pebblestore.CompanionSegmentMaxAbout {
			answered = answered[len(answered)-pebblestore.CompanionSegmentMaxAbout:]
		}
		item.About = answered
	case CompanionEventToolResult:
		if ev.Name == "manage_companion" && ev.Status == "completed" {
			record, ok := companionRecordedContext(ev.Output)
			if !ok {
				return nil
			}
			switch record.Kind {
			case pebblestore.CompanionContextKindDecision:
				item.Kind, item.Authority = pebblestore.CompanionItemDecision, pebblestore.CompanionAuthorityDecision
			case pebblestore.CompanionContextKindWork:
				item.Kind, item.Authority = pebblestore.CompanionItemWork, pebblestore.CompanionAuthorityAssistant
			default:
				return nil
			}
			text := record.Title
			if item.Kind == pebblestore.CompanionItemDecision && strings.TrimSpace(record.Summary) != "" {
				text += " — " + record.Summary
			}
			item.Text, item.Keywords = companionItemText(text), append([]string(nil), record.Keywords...)
			item.RecordID, item.RecordRevision, item.ContentSHA256 = record.ID, record.Revision, record.Source.ContentSHA256
			break
		}
		if !pebblestore.IsMachineToolName(ev.Name) && !pebblestore.IsBrowserToolName(ev.Name) {
			return nil
		}
		item.Kind, item.Authority, item.Untrusted = pebblestore.CompanionItemDevice, pebblestore.CompanionAuthorityReported, true
		item.Text = companionItemText(strings.TrimSpace(ev.Name + " " + ev.Status + " " + ev.Detail))
	case CompanionEventTaskOutcome:
		if len(ev.Tasks) == 0 {
			return nil
		}
		parts := make([]string, 0, len(ev.Tasks))
		untrusted := false
		for _, row := range ev.Tasks {
			part := row.TaskID + " " + firstNonEmptyString(row.Status, "reported")
			if title := strings.TrimSpace(row.Title); title != "" {
				part += fmt.Sprintf(" %q", companionOneLine(title, 80))
			}
			parts = append(parts, part)
			untrusted = untrusted || row.Machine != ""
		}
		item.Kind, item.Authority, item.Untrusted = pebblestore.CompanionItemOutcome, pebblestore.CompanionAuthorityReported, untrusted
		item.Text = companionItemText("task " + firstNonEmptyString(ev.Delivery, "outcome") + ": " + strings.Join(parts, "; "))
		item.Keywords = companionItemKeywords(item.Text)
		if ev.Message != nil {
			item.ContentSHA256 = pebblestore.CompanionMessageDigest(*ev.Message)
		}
	case CompanionEventApprovalDecided:
		item.Kind, item.Authority = pebblestore.CompanionItemApproval, pebblestore.CompanionAuthorityPerson
		item.Text = companionItemText("approval for " + firstNonEmptyString(ev.Name, "a tool") + ": " + ev.Status)
	case CompanionEventConfirmation:
		if ev.Status == ClientToolConfirmationPending {
			return nil
		}
		item.Kind, item.Authority = pebblestore.CompanionItemConfirmation, pebblestore.CompanionAuthorityPerson
		item.Text = companionItemText("confirmation for " + firstNonEmptyString(ev.Detail, ev.Name, "a client tool") + ": " + ev.Status)
		item.Keywords = companionItemKeywords(ev.Detail)
	default:
		return nil
	}
	if strings.TrimSpace(item.Text) == "" {
		return nil
	}
	return []pebblestore.CompanionSegmentItem{item}
}

// companionSealSegment closes the open draft as a segment ending at endSeq.
func companionSealSegment(state *pebblestore.CompanionState, endSeq uint64) pebblestore.CompanionSegment {
	c := &state.Continuity
	items := append([]pebblestore.CompanionSegmentItem(nil), c.Open...)
	seg := pebblestore.CompanionSegment{
		ID: pebblestore.CompanionSegmentID(state.SessionID, c.OpenStartSeq), SessionID: state.SessionID, Generation: state.Generation,
		StartEventSeq: c.OpenStartSeq, EndEventSeq: endSeq, Items: items, Keywords: pebblestore.CompanionSegmentKeywords(items),
		Revision: 1, SealedAt: items[len(items)-1].Source.At,
	}
	c.Sealed++
	c.LastSegmentID, c.LastSealedSeq = seg.ID, endSeq
	c.Open, c.OpenBytes, c.OpenStartSeq = []pebblestore.CompanionSegmentItem{}, 0, 0
	return seg
}

// companionContinuityStep advances continuity by one consumed event: it adds
// the event's items (sealing first when an item would exceed the draft's item
// or byte bound) and seals a draft that now spans CompanionSegmentMaxEvents
// events. Boundaries depend only on the event sequence, never on how the log
// was paged, so replaying a page seals the same segments.
func companionContinuityStep(state *pebblestore.CompanionState, seq uint64, items []pebblestore.CompanionSegmentItem) []pebblestore.CompanionSegment {
	var sealed []pebblestore.CompanionSegment
	c := &state.Continuity
	if c.Open == nil {
		c.Open = []pebblestore.CompanionSegmentItem{}
	}
	for _, item := range items {
		if len(c.Open) > 0 && (len(c.Open)+1 > pebblestore.CompanionSegmentMaxItems || c.OpenBytes+len(item.Text) > pebblestore.CompanionSegmentMaxTextBytes) {
			sealed = append(sealed, companionSealSegment(state, seq-1))
		}
		if len(c.Open) == 0 {
			c.OpenStartSeq = seq
		}
		c.Open = append(c.Open, item)
		c.OpenBytes += len(item.Text)
	}
	if len(c.Open) > 0 && seq-c.OpenStartSeq+1 >= pebblestore.CompanionSegmentMaxEvents {
		sealed = append(sealed, companionSealSegment(state, seq))
	}
	return sealed
}
