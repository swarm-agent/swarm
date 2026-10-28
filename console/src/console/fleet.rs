//! The fleet: the Fleet page (every machine, what needs the person across
//! all of them, spend), the machine rows in the sidebar and the status bar,
//! and linking and opening machines. What home may hand each machine is a
//! rule on the Permissions page.

use gpui::{AnyElement, ClickEvent, Context, Div, SharedString, Stateful, div, prelude::*, px};

use super::{AgentsFocus, Console, Open, Page};
use crate::swarm::needs::{self, Target};
use crate::data::{Health, Machine};

impl Console {
    /// One machine: its state, name, kind, and home or online/offline.
    pub(super) fn fleet_row(&self, mi: usize, m: &Machine, cx: &mut Context<Self>) -> Stateful<Div> {
        let t = &self.theme;
        let hover = t.hover;
            div()
                .id(SharedString::from(format!("fleet-{mi}")))
                .cursor_pointer()
                .hover(move |s| s.bg(hover))
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                    this.page = Page::Fleet;
                    this.agents_focus = AgentsFocus::Machine(mi);
                    cx.notify();
                }))
                .flex()
                .items_center()
                .gap_2()
                .px_3()
                .py(px(3.))
                .child(div().text_color(self.health_color(m.health)).child("●"))
                .child(div().text_color(t.muted).child(m.name.clone()))
                .child(div().flex_1().text_color(t.faint).text_size(px(10.5)).child(m.kind.clone()))
                .child(if mi == 0 {
                    div().text_color(t.accent).text_size(px(10.5)).child("home")
                } else if m.health == Health::Offline {
                    div().text_color(t.faint).text_size(px(10.5)).child("offline")
                } else {
                    div().text_color(t.muted).text_size(px(10.5)).child(m.health.label())
                })
    }

    /// The status bar's fleet count, which opens the full list.
    pub(super) fn fleet_status(&self, cx: &mut Context<Self>) -> Stateful<Div> {
        let t = &self.theme;
        let hover = t.hover;
        let online = self.machines.iter().filter(|m| m.health != Health::Offline).count();
        div()
            .id("fleet-status")
            .px_1()
            .rounded(px(t.radius))
            .cursor_pointer()
            .hover(move |s| s.bg(hover))
            .when(self.fleet_open, |d| d.text_color(t.text))
            .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                this.fleet_open = !this.fleet_open;
                cx.notify();
            }))
            .child(format!("fleet {online}/{} online", self.machines.len()))
    }

    /// Every machine, above the status bar, while its count is open; a click
    /// anywhere else closes it.
    pub(super) fn render_fleet_popover(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        if !self.fleet_open {
            return None;
        }
        let t = &self.theme;
        Some(
            div()
                .id("fleet-popover-backdrop")
                .absolute()
                .inset_0()
                .occlude()
                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                    this.fleet_open = false;
                    cx.notify();
                }))
                .child(
                    div()
                        .id("fleet-popover")
                        .absolute()
                        .left(px(8.))
                        .bottom(px(28.))
                        .w(px(360.))
                        .flex()
                        .flex_col()
                        .py_1()
                        .rounded(px(t.radius + 2.))
                        .border_1()
                        .border_color(t.border_strong)
                        .bg(t.panel)
                        .shadow_md()
                        .on_click(|_, _, cx| cx.stop_propagation())
                        .child(div().px_3().pt_1().pb_1().text_size(px(10.5)).text_color(t.faint).child(format!("FLEET · {} · click a machine for its page", self.machines.len())))
                        .children(self.machines.iter().enumerate().map(|(mi, m)| self.fleet_row(mi, m, cx))),
                )
                .into_any_element(),
        )
    }
}


impl Console {
    /// Linking a machine to home: the code is made on the machine (its owner
    /// decides who controls it) and redeemed here, on home.
    pub(super) fn link_section(&self, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let code = self.link_code.as_ref().map(|i| i.read(cx).text().trim().to_string()).unwrap_or_default();
        div()
            .child(self.section("LINK A MACHINE", None))
            .child(
                div()
                    .px_3()
                    .text_size(px(11.))
                    .text_color(t.faint)
                    .child("On the machine, as its owner: sudo swarmctl fleet invite IP, with its private IP (tailnet, LAN or WireGuard) that this computer reaches it on. Paste the code it prints:"),
            )
            .child(
                div()
                    .mx_3()
                    .mt_2()
                    .px_2()
                    .py_1()
                    .rounded(px(t.radius))
                    .border_1()
                    .border_color(t.border)
                    .bg(t.surface)
                    .children(self.link_code.clone()),
            )
            .child(
                div().px_3().pt_2().child(self.button("fleet-link", "link", !code.is_empty()).on_click(cx.listener(
                    |this, _: &ClickEvent, _, cx| {
                        let code = this.link_code.as_ref().map(|i| i.read(cx).text().trim().to_string()).unwrap_or_default();
                        if code.is_empty() {
                            this.notice = Some(("Paste the machine's link code first.".into(), true));
                        } else if let Some(live) = &this.live {
                            live.run(crate::swarm::live::Command::LinkMachine { code });
                            this.notice = Some(("Linking…".into(), false));
                        }
                        cx.notify();
                    },
                ))),
            )
    }

    /// Opens a linked machine in a window of its own: its Swarm, through home.
    pub(super) fn open_machine(&mut self, name: String, cx: &mut Context<Self>) {
        self.open_machine_at(name, None, cx);
    }

    /// The same, opening `target` there once it is connected.
    pub(super) fn open_machine_at(&mut self, name: String, target: Option<Target>, cx: &mut Context<Self>) {
        let title = format!("Swarm Console · {name}");
        let bounds = gpui::Bounds::centered(None, gpui::size(px(1480.), px(900.)), cx);
        let opened = cx.open_window(
            gpui::WindowOptions {
                window_bounds: Some(gpui::WindowBounds::Windowed(bounds)),
                titlebar: Some(gpui::TitlebarOptions { title: Some(title.into()), ..Default::default() }),
                app_id: Some("swarm-console".into()),
                ..Default::default()
            },
            move |_, cx| cx.new(|cx| Console::for_machine_at(name, target, cx)),
        );
        if let Err(e) = opened {
            self.notice = Some((format!("Could not open a window: {e}").into(), true));
        }
        cx.notify();
    }
}

/// The Fleet page: every machine, what needs the person across all of them,
/// and today's spend. Everything is what each machine's Swarm said, read
/// through home on the fleet tick (swarm::fleet, swarm::needs).
impl Console {
    /// Opens an item on this window's Swarm: the conversation (or the one
    /// that started the task) in a pane, or the run on the Automations page.
    pub(super) fn open_target(&mut self, target: Target, cx: &mut Context<Self>) {
        let by_session = |this: &Self, id: &str| this.sessions.iter().position(|s| s.live.as_ref().is_some_and(|l| l.session_id.as_ref() == id));
        match target {
            Target::Session { session_id, origin } => {
                let found = by_session(self, &session_id)
                    .or_else(|| self.sessions.iter().position(|s| s.tasks.iter().any(|t| t.session.as_ref() == session_id)));
                match found {
                    Some(i) => {
                        self.open(i, Open::Here);
                        self.page = Page::Sessions;
                    }
                    None => self.open_found(if origin.is_empty() { session_id } else { origin }, cx),
                }
            }
            Target::Task { task_id, session_id, origin, .. } => {
                match self.sessions.iter().position(|s| s.tasks.iter().any(|t| t.key.as_ref() == task_id)) {
                    Some(i) => {
                        self.open(i, Open::Here);
                        self.page = Page::Sessions;
                    }
                    None if !origin.is_empty() => self.open_found(origin, cx),
                    None if !session_id.is_empty() => self.open_found(session_id, cx),
                    None => self.notice = Some(("Swarm's record names no conversation for this task.".into(), true)),
                }
            }
            Target::Run { worker_id, run_id, session_id } => {
                self.open_run_ids(worker_id, run_id, session_id, cx);
                self.go(Page::Automations, cx);
            }
        }
        cx.notify();
    }

    pub(super) fn render_fleet(&self, cx: &mut Context<Self>) -> impl IntoElement + use<> {
        let t = &self.theme;
        let hover = t.hover;
        let fleet_read = self.via.is_none() && !self.needs.is_empty();
        let failed = needs::Count::of(self.needs.iter().map(|r| r.failed_today));
        let attention = needs::attention(&self.machines);
        let mut glance = format!("{} machine{} · {attention} need{} attention", self.machines.len(), if self.machines.len() == 1 { "" } else { "s" }, if attention == 1 { "s" } else { "" });
        if fleet_read {
            glance.push_str(&format!(" · {} failed today", failed.text()));
        }

        let cards: Vec<_> = (0..self.machines.len()).map(|mi| self.machine_card(mi, cx)).collect();

        let (spent, spend_unknown) = crate::data::spend_today(&self.machines);
        let spend_rows = self.machines.iter().map(|m| {
            div()
                .flex()
                .items_center()
                .gap_2()
                .px_3()
                .py(px(2.))
                .child(div().text_color(self.health_color(m.health)).child("●"))
                .child(div().flex_1().text_color(t.muted).child(m.name.clone()))
                .child(div().text_color(if m.cost_today.is_some() { t.text } else { t.faint }).child(match m.cost_today {
                    Some(c) => format!("${c:.2}"),
                    None => "unknown".into(),
                }))
        });

        div()
            .id("fleet-page")
            .size_full()
            .overflow_y_scroll()
            .p_5()
            .flex()
            .flex_col()
            .gap_4()
            .child(
                div()
                    .flex()
                    .flex_col()
                    .gap_1()
                    .child(div().text_size(px(16.)).text_color(t.text).child("Fleet"))
                    .child(div().text_color(if failed.known > 0 || attention > 0 { t.warn } else { t.muted }).child(glance))
                    .child(div().text_size(px(11.)).text_color(t.faint).child(
                        "Each machine as its own Swarm reports it, read through home every 20 seconds.",
                    )),
            )
            .child(self.section("MACHINES", Some(self.machines.len())).px_0())
            .child(
                div()
                    .flex()
                    .flex_wrap()
                    .gap_3()
                    .children(cards)
                    // Linking is in the fleet inspector (a code made on the machine).
                    .when(self.via.is_none() && self.link != super::Link::Demo, |d| {
                        d.child(
                            div()
                                .id("add-machine")
                                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                                    this.agents_focus = AgentsFocus::Fleet;
                                    cx.notify();
                                }))
                                .w(px(232.))
                                .min_h(px(150.))
                                .flex()
                                .flex_col()
                                .items_center()
                                .justify_center()
                                .gap_1()
                                .rounded(px(t.radius + 2.))
                                .border_1()
                                .border_dashed()
                                .border_color(t.border_strong)
                                .text_color(t.faint)
                                .cursor_pointer()
                                .hover(move |s| s.bg(hover))
                                .child(div().text_color(t.muted).child("+ link a machine"))
                                .child(div().text_size(px(10.5)).child("with a code from sudo swarmctl fleet invite")),
                        )
                    }),
            )
            .child(self.fleet_keys_section(cx))
            .child(self.section("SPEND TODAY", None).px_0())
            .child(
                div()
                    .rounded(px(t.radius + 2.))
                    .border_1()
                    .border_color(t.border)
                    .bg(t.surface)
                    .py_1()
                    .children(spend_rows)
                    .child(
                        div()
                            .flex()
                            .items_center()
                            .gap_2()
                            .px_3()
                            .pt_1()
                            .mt_1()
                            .border_t_1()
                            .border_color(t.border)
                            .child(div().flex_1().text_color(t.faint).child(if spend_unknown { "fleet (some machines unknown)" } else { "fleet" }))
                            .child(div().text_color(t.text).child(format!("${spent:.2}{}", if spend_unknown { "+?" } else { "" }))),
                    ),
            )
    }
}
