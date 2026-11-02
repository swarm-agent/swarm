//! Earlier messages of a conversation, read a page at a time as the person
//! scrolls up in any pane (split, popped out or the Companion's): the read
//! asked of the follow loop (one per session at a time), the quiet row at the
//! top of the transcript, and the view held still when a page arrives above.

use super::{Console, Pane};
use crate::data::{Session, Text};
use crate::layout::PaneId;
use gpui::{AnyElement, ClickEvent, Context, Pixels, SharedString, div, prelude::*, px};
use std::cell::RefCell;

/// The space between a transcript's rows (the pane's scroll).
pub(super) const GAP: Pixels = px(12.);
/// The top row's height. It stays while earlier messages may exist, so its
/// text coming and going moves nothing below it.
const ROW: Pixels = px(20.);

/// What a pane drew of its conversation at its last paint.
#[derive(Clone)]
struct Drawn {
    session: Text,
    /// Its entries from earlier pages, and all of them.
    older: usize,
    entries: usize,
    /// The scroll's child index of the first entry (the row comes before).
    first: usize,
}

/// A pane's memory of its last paint, for the checks below. Render takes
/// the console by reference, hence the cell.
#[derive(Default)]
pub(super) struct PaneEarlier {
    drawn: RefCell<Option<Drawn>>,
}

/// A page arrived above what the pane showed: `at` entries came at the
/// front, and the viewport's top was `shift` below the top of what was the
/// first entry (negative: above it, the row showing).
pub(super) struct Hold {
    pub at: usize,
    pub shift: Pixels,
}

impl Console {
    /// The pane's last paint drew this conversation with entries, so its
    /// scroll's measures (offset, size, rows) are of it.
    fn drew(pane: &Pane, s: &Session) -> Option<Drawn> {
        let id = &s.live.as_ref()?.session_id;
        pane.earlier.drawn.borrow().as_ref().filter(|d| d.session == *id && d.entries > 0).cloned()
    }

    /// Asks for the page before what a pane shows when its view is within a
    /// screen of the top (or all of it fits), earlier messages may exist,
    /// none is being read for the session and the last read did not fail
    /// (that waits for Try again). True when asked.
    pub(super) fn want_older(&self, pid: PaneId) -> bool {
        let Some(pane) = self.panes.iter().find(|p| p.id == pid) else { return false };
        let Some(s) = self.sessions.get(pane.session) else { return false };
        if !s.earlier.more || s.earlier.error.is_some() || s.entries.is_empty() || Self::drew(pane, s).is_none() {
            return false;
        }
        // GPUI's offset is 0 at the top and grows negative scrolling down.
        let (offset, max, screen) = (pane.scroll.offset(), pane.scroll.max_offset(), pane.scroll.bounds().size.height);
        if max.height > px(0.) && offset.y < -screen {
            return false;
        }
        self.ask_older(s)
    }

    /// Asks the follow loop for a session's page before what is shown,
    /// unless one is being read (its answer settles it, settle_older).
    fn ask_older(&self, s: &Session) -> bool {
        let (Some(live), Some(l)) = (&self.live, &s.live) else { return false };
        // Kicks sent while the stream is down are dropped when it reconnects.
        if self.link != super::Link::Live {
            return false;
        }
        let mut asked = self.older_asked.borrow_mut();
        if asked.contains_key(&l.session_id) {
            return false;
        }
        asked.insert(l.session_id.clone(), s.earlier.clone());
        live.read_older(&l.session_id);
        true
    }

    /// A read is over once its session's earlier pages moved (answered, the
    /// conversation read again) or the session is gone. After updates.
    pub(super) fn settle_older(&mut self) {
        let sessions = &self.sessions;
        self.older_asked.get_mut().retain(|id, at| sessions.iter().find(|s| s.live.as_ref().is_some_and(|l| l.session_id == *id)).is_some_and(|s| s.earlier == *at));
    }

    /// The stream went down: what it was asked is lost with it.
    pub(super) fn forget_older(&mut self) {
        self.older_asked.get_mut().clear();
    }

    /// Try again, after a failed read.
    fn retry_older(&mut self, id: &Text, cx: &mut Context<Self>) {
        if let Some(s) = self.sessions.iter().find(|s| s.live.as_ref().is_some_and(|l| l.session_id == *id)) {
            self.ask_older(s);
        }
        cx.notify();
    }

    /// Where to hold the pane's view this paint: a page arrived above since
    /// it last drew the conversation. Read before `drawn` is updated.
    pub(super) fn hold(&self, pane: &Pane, s: &Session) -> Option<Hold> {
        let d = Self::drew(pane, s)?;
        let at = s.earlier.older.checked_sub(d.older).filter(|at| *at > 0 && *at < s.entries.len())?;
        // The last paint's rows: the viewport's top against the old first entry's.
        let first = pane.scroll.bounds_for_item(d.first)?;
        let shift = (pane.scroll.bounds().top() - pane.scroll.offset().y) - first.top();
        Some(Hold { at, shift })
    }

    /// Notes what the pane draws now (`first`: the row comes before the
    /// entries). True when a check must wait for this paint: the pane did
    /// not draw this conversation before, or a page just arrived.
    pub(super) fn drawing(&self, pane: &Pane, s: &Session, first: usize, held: bool) -> bool {
        let Some(l) = &s.live else {
            pane.earlier.drawn.replace(None);
            return false;
        };
        let fresh = Self::drew(pane, s).is_none();
        pane.earlier.drawn.replace(Some(Drawn { session: l.session_id.clone(), older: s.earlier.older, entries: s.entries.len(), first }));
        s.earlier.more && (fresh || held)
    }

    /// After this paint, checks the pane again with its new measures (a
    /// short conversation fills up, a page that left the view near the top
    /// brings the next).
    pub(super) fn check_after_paint(&self, pid: PaneId, cx: &mut Context<Self>) {
        let this = cx.weak_entity();
        cx.defer(move |cx| {
            this.update(cx, |c, cx| {
                if c.want_older(pid) {
                    cx.notify();
                }
            })
            .ok();
        });
    }

    /// The quiet row at the top of a transcript: earlier messages loading,
    /// or why they did not come and Try again. Empty while none is asked;
    /// none at all when nothing earlier may exist.
    pub(super) fn older_row(&self, pid: usize, s: &Session, cx: &mut Context<Self>) -> Option<AnyElement> {
        let t = &self.theme;
        let id = s.live.as_ref()?.session_id.clone();
        let asked = self.older_asked.borrow().contains_key(&id);
        if s.entries.is_empty() || !(s.earlier.more || asked || s.earlier.error.is_some()) {
            return None;
        }
        let row = div().flex_none().h(ROW).flex().items_center().justify_center().gap_2().overflow_hidden().text_size(px(11.)).text_color(t.faint);
        Some(
            match (&s.earlier.error, asked) {
                (_, true) => row.child("Loading earlier messages…"),
                (Some(e), false) => row.child(div().min_w_0().truncate().text_color(t.err).child(format!("Swarm did not return earlier messages: {e}"))).child(
                    self.button(SharedString::from(format!("older-retry-{pid}")), "Try again", false).flex_none().on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        this.retry_older(&id, cx);
                    })),
                ),
                (None, false) => row,
            }
            .into_any_element(),
        )
    }
}

/// A zero-height row at the boundary between a page that arrived and what
/// was shown, `shift` below the top of the latter. Scrolled to its top
/// (ScrollHandle::scroll_to_top_of_item, applied with this paint's layout,
/// before anything is drawn), the viewport shows what it showed. Its margins
/// and the gap after it cancel, so nothing else moves.
pub(super) fn marker(shift: Pixels) -> AnyElement {
    div().flex_none().h(px(0.)).mt(shift).mb(-(shift + GAP)).into_any_element()
}
