//! Agents at work: the composer's bottom-row count and the Live page's
//! panel, both from the home's GET /v3/fleet/agents (swarm::agents).
//!
//! The read is Swarm's record; it is read again when the window
//! (re)connects, when a realtime frame or a fleet stream record says a run
//! started, ended or moved, and when the fleet stream (re)connects or misses
//! records. One read runs at a time; a reason to read that comes while one
//! runs reads once more when it ends (no timer). Tool events between reads
//! patch the card they name.

use gpui::{AnyElement, ClickEvent, Context, Div, Hsla, SharedString, Stateful, div, prelude::*, px};

use super::{Console, Link, Page, TextTooltip, alpha};
use crate::swarm::agents::{self, AgentsNow, Card, Finished, Indicator, Panel, PipeStep};
use crate::swarm::live_stream::{self as ls, Activity};
use crate::swarm::needs::Target;

#[derive(Default)]
pub(super) struct AtWork {
    /// Swarm's last answer; None until the first one.
    pub(super) now: Option<AgentsNow>,
    busy: bool,
    /// Asked to read while a read ran.
    dirty: bool,
    /// The last read's refusal or failure (the last answer still shows).
    error: Option<String>,
}

impl Console {
    /// Reads the fleet's agents from the home, or marks one more read due
    /// when one runs.
    pub(super) fn refresh_at_work(&mut self, cx: &mut Context<Self>) {
        if self.link == Link::Demo {
            if self.at_work.now.is_none() {
                self.at_work.now = Some(agents::demo(super::now_ms()));
            }
            return;
        }
        let Some(home) = self.home.clone() else { return };
        if self.at_work.busy {
            self.at_work.dirty = true;
            return;
        }
        self.at_work.busy = true;
        let now = super::now_ms();
        let since = agents::local_midnight(now, ls::local_offset(now));
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { agents::read(&home, since) }).await;
            let _ = this.update(cx, |c, cx| {
                c.at_work.busy = false;
                match result {
                    Ok(answer) => {
                        c.at_work.now = Some(answer);
                        c.at_work.error = None;
                    }
                    Err(e) => c.at_work.error = Some(e),
                }
                if std::mem::take(&mut c.at_work.dirty) {
                    c.refresh_at_work(cx);
                }
                cx.notify();
            });
        })
        .detach();
    }

    /// A fleet stream record: patches the card it names; a read when it
    /// moves runs or names a run the last answer lacks.
    pub(super) fn at_work_activity(&mut self, a: &Activity, cx: &mut Context<Self>) {
        let read = match self.at_work.now.as_mut() {
            Some(now) => now.apply(a),
            None => true,
        };
        if read {
            self.refresh_at_work(cx);
        }
    }

    /// Whether a card's clock should count up on screen.
    pub(super) fn at_work_ticking(&self) -> bool {
        self.page == Page::Live && self.at_work.now.as_ref().is_some_and(|n| !n.agents.is_empty())
    }

    /// The bottom row's count, once Swarm answered.
    pub(super) fn at_work_label(&self) -> Option<Indicator> {
        self.at_work.now.as_ref().map(agents::indicator)
    }

    /// The bottom row's count (`short`: "● 3"); a click opens the Live
    /// page's panel. None until Swarm answered.
    pub(super) fn at_work_indicator(&self, pid: usize, short: bool, cx: &mut Context<Self>) -> Option<Stateful<Div>> {
        let t = &self.theme;
        let label = self.at_work_label()?;
        let color = if label.active { t.ok } else { t.faint };
        let mut tip = label.full.clone();
        for m in &label.unread {
            tip.push_str(" · ");
            tip.push_str(m);
        }
        if let Some(e) = &self.at_work.error {
            tip.push_str(" · last read failed: ");
            tip.push_str(e);
        }
        let (bg, fg, border) = (t.surface, t.text, t.border);
        let hover = t.hover;
        let text = if short { label.short } else { label.full };
        Some(
            div()
                .id(SharedString::from(format!("composer-agents-{pid}")))
                .flex_none()
                .h(px(18.))
                .flex()
                .items_center()
                .px_1()
                .rounded(px(t.radius))
                .whitespace_nowrap()
                .text_color(color)
                .when(!label.unread.is_empty(), |d| d.text_color(t.warn))
                .cursor_pointer()
                .hover(move |s| s.bg(hover))
                .tooltip(move |_, cx| cx.new(|_| TextTooltip { text: tip.clone().into(), bg, fg, border }).into())
                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.go(Page::Live, cx)))
                .child(text),
        )
    }

    /// Opens what a card or a finished job is: on the home here; on a
    /// linked machine, that machine's window.
    fn open_at_work(&mut self, machine: String, target: Target, cx: &mut Context<Self>) {
        let home = self.at_work.now.as_ref().map(|n| n.home.clone()).unwrap_or_default();
        if machine == home || machine.is_empty() {
            self.open_target(target, cx);
        } else {
            self.open_machine_at(machine, Some(target), cx);
        }
    }

    fn at_work_kpi(&self, text: String, color: Hsla) -> Div {
        let t = &self.theme;
        div().flex_none().px_2().py(px(2.)).rounded(px(t.radius)).border_1().border_color(t.border).bg(t.panel).text_size(px(11.)).text_color(color).child(text)
    }

    fn at_work_pipeline(&self, steps: &[PipeStep]) -> Div {
        let t = &self.theme;
        let mut row = div().flex().flex_wrap().items_center().gap_1().text_size(px(10.5));
        for (i, step) in steps.iter().enumerate() {
            if i > 0 {
                row = row.child(div().text_color(t.faint).child("→"));
            }
            let chip = div().px_1().rounded(px(t.radius)).child(step.label.clone());
            row = row.child(if step.current {
                chip.bg(alpha(t.accent, 0.2)).border_1().border_color(alpha(t.accent, 0.7)).text_color(t.accent)
            } else if step.done {
                chip.text_color(t.ok)
            } else {
                chip.text_color(t.faint)
            });
        }
        row
    }

    fn at_work_kind_color(&self, kind: &str, state: &str) -> Hsla {
        let t = &self.theme;
        if state == "failed" {
            return t.err;
        }
        match kind {
            "read" => t.info,
            "edit" => t.accent,
            "command" => t.warn,
            "commit" => t.ok,
            _ => t.faint,
        }
    }

    fn at_work_card(&self, i: usize, card: &Card, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let hover = t.hover;
        let timeline = card.timeline.iter().map(|(kind, state)| {
            let c = self.at_work_kind_color(kind, state);
            div().w(px(8.)).h(px(8.)).rounded(px(2.)).bg(if state == "running" { c } else { alpha(c, 0.55) })
        });
        let shown = card.files.iter().take(3).map(|f| div().truncate().text_color(t.muted).child(f.clone()));
        let more = card.files.len().saturating_sub(3) as u64 + card.more_files;
        let (machine, open) = (card.machine.clone(), card.open.clone());
        div()
            .id(("at-work-card", i))
            .w(px(330.))
            .flex_none()
            .flex()
            .flex_col()
            .gap_1()
            .p_3()
            .rounded(px(t.radius + 2.))
            .border_1()
            .border_color(t.border)
            .bg(t.raised)
            .cursor_pointer()
            .hover(move |s| s.bg(hover))
            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.open_at_work(machine.clone(), open.clone(), cx)))
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .text_size(px(11.))
                    .child(div().text_color(t.accent).child(card.agent.clone()))
                    .child(div().text_color(self.machine_color(&card.machine)).child(card.machine.clone()))
                    .child(div().flex_1())
                    .child(div().text_color(t.faint).child(card.elapsed.clone())),
            )
            .child(div().truncate().text_color(t.text).child(card.title.clone()))
            .child(div().truncate().text_size(px(11.)).text_color(t.info).child(format!("now: {}", card.doing)))
            .when(!card.timeline.is_empty(), |d| d.child(div().flex().items_center().gap(px(3.)).children(timeline)))
            .child(div().text_size(px(10.5)).text_color(t.faint).child(if card.partial { format!("{} (latest events)", card.counts) } else { card.counts.clone() }))
            .when(!card.files.is_empty(), |d| {
                d.child(div().flex().flex_col().text_size(px(10.5)).children(shown).when(more > 0, |d| d.child(div().text_color(t.faint).child(format!("+{more} more")))))
            })
            .children(card.pipeline.as_ref().map(|steps| self.at_work_pipeline(steps)))
            .into_any_element()
    }

    fn at_work_finished(&self, i: usize, f: &Finished, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let hover = t.hover;
        let color = match f.state.as_str() {
            "landed" | "done" => t.ok,
            "needs_you" => t.warn,
            "stopped" => t.faint,
            _ => t.muted,
        };
        let row = div()
            .id(("at-work-finished", i))
            .flex()
            .items_center()
            .gap_2()
            .px_2()
            .py(px(3.))
            .rounded(px(t.radius))
            .text_size(px(11.))
            .child(div().flex_none().w(px(90.)).truncate().text_color(self.machine_color(&f.machine)).child(f.machine.clone()))
            .child(div().flex_none().px_1().rounded(px(t.radius)).border_1().border_color(alpha(color, 0.6)).text_color(color).child(agents::state_word(&f.state)))
            .child(div().flex_1().min_w_0().truncate().text_color(t.text).child(f.title.clone()))
            .child(div().flex_none().max_w(px(260.)).truncate().text_color(t.faint).child(f.reason.clone()));
        if f.task_id.is_empty() || f.project_id.is_empty() {
            return row.into_any_element();
        }
        let target = Target::Task { project_id: f.project_id.clone(), task_id: f.task_id.clone(), session_id: f.session_id.clone(), origin: String::new() };
        let machine = f.machine.clone();
        row.cursor_pointer().hover(move |s| s.bg(hover)).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.open_at_work(machine.clone(), target.clone(), cx))).into_any_element()
    }

    /// The panel above the Live page's stream: the header's counts, each
    /// machine, then a card per running agent, or "All quiet" with the
    /// last finished jobs.
    pub(super) fn render_at_work(&self, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let title = div().text_size(px(16.)).text_color(t.text).child("Agents at work");
        let Some(now) = self.at_work.now.as_ref() else {
            let note = self.at_work.error.clone().unwrap_or_else(|| "Reading what runs…".into());
            return div().flex().flex_col().gap_2().child(title).child(div().text_size(px(11.)).text_color(t.faint).child(note)).into_any_element();
        };
        let panel = agents::panel(now, super::now_ms());
        let header = match &panel {
            Panel::Working { header, .. } | Panel::Quiet { header, .. } => header.clone(),
        };
        let kpis = div()
            .flex()
            .flex_wrap()
            .items_center()
            .gap_1()
            .child(self.at_work_kpi(header.working, if now.agents.is_empty() { t.faint } else { t.ok }))
            .child(self.at_work_kpi(header.per_minute, t.muted))
            .child(self.at_work_kpi(header.landed, t.ok))
            .child(self.at_work_kpi(header.failed, if now.totals.failed_today > 0 { t.err } else { t.muted }));
        let machines = now.machines.iter().map(|m| {
            let (text, color) = match m.state.as_str() {
                "online" => (format!("{} · {} working", m.name, m.agents), self.machine_color(&m.name)),
                "not_shown" => (format!("{} · not shown: {}", m.name, m.reason), t.faint),
                _ => (format!("{} · unreachable: {}", m.name, m.reason), t.err),
            };
            self.at_work_kpi(text, color)
        });
        let error = self.at_work.error.as_ref().map(|e| div().text_size(px(11.)).text_color(t.warn).child(format!("Last read failed ({e}); showing the one before.")));
        let body = match &panel {
            Panel::Working { cards, .. } => div().flex().flex_wrap().gap_2().children(cards.iter().enumerate().map(|(i, c)| self.at_work_card(i, c, cx))).into_any_element(),
            Panel::Quiet { recent, .. } => div()
                .flex()
                .flex_col()
                .gap_1()
                .child(div().text_color(t.muted).child("All quiet"))
                .when(recent.is_empty(), |d| d.child(div().text_size(px(11.)).text_color(t.faint).child("Nothing finished yet.")))
                .when(!recent.is_empty(), |d| d.child(div().text_size(px(10.5)).text_color(t.faint).child("LAST FINISHED")))
                .children(recent.iter().enumerate().map(|(i, f)| self.at_work_finished(i, f, cx)))
                .into_any_element(),
        };
        div()
            .id("at-work")
            .flex()
            .flex_col()
            .gap_2()
            .max_h(px(460.))
            .overflow_y_scroll()
            .child(div().flex().flex_wrap().items_center().gap_3().child(title).child(kpis))
            .child(div().flex().flex_wrap().items_center().gap_1().child(div().text_size(px(10.5)).text_color(t.faint).child("MACHINES")).children(machines))
            .children(error)
            .child(body)
            .into_any_element()
    }
}
