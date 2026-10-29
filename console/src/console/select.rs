//! Selecting text in a conversation: press on any message text and drag, across
//! messages of the same pane if you like; ctrl-c copies it (to both
//! clipboards). Every piece of message text is a block, addressed by the
//! pane, the entry and the block's place in the entry, and a selection runs
//! from where you pressed to where you are.

use gpui::{
    Context, Hsla, InteractiveElement, IntoElement, MouseButton, MouseDownEvent, MouseMoveEvent, MouseUpEvent, ParentElement,
    SharedString, Styled, StyledText, TextRun, div,
};

use super::{Console, markdown};
use crate::data::Entry;

/// A place in a pane's text: entry, block, byte.
pub(super) type Spot = (usize, usize, usize);

#[derive(Clone, Debug, PartialEq)]
pub(super) struct Selection {
    pub pane: usize,
    pub anchor: Spot,
    pub head: Spot,
}

impl Selection {
    fn ordered(&self) -> (Spot, Spot) {
        if self.anchor <= self.head { (self.anchor, self.head) } else { (self.head, self.anchor) }
    }

    /// The selected bytes of one block, if any.
    pub fn range_in(&self, pane: usize, entry: usize, block: usize, len: usize) -> Option<(usize, usize)> {
        if pane != self.pane || self.anchor == self.head {
            return None;
        }
        let (start, end) = self.ordered();
        if (entry, block) < (start.0, start.1) || (entry, block) > (end.0, end.1) {
            return None;
        }
        let from = if (entry, block) == (start.0, start.1) { start.2 } else { 0 };
        let to = if (entry, block) == (end.0, end.1) { end.2 } else { len };
        (from < to).then_some((from.min(len), to.min(len)))
    }
}

/// The text of each block of an entry, in the order it is drawn.
pub(super) fn blocks(entry: &Entry) -> Vec<String> {
    match entry {
        Entry::User(text) | Entry::Queued { text, .. } | Entry::Notice(text) => vec![text.to_string()],
        Entry::Assistant(text) => markdown::parse(text).into_iter().map(|b| b.text).collect(),
        // Name, then summary and time (row one), then the detail (row two).
        Entry::Tool { .. } => entry.tool_stack(crate::data::now_ms()).map(|s| vec![s.name, s.tail, s.detail]).unwrap_or_default(),
    }
}

/// The selected text of `entries`, blocks joined by blank lines.
pub(super) fn selected_text(selection: &Selection, entries: &[Entry]) -> String {
    let (start, end) = selection.ordered();
    let mut parts = Vec::new();
    for ei in start.0..=end.0.min(entries.len().saturating_sub(1)) {
        for (bi, text) in blocks(&entries[ei]).iter().enumerate() {
            if let Some((from, to)) = selection.range_in(selection.pane, ei, bi, text.len()) {
                let (from, to) = (floor(text, from), floor(text, to));
                parts.push(text[from..to].to_string());
            }
        }
    }
    parts.join("\n\n")
}

/// The nearest character boundary at or before `at`.
fn floor(text: &str, mut at: usize) -> usize {
    at = at.min(text.len());
    while !text.is_char_boundary(at) {
        at -= 1;
    }
    at
}

/// Splits runs so a run boundary falls at `at`.
fn split(runs: Vec<TextRun>, at: usize) -> Vec<TextRun> {
    let mut out = Vec::with_capacity(runs.len() + 1);
    let mut start = 0;
    for run in runs {
        let end = start + run.len;
        if at > start && at < end {
            out.push(TextRun { len: at - start, ..run.clone() });
            out.push(TextRun { len: end - at, ..run });
        } else {
            out.push(run);
        }
        start = end;
    }
    out
}

/// Paints bytes `from..to` of the runs with the selection color.
pub(super) fn highlight(runs: Vec<TextRun>, from: usize, to: usize, color: Hsla) -> Vec<TextRun> {
    let mut runs = split(split(runs, from), to);
    let mut start = 0;
    for run in runs.iter_mut() {
        if start >= from && start + run.len <= to {
            run.background_color = Some(color);
        }
        start += run.len;
    }
    runs
}

impl Console {
    /// One block of message text that can be selected: its runs, with the
    /// selection painted.
    pub(super) fn selectable(
        &self,
        pane: usize,
        entry: usize,
        block: usize,
        text: SharedString,
        runs: Vec<TextRun>,
        links: Vec<(std::ops::Range<usize>, String)>,
        cx: &mut Context<Self>,
    ) -> impl IntoElement {
        let runs = match self.selection.as_ref().and_then(|s| s.range_in(pane, entry, block, text.len())) {
            Some((from, to)) => highlight(runs, from, to, super::alpha(self.theme.accent, 0.35)),
            None => runs,
        };
        let styled = StyledText::new(text).with_runs(runs);
        let (press, drag, release) = (styled.layout().clone(), styled.layout().clone(), styled.layout().clone());
        div()
            .id(SharedString::from(format!("text-{pane}-{entry}-{block}")))
            .cursor_text()
            // A click on a link (pressed and released in place) opens it in
            // the browser; a drag over it selects text as anywhere else.
            .on_mouse_up(
                MouseButton::Left,
                cx.listener(move |this, e: &MouseUpEvent, _, cx| {
                    let pressed_here = this.selection.as_ref().is_some_and(|s| s.pane == pane && s.anchor == s.head && s.anchor.0 == entry && s.anchor.1 == block);
                    if !pressed_here {
                        return;
                    }
                    let at = release.index_for_position(e.position).unwrap_or_else(|i| i);
                    if let Some((_, url)) = links.iter().find(|(r, _)| r.contains(&at)) {
                        cx.open_url(url);
                    }
                }),
            )
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(move |this, e: &MouseDownEvent, window, cx| {
                    let at = press.index_for_position(e.position).unwrap_or_else(|i| i);
                    this.selection = Some(Selection { pane, anchor: (entry, block, at), head: (entry, block, at) });
                    this.selecting = true;
                    // ctrl-c copies the selection, not a composer's text.
                    window.focus(&this.focus);
                    cx.notify();
                }),
            )
            .on_mouse_move(cx.listener(move |this, e: &MouseMoveEvent, _, cx| {
                if e.pressed_button != Some(MouseButton::Left) {
                    this.selecting = false;
                    return;
                }
                if !this.selecting {
                    return;
                }
                let Some(selection) = this.selection.as_mut().filter(|s| s.pane == pane) else { return };
                let at = drag.index_for_position(e.position).unwrap_or_else(|i| i);
                if selection.head != (entry, block, at) {
                    selection.head = (entry, block, at);
                    cx.notify();
                }
            }))
            .child(styled)
    }

    /// ctrl-c: the selected text to both clipboards.
    pub(super) fn copy_selection(&mut self, cx: &mut Context<Self>) {
        let Some(selection) = self.selection.clone() else { return };
        let Some(pane) = self.panes.iter().find(|p| p.id.0 == selection.pane) else { return };
        let s = &self.sessions[pane.session];
        let mut entries = s.entries.clone();
        entries.extend(s.outbox.iter().map(|(text, _)| Entry::User(text.clone())));
        let text = selected_text(&selection, &entries);
        if text.is_empty() {
            return;
        }
        cx.write_to_clipboard(gpui::ClipboardItem::new_string(text.clone()));
        cx.write_to_primary(gpui::ClipboardItem::new_string(text));
        self.notice = Some(("Copied.".into(), false));
        cx.notify();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_selection_across_entries() {
        let entries = vec![
            Entry::User("Fix the README".into()),
            Entry::Assistant("Done.\n\n- added a line\n- ran tests".into()),
            Entry::tool("read", "README.md"),
        ];
        // From "the" in the first message to "added" in the second's list.
        let selection = Selection { pane: 0, anchor: (1, 1, 5), head: (0, 0, 4) };
        assert_eq!(selected_text(&selection, &entries), "the README\n\nDone.\n\nadded");
        let same = Selection { pane: 0, anchor: (2, 0, 0), head: (2, 0, 4) };
        assert_eq!(selected_text(&same, &entries), "read");
        assert_eq!(same.range_in(1, 2, 0, 14), None, "another pane");
        // The stacked tool call: its name, then its detail on row two.
        let whole = Selection { pane: 0, anchor: (2, 0, 0), head: (2, 2, 9) };
        assert_eq!(selected_text(&whole, &entries), "read\n\nREADME.md");
    }

    #[test]
    fn highlight_splits_runs() {
        let run = |len| TextRun { len, font: gpui::font("x"), color: Hsla::default(), background_color: None, underline: None, strikethrough: None };
        let runs = highlight(vec![run(5), run(5)], 3, 7, Hsla::red());
        assert_eq!(runs.iter().map(|r| (r.len, r.background_color.is_some())).collect::<Vec<_>>(), vec![(3, false), (2, true), (2, true), (3, false)]);
    }
}

impl Console {
    /// One run of message text in the mono font.
    pub(super) fn plain_run(&self, len: usize, color: Hsla) -> TextRun {
        TextRun { len, font: gpui::font(self.theme.mono), color, background_color: None, underline: None, strikethrough: None }
    }
}
