//! The Companion page: the person's one persistent assistant, beside the
//! project Chats (the Sessions page, unchanged), with what its work is doing.
//!
//! The conversation is the Companion's canonical Swarm session, shown in the
//! same pane, composer, streaming, approvals and device-call cards as any
//! conversation, but in a pane of its own that the main window's layout
//! never holds: it has no close, split or pop-out, never appears in a
//! project's session list and is never picked from one. Swarm names the
//! session (GET/POST /v3/companion); the console only follows it. The left
//! sidebar, in place of the sessions list, shows the tasks the Companion
//! started, grouped needs attention / running / results, and a detail view
//! that opens over the list without touching the conversation, its draft or
//! its focus; the conversation takes the rest of the window.

use gpui::{AnyElement, ClickEvent, Context, SharedString, div, prelude::*, px};

use super::{Console, Link, Page, PaneId, live::Act};
use crate::data::Text;
use crate::icons::Icon;
use crate::swarm::companion::{self, Group, View, Work};
use crate::swarm::live::{Command, CompanionFeed};

/// The Companion's pane: outside the layout's ids (Layout::alloc counts up
/// from zero), so no split, close, focus cycle or drop ever reaches it.
pub(super) const PANE: PaneId = PaneId(usize::MAX);

/// What the Companion page holds between Swarm's answers. Swarm's records
/// stay the truth: this is the last answer, a read under way and what the
/// person has open.
#[derive(Default)]
pub(super) struct CompanionPage {
    /// Swarm's last answer (GET or POST /v3/companion); None before one.
    pub view: Option<View>,
    /// A GET answered at least once (None in `view` then means none yet).
    pub read: bool,
    pub busy: bool,
    /// Read again once the read under way ends.
    pub again: bool,
    /// The get-or-create is under way.
    pub ensuring: bool,
    /// The last failure, as Swarm (or the connection) worded it.
    pub error: Option<String>,
    /// The follow loop's Companion: its tasks and history paging.
    pub feed: Option<CompanionFeed>,
    /// An earlier page was asked for and the next snapshot has not come.
    pub older_asked: bool,
    /// The work item whose detail is open (Work::key, or "watch:" + its
    /// machine/project/task for a watched task this machine cannot show).
    pub detail: Option<String>,
}

impl CompanionPage {
    /// The session Swarm named as the Companion, once it exists.
    pub fn session_id(&self) -> Option<&str> {
        self.view.as_ref().filter(|v| v.ready()).map(|v| v.session_id.as_str())
    }

    /// How many of its items need the person (the nav badge), and how many run.
    pub fn counts(&self) -> (usize, usize) {
        let work = self.feed.as_ref().map(|f| f.work.as_slice()).unwrap_or(&[]);
        (work.iter().filter(|w| w.group == Group::Attention).count(), work.iter().filter(|w| w.group == Group::Running).count())
    }
}

/// A watched task's detail key.
fn watch_key(machine: &str, project: &str, task: &str) -> String {
    format!("watch:{machine}/{project}/{task}")
}

impl Console {
    /// The Companion session's index in `sessions`, once Swarm named it and
    /// the follow loop listed it.
    pub(super) fn companion_index(&self) -> Option<usize> {
        let id = self.companion.session_id()?;
        self.sessions.iter().position(|s| s.live.as_ref().is_some_and(|l| l.session_id.as_ref() == id))
    }

    /// Is this session the Companion? It belongs to no project list.
    pub(super) fn is_companion(&self, si: usize) -> bool {
        let Some(id) = self.companion.session_id() else { return false };
        self.sessions.get(si).and_then(|s| s.live.as_ref()).is_some_and(|l| l.session_id.as_ref() == id)
    }

    /// Keeps the Companion's pane on its session: present once the session
    /// is listed, gone when Swarm names none. Its composer (and draft)
    /// belongs to the pane, so navigation and updates never touch it.
    pub(super) fn sync_companion_pane(&mut self) {
        match self.companion_index() {
            Some(si) => match self.panes.iter_mut().find(|p| p.id == PANE) {
                Some(pane) => pane.session = si,
                None => self.panes.push(super::Pane { id: PANE, session: si, tray: false, tab: super::background::BarTab::Tasks, scroll: gpui::ScrollHandle::new() }),
            },
            None => self.panes.retain(|p| p.id != PANE),
        }
    }

    /// Tells the follow loop which session is the Companion.
    fn follow_companion(&self) {
        if let Some(live) = &self.live {
            live.follow_companion(self.companion.session_id().map(str::to_string));
        }
    }

    /// Reads the Companion and its activity (GET /v3/companion). Creates
    /// nothing; one read at a time, the next after it ends.
    pub(super) fn read_companion(&mut self, cx: &mut Context<Self>) {
        let Some(client) = self.live.as_ref().map(|l| l.client()) else { return };
        if self.companion.busy {
            self.companion.again = true;
            return;
        }
        self.companion.busy = true;
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { companion::read(&client) }).await;
            this.update(cx, |this, cx| {
                this.companion.busy = false;
                this.companion.read = true;
                match result {
                    Ok(view) => {
                        this.companion.view = view;
                        this.companion.error = None;
                    }
                    Err(e) => this.companion.error = Some(e),
                }
                this.follow_companion();
                this.sync_companion_pane();
                // Opened before Swarm had one: start it now.
                if this.page == Page::Companion && !this.companion.view.as_ref().is_some_and(View::ready) && this.companion.error.is_none() {
                    this.ensure_companion(cx);
                }
                if std::mem::take(&mut this.companion.again) {
                    this.read_companion(cx);
                }
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    /// The person opened the Companion and Swarm has none yet: Swarm's
    /// idempotent get-or-create (POST /v3/companion). Retried only by the
    /// person (the button on the page).
    pub(super) fn ensure_companion(&mut self, cx: &mut Context<Self>) {
        let Some(client) = self.live.as_ref().map(|l| l.client()) else { return };
        if self.companion.ensuring {
            return;
        }
        self.companion.ensuring = true;
        self.companion.error = None;
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { companion::ensure(&client) }).await;
            this.update(cx, |this, cx| {
                this.companion.ensuring = false;
                match result {
                    Ok(view) => {
                        this.companion.view = Some(view);
                        this.follow_companion();
                        this.pending_focus_companion = true;
                        // Its activity comes with a read.
                        this.read_companion(cx);
                    }
                    Err(e) => this.companion.error = Some(e),
                }
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    /// Goes to the Companion: its conversation and work, with the keyboard
    /// in its message box. The first visit starts it in Swarm.
    pub(super) fn open_companion(&mut self, cx: &mut Context<Self>) {
        self.page = Page::Companion;
        self.pending_focus_companion = true;
        if self.live.is_some() {
            if self.companion.view.as_ref().is_some_and(View::ready) {
                self.read_companion(cx);
            } else if self.companion.read && self.companion.error.is_none() {
                self.ensure_companion(cx);
            } else {
                self.read_companion(cx);
            }
        }
        cx.notify();
    }

    /// The person asked for earlier messages of the Companion.
    fn companion_older(&mut self, cx: &mut Context<Self>) {
        let (Some(live), Some(id)) = (&self.live, self.companion.session_id()) else { return };
        live.read_older(id);
        self.companion.older_asked = true;
        cx.notify();
    }

    /// Approve, reject or stop one of the Companion's tasks, in its own project.
    fn companion_task_act(&mut self, key: &str, act: Act, cx: &mut Context<Self>) {
        let Some(live) = &self.live else { return };
        let Some(w) = self.companion.feed.as_ref().and_then(|f| f.work.iter().find(|w| w.key() == key)).cloned() else { return };
        let (command, note) = match act {
            Act::Approve => (Command::Approve { project_id: w.project_id.clone(), task_id: w.task.key.to_string(), approve: true }, "Approving…"),
            Act::Reject => (Command::Approve { project_id: w.project_id.clone(), task_id: w.task.key.to_string(), approve: false }, "Rejecting…"),
            Act::Stop if !w.task.session.is_empty() => (Command::Stop { session_id: w.task.session.to_string(), waiting_run_id: String::new() }, "Stopping the task…"),
            Act::Stop => return,
        };
        self.failure = None;
        live.run(command);
        self.notice = Some((note.into(), false));
        cx.notify();
    }

    /// The Companion page: its conversation, over the whole main area. Its
    /// work is in the left sidebar, in place of the sessions list
    /// (Page::left_body), so there is no column on the right.
    pub(super) fn render_companion(&self, cx: &mut Context<Self>) -> AnyElement {
        let conversation = match self.companion_index().filter(|_| self.panes.iter().any(|p| p.id == PANE)) {
            Some(_) => self.render_pane(PANE, cx),
            None => self.render_companion_placeholder(cx),
        };
        div().id("companion-page").flex_1().min_w_0().h_full().flex().p(px(6.)).child(div().flex_1().min_w_0().h_full().flex().child(conversation)).into_any_element()
    }

    /// Before the Companion's session is listed: why, and what to do.
    fn render_companion_placeholder(&self, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let (text, retry): (String, bool) = match (&self.link, &self.companion) {
            (Link::Demo, _) => ("The Companion lives in a running Swarm; the demo data has none.".into(), false),
            (Link::Lost(e), _) => (format!("Swarm is not answering: {e}"), false),
            (Link::Connecting, _) => ("Connecting to Swarm…".into(), false),
            (Link::Live, c) if c.error.is_some() => (format!("Swarm could not open your Companion: {}", c.error.clone().unwrap_or_default()), true),
            (Link::Live, c) if c.ensuring => ("Starting your Companion…".into(), false),
            (Link::Live, c) if c.view.as_ref().is_some_and(View::ready) => ("Reading the conversation…".into(), false),
            (Link::Live, _) => ("Reading your Companion…".into(), false),
        };
        div()
            .size_full()
            .flex()
            .flex_col()
            .items_center()
            .justify_center()
            .gap_3()
            .rounded(px(t.radius + 2.))
            .border_1()
            .border_color(t.border)
            .bg(t.surface)
            .child(self.icon(Icon::Companion, 28., t.faint))
            .child(div().max_w(px(420.)).text_center().text_color(t.faint).line_height(px(19.)).child(text))
            .when(retry, |d| {
                d.child(self.button("companion-retry", "try again", true).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                    this.companion.error = None;
                    this.open_companion(cx);
                })))
            })
            .into_any_element()
    }

    /// Above the Companion's conversation: earlier messages on request.
    pub(super) fn companion_history_row(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let t = &self.theme;
        let feed = self.companion.feed.as_ref()?;
        let error = feed.older_error.clone();
        let label = if self.companion.older_asked {
            "Reading earlier messages…".to_string()
        } else if feed.more_older == Some(true) {
            "Earlier messages".to_string()
        } else if error.is_none() {
            return None;
        } else {
            "Try reading earlier messages again".to_string()
        };
        Some(
            div()
                .flex()
                .flex_col()
                .items_center()
                .gap_1()
                .child(
                    self.button("companion-older", label, false)
                        .when(!self.companion.older_asked, |d| d.on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.companion_older(cx)))),
                )
                .children(error.map(|e| div().text_size(px(11.)).text_color(t.err).child(format!("Swarm did not return them: {e}"))))
                .into_any_element(),
        )
    }

    /// The Companion's work, for the left sidebar: what needs the person,
    /// what runs and the results, and one item's detail over the list.
    pub(super) fn render_companion_work(&self, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let feed = self.companion.feed.as_ref();
        let work: &[Work] = feed.map(|f| f.work.as_slice()).unwrap_or(&[]);
        let activity = self.companion.view.as_ref().and_then(|v| v.activity.as_ref());
        let elsewhere = activity.map(|a| companion::watched_elsewhere(a, work)).unwrap_or_default();
        let line = activity.map(companion::activity_line).filter(|l| !l.is_empty());

        let head = div()
            .flex_none()
            .px_3()
            .py_2()
            .flex()
            .flex_col()
            .gap_1()
            .border_b_1()
            .border_color(t.border)
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .child(self.icon(Icon::Tasks, 13., t.faint))
                    .child(div().flex_1().text_color(t.text).child("Work"))
                    .children((self.companion.busy).then(|| div().text_size(px(10.5)).text_color(t.faint).child("reading…"))),
            )
            .child(div().text_size(px(11.)).text_color(t.faint).line_height(px(16.)).child(
                line.unwrap_or_else(|| if self.companion.session_id().is_some() { "Idle. Background work keeps running while you talk.".to_string() } else { String::new() }),
            ));

        let body = match self.companion.detail.as_deref() {
            Some(key) => self.render_companion_detail(key, work, &elsewhere, cx),
            None => self.render_companion_list(work, &elsewhere, cx),
        };
        div().size_full().flex().flex_col().child(head).child(div().id("companion-work-scroll").flex_1().min_h_0().overflow_y_scroll().pb_2().child(body)).into_any_element()
    }

    fn render_companion_list(&self, work: &[Work], elsewhere: &[(String, String, String, String)], cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let hover = t.hover;
        let row = |id: String, key: String, glyph: (&'static str, gpui::Hsla), title: String, sub: String| {
            div()
                .id(SharedString::from(id))
                .mx_1()
                .px_2()
                .py(px(5.))
                .flex()
                .items_start()
                .gap_2()
                .rounded(px(t.radius))
                .cursor_pointer()
                .hover(move |s| s.bg(hover))
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                    this.companion.detail = Some(key.clone());
                    cx.notify();
                }))
                .child(div().flex_none().text_color(glyph.1).child(glyph.0))
                .child(
                    div()
                        .flex_1()
                        .min_w_0()
                        .flex()
                        .flex_col()
                        .child(div().truncate().text_color(t.text).child(title))
                        .child(div().truncate().text_size(px(10.5)).text_color(t.faint).child(sub)),
                )
        };
        let mut out = div().flex().flex_col();
        for group in Group::ALL {
            let items: Vec<&Work> = work.iter().filter(|w| w.group == group).collect();
            if items.is_empty() {
                continue;
            }
            out = out.child(self.section(group.label(), Some(items.len()))).children(items.into_iter().map(|w| {
                let state = if w.task.progress.task_state.label().is_empty() { w.task.status.label() } else { w.task.progress.task_state.label() };
                row(format!("companion-work-{}", w.key()), w.key(), self.task_glyph(w.task.status), w.task.title.to_string(), format!("{} · {} · {state}", w.project, w.task.role))
            }));
        }
        if !elsewhere.is_empty() {
            out = out.child(self.section("WATCHING ELSEWHERE", Some(elsewhere.len()))).children(elsewhere.iter().map(|(machine, project, task, title)| {
                let place = if machine.is_empty() { "this machine".to_string() } else { format!("on {machine}") };
                row(
                    format!("companion-watch-{machine}-{project}-{task}"),
                    watch_key(machine, project, task),
                    ("◷", t.accent),
                    if title.is_empty() { task.clone() } else { title.clone() },
                    format!("{place} · {project}"),
                )
            }));
        }
        if work.is_empty() && elsewhere.is_empty() {
            out = out.child(
                div()
                    .px_3()
                    .pt_3()
                    .text_size(px(11.5))
                    .line_height(px(17.))
                    .text_color(t.faint)
                    .child("Nothing yet. Work the Companion starts shows here while it runs, when it needs you, and with its results; your conversation stays put."),
            );
        }
        out.into_any_element()
    }

    fn render_companion_detail(&self, key: &str, work: &[Work], elsewhere: &[(String, String, String, String)], cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let back = div().px_2().pt_2().child(self.button("companion-detail-back", "‹ all work", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
            this.companion.detail = None;
            cx.notify();
        })));
        let field = |label: &'static str, value: String| {
            (!value.trim().is_empty()).then(|| {
                div()
                    .flex()
                    .flex_col()
                    .gap(px(2.))
                    .child(div().text_size(px(10.5)).text_color(t.faint).child(label))
                    .child(div().text_size(px(11.5)).line_height(px(17.)).text_color(t.text).child(value))
            })
        };
        let body = div().px_3().pt_2().flex().flex_col().gap_3();
        if let Some(w) = work.iter().find(|w| w.key() == key) {
            let task = &w.task;
            let state = if task.progress.task_state.label().is_empty() { task.status.label().to_string() } else { task.progress.task_state.label().to_string() };
            let (glyph, color) = self.task_glyph(task.status);
            let key_owned = key.to_string();
            let act_button = |label: &'static str, strong: bool, act: Act| {
                let key = key_owned.clone();
                self.button(SharedString::from(format!("companion-act-{label}")), label, strong)
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.companion_task_act(&key, act, cx)))
            };
            let actions = div()
                .flex()
                .gap_1()
                .when(task.status.needs_you(), |d| d.child(act_button("approve", true, Act::Approve)).child(act_button("reject", false, Act::Reject)))
                .when(task.status == crate::data::TaskStatus::Running && !task.session.is_empty(), |d| d.child(act_button("stop", false, Act::Stop)));
            let history = task.progress.history.iter().rev().map(|(at, what)| {
                let when = if *at > 0 { super::ago(super::now_ms().saturating_sub(*at)) } else { String::new() };
                div()
                    .flex()
                    .gap_2()
                    .text_size(px(11.))
                    .child(div().flex_none().w(px(34.)).text_color(t.faint).child(when))
                    .child(div().flex_1().min_w_0().text_color(t.muted).child(what.clone()))
            });
            return div()
                .flex()
                .flex_col()
                .child(back)
                .child(
                    body.child(
                        div()
                            .flex()
                            .items_start()
                            .gap_2()
                            .child(div().text_color(color).child(glyph))
                            .child(div().flex_1().min_w_0().text_color(t.text).line_height(px(19.)).child(task.title.clone())),
                    )
                    .child(div().text_size(px(11.)).text_color(t.faint).child(format!("{} · {} · {state}", w.project, task.role)))
                    .child(actions)
                    .children(field("WHERE IT STANDS", task.progress.summary.to_string()))
                    .children(field("WHY", task.progress.task_state_reason.to_string()))
                    .children(field("NOW", task.activity.to_string()))
                    .children(field("RESULT", task.report.to_string()))
                    .when(!task.progress.history.is_empty(), |d| {
                        d.child(div().text_size(px(10.5)).text_color(t.faint).child("HISTORY")).child(div().flex().flex_col().gap_1().children(history))
                    }),
                )
                .into_any_element();
        }
        if let Some((machine, project, task, title)) = elsewhere.iter().find(|(m, p, k, _)| watch_key(m, p, k) == key) {
            return div()
                .flex()
                .flex_col()
                .child(back)
                .child(
                    body.child(div().text_color(t.text).child(if title.is_empty() { task.clone() } else { title.clone() }))
                        .children(field("MACHINE", if machine.is_empty() { "this machine".into() } else { machine.clone() }))
                        .children(field("PROJECT", project.clone()))
                        .children(field("TASK", task.clone()))
                        .child(
                            div()
                                .text_size(px(11.))
                                .line_height(px(16.))
                                .text_color(t.faint)
                                .child("The Companion watches this task; its outcome arrives in the conversation. Its machine holds its full record."),
                        ),
                )
                .into_any_element();
        }
        div().flex().flex_col().child(back).child(body.child(div().text_size(px(11.5)).text_color(t.faint).child("Swarm no longer lists this work."))).into_any_element()
    }

    /// The nav's figure for the Companion: what needs you, else what runs.
    pub(super) fn companion_badge(&self) -> Option<(String, gpui::Hsla, bool)> {
        let t = &self.theme;
        let (attention, running) = self.companion.counts();
        let waits = self.companion_index().map(|si| {
            let s = &self.sessions[si];
            usize::from(s.pending.is_some()) + s.calls.iter().filter(|c| c.execution.as_ref() == "awaiting_approval" || c.tool.starts_with("request_")).count()
        });
        let needs = attention + waits.unwrap_or(0);
        if needs > 0 {
            Some((needs.to_string(), t.warn, true))
        } else if running > 0 {
            Some((format!("{running} running"), t.accent, false))
        } else {
            None
        }
    }

    /// The Companion's work key as text, for the titlebar (none open: none).
    pub(super) fn companion_focus_title(&self) -> Option<SharedString> {
        let key = self.companion.detail.as_deref()?;
        let work = self.companion.feed.as_ref()?.work.iter().find(|w| w.key() == key)?;
        Some(SharedString::from(work.task.title.to_string()))
    }
}

/// The Companion's message box placeholder.
pub(super) const PLACEHOLDER: &str = "Talk to your Companion…";

/// The Companion's empty conversation, before its first message.
pub(super) fn empty_line() -> Text {
    "Your Companion: one conversation that stays with you across projects. Ask anything, get help on this computer, or hand it work; what it starts shows on the left while you keep talking.".into()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::companion::Activity;

    /// Requirement: the Companion's pane id can never collide with a layout
    /// pane (Layout::alloc counts up from zero), so no split, close, drop or
    /// focus cycle of the main window ever reaches it. Threat: a layout pane
    /// sharing its id, which would give the Companion a close button.
    #[test]
    fn companion_pane_is_outside_the_layout() {
        let mut layout = crate::layout::Layout::single();
        for _ in 0..64 {
            let id = layout.alloc();
            assert_ne!(id, PANE);
        }
        assert!(!layout.contains(PANE));
        assert!(!layout.focus(PANE), "the layout cannot focus the Companion's pane");
        assert!(!layout.close(PANE), "nor close it");
    }

    /// Requirement: on the Companion page the left sidebar (and its rail)
    /// holds the Companion's work in place of the sessions list, and every
    /// other page, Chats (Sessions) first, keeps the sessions list; the
    /// Companion page renders no right-hand work column. Threat: the work
    /// list shown beside an ordinary sessions column (two sidebars), or the
    /// sessions list lost on the Chats page. Boundary: Page::left_body, the
    /// one selector render_left and render_rail branch on. Narrowest layer:
    /// that selector (no GPUI render harness in this crate; the rendered
    /// layout itself is unverified here).
    #[test]
    fn left_sidebar_holds_companion_work_only_on_the_companion_page() {
        assert_eq!(Page::Companion.left_body(), super::super::LeftBody::CompanionWork);
        for page in Page::ALL.into_iter().chain([Page::Projects]).filter(|p| *p != Page::Companion) {
            assert_eq!(page.left_body(), super::super::LeftBody::Sessions, "{page:?} must keep the sessions list");
        }
        assert_eq!(Page::ALL.iter().filter(|p| p.left_body() == super::super::LeftBody::CompanionWork).count(), 1, "only one page shows the Companion's work");
    }

    /// Requirement: with the right-hand Work column gone, the Companion's
    /// open work item (its detail and approve/reject/stop) stays reachable in
    /// a narrow or collapsed window: below NARROW_WIDTH, or collapsed, the
    /// left is the rail, and on the Companion page an open item then shows
    /// over the conversation beside it. Wide and expanded is the sidebar
    /// whatever the page; other pages never show the Companion's detail.
    /// Threat: a rail press that selects an item no layout can draw (the
    /// blocked regression), or the detail leaking onto Chats. Boundary:
    /// Console::left_layout, the one choice render branches on. Narrowest
    /// layer: that pure choice (no GPUI render harness in this crate; the
    /// drawn overlay itself is unverified here).
    #[test]
    fn narrow_companion_shows_open_work_detail_beside_the_rail() {
        use super::super::{LeftLayout, NARROW_WIDTH};
        let layout = Console::left_layout;
        let narrow = NARROW_WIDTH - 1.;
        let wide = NARROW_WIDTH;
        // Narrow Companion: an open item shows; none open is the bare rail.
        assert_eq!(layout(Page::Companion, false, narrow, true), LeftLayout::RailWithCompanionDetail);
        assert_eq!(layout(Page::Companion, false, narrow, false), LeftLayout::Rail);
        assert_eq!(layout(Page::Companion, false, 320., true), LeftLayout::RailWithCompanionDetail);
        // Collapsed by choice, wide window: same, without un-collapsing.
        assert_eq!(layout(Page::Companion, true, 1400., true), LeftLayout::RailWithCompanionDetail);
        assert_eq!(layout(Page::Companion, true, 1400., false), LeftLayout::Rail);
        // Wide and expanded: the sidebar, which holds the detail itself.
        assert_eq!(layout(Page::Companion, false, wide, true), LeftLayout::Sidebar);
        assert_eq!(layout(Page::Companion, false, wide, false), LeftLayout::Sidebar);
        // Every other page (Chats first): never the Companion's detail.
        for page in Page::ALL.into_iter().chain([Page::Projects]).filter(|p| *p != Page::Companion) {
            assert_eq!(layout(page, false, narrow, true), LeftLayout::Rail, "{page:?} narrow");
            assert_eq!(layout(page, true, 1400., true), LeftLayout::Rail, "{page:?} collapsed");
            assert_eq!(layout(page, false, wide, true), LeftLayout::Sidebar, "{page:?} wide");
        }
    }

    /// Requirement: the page follows only the session Swarm named, and only
    /// once it is ready; its badge counts the work that needs the person.
    #[test]
    fn companion_page_follows_only_a_ready_session() {
        let mut page = CompanionPage::default();
        assert_eq!(page.session_id(), None);
        page.view = Some(View { session_id: "cmp".into(), generation: 1, state: "uncreated".into(), activity: None });
        assert_eq!(page.session_id(), None, "an uncreated Companion has no session to show");
        page.view = Some(View { session_id: "cmp".into(), generation: 1, state: "ready".into(), activity: Some(Activity::default()) });
        assert_eq!(page.session_id(), Some("cmp"));
        assert_eq!(page.counts(), (0, 0));
    }
}
