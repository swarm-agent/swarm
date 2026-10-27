//! The Live page (console mode): everything happening across the fleet, one
//! compact line per event, newest at the bottom. It reads the home's merged
//! GET /v3/fleet/activity/stream (swarm::live_stream); the connection is
//! open only while this page shows in the home's window, and its
//! per-machine cursors stay in memory so a reconnect, or opening the page
//! again, resumes where it was.

use std::ops::Range;
use std::rc::Rc;

use gpui::{
    AnyElement, ClickEvent, Context, Div, Hsla, ScrollStrategy, SharedString, Stateful, StyledText, UniformListScrollHandle, Window, div,
    point, prelude::*, px, uniform_list,
};

use super::{Console, Link, Page, alpha};
use crate::swarm::live_stream::{self as ls, Connection, Event, Feed, Filter, Kind, Open};
use crate::swarm::needs::Target;

/// One line's height; the list is uniform.
const ROW: f32 = 20.;

/// Where the connection stands, as the page says it.
#[derive(Clone, Debug, PartialEq)]
pub(super) enum Stream {
    Closed,
    Connecting,
    Streaming,
    Retrying { reason: String, secs: u64 },
    /// Swarm refused the stream; tried again on "retry" or the next visit.
    Stopped(String),
}

pub(super) struct LivePage {
    feed: Feed,
    filter: Filter,
    conn: Option<Connection>,
    task: Option<gpui::Task<()>>,
    /// Events from an older connection are ignored.
    generation: u64,
    stream: Stream,
    scroll: UniformListScrollHandle,
}

impl Default for LivePage {
    fn default() -> Self {
        LivePage { feed: Feed::default(), filter: Filter::default(), conn: None, task: None, generation: 0, stream: Stream::Closed, scroll: UniformListScrollHandle::new() }
    }
}

impl Console {
    /// Why this window shows no stream, if it does not.
    fn live_note(&self) -> Option<&'static str> {
        if self.via.is_some() {
            return Some("The fleet's live stream lives on the home: open Live in the home's window.");
        }
        match &self.link {
            Link::Demo => None,
            _ if self.home.is_none() => Some("Connecting to Swarm…"),
            _ => None,
        }
    }

    /// Opens the stream while the page shows and closes it when it does not
    /// (called on every render, so every way of changing pages counts).
    pub(super) fn sync_live_page(&mut self, cx: &mut Context<Self>) {
        if self.link == Link::Demo {
            if self.page == Page::Live && self.live_page.feed.lines().is_empty() {
                demo(&mut self.live_page.feed, super::now_ms());
            }
            return;
        }
        let want = self.page == Page::Live && self.live_note().is_none();
        let p = &self.live_page;
        if want && p.conn.is_none() && matches!(p.stream, Stream::Closed) {
            self.open_live_stream(cx);
        } else if !want && (p.conn.is_some() || p.stream != Stream::Closed) {
            self.close_live_stream();
        }
    }

    fn open_live_stream(&mut self, cx: &mut Context<Self>) {
        let Some(home) = self.home.clone() else { return };
        let p = &mut self.live_page;
        p.generation += 1;
        let generation = p.generation;
        let (conn, rx) = Connection::open(home.base(), home.token(), p.feed.cursors.clone());
        p.conn = Some(conn);
        p.stream = Stream::Connecting;
        // Wakes on every event the connection thread sends; ends with it.
        p.task = Some(cx.spawn(async move |this, cx| {
            let mut rx = rx;
            loop {
                let (back, batch) = cx
                    .background_executor()
                    .spawn(async move {
                        let mut batch = Vec::new();
                        if let Ok(first) = rx.recv() {
                            batch.push(first);
                            batch.extend(rx.try_iter());
                        }
                        (rx, batch)
                    })
                    .await;
                rx = back;
                if batch.is_empty() {
                    return;
                }
                if this.update(cx, |c, cx| c.live_events(generation, batch, cx)).is_err() {
                    return;
                }
            }
        }));
    }

    fn close_live_stream(&mut self) {
        let p = &mut self.live_page;
        p.generation += 1;
        // Dropping the connection shuts its socket and ends its thread.
        p.conn = None;
        p.task = None;
        p.stream = Stream::Closed;
    }

    /// The list's scroll offset and its furthest offset, at the last paint.
    fn live_scroll(&self) -> (gpui::Point<gpui::Pixels>, gpui::Pixels) {
        let base = self.live_page.scroll.0.borrow().base_handle.clone();
        (base.offset(), base.max_offset().height)
    }

    /// At the bottom at the last paint (or nothing to scroll): new lines are
    /// followed. Scrolled up, the view stays put.
    fn live_at_bottom(&self) -> bool {
        let (offset, max) = self.live_scroll();
        max <= px(0.) || offset.y <= -(max - px(ROW / 2.))
    }

    fn live_events(&mut self, generation: u64, batch: Vec<Event>, cx: &mut Context<Self>) {
        if generation != self.live_page.generation {
            return;
        }
        let now = super::now_ms();
        let at_bottom = self.live_at_bottom();
        let mut fell_off = 0usize;
        for event in batch {
            let p = &mut self.live_page;
            match event {
                Event::Connected => {
                    p.feed.connected();
                    p.stream = Stream::Streaming;
                }
                Event::Frame(frame) => {
                    fell_off += p.feed.apply(frame, now).iter().filter(|l| p.filter.shows(l)).count();
                }
                Event::Dropped { reason, retry_in } => p.stream = Stream::Retrying { reason, secs: retry_in.as_secs().max(1) },
                Event::Refused(reason) => {
                    p.stream = Stream::Stopped(reason);
                    p.conn = None;
                }
            }
        }
        // Scrolled up while old lines fall off the top: keep the same lines
        // in view.
        if fell_off > 0 && !at_bottom {
            let (offset, _) = self.live_scroll();
            let y = (offset.y + px(ROW * fell_off as f32)).min(px(0.));
            self.live_page.scroll.0.borrow().base_handle.set_offset(point(offset.x, y));
        }
        cx.notify();
    }

    /// The lines that pass the filters, as indexes into the feed.
    fn live_visible(&self) -> Vec<usize> {
        let p = &self.live_page;
        p.feed.lines().iter().enumerate().filter(|(_, l)| p.filter.shows(l)).map(|(i, _)| i).collect()
    }

    fn live_jump(&mut self, cx: &mut Context<Self>) {
        let n = self.live_visible().len();
        if n > 0 {
            self.live_page.scroll.scroll_to_item(n - 1, ScrollStrategy::Bottom);
        }
        cx.notify();
    }

    /// One stable color per machine, readable on the theme.
    fn machine_color(&self, name: &str) -> Hsla {
        let dark = self.theme.bg.l < 0.5;
        gpui::hsla(self.live_page.feed.hue(name), if dark { 0.6 } else { 0.55 }, if dark { 0.7 } else { 0.38 }, 1.)
    }

    /// Opens where a line happened: on the home here; on a linked machine,
    /// that machine's window, which opens it once connected.
    fn open_live_line(&mut self, machine: String, open: Open, cx: &mut Context<Self>) {
        let target = match open {
            Open::Session(session_id) => Target::Session { session_id, origin: String::new() },
            Open::Run { worker_id, run_id, session_id } => Target::Run { worker_id, run_id, session_id },
        };
        if self.live_page.feed.home() == Some(machine.as_str()) {
            self.open_target(target, cx);
        } else {
            self.open_machine_at(machine, Some(target), cx);
        }
    }

    fn live_chip(&self, id: SharedString, label: String, on: bool, dot: Option<Hsla>) -> Stateful<Div> {
        let t = &self.theme;
        let hover = t.hover;
        div()
            .id(id)
            .flex()
            .flex_none()
            .items_center()
            .gap_1()
            .px_2()
            .py(px(2.))
            .rounded(px(t.radius))
            .border_1()
            .border_color(if on { alpha(t.accent, 0.6) } else { t.border })
            .when(on, |d| d.bg(alpha(t.accent, 0.12)))
            .cursor_pointer()
            .hover(move |s| s.bg(hover))
            .text_size(px(11.))
            .text_color(if on { t.text } else { t.faint })
            .children(dot.map(|c| div().text_color(if on { c } else { alpha(c, 0.35) }).child("●")))
            .child(label)
    }

    fn live_filters(&self, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let p = &self.live_page;
        let machines = p.feed.machines().into_iter().map(|name| {
            let on = p.filter.machine_on(&name);
            let label = match p.feed.state(&name) {
                Some("offline") => format!("{name} · offline"),
                Some("not_shown") => format!("{name} · not shown"),
                _ => name.clone(),
            };
            let toggle = name.clone();
            self.live_chip(SharedString::from(format!("live-machine-{name}")), label, on, Some(self.machine_color(&name)))
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                    this.live_page.filter.toggle_machine(&toggle);
                    cx.notify();
                }))
        });
        let kinds = Kind::ALL.into_iter().map(|kind| {
            self.live_chip(SharedString::from(format!("live-kind-{}", kind.label())), kind.label().to_string(), p.filter.kind_on(kind), None)
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                    this.live_page.filter.toggle_kind(kind);
                    cx.notify();
                }))
        });
        let only = self
            .live_chip("live-only-problems".into(), "only problems".into(), p.filter.only_problems, Some(t.err))
            .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                this.live_page.filter.only_problems = !this.live_page.filter.only_problems;
                cx.notify();
            }));
        let label = |text: &'static str| div().flex_none().text_size(px(10.5)).text_color(t.faint).child(text);
        div()
            .flex()
            .flex_wrap()
            .items_center()
            .gap_1()
            .child(label("MACHINES"))
            .children(machines)
            .child(div().w(px(10.)))
            .child(label("KINDS"))
            .children(kinds)
            .child(div().w(px(10.)))
            .child(only)
    }

    /// One line: time, machine, kind, then what happened in the tool
    /// stream's parts.
    fn live_row(&self, i: usize, offset_secs: i64, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let Some(line) = self.live_page.feed.lines().get(i) else { return div().h(px(ROW)).into_any_element() };
        let hover = t.hover;
        let name_color = if line.problem { t.err } else if line.tool { t.info } else { t.accent };
        let detail_color = if line.tool { t.muted } else { t.text };
        let runs: Vec<_> = [
            (line.text.name, name_color),
            (line.text.detail, detail_color),
            (line.text.summary, if line.problem { t.err } else { t.faint }),
            (line.text.time, t.faint),
        ]
        .into_iter()
        .filter(|(len, _)| *len > 0)
        .map(|(len, color)| self.plain_run(len, color))
        .collect();
        let text = StyledText::new(line.text.text.clone()).with_runs(runs);
        // Clickable only when the stream said which machine is the home.
        let open = line.open.clone().filter(|_| self.live_page.feed.home().is_some()).map(|o| (line.machine.clone(), o));
        let row = div()
            .id(("live-row", i))
            .h(px(ROW))
            .w_full()
            .flex()
            .items_center()
            .gap_2()
            .px_3()
            .whitespace_nowrap()
            .overflow_hidden()
            .when(line.problem, |d| d.bg(alpha(t.err, 0.07)))
            .child(div().flex_none().w(px(60.)).text_color(t.faint).child(ls::hms(line.at, offset_secs)))
            .child(div().flex_none().w(px(112.)).truncate().text_color(self.machine_color(&line.machine)).child(line.machine.clone()))
            .child(div().flex_none().w(px(76.)).truncate().text_color(if line.problem { t.err } else { t.muted }).child(line.word))
            .child(div().flex_none().w(px(10.)).text_color(t.faint).child(if line.tool { "└" } else { "" }))
            .child(div().flex_1().min_w_0().truncate().child(text));
        match open {
            Some((machine, open)) => row
                .cursor_pointer()
                .hover(move |s| s.bg(hover))
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.open_live_line(machine.clone(), open.clone(), cx)))
                .into_any_element(),
            None => row.into_any_element(),
        }
    }

    pub(super) fn render_live(&self, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let title = div().text_size(px(16.)).text_color(t.text).child("Live");
        let page = div().id("live-page").size_full().flex().flex_col().p_4().gap_3();
        if let Some(note) = self.live_note() {
            return page.child(title).child(div().text_color(t.faint).child(note)).into_any_element();
        }
        let p = &self.live_page;
        let home = p.feed.home().unwrap_or("the home").to_string();
        let (status, color) = match &p.stream {
            _ if self.link == Link::Demo => ("demo data".to_string(), t.faint),
            Stream::Closed | Stream::Connecting => ("connecting to the home's fleet stream…".to_string(), t.faint),
            Stream::Streaming => (format!("● streaming from {home}"), t.ok),
            Stream::Retrying { reason, secs } => (format!("{reason}: reconnecting in {secs}s"), t.warn),
            Stream::Stopped(reason) => (reason.clone(), t.err),
        };
        let stopped = matches!(p.stream, Stream::Stopped(_));
        let visible = Rc::new(self.live_visible());
        let total = p.feed.lines().len();
        let count = if total >= ls::BUFFER {
            format!("{} of the last {} lines", visible.len(), ls::BUFFER)
        } else if visible.len() == total {
            format!("{total} line{}", if total == 1 { "" } else { "s" })
        } else {
            format!("{} of {total} lines", visible.len())
        };
        let at_bottom = self.live_at_bottom();
        if at_bottom && !visible.is_empty() {
            p.scroll.scroll_to_item(visible.len() - 1, ScrollStrategy::Bottom);
        }
        let header = div()
            .flex()
            .items_center()
            .gap_3()
            .child(title)
            .child(div().flex_1().min_w_0().truncate().text_size(px(11.)).text_color(color).child(status))
            .when(stopped, |d| {
                d.child(self.button("live-retry", "retry", true).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                    this.live_page.stream = Stream::Closed;
                    cx.notify();
                })))
            })
            .child(div().flex_none().text_size(px(11.)).text_color(t.faint).child(count));
        let body: AnyElement = if visible.is_empty() {
            let empty = if total == 0 {
                "Nothing yet. What any machine does shows here as it happens."
            } else {
                "No line matches these filters."
            };
            div().px_3().py_2().text_color(t.faint).child(empty).into_any_element()
        } else {
            let rows = visible.clone();
            uniform_list(
                "live-lines",
                visible.len(),
                cx.processor(move |this: &mut Console, range: Range<usize>, _window: &mut Window, cx| {
                    let offset_secs = ls::local_offset(super::now_ms());
                    range.filter_map(|i| rows.get(i).copied()).map(|i| this.live_row(i, offset_secs, cx)).collect::<Vec<_>>()
                }),
            )
            .track_scroll(p.scroll.clone())
            .size_full()
            .py_1()
            .into_any_element()
        };
        let accent = t.accent;
        let jump = (!at_bottom && !visible.is_empty()).then(|| {
            div()
                .id("live-jump")
                .absolute()
                .bottom(px(12.))
                .right(px(18.))
                .px_3()
                .py_1()
                .rounded(px(t.radius))
                .border_1()
                .border_color(alpha(accent, 0.6))
                .bg(t.panel)
                .shadow_md()
                .text_size(px(11.))
                .text_color(accent)
                .cursor_pointer()
                .hover(move |s| s.bg(alpha(accent, 0.12)))
                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.live_jump(cx)))
                .child("↓ jump to live")
        });
        page.child(header)
            .child(self.live_filters(cx))
            .child(
                div()
                    .relative()
                    .flex_1()
                    .min_h_0()
                    .rounded(px(t.radius + 2.))
                    .border_1()
                    .border_color(t.border)
                    .bg(t.surface)
                    .text_size(px(11.5))
                    .child(body)
                    .children(jump),
            )
            .into_any_element()
    }
}

/// The design set's stream (SWARM_CONSOLE_DEMO), read through the same
/// parser as Swarm's, a few seconds apart.
fn demo(feed: &mut Feed, now: u64) {
    let frames: &[(&str, &str)] = &[
        ("machine", r#"{"kind":"machine","machine":"pc","state":"online"}"#),
        ("machine", r#"{"kind":"machine","machine":"swarm-deploy","state":"not_shown","reason":"linked work rule is off"}"#),
        ("machine", r#"{"kind":"machine","machine":"forge","state":"online"}"#),
        ("machine", r#"{"kind":"machine","machine":"social","state":"online"}"#),
        ("activity", r#"{"machine":"pc","kind":"session.created","session_id":"s1","state":"started"}"#),
        ("activity", r#"{"machine":"pc","kind":"session.assistant.started","session_id":"s1","state":"running"}"#),
        ("activity", r#"{"machine":"forge","kind":"session.tool.started","session_id":"s2","state":"started","tool_name":"read","file_path":"swarmd/internal/api/fleet_activity.go"}"#),
        ("activity", r#"{"machine":"forge","kind":"session.tool.completed","session_id":"s2","state":"finished","tool_name":"read","file_path":"swarmd/internal/api/fleet_activity.go"}"#),
        ("activity", r#"{"machine":"forge","kind":"session.tool.started","session_id":"s2","state":"started","tool_name":"bash"}"#),
        ("activity", r#"{"machine":"social","kind":"job.started","job_id":"worker/w1/r1","worker_id":"w1","worker_name":"daily-reel","session_id":"s3","state":"running"}"#),
        ("activity", r#"{"machine":"forge","kind":"session.tool.failed","session_id":"s2","state":"failed","tool_name":"bash"}"#),
        ("machine", r#"{"kind":"machine","machine":"forge","state":"offline","error":"HTTP 429"}"#),
        ("activity", r#"{"machine":"pc","kind":"session.task.reported","session_id":"s1","state":"changed"}"#),
        ("gap", r#"{"kind":"gap","machine":"social","from":120,"to":131,"cursor":"131"}"#),
        ("activity", r#"{"machine":"social","kind":"job.finished","job_id":"worker/w1/r1","worker_id":"w1","worker_name":"daily-reel","session_id":"s3","state":"finished"}"#),
        ("activity", r#"{"machine":"pc","kind":"session.delivery.updated","session_id":"s1","state":"job.pull_request"}"#),
        ("machine", r#"{"kind":"machine","machine":"forge","state":"online"}"#),
        ("activity", r#"{"machine":"pc","kind":"session.run.completed","session_id":"s1","state":"completed"}"#),
    ];
    feed.connected();
    let n = frames.len() as u64;
    for (i, (event, data)) in frames.iter().enumerate() {
        if let Some(frame) = ls::frame(event, data) {
            feed.apply(frame, now.saturating_sub((n - i as u64) * 4_000));
        }
    }
}
