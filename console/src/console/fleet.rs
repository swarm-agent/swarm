//! The fleet in the sidebar and the status bar: counts, and only the machines
//! that need a look; the full list opens from the status bar. The Agents
//! page holds the detail and each machine's level.

use gpui::{AnyElement, ClickEvent, Context, Div, SharedString, Stateful, div, prelude::*, px};

use super::{AgentsFocus, Console, Page};
use crate::data::{Health, Machine};
use crate::fleet_config::Level;

impl Console {
    /// One machine: its state, name, kind and level (or "home").
    pub(super) fn fleet_row(&self, mi: usize, m: &Machine, cx: &mut Context<Self>) -> Stateful<Div> {
        let t = &self.theme;
        let hover = t.hover;
            div()
                .id(SharedString::from(format!("fleet-{mi}")))
                .cursor_pointer()
                .hover(move |s| s.bg(hover))
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                    this.page = Page::Agents;
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
                    div().text_color(t.accent).text_size(px(10.5)).child("home").into_any_element()
                } else {
                    // What the Orchestrator may do there; click to change.
                    let name = m.name.to_string();
                    let level = self.fleet_config.level(&name);
                    div()
                        .id(SharedString::from(format!("fleet-level-{mi}")))
                        .px_1()
                        .rounded(px(t.radius))
                        .cursor_pointer()
                        .hover(move |s| s.bg(hover))
                        .text_size(px(10.5))
                        .text_color(match level {
                            Level::Full => t.warn,
                            Level::Ask => t.muted,
                            Level::Off => t.faint,
                        })
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            let next = match this.fleet_config.level(&name) {
                                Level::Full => Level::Ask,
                                Level::Ask => Level::Off,
                                Level::Off => Level::Full,
                            };
                            this.fleet_config.access.insert(name.clone(), next);
                            if let Err(e) = this.fleet_config.save() {
                                this.notice = Some((format!("Could not save: {e}").into(), true));
                            }
                            this.sync_fleet(cx);
                            cx.stop_propagation();
                            cx.notify();
                        }))
                        .child(if m.health == Health::Offline { format!("offline · {}", level.label()) } else { level.label().to_string() })
                        .into_any_element()
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
        let levels = "full: runs as you · ask: you approve each · off";
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
                        .children(self.machines.iter().enumerate().map(|(mi, m)| self.fleet_row(mi, m, cx)))
                        .child(div().px_3().pt_1().text_size(px(10.5)).text_color(t.faint).child(levels)),
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
        let title = format!("Swarm Console · {name}");
        let bounds = gpui::Bounds::centered(None, gpui::size(px(1480.), px(900.)), cx);
        let opened = cx.open_window(
            gpui::WindowOptions {
                window_bounds: Some(gpui::WindowBounds::Windowed(bounds)),
                titlebar: Some(gpui::TitlebarOptions { title: Some(title.into()), ..Default::default() }),
                app_id: Some("swarm-console".into()),
                ..Default::default()
            },
            move |_, cx| cx.new(|cx| Console::for_machine(name, cx)),
        );
        if let Err(e) = opened {
            self.notice = Some((format!("Could not open a window: {e}").into(), true));
        }
        cx.notify();
    }
}
