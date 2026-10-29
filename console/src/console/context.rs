//! Each Orchestrator conversation's context: the meter in its pane header,
//! the panel the meter (or `/context`) opens, and `/compact`. Everything shown
//! is Swarm's own reading (`crate::swarm::context`); the console keeps only
//! the latest one per conversation, read once per finished turn and after a
//! compaction, never on a timer.

use std::collections::{HashMap, HashSet};

use gpui::{AnyElement, ClickEvent, Context, SharedString, div, prelude::*, px};
use serde_json::Value;

use super::{Console, alpha, ago, now_ms};
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

/// The panel's rows for a reading at `now` (ms): what Swarm reports, and
/// what it does not.
pub(super) fn rows(r: &Reading, now: u64) -> Vec<(&'static str, String)> {
    let tokens = |n: u64| crate::swarm::map::tokens(n as f64);
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
        rows.push((
            "last request",
            format!(
                "input {} (cache read {}, cache write {}) · output {} · thinking {}",
                tokens(r.input),
                tokens(r.cache_read),
                tokens(r.cache_write),
                tokens(r.output),
                tokens(r.thinking)
            ),
        ));
    }
    if !r.model.is_empty() || r.updated_at > 0 {
        let when = if r.updated_at > 0 { format!("{} ago", ago(now.saturating_sub(r.updated_at))) } else { String::new() };
        rows.push(("measured", [r.model.as_str(), when.as_str()].iter().filter(|x| !x.is_empty()).copied().collect::<Vec<_>>().join(" · ")));
    }
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
    pub(super) fn compact(&mut self, si: usize, threshold: Option<f64>, note: String, cx: &mut Context<Self>) {
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
        let (label, color) = match self.contexts.readings.get(&id) {
            Some(Ok(r)) => {
                // At or past the threshold Swarm was given: warn.
                let low = matches!((r.threshold, r.percent()), (Some(th), Some(p)) if 100. - p <= th);
                (format!("ctx {}", r.meter()), if low { t.warn } else { t.muted })
            }
            Some(Err(_)) => ("ctx unread".to_string(), t.err),
            None => ("ctx …".to_string(), t.faint),
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
        let outcome = self.contexts.outcome.get(&id).cloned();
        let open = self.contexts.open.contains(&pid);
        if outcome.is_none() && !open {
            return None;
        }
        let compacting = self.contexts.compacting.contains(&id);
        let panel = open.then(|| {
            let body: Vec<AnyElement> = match self.contexts.readings.get(&id) {
                Some(Ok(r)) => rows(r, now_ms())
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
            let compact = if compacting {
                div().text_size(px(11.)).text_color(t.faint).child("Compacting…").into_any_element()
            } else {
                self.button(SharedString::from(format!("ctx-compact-{pid}")), "Compact now", true)
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.compact(si, None, String::new(), cx)))
                    .into_any_element()
            };
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
                    div()
                        .flex()
                        .items_center()
                        .gap_2()
                        .child(div().flex_1().text_color(t.text).child("Context"))
                        .child(compact)
                        .child(
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
        };
        let rows = rows(&r, NOW);
        let get = |k: &str| rows.iter().find(|(name, _)| *name == k).map(|(_, v)| v.clone()).unwrap_or_default();
        assert_eq!(get("in use"), "124k of 200k tokens (62%)");
        assert_eq!(get("last request"), "input 120k (cache read 90k, cache write 1k) · output 4k · thinking 500");
        assert_eq!(get("measured"), "m · 2m ago");
        assert_eq!(get("last compaction"), "manual, 3h ago (compaction 2)");
        assert!(get("auto-compact").starts_with("when 5% of the context is left"));
        assert!(get("parts").contains("totals only"));
        // Nothing measured yet: said so, with no token rows.
        let empty = Reading { window: 200000, ..Default::default() };
        let rows2 = super::rows(&empty, 0);
        assert!(rows2.iter().any(|(k, v)| *k == "in use" && v.starts_with("not measured yet")));
        assert!(!rows2.iter().any(|(k, _)| *k == "last request"));
    }
}
