//! Agents: each machine's model for every role, and the inspector the Fleet
//! page shares (a machine, the whole fleet, linking).
//!
//! Every card and cell is what that machine's own Swarm said (its
//! GET /v3/box/summary and GET /v1/agent-model-settings, a linked machine's
//! read through home). The first column is the setting on the Swarm this
//! console is connected to; other machines' cells say whether they run the
//! same model, another one, or are unknown. In home's window a linked
//! machine that runs another model can be given home's setting for that
//! role, on the person's click only, never while home's linked work rule
//! for it is off (swarm::fleet_models); the machine's settings are then
//! read again.

use std::collections::{HashMap, HashSet};

use gpui::{AnyElement, ClickEvent, Context, Div, Hsla, SharedString, div, prelude::*, px, relative};

use super::{AgentsFocus, Console, Link, Page, alpha};
use crate::data::{Assignment, Cell, Health, Machine};
use crate::swarm::fleet_models;

/// Model settings being sent to linked machines, by (role key, machine),
/// and the last refusal of each, in the machine's words.
#[derive(Default)]
pub(super) struct ModelApply {
    saving: HashSet<(String, String)>,
    refused: HashMap<(String, String), String>,
}

const COL: f32 = 128.;
const ROLE_COL: f32 = 200.;
const DEFAULT_COL: f32 = 170.;

impl Console {
    fn cell_look(&self, cell: Cell) -> (&'static str, Hsla, String) {
        let t = &self.theme;
        match cell {
            Cell::InSync => ("●", alpha(t.ok, 0.75), String::new()),
            Cell::Custom(a) => ("◆", t.accent, a.short()),
            Cell::Unknown => ("?", t.faint, "unknown".into()),
        }
    }

    /// Role `ri` on machine `mi`, from what that machine's Swarm said.
    fn cell(&self, ri: usize, mi: usize) -> Cell {
        self.roles.get(ri).map(|r| crate::data::cell(r, &self.machines, mi)).unwrap_or(Cell::Unknown)
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

    /// The status word and its color; "offline" with when home last reached it.
    fn status_text(&self, m: &Machine) -> (String, Hsla) {
        let t = &self.theme;
        let color = match m.health {
            Health::Attention => t.warn,
            Health::Degraded => t.err,
            _ => t.muted,
        };
        let contact = m.contact_line(super::now_ms());
        let word = if m.health == Health::Offline && !contact.is_empty() {
            format!("offline · {contact}")
        } else {
            m.health.label().to_string()
        };
        (word, color)
    }

    pub(super) fn machine_card(&self, mi: usize, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let m = &self.machines[mi];
        let selected = self.agents_focus == AgentsFocus::Machine(mi);
        let offline = m.health == Health::Offline;
        let differs = (0..self.roles.len()).filter(|ri| matches!(self.cell(*ri, mi), Cell::Custom(_))).count();
        let hover = t.border_strong;
        let (status, status_color) = self.status_text(m);
        let row = |k: &'static str, v: AnyElement| {
            div()
                .flex()
                .items_center()
                .gap_2()
                .text_size(px(11.))
                .child(div().w(px(52.)).flex_none().text_color(t.faint).child(k))
                .child(div().flex_1().min_w_0().truncate().text_color(t.muted).child(v))
        };
        let text = |s: String, c: Hsla| div().text_color(c).child(s).into_any_element();
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
            .child(row("status", text(status, status_color)))
            .children(m.reasons.first().map(|r| {
                let more = m.reasons.len() - 1;
                div()
                    .text_size(px(10.5))
                    .text_color(status_color)
                    .truncate()
                    .child(if more > 0 { format!("{r} (+{more})") } else { r.to_string() })
            }))
            .child(row("build", text(m.build_line(), if m.update.warns() { t.warn } else { t.muted })))
            .child(row("now", text(m.now_line(), t.muted)))
            .child(row("runs", text(m.runs_line(), if m.runs.is_some_and(|r| r.failed > 0) { t.warn } else { t.muted })))
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .pt_1()
                    .text_size(px(10.5))
                    .text_color(t.faint)
                    .child(format!("up {}", m.uptime_line()))
                    .child("·")
                    .child(format!("sandbox {}", m.sandbox_line()))
                    .child(div().flex_1())
                    .child(div().text_color(t.muted).child(format!("{} today", m.cost_line()))),
            )
            .when(differs > 0, |d| {
                d.child(
                    div()
                        .text_size(px(10.5))
                        .text_color(t.accent)
                        .child(format!("◆ {differs} role{} on another model", if differs == 1 { "" } else { "s" })),
                )
            })
            .into_any_element()
    }

    pub(super) fn render_agents(&self, cx: &mut Context<Self>) -> impl IntoElement + use<> {
        let t = &self.theme;
        let hover = t.hover;

        // Grid header: role, the default here, one column per machine.
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
            .child(div().w(px(DEFAULT_COL)).flex_none().child("DEFAULT"))
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
            let cells = (0..self.machines.len()).map(|mi| {
                let cell = self.cell(ri, mi);
                let (glyph, color, text) = self.apply_cell_text(ri, mi).unwrap_or_else(|| self.cell_look(cell.clone()));
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

        let legend = div()
            .flex()
            .gap_4()
            .px_3()
            .py_2()
            .text_size(px(10.5))
            .text_color(t.faint)
            .children(
                [
                    (Cell::InSync, "same as default"),
                    (Cell::Custom(Assignment { provider: "".into(), model: "".into(), thinking: "".into(), tier: "".into() }), "another model"),
                    (Cell::Unknown, "not read"),
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
                        "Each machine's model for every role, as its own Swarm reports it. DEFAULT is the setting on the Swarm this window is connected to; other machines show whether they run the same model; select a role or a cell on another model to give a linked machine the home's setting. The machines themselves are on the Fleet page.",
                    )),
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
        // The Fleet page selects machines; a role or cell belongs to Agents.
        let focus = match (self.page, self.agents_focus) {
            (super::Page::Fleet, AgentsFocus::Role(_) | AgentsFocus::Cell(..)) => AgentsFocus::Fleet,
            (_, focus) => focus,
        };
        let tip = if self.page == super::Page::Fleet {
            "Select a machine for what its Swarm reports."
        } else {
            "Select a role to change its default on this Swarm, or a cell for one machine's model."
        };
        let body = match focus {
            AgentsFocus::Fleet => {
                let online = self.machines.iter().filter(|m| m.health != Health::Offline).count();
                let attention = self.machines.iter().filter(|m| matches!(m.health, Health::Attention | Health::Degraded)).count();
                div()
                    .child(self.inspector_title("FLEET", "Whole fleet".into()))
                    .child(
                        div()
                            .pt_2()
                            .child(self.kv_row("machines", format!("{online} of {} reached", self.machines.len())))
                            .child(self.kv_row("attention", format!("{attention}")))
                            .child(self.kv_row("roles", format!("{}", self.roles.len()))),
                    )
                    .when(self.via.is_none() && self.link != super::Link::Demo, |d| d.child(self.link_section(cx)))
                    .child(self.section("TIP", None))
                    .child(
                        div()
                            .px_3()
                            .text_size(px(11.))
                            .text_color(t.faint)
                            .child(tip),
                    )
            }
            AgentsFocus::Machine(mi) => {
                let m = &self.machines[mi];
                let (status, status_color) = self.status_text(m);
                let differs: Vec<_> = self
                    .roles
                    .iter()
                    .enumerate()
                    .filter_map(|(ri, r)| match self.cell(ri, mi) {
                        Cell::Custom(a) => Some((ri, r.label.clone(), a)),
                        _ => None,
                    })
                    .collect();
                let update_reason = m.update.reason();
                div()
                    .child(self.inspector_title("MACHINE", m.name.to_string()))
                    .child(
                        div()
                            .pt_2()
                            .child(self.kv_row(
                                "status",
                                div()
                                    .flex()
                                    .gap_1()
                                    .child(div().text_color(self.health_color(m.health)).child("●"))
                                    .child(div().text_color(status_color).child(status)),
                            ))
                            .children(m.reasons.iter().map(|r| div().px_3().pl(px(92.)).py(px(1.)).text_size(px(11.)).text_color(status_color).child(r.clone())))
                            .child(self.kv_row("reached", m.kind.clone()))
                            .child(self.kv_row("access", m.access.clone()))
                            .when(!m.gateway.is_empty(), |d| d.child(self.kv_row("address", m.gateway.clone())))
                            .child(self.kv_row("build", m.build_line()))
                            .when(!update_reason.is_empty(), |d| {
                                d.child(div().px_3().pl(px(92.)).py(px(1.)).text_size(px(11.)).text_color(t.faint).child(update_reason.clone()))
                            })
                            .child(self.kv_row("sandbox", m.sandbox_line()))
                            .child(self.kv_row("uptime", m.uptime_line()))
                            .child(self.kv_row("now", m.now_line()))
                            .child(self.kv_row("today", format!("{} · {} spent", m.runs_line(), m.cost_line()))),
                    )
                    .when(mi > 0, |d| {
                        d.child(self.section("ANOTHER MODEL THAN DEFAULT", None))
                            .when(m.models.is_none(), |d| d.child(div().px_3().text_color(t.faint).child("Its models were not read.")))
                            .when(m.models.is_some() && differs.is_empty(), |d| d.child(div().px_3().text_color(t.faint).child("None.")))
                            .children(differs.into_iter().map(|(ri, label, a)| {
                                div()
                                    .flex()
                                    .gap_2()
                                    .px_3()
                                    .py(px(2.))
                                    .child(div().text_color(t.accent).child("◆"))
                                    .child(div().w(px(84.)).text_color(t.muted).child(label))
                                    .child(div().flex_1().min_w_0().truncate().text_color(t.faint).child(a.short()))
                                    .id(SharedString::from(format!("differs-{ri}")))
                            }))
                    })
                    .when(mi > 0 && self.via.is_none(), |d| {
                        let name = m.name.to_string();
                        let unlink = name.clone();
                        d.child(
                            div()
                                .flex()
                                .gap_1()
                                .px_3()
                                .pt_3()
                                .child(self.button("machine-open", "open", true).on_click(cx.listener(
                                    move |this, _: &ClickEvent, _, cx| this.open_machine(name.clone(), cx),
                                )))
                                .child(self.button("machine-unlink", "unlink", false).on_click(cx.listener(
                                    move |this, _: &ClickEvent, _, cx| {
                                        if let Some(live) = &this.live {
                                            live.run(crate::swarm::live::Command::UnlinkMachine { name: unlink.clone() });
                                        }
                                        cx.notify();
                                    },
                                ))),
                        )
                        .child(self.section("ITS ACCESS", None))
                        .child(
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
                    .children(
                        (0..self.machines.len())
                            .map(|mi| {
                                let cell = self.cell(ri, mi);
                                let (glyph, color, text) = self.cell_look(cell);
                                div()
                                    .flex()
                                    .flex_col()
                                    .px_3()
                                    .py(px(2.))
                                    .child(
                                        div()
                                            .flex()
                                            .gap_2()
                                            .child(div().text_color(color).child(glyph))
                                            .child(div().w(px(64.)).text_color(t.muted).child(self.machines[mi].name.clone()))
                                            .child(div().text_color(t.faint).child(if text.is_empty() { "same as default".to_string() } else { text })),
                                    )
                                    .children(self.apply_control(ri, mi, cx).map(|c| div().pl(px(20.)).pt(px(2.)).child(c)))
                            })
                            .collect::<Vec<_>>(),
                    )
                    .children(self.apply_all_control(ri, cx).map(|c| div().px_3().pt_2().child(c)))
                    .child(div().px_3().pt_3().child(
                        self.button("role-change", "change default…", true)
                            .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.open_picker(super::models::PickerScope::Role(ri), window, cx))),
                    ))
                    .children(self.render_model_picker_for_role(ri, cx).map(|p| div().px_2().pt_2().child(p)))
            }
            AgentsFocus::Cell(ri, mi) => {
                let role = &self.roles[ri];
                let machine = self.machines[mi].name.clone();
                let cell = self.cell(ri, mi);
                let (glyph, color, _) = self.cell_look(cell.clone());
                let (state, explain, have): (&str, String, Option<Assignment>) = match cell {
                    Cell::InSync if mi == 0 => ("default", format!("The default for {} on this Swarm.", role.label), Some(role.default.clone())),
                    Cell::InSync => ("same as default", format!("{machine}'s Swarm runs the same model for {} as the default.", role.label), Some(role.default.clone())),
                    Cell::Custom(a) => ("another model", format!("{machine}'s Swarm runs another model for {} than the default.", role.label), Some(a)),
                    Cell::Unknown => ("unknown", format!("{machine}'s setting for {} was not read.", role.label), None),
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
                            .child(self.kv_row("default", role.default.short()))
                            .child(self.kv_row("here", have.map(|a| a.short()).unwrap_or_else(|| "—".into()))),
                    )
                    .child(div().px_3().pt_3().text_size(px(11.)).text_color(t.faint).child(explain))
                    // A linked machine on another model can be given home's setting.
                    .children(self.apply_control(ri, mi, cx).map(|c| div().px_3().pt_3().child(c)))
                    // Only the connected Swarm's own setting is changed here.
                    .when(mi == 0, |d| {
                        d.child(div().px_3().pt_3().child(
                            self.button("cell-change", "change the default…", false)
                                .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.open_picker(super::models::PickerScope::Role(ri), window, cx))),
                        ))
                        .children(self.render_model_picker_for_role(ri, cx).map(|p| div().px_2().pt_2().child(p)))
                    })
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

/// Giving a linked machine home's setting for a role (home's window only).
impl Console {
    /// On the Agents page in home's window: home's permissions are read
    /// once, for the linked work rule each action respects.
    pub(super) fn sync_agents(&mut self, _cx: &mut Context<Self>) {
        if self.page != Page::Agents || self.via.is_some() || self.link == Link::Demo {
            return;
        }
        if self.permissions.is_none() && !self.permissions_busy && self.permissions_error.is_none() {
            self.load_permissions();
        }
    }

    /// Whether home's setting for a role may be sent to machine `mi` now;
    /// Err says why not.
    fn apply_allowed(&self, mi: usize) -> Result<(), String> {
        if self.via.is_some() || self.link == Link::Demo || mi == 0 {
            return Err("Only home's window changes a linked machine.".into());
        }
        if self.home.is_none() {
            return Err("Connecting to Swarm…".into());
        }
        let name = self.machines.get(mi).map(|m| m.name.to_string()).unwrap_or_default();
        match fleet_models::work_blocked(self.permissions.as_ref(), &name) {
            Some(why) => Err(why),
            None => Ok(()),
        }
    }

    fn apply_key(&self, ri: usize, mi: usize) -> Option<(String, String)> {
        Some((self.roles.get(ri)?.key.to_string(), self.machines.get(mi)?.name.to_string()))
    }

    /// Sends home's setting for role `ri` to machine `mi`, then reads that
    /// machine's settings again. Only on the person's click.
    fn apply_home_setting(&mut self, ri: usize, mi: usize, cx: &mut Context<Self>) {
        let (Some(role), Some(key)) = (self.roles.get(ri).cloned(), self.apply_key(ri, mi)) else { return };
        if !matches!(self.cell(ri, mi), Cell::Custom(_)) || self.apply_allowed(mi).is_err() {
            return;
        }
        let Some(home) = self.home.clone() else { return };
        if !self.model_apply.saving.insert(key.clone()) {
            return;
        }
        self.model_apply.refused.remove(&key);
        cx.notify();
        let name = key.1.clone();
        cx.spawn(async move |this, cx| {
            let (machine, role_key, setting) = (name.clone(), role.key.to_string(), role.default.clone());
            let result = cx.background_executor().spawn(async move { fleet_models::apply(&home, &machine, &role_key, &setting) }).await;
            this.update(cx, |this, cx| {
                this.model_apply.saving.remove(&key);
                this.notice = Some(match result {
                    Ok(Ok(models)) => {
                        let now = models.iter().find(|(k, _)| k.as_ref() == role.key.as_ref()).map(|(_, a)| a.clone());
                        if let Some(m) = this.machines.iter_mut().find(|m| m.name.as_ref() == name.as_str()) {
                            m.models = Some(models);
                        }
                        match now {
                            Some(a) if a == role.default => (format!("{name} runs home's setting for {} now.", role.label).into(), false),
                            Some(a) => (format!("{name} answered and reports {} for {}.", a.short(), role.label).into(), true),
                            None => (format!("{name} answered but does not list {}.", role.label).into(), true),
                        }
                    }
                    Ok(Err(e)) => (format!("{name} took the setting; reading it back failed: {e}").into(), true),
                    Err(e) => {
                        let text = format!("{name} did not take the setting: {e}");
                        this.model_apply.refused.insert(key.clone(), e);
                        (text.into(), true)
                    }
                });
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    /// Home's setting for role `ri` to every machine known to run another
    /// model, where the linked work rule allows it.
    fn apply_to_all(&mut self, ri: usize, cx: &mut Context<Self>) {
        let Some(role) = self.roles.get(ri) else { return };
        for mi in fleet_models::differing(role, &self.machines) {
            if self.apply_allowed(mi).is_ok() {
                self.apply_home_setting(ri, mi, cx);
            }
        }
    }

    /// A cell's text while a setting is sent to it or after it was refused.
    fn apply_cell_text(&self, ri: usize, mi: usize) -> Option<(&'static str, Hsla, String)> {
        let key = self.apply_key(ri, mi)?;
        if self.model_apply.saving.contains(&key) {
            return Some(("…", self.theme.info, "saving…".into()));
        }
        self.model_apply.refused.contains_key(&key).then(|| ("✕", self.theme.err, "refused".into()))
    }

    /// "apply home's setting" for one machine: the button, "saving…", or
    /// why it is off; and the machine's last refusal.
    fn apply_control(&self, ri: usize, mi: usize, cx: &mut Context<Self>) -> Option<Div> {
        if self.via.is_some() || self.link == Link::Demo || mi == 0 || !matches!(self.cell(ri, mi), Cell::Custom(_)) {
            return None;
        }
        let t = &self.theme;
        let key = self.apply_key(ri, mi)?;
        let control = if self.model_apply.saving.contains(&key) {
            div().text_size(px(11.)).text_color(t.info).child("saving…")
        } else {
            match self.apply_allowed(mi) {
                Ok(()) => div().child(
                    self.button(SharedString::from(format!("apply-{ri}-{mi}")), "apply home's setting", false)
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.apply_home_setting(ri, mi, cx))),
                ),
                Err(why) => div()
                    .flex()
                    .flex_col()
                    .gap(px(2.))
                    .child(div().flex().child(self.off_button("apply home's setting")))
                    .child(div().text_size(px(10.5)).text_color(t.faint).child(why)),
            }
        };
        Some(
            div()
                .flex()
                .flex_col()
                .gap(px(2.))
                .child(control)
                .children(self.model_apply.refused.get(&key).map(|e| div().text_size(px(10.5)).text_color(t.err).child(format!("Refused: {e}")))),
        )
    }

    /// "apply to all machines" for a role, when any machine runs another model.
    fn apply_all_control(&self, ri: usize, cx: &mut Context<Self>) -> Option<Div> {
        if self.via.is_some() || self.link == Link::Demo {
            return None;
        }
        let role = self.roles.get(ri)?;
        let differing = fleet_models::differing(role, &self.machines);
        if differing.is_empty() {
            return None;
        }
        let ready = differing
            .iter()
            .filter(|mi| self.apply_allowed(**mi).is_ok() && self.apply_key(ri, **mi).is_some_and(|k| !self.model_apply.saving.contains(&k)))
            .count();
        let t = &self.theme;
        Some(
            div()
                .flex()
                .flex_col()
                .gap(px(2.))
                .child(if ready > 0 {
                    div().flex().child(
                        self.button(SharedString::from(format!("apply-all-{ri}")), format!("apply to all machines ({ready})"), false)
                            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.apply_to_all(ri, cx))),
                    )
                } else {
                    div().flex().child(self.off_button("apply to all machines"))
                })
                .when(ready < differing.len(), |d| {
                    d.child(div().text_size(px(10.5)).text_color(t.faint).child(format!(
                        "{} of {} machine{} on another model can't be changed now; see each below.",
                        differing.len() - ready,
                        differing.len(),
                        if differing.len() == 1 { "" } else { "s" }
                    )))
                }),
        )
    }
}
