//! Each Orchestrator conversation's context: the meter in its pane header,
//! the panel the meter (or `/context`) opens, and `/compact`. Everything shown
//! is Swarm's own reading (`crate::swarm::context`); the console keeps only
//! the latest one per conversation, read once per finished turn and after a
//! compaction, never on a timer.
//!
//! The Companion is not a chat with a growing window: each of its turns is
//! rebuilt from a bounded tail of recent messages plus a bounded block
//! retrieved from its context bank, and Swarm refuses to compact it
//! (run.ErrCompanionCompactionRefused). So its panel names the reading for
//! what it is, the last measured request, and offers no compaction, no
//! threshold and no compaction outcome. Which panel a conversation gets is
//! decided by Swarm's identity (`Mode`), never by a title or the page.

use std::collections::{HashMap, HashSet};

use gpui::{AnyElement, ClickEvent, Context, SharedString, div, prelude::*, px};
use serde_json::Value;

use super::{Console, ago, alpha, now_ms};
use crate::data::RunState;
use crate::swarm::context::{self as swarm_context, Reading};
use crate::swarm::live::Command;

#[derive(Default)]
pub(super) struct Contexts {
    /// Swarm's latest reading per conversation (by session id), or why it
    /// could not be read.
    readings: HashMap<String, Result<Reading, String>>,
    /// The run-clock stamp (last message time) each was last read at.
    asked: HashMap<String, u64>,
    /// Panes whose context panel is open.
    open: HashSet<usize>,
    /// Conversations compacting now.
    compacting: HashSet<String>,
    /// The last compaction's line per conversation (text, is an error).
    outcome: HashMap<String, (SharedString, bool)>,
}

/// Which context a conversation has: an ordinary chat's cumulative window
/// (compacted on request or at a threshold), or the Companion's bounded,
/// per-turn context (never compacted).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Mode {
    Chat,
    Companion,
}

/// The mode from Swarm's identity only: the session Swarm named as the
/// Companion (GET /v3/companion, matched by session id), or the protected
/// `swarm_v3_companion` marker on the session detail the reading came from.
/// Either is enough; a title, the open page or a pane is never consulted.
pub(super) fn mode_of(named_companion: bool, reading: Option<&Reading>) -> Mode {
    if named_companion || reading.is_some_and(|r| r.companion) { Mode::Companion } else { Mode::Chat }
}

impl Mode {
    /// Offers "Compact now", the auto-compact threshold and a compaction's
    /// outcome line. Swarm refuses every compaction of a Companion.
    pub(super) fn compacts(self) -> bool {
        self == Mode::Chat
    }
}

/// What the console says when a Companion is asked to compact (`/compact`,
/// or any other path), in place of sending the request.
pub(super) const COMPANION_NO_COMPACT: &str = "The Companion is not compacted: each turn is built from a bounded set of recent messages plus context retrieved for it, so its long history does not pile up in the model's window. /context shows its last request.";

/// The meter's text for a mode. A chat's is its window in use; the
/// Companion's is its last measured request, which the next turn does not
/// add to.
pub(super) fn meter(r: &Reading, mode: Mode) -> String {
    match mode {
        Mode::Chat => format!("ctx {}", r.meter()),
        Mode::Companion => {
            let window = if r.window > 0 { crate::swarm::map::tokens(r.window as f64) } else { "?".into() };
            match (r.used.filter(|_| !r.reset()), r.percent()) {
                (Some(used), Some(p)) => format!("last req {p:.0}% · {} / {window}", crate::swarm::map::tokens(used as f64)),
                (Some(used), None) => format!("last req {} / {window}", crate::swarm::map::tokens(used as f64)),
                (None, _) => format!("last req — / {window}"),
            }
        }
    }
}

/// The panel's rows for a reading at `now` (ms) in a mode: what Swarm
/// reports, and what it does not.
pub(super) fn rows(r: &Reading, now: u64, mode: Mode) -> Vec<(&'static str, String)> {
    match mode {
        Mode::Chat => chat_rows(r, now),
        Mode::Companion => companion_rows(r, now),
    }
}

fn tokens(n: u64) -> String {
    crate::swarm::map::tokens(n as f64)
}

fn measured(r: &Reading, now: u64) -> Option<(&'static str, String)> {
    (!r.model.is_empty() || r.updated_at > 0).then(|| {
        let when = if r.updated_at > 0 { format!("{} ago", ago(now.saturating_sub(r.updated_at))) } else { String::new() };
        ("measured", [r.model.as_str(), when.as_str()].iter().filter(|x| !x.is_empty()).copied().collect::<Vec<_>>().join(" · "))
    })
}

fn breakdown(r: &Reading) -> String {
    format!(
        "input {} (cache read {}, cache write {}) · output {} · thinking {}",
        tokens(r.input),
        tokens(r.cache_read),
        tokens(r.cache_write),
        tokens(r.output),
        tokens(r.thinking)
    )
}

/// The Companion's rows: the last measured request, labelled as such, and
/// how a turn is built. No compaction, threshold or checkpoint row: Swarm
/// never compacts a Companion, whatever its metadata or messages hold.
fn companion_rows(r: &Reading, now: u64) -> Vec<(&'static str, String)> {
    let window = if r.window > 0 { tokens(r.window) } else { "an unknown window".into() };
    let used = r.used.filter(|_| !r.reset());
    let mut rows = vec![(
        "last request",
        match (used, r.percent()) {
            (Some(used), Some(p)) => format!("{} of {window} tokens ({p:.0}%) in the latest measured request", tokens(used)),
            (Some(used), None) => format!("{} tokens of {window} in the latest measured request", tokens(used)),
            (None, _) => format!("unknown: Swarm has not measured a request with this model yet ({window} window)"),
        },
    )];
    if used.is_some() {
        rows.push(("its tokens", breakdown(r)));
    }
    rows.extend(measured(r, now));
    rows.push(("next request", "unknown until it is sent; it is built afresh, not added to the last one".into()));
    rows.push((
        "each turn",
        "a bounded tail of recent messages plus a bounded block retrieved from the Companion's context bank (focus, decisions, related earlier topics, work pointers); smaller fixed sizes if the model reports an overflow. Older messages stay stored but are not all sent, and nothing is compacted.".into(),
    ));
    rows.push(("parts", "Swarm reports request totals only, not what the recent messages, the context bank, the prompt or the tool definitions each take.".into()));
    rows
}

fn chat_rows(r: &Reading, now: u64) -> Vec<(&'static str, String)> {
    let mut rows = Vec::new();
    let window = if r.window > 0 { tokens(r.window) } else { "an unknown window".into() };
    rows.push((
        "in use",
        match (r.used, r.percent()) {
            _ if r.reset() => format!("reset by the last compaction; Swarm measures it on the next turn ({window} window)"),
            (Some(used), Some(p)) => format!("{} of {window} tokens ({p:.0}%)", tokens(used)),
            (Some(used), None) => format!("{} tokens of {window}", tokens(used)),
            (None, _) => format!("not measured yet; Swarm measures it on the next turn ({window} window)"),
        },
    ));
    if r.used.is_some() && !r.reset() {
        rows.push(("last request", breakdown(r)));
    }
    rows.extend(measured(r, now));
    rows.push((
        "last compaction",
        match &r.checkpoint {
            Some(c) => {
                let mut line = format!("{} ago", ago(now.saturating_sub(c.at)));
                if !c.origin.is_empty() {
                    line = format!("{}, {line}", c.origin);
                }
                if c.index > 0 {
                    line.push_str(&format!(" (compaction {})", c.index));
                }
                line
            }
            None => "none among the recent messages".into(),
        },
    ));
    rows.push((
        "auto-compact",
        match r.threshold {
            Some(t) => format!("when {t}% of the context is left (/compact {t}% changes it, /compact 0% turns it off)"),
            None => "not set (/compact 5% sets it)".into(),
        },
    ));
    rows.push(("parts", "Swarm reports totals only, not what the prompt, project.md, task list, memory, history or tool output each take.".into()));
    rows
}

impl Console {
    fn context_id(&self, si: usize) -> Option<String> {
        self.sessions.get(si)?.live.as_ref().map(|l| l.session_id.to_string()).filter(|id| !id.is_empty())
    }

    /// The conversation's context mode, from Swarm's identity (see mode_of)
    /// and the reading already held: no read is started here.
    pub(super) fn context_mode(&self, si: usize) -> Mode {
        let reading = self.context_id(si).and_then(|id| self.contexts.readings.get(&id).and_then(|r| r.as_ref().ok()));
        mode_of(self.is_companion(si), reading)
    }

    /// Reads the context of each conversation open in a pane that is not
    /// running and changed since it was last read.
    pub(super) fn want_contexts(&mut self) {
        let Some(live) = &self.live else { return };
        for pane in &self.panes {
            let s = &self.sessions[pane.session];
            let Some(id) = s.live.as_ref().map(|l| l.session_id.to_string()).filter(|id| !id.is_empty()) else { continue };
            if s.state == RunState::Running || self.contexts.compacting.contains(&id) {
                continue;
            }
            let stamp = s.run.map(|r| r.last_message_at).unwrap_or(0);
            if self.contexts.asked.get(&id) == Some(&stamp) {
                continue;
            }
            self.contexts.asked.insert(id.clone(), stamp);
            live.run(Command::ReadContext { session_id: id });
        }
    }

    /// Is the pane's context panel open?
    pub(super) fn contexts_open(&self, pid: usize) -> bool {
        self.contexts.open.contains(&pid)
    }

    /// Opens or closes a pane's context panel (its meter, or `/context`),
    /// reading the context again on opening.
    pub(super) fn toggle_context(&mut self, pid: usize, cx: &mut Context<Self>) {
        if !self.contexts.open.remove(&pid) {
            self.contexts.open.insert(pid);
            let si = self.panes.iter().find(|p| p.id.0 == pid).map(|p| p.session);
            if let Some(id) = si.and_then(|si| self.context_id(si)) {
                self.contexts.asked.remove(&id);
            }
            self.want_contexts();
        }
        cx.notify();
    }

    /// `/compact [N%] [note]`, or the panel's "compact now": Swarm compacts
    /// the conversation, after setting its auto-compact threshold when one is given.
    /// A Companion is never compacted: the request is not sent, and the
    /// person is told why.
    pub(super) fn compact(&mut self, si: usize, threshold: Option<f64>, note: String, cx: &mut Context<Self>) {
        if !self.context_mode(si).compacts() {
            self.notice = Some((COMPANION_NO_COMPACT.into(), false));
            cx.notify();
            return;
        }
        let Some(id) = self.context_id(si) else {
            self.notice = Some(("This session has not started.".into(), true));
            cx.notify();
            return;
        };
        if self.sessions[si].state == RunState::Running {
            self.notice = Some(("Swarm compacts between runs: stop the run or wait for it to finish.".into(), true));
            cx.notify();
            return;
        }
        let Some(live) = &self.live else { return };
        if !self.contexts.compacting.insert(id.clone()) {
            return;
        }
        live.run(Command::Compact { session_id: id.clone(), note, threshold });
        self.contexts.outcome.remove(&id);
        self.acting = Some(si);
        self.failure = None;
        self.notice = Some(("Compacting…".into(), false));
        cx.notify();
    }

    /// A context read or a compaction finished; the answer names its session.
    pub(super) fn context_done(&mut self, what: &str, answer: Value) {
        let id = answer.get("session_id").and_then(Value::as_str).unwrap_or("").to_string();
        let error = answer.get("error").and_then(Value::as_str).map(str::to_string);
        if what == "context" {
            let reading = match error {
                Some(e) => Err(e),
                None => Ok(swarm_context::reading(answer.get("detail").unwrap_or(&Value::Null))),
            };
            self.contexts.readings.insert(id, reading);
            return;
        }
        self.contexts.compacting.remove(&id);
        let si = (0..self.sessions.len()).find(|i| self.context_id(*i).as_deref() == Some(id.as_str()));
        match error {
            Some(e) => {
                let text: SharedString = format!("Could not compact: {e}").into();
                if let Some(si) = si {
                    self.failure = Some((si, text.clone()));
                }
                self.contexts.outcome.insert(id, (text.clone(), true));
                self.notice = Some((text, true));
            }
            None => {
                let text: SharedString = swarm_context::outcome(&answer).into();
                if let Some(after) = answer.get("after").filter(|v| v.is_object()) {
                    self.contexts.readings.insert(id.clone(), Ok(swarm_context::reading(after)));
                }
                self.contexts.outcome.insert(id, (text.clone(), false));
                self.notice = Some((text, false));
            }
        }
    }

    /// The pane header's meter: used / window and percent, from Swarm's
    /// latest reading. A click opens the panel.
    pub(super) fn context_meter(&self, si: usize, pid: usize, cx: &mut Context<Self>) -> Option<AnyElement> {
        let id = self.context_id(si)?;
        let t = &self.theme;
        let mode = self.context_mode(si);
        let prefix = if mode == Mode::Companion { "last req" } else { "ctx" };
        let (label, color) = match self.contexts.readings.get(&id) {
            Some(Ok(r)) => {
                // At or past the threshold Swarm was given: warn. A
                // Companion has no threshold, whatever its metadata says.
                let low = mode.compacts() && matches!((r.threshold, r.percent()), (Some(th), Some(p)) if 100. - p <= th);
                (meter(r, mode), if low { t.warn } else { t.muted })
            }
            Some(Err(_)) => (format!("{prefix} unread"), t.err),
            None => (format!("{prefix} …"), t.faint),
        };
        let open = self.contexts.open.contains(&pid);
        let hover = t.hover;
        Some(
            div()
                .id(SharedString::from(format!("ctx-meter-{pid}")))
                .flex_none()
                .px_1()
                .rounded(px(t.radius))
                .cursor_pointer()
                .text_size(px(11.))
                .text_color(color)
                .when(open, |d| d.bg(alpha(t.accent, 0.12)))
                .hover(move |s| s.bg(hover))
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.toggle_context(pid, cx)))
                .child(label)
                .into_any_element(),
        )
    }

    /// Above the composer: the last compaction's line, and the context panel
    /// when it is open.
    pub(super) fn render_context(&self, si: usize, pid: usize, cx: &mut Context<Self>) -> Option<AnyElement> {
        let id = self.context_id(si)?;
        let t = &self.theme;
        let mode = self.context_mode(si);
        // A Companion shows no compaction outcome: none can have happened.
        let outcome = self.contexts.outcome.get(&id).cloned().filter(|_| mode.compacts());
        let open = self.contexts.open.contains(&pid);
        if outcome.is_none() && !open {
            return None;
        }
        let compacting = self.contexts.compacting.contains(&id);
        let panel = open.then(|| {
            let body: Vec<AnyElement> = match self.contexts.readings.get(&id) {
                Some(Ok(r)) => rows(r, now_ms(), mode)
                    .into_iter()
                    .map(|(k, v)| {
                        div()
                            .flex()
                            .gap_3()
                            .child(div().w(px(104.)).flex_none().text_color(t.faint).child(k))
                            .child(div().flex_1().min_w_0().text_color(t.text).child(v))
                            .into_any_element()
                    })
                    .collect(),
                Some(Err(e)) => vec![div().text_color(t.err).child(format!("Could not read the context: {e}")).into_any_element()],
                None => vec![div().text_color(t.faint).child("Reading the context…").into_any_element()],
            };
            let compact = if !mode.compacts() {
                None
            } else if compacting {
                Some(div().text_size(px(11.)).text_color(t.faint).child("Compacting…").into_any_element())
            } else {
                Some(
                    self.button(SharedString::from(format!("ctx-compact-{pid}")), "Compact now", true)
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.compact(si, None, String::new(), cx)))
                        .into_any_element(),
                )
            };
            let title = if mode == Mode::Companion { "Companion context: last request" } else { "Context" };
            div()
                .flex()
                .flex_col()
                .gap_1()
                .p_2()
                .rounded(px(t.radius))
                .border_1()
                .border_color(t.border)
                .bg(t.panel)
                .text_size(px(11.5))
                .child(
                    div().flex().items_center().gap_2().child(div().flex_1().text_color(t.text).child(title)).children(compact).child(
                        self.button(SharedString::from(format!("ctx-close-{pid}")), "close", false)
                            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.toggle_context(pid, cx))),
                    ),
                )
                .children(body)
        });
        Some(
            div()
                .flex_none()
                .mx_2()
                .mb_1()
                .flex()
                .flex_col()
                .gap_1()
                .children(outcome.map(|(text, bad)| div().px_1().text_size(px(11.5)).text_color(if bad { t.err } else { t.muted }).child(text)))
                .children(panel)
                .into_any_element(),
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::context::Checkpoint;

    const NOW: u64 = 1_792_540_800_000;

    /// A measured reading, with a threshold and a checkpoint as a chat's
    /// session detail may carry them; `companion` is Swarm's marker.
    fn measured_reading(companion: bool) -> Reading {
        Reading {
            window: 200000,
            used: Some(124000),
            input: 120000,
            output: 4000,
            cache_read: 90000,
            cache_write: 1000,
            thinking: 500,
            model: "m".into(),
            source: "anthropic_api_usage".into(),
            updated_at: NOW - 120_000,
            threshold: Some(5.),
            checkpoint: Some(Checkpoint { at: NOW - 3 * 3_600_000, origin: "manual".into(), index: 2 }),
            companion,
        }
    }

    fn get(rows: &[(&'static str, String)], k: &str) -> Option<String> {
        rows.iter().find(|(name, _)| *name == k).map(|(_, v)| v.clone())
    }

    /// Requirement: a Companion's context panel describes a bounded,
    /// per-turn request, never a chat's compaction. Swarm refuses every
    /// Companion compaction (run.ErrCompanionCompactionRefused), so the panel
    /// offers no Compact now, no auto-compact threshold or /compact advice
    /// and no last-compaction row, even when the generic session detail
    /// (read_context telemetry) still carries a threshold in the metadata or
    /// a checkpoint among the messages. The reading is labelled as the last
    /// measured request, the next request is unknown rather than inferred,
    /// and no per-part token figure is invented. Threat: the regression this
    /// fixes (a Companion panel that advertised an action Swarm refuses and
    /// implied a window that fills forever), or the reading's own fields
    /// switching that advice back on. Boundary: mode_of, Mode::compacts,
    /// rows and meter, the pure choices context_meter, render_context,
    /// compact and slash branch on; they read only the cached Reading, so
    /// the panel starts no read of history, memory or the context bank.
    /// Narrowest layer: those choices (no GPUI render harness in this crate;
    /// the drawn panel itself is unverified here).
    #[test]
    fn companion_context_never_offers_compaction() {
        // The generic reading carries the marker, a threshold and a checkpoint.
        let r = measured_reading(true);
        let mode = mode_of(false, Some(&r));
        assert_eq!(mode, Mode::Companion, "Swarm's marker on the reading is enough");
        assert!(!mode.compacts(), "no Compact now, threshold or outcome for a Companion");
        let rows = rows(&r, NOW, mode);
        for (k, v) in &rows {
            let text = format!("{k} {v}").to_lowercase();
            assert!(!text.contains("/compact") && !text.contains("auto-compact") && !text.contains("compaction"), "row {k}: {v}");
        }
        assert!(get(&rows, "last compaction").is_none() && get(&rows, "auto-compact").is_none() && get(&rows, "in use").is_none());
        assert_eq!(get(&rows, "last request").as_deref(), Some("124k of 200k tokens (62%) in the latest measured request"));
        assert_eq!(get(&rows, "its tokens").as_deref(), Some("input 120k (cache read 90k, cache write 1k) · output 4k · thinking 500"));
        assert_eq!(get(&rows, "measured").as_deref(), Some("m · 2m ago"));
        assert!(get(&rows, "next request").is_some_and(|v| v.starts_with("unknown")), "the next request is not estimated");
        let turn = get(&rows, "each turn").unwrap_or_default();
        assert!(turn.contains("bounded tail of recent messages") && turn.contains("context bank"), "{turn}");
        // No invented sizes or per-part figures: only the reading's numbers appear.
        for k in ["next request", "each turn", "parts"] {
            assert!(!get(&rows, k).unwrap_or_default().chars().any(|c| c.is_ascii_digit()), "{k} carries a figure Swarm did not report");
        }
        assert_eq!(meter(&r, mode), "last req 62% · 124k / 200k");
        // Nothing measured: unknown, with no token rows and no inferred value.
        let empty = Reading { window: 200000, companion: true, ..Default::default() };
        let rows = super::rows(&empty, NOW, Mode::Companion);
        assert!(get(&rows, "last request").is_some_and(|v| v.starts_with("unknown")));
        assert!(get(&rows, "its tokens").is_none());
        assert_eq!(meter(&empty, Mode::Companion), "last req — / 200k");
        // A reset left by some earlier compaction is not presented as one.
        let reset = Reading { source: crate::swarm::context::RESET_SOURCE.into(), used: Some(0), ..measured_reading(true) };
        assert_eq!(meter(&reset, Mode::Companion), "last req — / 200k");
        let reset_rows = super::rows(&reset, NOW, Mode::Companion);
        assert!(get(&reset_rows, "last request").is_some_and(|v| v.starts_with("unknown")), "a reset is not a measured request");
        assert!(!rows_text(&reset_rows).contains("compaction") && !rows_text(&reset_rows).contains("reset"));
        // The same choice made twice from the same cached reading is the same:
        // nothing here reads anything new.
        assert_eq!(super::rows(&r, NOW, mode), super::rows(&r, NOW, mode));
    }

    fn rows_text(rows: &[(&'static str, String)]) -> String {
        rows.iter().map(|(k, v)| format!("{k} {v}\n")).collect::<String>().to_lowercase()
    }

    /// Requirement: the mode comes from Swarm's identity only (the session
    /// GET /v3/companion named, by id, or the protected swarm_v3_companion
    /// marker on the session detail), so the Companion gets its panel in any
    /// pane that shows it, including the generic pane before its first
    /// reading, while an ordinary chat titled "Companion" keeps the chat
    /// panel and its compaction. Threat: title spoofing turning compaction
    /// off for a chat, or the Companion falling back to the chat panel when
    /// shown outside its page. Boundary: names_companion (Console::
    /// is_companion), swarm::context::reading and mode_of.
    #[test]
    fn the_mode_comes_from_identity_not_title() {
        use crate::data::LiveRef;
        use crate::swarm::context::reading;
        use serde_json::json;
        let live = |id: &'static str| LiveRef { task_id: "".into(), session_id: id.into() };
        let named = super::super::companion::names_companion;
        assert!(named(Some("cmp"), Some(&live("cmp"))));
        assert!(!named(Some("cmp"), Some(&live("chat"))));
        assert!(!named(None, Some(&live("cmp"))), "no Companion named yet");
        assert!(!named(Some(""), Some(&live(""))), "an empty id names nothing");
        assert!(!named(Some("cmp"), None));
        // Before any reading, the named session is the Companion.
        assert_eq!(mode_of(true, None), Mode::Companion);
        // A chat titled "Companion", with a threshold: still a chat.
        let spoof = reading(&json!({"session": {"id": "x", "title": "Companion", "metadata": {"context_compaction_threshold_percent": 5}}, "context_window": 1000}));
        assert!(!spoof.companion);
        assert_eq!(mode_of(false, Some(&spoof)), Mode::Chat);
        assert!(mode_of(false, Some(&spoof)).compacts());
        // The marker, whatever the title.
        let marked =
            reading(&json!({"session": {"id": "c", "title": "Anything", "metadata": {"swarm_v3_companion": {"generation": 1}, "context_compaction_threshold_percent": 5}}}));
        assert!(marked.companion);
        assert_eq!(mode_of(false, Some(&marked)), Mode::Companion);
        // A null marker is no marker.
        let null = reading(&json!({"session": {"id": "n", "metadata": {"swarm_v3_companion": null}}}));
        assert_eq!(mode_of(false, Some(&null)), Mode::Chat);
    }

    #[test]
    fn the_panel_lists_what_swarm_reports() {
        let r = Reading {
            window: 200000,
            used: Some(124000),
            input: 120000,
            output: 4000,
            cache_read: 90000,
            cache_write: 1000,
            thinking: 500,
            model: "m".into(),
            source: "anthropic_api_usage".into(),
            updated_at: NOW - 120_000,
            threshold: Some(5.),
            checkpoint: Some(Checkpoint { at: NOW - 3 * 3_600_000, origin: "manual".into(), index: 2 }),
            companion: false,
        };
        // An ordinary chat keeps its window, compaction and threshold.
        assert_eq!(mode_of(false, Some(&r)), Mode::Chat);
        assert!(Mode::Chat.compacts());
        assert_eq!(meter(&r, Mode::Chat), "ctx 62% · 124k / 200k");
        let rows = rows(&r, NOW, Mode::Chat);
        let get = |k: &str| rows.iter().find(|(name, _)| *name == k).map(|(_, v)| v.clone()).unwrap_or_default();
        assert_eq!(get("in use"), "124k of 200k tokens (62%)");
        assert_eq!(get("last request"), "input 120k (cache read 90k, cache write 1k) · output 4k · thinking 500");
        assert_eq!(get("measured"), "m · 2m ago");
        assert_eq!(get("last compaction"), "manual, 3h ago (compaction 2)");
        assert!(get("auto-compact").starts_with("when 5% of the context is left"));
        assert!(get("parts").contains("totals only"));
        // Nothing measured yet: said so, with no token rows.
        let empty = Reading { window: 200000, ..Default::default() };
        let rows2 = super::rows(&empty, 0, Mode::Chat);
        assert!(rows2.iter().any(|(k, v)| *k == "in use" && v.starts_with("not measured yet")));
        assert!(!rows2.iter().any(|(k, _)| *k == "last request"));
    }
}
