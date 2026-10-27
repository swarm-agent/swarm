//! Projects: the title bar's project menu (switch, add folders, a new
//! project, the Projects page) and the Projects page itself: each project's
//! folders and project.md, a rename, an edit of project.md. A new project
//! and more folders go through the onboarding's project step, which readies
//! folders the way the first run does. Everything else is Swarm's record,
//! changed with PATCH /v3/projects/{id}.

use gpui::{AnyElement, ClickEvent, Context, Div, Focusable, SharedString, Stateful, div, prelude::*, px};
use serde_json::json;

use super::{Console, Link, Page, alpha};
use crate::icons::Icon;
use crate::onboarding::{Existing, Onboarding, OnboardingEvent};
use crate::swarm::live::Command;
use crate::swarm::target::Target;

#[derive(Clone)]
struct DraggedProject { id: String }

impl Console {
    pub(super) fn select_project(&mut self, id: &str, cx: &mut Context<Self>) {
        let Some(pi) = project_position(self.projects.iter().map(|p| p.id.as_ref()), id) else { return };
        self.project = pi;
        self.projects_tab = Some(id.to_string());
        self.page = Page::Projects;
        self.project_menu = false;
        self.project_rename = None;
        self.context_edit = None;
        self.follow_theme(cx);
        cx.notify();
    }

    fn move_project(&mut self, id: &str, offset: isize, cx: &mut Context<Self>) {
        let ids: Vec<String> = self.projects.iter().filter(|p| !p.id.is_empty()).map(|p| p.id.to_string()).collect();
        if let (Some(order), Some(live)) = (moved_order(ids, id, offset), &self.live) {
            live.run(Command::OrderProjects { project_ids: order });
        }
        cx.notify();
    }

    /// Swarm's mark (the brand SVG's four nested squares and opacities) at
    /// a small size: each square steps in by whole pixels, so the edges stay
    /// crisp where the SVG's fractional insets would blur into one blob.
    pub(super) fn brand_mark(&self, size: f32) -> Div {
        let accent = self.theme.accent;
        // Three of the four squares: at this size the innermost one made the
        // middle read as a raised knob; without it the centre is a flat top.
        let base = [0.15_f32, 0.35, 0.6];
        let step = (size / 8.).round().max(1.);
        div().relative().flex_none().size(px(size)).children((0..3).map(move |i| {
            let inset = step * i as f32;
            let side = size - 2. * inset;
            div().absolute().top(px(inset)).left(px(inset)).size(px(side)).rounded(px((side * 0.25).round())).bg(alpha(accent, base[i]))
        }))
    }

    /// How many tasks in a project's sessions wait on the person.
    fn needs_in(&self, p: usize) -> usize {
        self.sessions.iter().filter(|s| s.project == p).map(|s| s.tasks.iter().filter(|x| x.status.needs_you()).count()).sum()
    }

    fn needs_badge(&self, n: usize) -> Div {
        let t = &self.theme;
        div().px(px(5.)).rounded(px(t.radius)).bg(alpha(t.warn, 0.16)).text_color(t.warn).text_size(px(10.5)).child(n.to_string())
    }

    /// The title bar: the mark, the project (a menu), the page, and what is
    /// in focus (the open conversation).
    pub(super) fn render_titlebar(&self, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let hover = t.hover;
        let project = &self.projects[self.project];
        let needs = self.needs_in(self.project);
        let focus: Option<SharedString> = match self.page {
            Page::Sessions => self.active_session_title(),
            _ => None,
        };
        div()
            .h(px(36.))
            .flex_none()
            .flex()
            .items_center()
            .gap_3()
            .px_3()
            .border_b_1()
            .border_color(t.border)
            .bg(t.panel)
            .child(self.brand_mark(24.))
            .child(
                div()
                    .id("project-menu-button")
                    .flex()
                    .items_center()
                    .gap_2()
                    .px_2()
                    .py(px(4.))
                    .rounded(px(t.radius))
                    .cursor_pointer()
                    .hover(move |s| s.bg(hover))
                    .when(self.project_menu, |d| d.bg(t.raised))
                    .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                        this.project_menu = !this.project_menu;
                        cx.notify();
                    }))
                    .child(self.icon(Icon::Project, 14., t.accent))
                    .child(div().text_color(t.text).child(project.name.clone()))
                    .when(needs > 0, |d| d.child(self.needs_badge(needs)))
                    .child(self.icon(if self.project_menu { Icon::ChevronDown } else { Icon::ChevronRight }, 12., t.faint)),
            )
            .child(div().text_color(t.border_strong).child("/"))
            .child(div().text_color(t.muted).child(self.page.label()))
            .when_some(focus, |d, title| d.child(div().text_color(t.border_strong).child("/")).child(div().min_w_0().truncate().text_color(t.faint).child(title)))
    }

    /// The title of the conversation in the focused pane.
    fn active_session_title(&self) -> Option<SharedString> {
        let pane = self.panes.iter().find(|p| p.id == self.active)?;
        let s = self.sessions.get(pane.session)?;
        Some(s.title.clone())
    }

    /// The project menu, under the title bar: every project (switch), more
    /// folders for this one, a new project, and the Projects page.
    pub(super) fn render_project_menu(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        if !self.project_menu {
            return None;
        }
        let t = &self.theme;
        let hover = t.hover;
        let entry = |id: &'static str, label: SharedString, strong: bool| {
            div()
                .id(SharedString::from(id))
                .px_3()
                .py(px(5.))
                .cursor_pointer()
                .hover(move |s| s.bg(hover))
                .flex()
                .items_center()
                .gap_2()
                .text_color(if strong { t.text } else { t.muted })
                .child(label)
        };
        let rows: Vec<Stateful<Div>> = self
            .projects
            .iter()
            .enumerate()
            .map(|(pi, p)| {
                let id = p.id.to_string();
                let count = self.sessions.iter().filter(|s| s.project == pi).count();
                let needs = self.needs_in(pi);
                let on = pi == self.project;
                div()
                    .id(SharedString::from(format!("project-{}", p.id)))
                    .px_3()
                    .py(px(5.))
                    .cursor_pointer()
                    .when(on, |d| d.bg(t.raised))
                    .hover(move |s| s.bg(hover))
                    .flex()
                    .items_center()
                    .gap_2()
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        this.select_project(&id, cx);
                    }))
                    .child(self.icon(Icon::Project, 13., if on { t.accent } else { t.faint }))
                    .child(div().flex_1().text_color(if on { t.text } else { t.muted }).child(p.name.clone()))
                    .child(div().text_size(px(10.5)).text_color(t.faint).child(p.workspaces.first().map(|w| w.to_string()).unwrap_or_default()))
                    .when(needs > 0, |d| d.child(self.needs_badge(needs)))
                    .child(div().text_size(px(10.5)).text_color(t.faint).child(format!("{count}")))
            })
            .collect();
        let name = self.projects[self.project].name.clone();
        let folders_id = self.projects[self.project].id.to_string();
        Some(
            div()
                .id("project-menu-backdrop")
                .absolute()
                .inset_0()
                .occlude()
                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                    this.project_menu = false;
                    cx.notify();
                }))
                .child(
                    div()
                        .id("project-menu")
                        .absolute()
                        .left(px(36.))
                        .top(px(38.))
                        .w(px(420.))
                        .flex()
                        .flex_col()
                        .py_1()
                        .rounded(px(t.radius + 2.))
                        .border_1()
                        .border_color(t.border_strong)
                        .bg(t.panel)
                        .shadow_md()
                        .on_click(|_, _, cx| cx.stop_propagation())
                        .child(div().px_3().pt_1().pb_1().text_size(px(10.5)).text_color(t.faint).child("PROJECTS"))
                        .children(rows)
                        .child(div().h(px(1.)).my_1().bg(t.border))
                        .child(entry("project-add-folders", format!("add folders to {name}…").into(), false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            this.project_menu = false;
                            this.add_project_folders(&folders_id, cx);
                        })))
                        .child(entry("project-new", "new project…".into(), false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                            this.project_menu = false;
                            this.new_project(cx);
                        })))
                        .child(entry("project-manage", "all projects: folders, project.md".into(), true).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                            this.project_menu = false;
                            this.page = Page::Projects;
                            cx.notify();
                        }))),
                )
                .into_any_element(),
        )
    }

    /// Opens the onboarding's project step over the console. `prepare` says
    /// which way: a new project, or more folders for one.
    fn start_onboarding(&mut self, prepare: impl FnOnce(&mut Onboarding, &mut Context<Onboarding>), cx: &mut Context<Self>) {
        if self.link == Link::Demo {
            self.notice = Some(("Not in the demo.".into(), true));
            cx.notify();
            return;
        }
        // Folder setup runs root steps on this computer, not on a linked machine.
        if let Some(name) = &self.via {
            self.notice = Some((format!("Add folders on {name} itself: sudo swarmctl workspace add PATH there, then pick them here.").into(), true));
            cx.notify();
            return;
        }
        let target = Target::from_env().unwrap_or(Target::Local);
        let (theme, icons) = (self.theme.clone(), self.icon_style());
        let onboarding = cx.new(|cx| {
            let mut o = Onboarding::new(theme, icons, target, cx);
            prepare(&mut o, cx);
            o
        });
        self._onboarding_done = Some(cx.subscribe(&onboarding, |this, entity, _: &OnboardingEvent, cx| {
            this.pending_project = entity.read(cx).project_id();
            this.page = Page::Projects;
            this.onboarding = None;
            this._onboarding_done = None;
            this.pending_focus = true;
            this.connect_live(cx);
            cx.notify();
        }));
        self.onboarding = Some(onboarding);
        cx.notify();
    }

    pub(super) fn new_project(&mut self, cx: &mut Context<Self>) {
        self.start_onboarding(|o, cx| o.another_project(cx), cx);
    }

    pub(super) fn add_project_folders(&mut self, id: &str, cx: &mut Context<Self>) {
        let Some(p) = self.projects.iter().find(|p| p.id.as_ref() == id) else { return };
        if p.id.is_empty() {
            return;
        }
        let existing = Existing {
            id: p.id.to_string(),
            name: p.name.to_string(),
            workspaces: p.workspaces.iter().zip(p.roles.iter()).map(|(path, role)| json!({"path": path.to_string(), "role": role.to_string()})).collect(),
        };
        self.start_onboarding(move |o, cx| o.add_folders_to(existing, cx), cx);
    }

    /// After a snapshot: the project onboarding just made or changed is the
    /// open one.
    pub(super) fn select_pending_project(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.pending_project.clone() else { return };
        if let Some(pi) = self.projects.iter().position(|p| p.id.as_ref() == id) {
            self.pending_project = None;
            self.projects_tab = Some(id);
            self.project = pi;
            self.follow_theme(cx);
        }
    }

    fn start_project_rename(&mut self, id: &str, window: &mut gpui::Window, cx: &mut Context<Self>) {
        let Some(name) = self.projects.iter().find(|p| p.id.as_ref() == id).map(|p| p.name.to_string()) else { return };
        self.project_rename = Some(id.to_string());
        if let Some(input) = &self.rename_input {
            input.update(cx, |f, cx| f.set_text(name, cx));
            window.focus(&input.focus_handle(cx));
        }
        cx.notify();
    }

    /// Enter in the name field while a project is being renamed.
    pub(super) fn finish_project_rename(&mut self, cx: &mut Context<Self>) -> bool {
        let Some(id) = self.project_rename.take() else { return false };
        let name = self.rename_input.as_ref().map(|f| f.read(cx).text().trim().to_string()).unwrap_or_default();
        if let (Some(p), false) = (self.projects.iter().find(|p| p.id.as_ref() == id.as_str()), name.is_empty()) {
            if let Some(live) = &self.live {
                live.run(Command::PatchProject { project_id: p.id.to_string(), body: json!({"name": name}) });
            }
        }
        cx.notify();
        true
    }

    pub(super) fn cancel_project_rename(&mut self) -> bool {
        self.project_rename.take().is_some()
    }

    fn start_context_edit(&mut self, id: &str, window: &mut gpui::Window, cx: &mut Context<Self>) {
        let Some(context) = self.projects.iter().find(|p| p.id.as_ref() == id).map(|p| p.context.to_string()) else { return };
        self.context_edit = Some(id.to_string());
        if let Some(input) = &self.context_input {
            input.update(cx, |f, cx| f.set_text(context, cx));
            window.focus(&input.focus_handle(cx));
        }
        cx.notify();
    }

    fn save_context(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.context_edit.take() else { return };
        let context = self.context_input.as_ref().map(|f| f.read(cx).text()).unwrap_or_default();
        if let (Some(p), Some(live)) = (self.projects.iter().find(|p| p.id.as_ref() == id.as_str()), &self.live) {
            live.run(Command::PatchProject { project_id: p.id.to_string(), body: json!({"project_context": context}) });
        }
        cx.notify();
    }

    /// The Projects page: every project, its folders and project.md.
    pub(super) fn render_projects(&self, cx: &mut Context<Self>) -> impl IntoElement + use<> {
        let t = &self.theme;
        // One project at a time, picked from a row of tabs; the open project's
        // tab until another is picked.
        let hover = t.hover;
        let tab = self.projects_tab.as_deref().and_then(|id| self.projects.iter().position(|p| p.id.as_ref() == id)).unwrap_or(self.project);
        let tabs = div().flex_none().flex().flex_wrap().gap_1().children(self.projects.iter().enumerate().map(|(pi, p)| {
            let on = pi == tab;
            let open = pi == self.project;
            let needs = self.needs_in(pi);
            let ghost = self.ghost(p.name.clone());
            let target_id = p.id.to_string();
            div()
                .id(SharedString::from(format!("project-tab-{}", p.id)))
                .on_drag(DraggedProject { id: p.id.to_string() }, move |_, _, _, cx| cx.new(|_| ghost.clone()))
                .on_drop(cx.listener(move |this, dragged: &DraggedProject, _, cx| {
                    let from = this.projects.iter().position(|p| p.id.as_ref() == dragged.id.as_str());
                    let to = this.projects.iter().position(|p| p.id.as_ref() == target_id.as_str());
                    if let (Some(from), Some(to)) = (from, to) {
                        this.move_project(&dragged.id, to as isize - from as isize, cx);
                    }
                }))
                .px_3()
                .py(px(5.))
                .rounded(px(t.radius))
                .border_1()
                .border_color(if on { alpha(t.accent, 0.7) } else { t.border })
                .when(on, |d| d.bg(alpha(t.accent, 0.12)))
                .cursor_pointer()
                .hover(move |s| s.bg(hover))
                .flex()
                .items_center()
                .gap_2()
                .on_click(cx.listener({
                    let id = p.id.to_string();
                    move |this, _: &ClickEvent, _, cx| {
                    this.select_project(&id, cx);
                }}))
                .child(self.icon(Icon::Project, 12., if open { t.accent } else { t.faint }))
                .child(div().text_color(if on { t.text } else { t.muted }).child(p.name.clone()))
                .when(needs > 0, |d| d.child(self.needs_badge(needs)))
        }));
        let cards: Vec<AnyElement> = self.projects.get(tab).map(|_| self.render_project_card(tab, cx)).into_iter().collect();
        div()
            .id("projects-page")
            .size_full()
            .overflow_y_scroll()
            .p_5()
            .flex()
            .flex_col()
            .gap_4()
            .max_w(px(860.))
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_3()
                    .child(div().flex_1().text_size(px(16.)).text_color(t.text).child("Projects"))
                    .child(self.button("projects-new", "new project", true).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.new_project(cx)))),
            )
            .child(div().text_size(px(11.)).text_color(t.faint).child("A project is a name and folders on a machine; project.md is what Swarm wrote about them, and every agent reads it. Swarm keeps all of it."))
            .child(div().text_size(px(11.)).text_color(t.faint).child("The first project is the one the app opens with. Move projects to set Swarm's saved order. New projects appear first; edits never change the order."))
            .child(tabs)
            .children(cards)
    }

    fn render_project_card(&self, pi: usize, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let p = &self.projects[pi];
        let on = pi == self.project;
        let renaming = self.project_rename.as_deref() == Some(p.id.as_ref());
        let editing = self.context_edit.as_deref() == Some(p.id.as_ref());
        let field = |input: Option<gpui::Entity<crate::text_input::TextInput>>| {
            div()
                .flex_1()
                .min_w_0()
                .px_2()
                .py(px(4.))
                .rounded(px(t.radius))
                .border_1()
                .border_color(alpha(t.accent, 0.6))
                .bg(t.surface)
                .text_color(t.text)
                .children(input)
        };
        let name: AnyElement = if renaming {
            div()
                .key_context("Rename")
                .on_action(cx.listener(|this, _: &super::RenameSave, _, cx| {
                    this.finish_project_rename(cx);
                }))
                .on_action(cx.listener(|this, _: &super::RenameCancel, _, cx| {
                    this.cancel_project_rename();
                    cx.notify();
                }))
                .flex_1()
                .min_w_0()
                .flex()
                .child(field(self.rename_input.clone()))
                .into_any_element()
        } else {
            div().flex_1().min_w_0().text_size(px(14.)).text_color(t.text).child(p.name.clone()).into_any_element()
        };
        let folders: Vec<AnyElement> = p
            .workspaces
            .iter()
            .zip(p.roles.iter().chain(std::iter::repeat(&SharedString::from(""))))
            .map(|(path, role)| {
                let id = p.id.to_string();
                let folder = path.to_string();
                let confirming = self.folder_remove.as_ref() == Some(&(id.clone(), folder.clone()));
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .px_3()
                    .py(px(4.))
                    .child(div().text_color(t.faint).child("▸"))
                    .child(div().flex_1().min_w_0().truncate().text_color(t.text).child(path.clone()))
                    .child(div().text_size(px(10.5)).text_color(t.faint).child(role.replace('_', " ")))
                    .child(self.button(SharedString::from(format!("remove-{}-{path}", p.id)), if confirming { "confirm removal" } else { "remove" }, false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        if let Some(name) = &this.via {
                            this.notice = Some((format!("Change folders on {name} itself.").into(), true));
                        } else if confirming {
                            if let (Some(project), Some(live)) = (this.projects.iter().find(|p| p.id.as_ref() == id.as_str()), &this.live) {
                                let folders: Vec<_> = project.workspaces.iter().zip(project.roles.iter()).filter(|(path, _)| path.as_ref() != folder.as_str()).map(|(path, role)| json!({"path": path.to_string(), "role": role.to_string()})).collect();
                                live.run(Command::PatchProject { project_id: id.clone(), body: json!({"workspaces": folders}) });
                            }
                            this.folder_remove = None;
                        } else {
                            this.folder_remove = Some((id.clone(), folder.clone()));
                        }
                        cx.notify();
                    })))
                    .when(confirming, |d| d.child(self.button(SharedString::from(format!("cancel-remove-{}-{path}", p.id)), "cancel", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| { this.folder_remove = None; cx.notify(); }))))
                    .into_any_element()
            })
            .collect();
        let context: AnyElement = if editing {
            div()
                .flex()
                .flex_col()
                .gap_2()
                // A set width: the field measures itself against a definite one.
                .child(div().w(px(800.)).px_2().py(px(4.)).rounded(px(t.radius)).border_1().border_color(alpha(t.accent, 0.6)).bg(t.surface).text_color(t.text).text_size(px(11.5)).children(self.context_input.clone()))
                .child(
                    div()
                        .flex()
                        .gap_1()
                        .child(self.button(SharedString::from(format!("project-{}-save-context", p.id)), "save project.md", true).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.save_context(cx))))
                        .child(self.button(SharedString::from(format!("project-{}-cancel-context", p.id)), "cancel", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                            this.context_edit = None;
                            cx.notify();
                        }))),
                )
                .into_any_element()
        } else if p.context_generation.updating() {
            div().into_any_element()
        } else if p.context.is_empty() {
            div().px_3().py_2().text_size(px(11.)).text_color(t.faint).child("Swarm has not written project.md yet.").into_any_element()
        } else {
            div()
                .id(SharedString::from(format!("project-{}-context", p.id)))
                .w_full()
                .min_w_0()
                .flex_none()
                .px_3()
                .py_2()
                .text_size(px(11.5))
                .line_height(px(17.))
                .text_color(t.muted)
                .child(self.markdown(&p.context, usize::MAX, pi, cx).w_full())
                .into_any_element()
        };
        div()
            .flex()
            .flex_col()
            .flex_none()
            .w_full()
            .min_w_0()
            .rounded(px(t.radius + 2.))
            .border_1()
            .border_color(if on { alpha(t.accent, 0.5) } else { t.border })
            .bg(t.surface)
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .px_3()
                    .py_2()
                    .border_b_1()
                    .border_color(alpha(t.border, 0.7))
                    .child(self.icon(Icon::Project, 14., if on { t.accent } else { t.faint }))
                    .child(name)
                    .when(on, |d| d.child(div().text_size(px(10.5)).text_color(t.accent).child("open")))
                    .when(!on, |d| {
                        let id = p.id.to_string();
                        d.child(self.button(SharedString::from(format!("project-{}-open", p.id)), "open", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            this.select_project(&id, cx);
                        })))
                    })
                    .when(pi != 0 && !p.id.is_empty(), |d| {
                        let id = p.id.to_string();
                        d.child(self.button(SharedString::from(format!("project-{}-front", p.id)), "move to front", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.move_project(&id, isize::MIN, cx))))
                    })
                    .when(pi == 0, |d| d.child(div().text_size(px(10.5)).text_color(t.faint).child("default")))
                    .when(!renaming, |d| {
                        let id = p.id.to_string();
                        d.child(self.button(SharedString::from(format!("project-{}-rename", p.id)), "rename", false).on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.start_project_rename(&id, window, cx))))
                    })
                    .child(self.button(SharedString::from(format!("project-{}-folders", p.id)), "add folders", false).on_click(cx.listener({ let id = p.id.to_string(); move |this, _: &ClickEvent, _, cx| this.add_project_folders(&id, cx) })))
                    .when(!editing && !p.id.is_empty(), |d| {
                        let id = p.id.to_string();
                        d.child(self.button(SharedString::from(format!("project-{}-edit-context", p.id)), "edit project.md", false).on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.start_context_edit(&id, window, cx))))
                    }),
            )
            .child(div().flex().gap_2().px_3().py_2().children([(-1, "move left"), (1, "move right")].into_iter().map(|(offset, label)| {
                let id = p.id.to_string();
                self.button(SharedString::from(format!("project-{}-{label}", p.id)), label, false)
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.move_project(&id, offset, cx)))
            })))
            .child(div().px_3().pt_2().text_size(px(10.5)).text_color(t.faint).child("FOLDERS"))
            .child(div().flex().flex_col().pb_1().children(folders))
            .child(div().px_3().pt_1().text_size(px(10.5)).text_color(t.faint).child("PROJECT.MD"))
            .child(div().px_3().py_2().child(p.context_generation.label()))
            .when(matches!(p.context_generation.status.as_ref(), "ready" | "failed"), |d| {
                let id = p.id.to_string();
                let attempt = p.context_generation.attempt;
                d.child(div().flex().px_3().child(self.button(SharedString::from(format!("refresh-context-{}", p.id)), if p.context_generation.status.as_ref() == "failed" { "retry" } else { "refresh project.md" }, false).on_click(cx.listener(move |this, _: &ClickEvent, _, _| {
                    if let Some(live) = &this.live { live.run(Command::RefreshProjectContext { project_id: id.clone(), expected_attempt: attempt }); }
                }))))
            })
            .child(context)
            .into_any_element()
    }
}

fn project_position<'a>(ids: impl Iterator<Item = &'a str>, id: &str) -> Option<usize> {
    ids.enumerate().find_map(|(index, value)| (value == id).then_some(index))
}

/// Map an ID-based action against the latest server order, never a captured index.
fn moved_order(mut ids: Vec<String>, id: &str, offset: isize) -> Option<Vec<String>> {
    let from = ids.iter().position(|value| value == id)?;
    let to = from.saturating_add_signed(offset).min(ids.len() - 1);
    let item = ids.remove(from);
    ids.insert(to, item);
    Some(ids)
}

#[cfg(test)]
mod tests {
    use super::{moved_order, project_position};

    // Purpose: select_project resolves identity at click time. This pure lookup
    // proves refreshed ordering cannot select a different project or a removed ID.
    #[test]
    fn selection_follows_id() {
        assert_eq!(project_position(["a", "b"].into_iter(), "b"), Some(1));
        assert_eq!(project_position(["b", "a"].into_iter(), "b"), Some(0));
        assert_eq!(project_position(["a"].into_iter(), "b"), None);
    }

    // Purpose: moved_order is the pure command mapping boundary. A stale row's ID
    // must still move that project after refresh, and a removed ID must do nothing.
    #[test]
    fn order_actions_follow_ids_after_refresh() {
        let ids = || vec!["c".into(), "a".into(), "b".into()];
        assert_eq!(moved_order(ids(), "a", -1).unwrap(), vec!["a", "c", "b"]);
        assert_eq!(moved_order(ids(), "a", 1).unwrap(), vec!["c", "b", "a"]);
        assert_eq!(moved_order(ids(), "b", isize::MIN).unwrap(), vec!["b", "c", "a"]);
        assert_eq!(moved_order(ids(), "missing", 1), None);
        assert_eq!(moved_order(Vec::new(), "a", 1), None);
    }
}
