//! Agents: the fleet's machines and one model table for every role.
//!
//! Each machine runs the fleet default per role unless it has an override.
//! The grid stays quiet where machines agree and only spells out models where
//! one differs; details and actions live in the inspector on the right.

use gpui::{AnyElement, ClickEvent, Context, Div, Hsla, SharedString, div, prelude::*, px, relative};

use super::{AgentsFocus, Console, alpha};
use crate::data::{Assignment, Cell, Health};

const COL: f32 = 128.;
const ROLE_COL: f32 = 200.;
const DEFAULT_COL: f32 = 170.;

impl Console {
    fn cell_look(&self, cell: Cell) -> (&'static str, Hsla, String) {
        let t = &self.theme;
        match cell {
            Cell::InSync => ("●", alpha(t.ok, 0.75), String::new()),
            Cell::Custom(a) => ("◆", t.accent, a.short()),
            Cell::Drift(a) => ("▲", t.warn, format!("has {}", a.short())),
            Cell::Unavailable => ("○", t.err, "no provider".into()),
            Cell::Unknown => ("?", t.faint, "offline".into()),
        }
    }

    fn set_cell(&mut self, role: usize, machine: usize, cell: Cell) {
        if let Some(c) = self.roles.get_mut(role).and_then(|r| r.cells.get_mut(machine)) {
            *c = cell;
        }
    }

    fn apply_all(&mut self) {
        for role in &mut self.roles {
            for cell in &mut role.cells {
                if matches!(cell, Cell::Drift(_)) {
                    *cell = Cell::InSync;
                }
            }
        }
    }

    pub(super) fn bar(&self, frac: f32, color: Hsla, width: f32) -> Div {
        let t = &self.theme;
        div()
            .w(px(width))
            .h(px(4.))
            .rounded(px(2.))
            .bg(t.border)
            .child(div().h_full().w(relative(frac.clamp(0., 1.))).rounded(px(2.)).bg(color))
    }

    fn machine_card(&self, mi: usize, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let m = &self.machines[mi];
        let selected = self.agents_focus == AgentsFocus::Machine(mi);
        let offline = m.health == Health::Offline;
        let overrides = self
            .roles
            .iter()
            .filter(|r| matches!(r.cells[mi], Cell::Custom(_) | Cell::Drift(_)))
            .count();
        let hover = t.border_strong;
        let build = if offline {
            "unreachable".to_string()
        } else if m.behind == 0 {
            format!("{} · up to date", m.build)
        } else {
            format!("{} · {} behind", m.build, m.behind)
        };
        let row = |k: &'static str, v: AnyElement| {
            div()
                .flex()
                .items_center()
                .gap_2()
                .text_size(px(11.))
                .child(div().w(px(52.)).flex_none().text_color(t.faint).child(k))
                .child(div().flex_1().min_w_0().truncate().text_color(t.muted).child(v))
        };
        div()
            .id(SharedString::from(format!("machine-card-{mi}")))
            .w(px(232.))
            .flex_none()
            .p_3()
            .flex()
            .flex_col()
            .gap(px(6.))
            .rounded(px(t.radius + 2.))
            .border_1()
            .border_color(if selected { alpha(t.accent, 0.7) } else { t.border })
            .bg(t.surface)
            .cursor_pointer()
            .hover(move |s| s.border_color(hover))
            .when(offline, |d| d.opacity(0.6))
            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                this.agents_focus = AgentsFocus::Machine(mi);
                cx.notify();
            }))
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .child(div().text_color(self.health_color(m.health)).child("●"))
                    .child(div().flex_1().text_color(t.text).child(m.name.clone()))
                    .child(div().text_size(px(10.5)).text_color(t.faint).child(m.kind.clone())),
            )
            .child(row(
                "build",
                div()
                    .text_color(if m.behind > 0 { t.warn } else { t.muted })
                    .child(build)
                    .into_any_element(),
            ))
            .child(row(
                "jobs",
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .child(self.bar(m.running as f32 / m.slots as f32, t.accent, 64.))
                    .child(format!("{}/{}", m.running, m.slots))
                    .into_any_element(),
            ))
            .child(row(
                "cpu",
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .child(self.bar(m.cpu, t.muted, 64.))
                    .child(format!("{:.0}%", m.cpu * 100.))
                    .into_any_element(),
            ))
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .pt_1()
                    .text_size(px(10.5))
                    .text_color(t.faint)
                    .child(format!("{} key", m.access))
                    .child("·")
                    .child(m.sandbox.clone())
                    .child(div().flex_1())
                    .child(div().text_color(t.muted).child(format!("${:.2}", m.cost_today))),
            )
            .when(overrides > 0, |d| {
                d.child(
                    div()
                        .text_size(px(10.5))
                        .text_color(t.accent)
                        .child(format!("◆ {overrides} override{}", if overrides == 1 { "" } else { "s" })),
                )
            })
            .into_any_element()
    }

    pub(super) fn render_agents(&self, cx: &mut Context<Self>) -> impl IntoElement + use<> {
        let t = &self.theme;
        let drift = self.drift_count();
        let hover = t.hover;

        let pending_bar = div()
            .flex()
            .items_center()
            .gap_3()
            .px_3()
            .py_2()
            .rounded(px(t.radius))
            .border_1()
            .map(|d| {
                if drift > 0 {
                    d.border_color(alpha(t.warn, 0.45))
                        .bg(alpha(t.warn, 0.07))
                        .child(div().text_color(t.warn).child("▲"))
                        .child(
                            div()
                                .flex_1()
                                .text_color(t.text)
                                .child(format!("{drift} setting{} differ from the fleet table", if drift == 1 { "" } else { "s" })),
                        )
                        .child(self.button("agents-review", "review", false).on_click(cx.listener(
                            |this, _: &ClickEvent, _, cx| {
                                this.agents_focus = AgentsFocus::Fleet;
                                cx.notify();
                            },
                        )))
                        .child(self.button("agents-apply-all", "apply all", true).on_click(cx.listener(
                            |this, _: &ClickEvent, _, cx| {
                                this.apply_all();
                                cx.notify();
                            },
                        )))
                } else {
                    d.border_color(t.border)
                        .child(div().text_color(t.ok).child("●"))
                        .child(div().flex_1().text_color(t.muted).child("Every machine runs the fleet table"))
                }
            });

        // Grid header: role, fleet default, one column per machine.
        let header = div()
            .flex()
            .items_center()
            .h(px(30.))
            .px_3()
            .border_b_1()
            .border_color(t.border)
            .text_size(px(10.5))
            .text_color(t.faint)
            .child(div().w(px(ROLE_COL)).flex_none().child("ROLE"))
            .child(div().w(px(DEFAULT_COL)).flex_none().child("FLEET DEFAULT"))
            .children(self.machines.iter().map(|m| {
                div()
                    .w(px(COL))
                    .flex_none()
                    .flex()
                    .items_center()
                    .gap_1()
                    .child(div().text_color(self.health_color(m.health)).child("●"))
                    .child(m.name.to_uppercase())
            }));

        let rows = self.roles.iter().enumerate().map(|(ri, role)| {
            let role_selected = self.agents_focus == AgentsFocus::Role(ri);
            let cells = role.cells.iter().enumerate().map(|(mi, cell)| {
                let (glyph, color, text) = self.cell_look(cell.clone());
                let selected = self.agents_focus == AgentsFocus::Cell(ri, mi);
                div()
                    .id(SharedString::from(format!("cell-{ri}-{mi}")))
                    .w(px(COL))
                    .flex_none()
                    .h(px(26.))
                    .px_1()
                    .mr_1()
                    .flex()
                    .items_center()
                    .gap(px(6.))
                    .rounded(px(t.radius))
                    .cursor_pointer()
                    .border_1()
                    .border_color(if selected { alpha(t.accent, 0.7) } else { alpha(t.accent, 0.0) })
                    .when(selected, |d| d.bg(t.raised))
                    .hover(move |s| s.bg(hover))
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        this.agents_focus = AgentsFocus::Cell(ri, mi);
                        cx.notify();
                    }))
                    .child(div().text_color(color).text_size(px(11.)).child(glyph))
                    .child(div().min_w_0().truncate().text_size(px(11.)).text_color(t.muted).child(text))
            });
            div()
                .flex()
                .items_center()
                .px_3()
                .py(px(3.))
                .border_b_1()
                .border_color(alpha(t.border, 0.6))
                .when(role_selected, |d| d.bg(alpha(t.accent, 0.05)))
                .child(
                    div()
                        .id(SharedString::from(format!("role-{ri}")))
                        .w(px(ROLE_COL))
                        .flex_none()
                        .min_w_0()
                        .pr_3()
                        .cursor_pointer()
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            this.agents_focus = AgentsFocus::Role(ri);
                            cx.notify();
                        }))
                        .child(div().truncate().text_color(t.text).child(role.label.clone()))
                        .child(div().truncate().text_size(px(10.5)).text_color(t.faint).child(role.purpose.clone())),
                )
                .child(
                    div()
                        .id(SharedString::from(format!("default-{ri}")))
                        .w(px(DEFAULT_COL))
                        .flex_none()
                        .min_w_0()
                        .pr_3()
                        .rounded(px(t.radius))
                        .cursor_pointer()
                        .hover(move |s| s.bg(hover))
                        .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| {
                            this.agents_focus = AgentsFocus::Role(ri);
                            this.open_picker(super::models::PickerScope::Role(ri), window, cx);
                        }))
                        .child(div().truncate().text_color(t.muted).child(role.default.short()))
                        .child(div().truncate().text_size(px(10.5)).text_color(t.faint).child(role.default.provider.clone())),
                )
                .children(cells)
        })
        .collect::<Vec<_>>();

        let cards: Vec<_> = (0..self.machines.len()).map(|mi| self.machine_card(mi, cx)).collect();

        let legend = div()
            .flex()
            .gap_4()
            .px_3()
            .py_2()
            .text_size(px(10.5))
            .text_color(t.faint)
            .children(
                [
                    (Cell::InSync, "fleet default"),
                    (Cell::Custom(Assignment { provider: "".into(), model: "".into(), thinking: "".into(), tier: "".into() }), "override"),
                    (Cell::Drift(Assignment { provider: "".into(), model: "".into(), thinking: "".into(), tier: "".into() }), "differs, apply fixes"),
                    (Cell::Unavailable, "provider missing"),
                    (Cell::Unknown, "unreachable"),
                ]
                .into_iter()
                .map(|(cell, label)| {
                    let (glyph, color, _) = self.cell_look(cell.clone());
                    div().flex().gap_1().child(div().text_color(color).child(glyph)).child(label)
                }),
            );

        div()
            .id("agents-page")
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
                    .child(div().text_size(px(16.)).text_color(t.text).child("Agents"))
                    .child(div().text_color(t.faint).child(
                        "One model per role for the whole fleet. A machine differs only where you give it an override.",
                    )),
            )
            .child(pending_bar)
            .child(self.section("MACHINES", Some(self.machines.len())).px_0())
            .child(
                div()
                    .flex()
                    .flex_wrap()
                    .gap_3()
                    .children(cards)
                    .child(
                        div()
                            .id("add-machine")
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
                            .child(div().text_color(t.muted).child("+ add machine"))
                            .child(div().text_size(px(10.5)).child("this pc · tailnet · relay · ssh")),
                    ),
            )
            .child(self.section("ROLES", Some(self.roles.len())).px_0())
            .child(
                div()
                    .rounded(px(t.radius + 2.))
                    .border_1()
                    .border_color(t.border)
                    .bg(t.surface)
                    .child(header)
                    .children(rows)
                    .child(legend),
            )
    }

    // Inspector

    fn kv_row(&self, k: &'static str, v: impl IntoElement) -> Div {
        let t = &self.theme;
        div()
            .flex()
            .gap_2()
            .px_3()
            .py(px(2.))
            .child(div().w(px(72.)).flex_none().text_color(t.faint).child(k))
            .child(div().flex_1().min_w_0().text_color(t.muted).child(v))
    }

    fn inspector_title(&self, kicker: &'static str, title: String) -> Div {
        let t = &self.theme;
        div()
            .px_3()
            .pt_3()
            .pb_2()
            .border_b_1()
            .border_color(t.border)
            .child(div().text_color(t.faint).text_size(px(10.5)).child(kicker))
            .child(div().pt_1().text_color(t.text).child(title))
    }

    pub(super) fn render_agents_inspector(&self, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let body = match self.agents_focus {
            AgentsFocus::Fleet => {
                let online = self.machines.iter().filter(|m| m.health != Health::Offline).count();
                let mut pending = Vec::new();
                for (ri, role) in self.roles.iter().enumerate() {
                    for (mi, cell) in role.cells.iter().enumerate() {
                        if let Cell::Drift(have) = cell {
                            pending.push(
                                div()
                                    .id(SharedString::from(format!("pending-{ri}-{mi}")))
                                    .mx_1()
                                    .px_2()
                                    .py(px(4.))
                                    .rounded(px(t.radius))
                                    .cursor_pointer()
                                    .hover({
                                        let h = t.hover;
                                        move |s| s.bg(h)
                                    })
                                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                        this.agents_focus = AgentsFocus::Cell(ri, mi);
                                        cx.notify();
                                    }))
                                    .child(
                                        div()
                                            .flex()
                                            .gap_2()
                                            .child(div().text_color(t.warn).child("▲"))
                                            .child(div().text_color(t.text).child(role.label.clone()))
                                            .child(div().text_color(t.faint).child(self.machines[mi].name.clone())),
                                    )
                                    .child(
                                        div()
                                            .pl(px(18.))
                                            .text_size(px(11.))
                                            .text_color(t.faint)
                                            .child(format!("{} → {}", have.short(), role.default.short())),
                                    ),
                            );
                        }
                    }
                }
                let has_pending = !pending.is_empty();
                div()
                    .child(self.inspector_title("FLEET", "Whole fleet".into()))
                    .child(
                        div()
                            .pt_2()
                            .child(self.kv_row("machines", format!("{online} of {} online", self.machines.len())))
                            .child(self.kv_row("roles", format!("{}", self.roles.len())))
                            .child(self.kv_row("pending", format!("{} change{}", pending.len(), if pending.len() == 1 { "" } else { "s" }))),
                    )
                    .child(self.section("PENDING", None))
                    .when(!has_pending, |d| {
                        d.child(div().px_3().text_color(t.faint).child("Nothing to apply."))
                    })
                    .children(pending)
                    .when(has_pending, |d| {
                        d.child(
                            div().px_3().pt_2().child(
                                self.button("inspector-apply-all", "apply all", true).on_click(cx.listener(
                                    |this, _: &ClickEvent, _, cx| {
                                        this.apply_all();
                                        cx.notify();
                                    },
                                )),
                            ),
                        )
                    })
                    .when(self.via.is_none() && self.link != super::Link::Demo, |d| d.child(self.link_section(cx)))
                    .child(self.section("TIP", None))
                    .child(
                        div()
                            .px_3()
                            .text_size(px(11.))
                            .text_color(t.faint)
                            .child("Select a cell to override one machine, or a role to change the fleet default."),
                    )
            }
            AgentsFocus::Machine(mi) => {
                let m = &self.machines[mi];
                let overrides = self.roles.iter().enumerate().filter_map(|(ri, r)| match &r.cells[mi] {
                    Cell::Custom(a) | Cell::Drift(a) => Some((ri, r.label.clone(), a.clone(), matches!(r.cells[mi], Cell::Drift(_)))),
                    _ => None,
                });
                div()
                    .child(self.inspector_title("MACHINE", m.name.to_string()))
                    .child(
                        div()
                            .pt_2()
                            .child(self.kv_row(
                                "health",
                                div()
                                    .flex()
                                    .gap_1()
                                    .child(div().text_color(self.health_color(m.health)).child("●"))
                                    .child(match m.health {
                                        Health::Online => "online",
                                        Health::Busy => "busy",
                                        Health::Offline => "offline",
                                    }),
                            ))
                            .child(self.kv_row("reached", m.kind.clone()))
                            .child(self.kv_row("key", format!("{} access", m.access)))
                            .child(self.kv_row(
                                "build",
                                if m.behind == 0 {
                                    format!("{} · up to date", m.build)
                                } else {
                                    format!("{} · {} behind", m.build, m.behind)
                                },
                            ))
                            .child(self.kv_row("sandbox", m.sandbox.clone()))
                            .child(self.kv_row("uptime", m.uptime.clone()))
                            .child(self.kv_row("jobs", format!("{} running of {}", m.running, m.slots)))
                            .child(self.kv_row("memory", format!("{:.0}%", m.mem * 100.)))
                            .child(self.kv_row("today", format!("${:.2}", m.cost_today))),
                    )
                    .child(self.section("OVERRIDES", None))
                    .children(overrides.map(|(ri, label, a, drift)| {
                        div()
                            .flex()
                            .gap_2()
                            .px_3()
                            .py(px(2.))
                            .child(div().text_color(if drift { t.warn } else { t.accent }).child(if drift { "▲" } else { "◆" }))
                            .child(div().w(px(84.)).text_color(t.muted).child(label))
                            .child(div().flex_1().min_w_0().truncate().text_color(t.faint).child(a.short()))
                            .id(SharedString::from(format!("ovr-{ri}")))
                    }))
                    .child(
                        div()
                            .flex()
                            .gap_1()
                            .px_3()
                            .pt_3()
                            .child(self.button("machine-sync", "sync to fleet", true).on_click(cx.listener(
                                move |this, _: &ClickEvent, _, cx| {
                                    for role in &mut this.roles {
                                        if matches!(role.cells[mi], Cell::Drift(_)) {
                                            role.cells[mi] = Cell::InSync;
                                        }
                                    }
                                    cx.notify();
                                },
                            )))
                            .when(mi > 0 && self.via.is_none(), |d| {
                                let name = m.name.to_string();
                                let unlink = name.clone();
                                d.child(self.button("machine-open", "open", true).on_click(cx.listener(
                                    move |this, _: &ClickEvent, _, cx| this.open_machine(name.clone(), cx),
                                )))
                                .child(self.button("machine-unlink", "unlink", false).on_click(cx.listener(
                                    move |this, _: &ClickEvent, _, cx| {
                                        if let Some(live) = &this.live {
                                            live.run(crate::swarm::live::Command::UnlinkMachine { name: unlink.clone() });
                                        }
                                        cx.notify();
                                    },
                                )))
                            }),
                    )
                    .when(mi > 0 && self.via.is_none(), |d| {
                        d.child(self.section("ITS ACCESS", None)).child(
                            div().px_3().text_size(px(11.)).text_color(t.faint).child(
                                "Home holds an owner session on it and reaches it over its private link; it cannot reach home. Open shows its own Swarm through home. On the machine, sudo swarmctl fleet drop HOME ends home's control at once.",
                            ),
                        )
                    })
            }
            AgentsFocus::Role(ri) => {
                let role = &self.roles[ri];
                div()
                    .child(self.inspector_title("ROLE", role.label.to_string()))
                    .child(
                        div()
                            .pt_2()
                            .child(self.kv_row("does", role.purpose.clone()))
                            .child(self.kv_row("setting", format!("{} slot", role.key)))
                            .child(self.kv_row("provider", role.default.provider.clone()))
                            .child(self.kv_row("model", role.default.model.clone()))
                            .child(self.kv_row(
                                "thinking",
                                if role.default.thinking.is_empty() { "default".into() } else { role.default.thinking.clone() },
                            )),
                    )
                    .child(self.section("ON EACH MACHINE", None))
                    .children(role.cells.iter().enumerate().map(|(mi, cell)| {
                        let (glyph, color, text) = self.cell_look(cell.clone());
                        div()
                            .flex()
                            .gap_2()
                            .px_3()
                            .py(px(2.))
                            .child(div().text_color(color).child(glyph))
                            .child(div().w(px(64.)).text_color(t.muted).child(self.machines[mi].name.clone()))
                            .child(div().text_color(t.faint).child(if text.is_empty() { "fleet default".to_string() } else { text }))
                    }))
                    .child(div().px_3().pt_3().child(
                        self.button("role-change", "change default…", true)
                            .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.open_picker(super::models::PickerScope::Role(ri), window, cx))),
                    ))
                    .children(self.render_model_picker_for_role(ri, cx).map(|p| div().px_2().pt_2().child(p)))
            }
            AgentsFocus::Cell(ri, mi) => {
                let role = &self.roles[ri];
                let machine = self.machines[mi].name.clone();
                let cell = role.cells[mi].clone();
                let (glyph, color, _) = self.cell_look(cell.clone());
                let (state, explain, have): (&str, String, Option<Assignment>) = match cell.clone() {
                    Cell::InSync => ("fleet default", format!("{machine} runs the fleet default for {}.", role.label), None),
                    Cell::Custom(a) => ("override", format!("{machine} deliberately runs its own model for {}.", role.label), Some(a)),
                    Cell::Drift(a) => (
                        "differs",
                        format!("{machine} runs something other than the fleet table says, and has no override. Apply puts it back, or keep it as an override."),
                        Some(a),
                    ),
                    Cell::Unavailable => (
                        "provider missing",
                        format!("{} is not connected on {machine}, so {} cannot run there.", role.default.provider, role.label),
                        None,
                    ),
                    Cell::Unknown => ("unreachable", format!("{machine} is offline. Its setting is unknown."), None),
                };
                let actions = div().flex().flex_wrap().gap_1().px_3().pt_3();
                let actions = match cell.clone() {
                    Cell::Drift(a) => actions
                        .child(self.button("cell-apply", "apply fleet default", true).on_click(cx.listener(
                            move |this, _: &ClickEvent, _, cx| {
                                this.set_cell(ri, mi, Cell::InSync);
                                cx.notify();
                            },
                        )))
                        .child(self.button("cell-keep", "keep as override", false).on_click(cx.listener(
                            move |this, _: &ClickEvent, _, cx| {
                                this.set_cell(ri, mi, Cell::Custom(a.clone()));
                                cx.notify();
                            },
                        ))),
                    Cell::Custom(_) => actions
                        .child(self.button("cell-default", "use fleet default", true).on_click(cx.listener(
                            move |this, _: &ClickEvent, _, cx| {
                                this.set_cell(ri, mi, Cell::InSync);
                                cx.notify();
                            },
                        )))
                        .child(self.button("cell-edit", "change…", false)),
                    Cell::InSync => actions.child(
                        self.button("cell-override", "change the default…", false)
                            .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.open_picker(super::models::PickerScope::Role(ri), window, cx))),
                    ),
                    Cell::Unavailable => actions.child(self.button("cell-connect", "connect provider…", true)),
                    Cell::Unknown => actions,
                };
                div()
                    .child(self.inspector_title("ROLE ON MACHINE", format!("{} on {machine}", role.label)))
                    .child(
                        div()
                            .pt_2()
                            .child(self.kv_row(
                                "state",
                                div().flex().gap_1().child(div().text_color(color).child(glyph)).child(state),
                            ))
                            .child(self.kv_row("fleet", role.default.short()))
                            .child(self.kv_row("here", have.map(|a| a.short()).unwrap_or_else(|| match cell {
                                Cell::InSync => role.default.short(),
                                _ => "—".into(),
                            }))),
                    )
                    .child(div().px_3().pt_3().text_size(px(11.)).text_color(t.faint).child(explain))
                    .child(actions)
                    .children(self.render_model_picker_for_role(ri, cx).map(|p| div().px_2().pt_2().child(p)))
            }
        };

        div()
            .w(px(312.))
            .flex_none()
            .h_full()
            .bg(t.panel)
            .border_l_1()
            .border_color(t.border)
            .child(div().id("agents-inspector").size_full().overflow_y_scroll().pb_3().child(body))
    }
}
