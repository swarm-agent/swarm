//! Earlier history above a conversation: every live conversation (project
//! Orchestrator chats and the Companion alike) shows a control when Swarm
//! holds messages before those read, and reads one page per click on the
//! follow thread (Live::read_older). Swarm's paging metadata says whether
//! more exist; the console never loads the whole history on its own.

use gpui::{AnyElement, ClickEvent, Context, SharedString, div, prelude::*, px};

use super::Console;
use crate::data::{History, Session};

/// The control's label for a conversation's history: None when there is
/// nothing to offer (no earlier messages, or not known yet).
pub(super) fn label(history: &History) -> Option<&'static str> {
    if history.asked {
        Some("Reading earlier messages…")
    } else if history.more_older == Some(true) {
        Some("Earlier messages")
    } else if history.older_error.is_some() {
        Some("Try reading earlier messages again")
    } else {
        None
    }
}

/// Whether a conversation shows the control at all: a live one with
/// something already read (an empty one has its own line).
pub(super) fn shows(s: &Session) -> bool {
    s.live.is_some() && !s.entries.is_empty() && label(&s.history).is_some()
}

impl Console {
    /// The person asked for earlier messages of the session at `si`.
    fn read_older(&mut self, si: usize, cx: &mut Context<Self>) {
        let Some(live) = &self.live else { return };
        let Some(s) = self.sessions.get_mut(si) else { return };
        let Some(l) = &s.live else { return };
        live.read_older(l.session_id.as_ref());
        s.history.asked = true;
        cx.notify();
    }

    /// Above a conversation's messages: earlier ones on request.
    pub(super) fn history_row(&self, si: usize, pid: usize, cx: &mut Context<Self>) -> Option<AnyElement> {
        let t = &self.theme;
        let s = self.sessions.get(si).filter(|s| shows(s))?;
        let text = label(&s.history)?;
        let asked = s.history.asked;
        let error = s.history.older_error.clone();
        Some(
            div()
                .flex()
                .flex_col()
                .items_center()
                .gap_1()
                .child(
                    self.button(SharedString::from(format!("pane-older-{pid}")), text, false)
                        .when(!asked, |d| d.on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.read_older(si, cx)))),
                )
                .children(error.map(|e| div().text_size(px(11.)).text_color(t.err).child(format!("Swarm did not return them: {e}"))))
                .into_any_element(),
        )
    }
}

#[cfg(test)]
mod tests {
    // Purpose: the earlier-history control is the same for every live
    // conversation and reflects Swarm's paging state exactly.
    // Requirement: shown while Swarm may hold earlier messages, while a page
    // read is under way, or after a failed read (to retry); hidden when
    // Swarm said there are none or nothing is known yet, and for a
    // conversation with nothing read or not started in Swarm.
    // Authority: history::label/shows over data::History, which the follow
    // loop fills from Swarm's omission cursor and page answers. A pure check
    // is the narrowest layer: rendering needs a GPUI window.
    use super::{label, shows};
    use crate::data::{Entry, History, LiveRef, Session};

    fn chat(history: History) -> Session {
        let mut s = crate::data::sessions().remove(0);
        s.live = Some(LiveRef { task_id: "".into(), session_id: "s1".into() });
        s.entries = vec![Entry::User("hi".into())];
        s.history = history;
        s
    }

    #[test]
    fn history_control_follows_paging_state() {
        assert_eq!(label(&History { more_older: Some(true), ..Default::default() }), Some("Earlier messages"));
        assert_eq!(label(&History { more_older: Some(true), asked: true, ..Default::default() }), Some("Reading earlier messages…"));
        assert_eq!(label(&History { more_older: Some(true), older_error: Some("lost".into()), ..Default::default() }), Some("Earlier messages"));
        assert_eq!(label(&History { more_older: None, older_error: Some("lost".into()), ..Default::default() }), Some("Try reading earlier messages again"));
        assert_eq!(label(&History { more_older: Some(false), ..Default::default() }), None, "no more: no control");
        assert_eq!(label(&History::default()), None, "not known yet: no control");

        // An ordinary project chat shows it like the Companion does.
        assert!(shows(&chat(History { more_older: Some(true), ..Default::default() })));
        // Nothing read yet, or not started in Swarm: no control.
        let mut empty = chat(History { more_older: Some(true), ..Default::default() });
        empty.entries.clear();
        assert!(!shows(&empty));
        let mut draft = chat(History { more_older: Some(true), ..Default::default() });
        draft.live = None;
        assert!(!shows(&draft));
    }
}
