//! A project's folders on the Projects page: tick folders to remove several
//! at once, or open the picker under them to add several at once. The picker
//! starts at the home folder and lists repositories with an AGENTS.md or
//! CLAUDE.md first; ticks hold while moving between directories. Nothing
//! here goes through the onboarding (swarm::folders).

use std::collections::{BTreeSet, HashMap};
use std::path::PathBuf;

use gpui::{AnyElement, ClickEvent, Context, Div, SharedString, div, prelude::*, px};

use super::{Console, alpha};
use crate::swarm::folders::{self, Listing};
use crate::swarm::target::Target;

/// The picker open under one project's folders.
#[derive(Default)]
pub(super) struct Pick {
    project_id: String,
    listing: Option<Result<Listing, String>>,
    loading: bool,
    selected: BTreeSet<String>,
    adding: bool,
    /// What the last add came to, or Swarm's refusal.
    result: Option<Result<Outcome, String>>,
}

#[derive(Clone, Default)]
struct Outcome {
    added: Vec<String>,
    invisible: Vec<String>,
    need_first_commit: Vec<String>,
    notes: Vec<String>,
}

#[derive(Default)]
pub(super) struct FolderState {
    pub(super) pick: Option<Pick>,
    /// Folders ticked for removal, per project.
    marks: HashMap<String, BTreeSet<String>>,
    /// The project whose ticked folders wait for "confirm".
    confirm: Option<String>,
}

impl Console {
    /// This window's Swarm runs on the computer this app runs on: its
    /// directories are read from disk; otherwise through Swarm's browser.
    fn folders_local(&self) -> bool {
        self.via.is_none() && matches!(Target::from_env().unwrap_or(Target::Local), Target::Local)
    }

    fn project_folders(&self, id: &str) -> Vec<(String, String)> {
        self.projects
            .iter()
            .find(|p| p.id.as_ref() == id)
            .map(|p| p.workspaces.iter().zip(p.roles.iter().chain(std::iter::repeat(&SharedString::from("auxiliary")))).map(|(w, r)| (w.to_string(), r.to_string())).collect())
            .unwrap_or_default()
    }

    pub(super) fn open_folder_pick(&mut self, id: &str, cx: &mut Context<Self>) {
        self.folders.pick = Some(Pick { project_id: id.to_string(), ..Default::default() });
        let home = if self.folders_local() { folders::local_home().display().to_string() } else { String::new() };
        self.folder_go(home, cx);
    }

    fn folder_go(&mut self, path: String, cx: &mut Context<Self>) {
        let Some(pick) = &mut self.folders.pick else { return };
        pick.loading = true;
        let local = self.folders_local();
        let client = self.live.as_ref().map(|l| l.client());
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move {
                    if local {
                        folders::read_local(&PathBuf::from(&path))
                    } else {
                        match client {
                            Some(c) => folders::browse(&c, &path),
                            None => Err("Not connected to Swarm.".into()),
                        }
                    }
                })
                .await;
            this.update(cx, |this, cx| {
                if let Some(pick) = &mut this.folders.pick {
                    pick.loading = false;
                    pick.listing = Some(result);
                }
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    fn add_picked(&mut self, cx: &mut Context<Self>) {
        let Some(pick) = &mut self.folders.pick else { return };
        if pick.adding || pick.selected.is_empty() {
            return;
        }
        let Some(client) = self.live.as_ref().map(|l| l.client()) else { return };
        let id = pick.project_id.clone();
        let paths: Vec<String> = pick.selected.iter().cloned().collect();
        pick.adding = true;
        pick.result = None;
        let current = self.project_folders(&id);
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { folders::add(&client, &id, &current, &paths) }).await;
            this.update(cx, |this, cx| {
                if let Some(pick) = &mut this.folders.pick {
                    pick.adding = false;
                    match result {
                        Ok(a) => {
                            for p in &a.added {
                                pick.selected.remove(p);
                            }
                            pick.result = Some(Ok(Outcome { added: a.added, invisible: a.invisible, need_first_commit: a.need_first_commit, notes: a.notes }));
                        }
                        Err(e) => pick.result = Some(Err(e)),
                    }
                }
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    fn remove_marked(&mut self, id: &str, cx: &mut Context<Self>) {
        let removing: Vec<String> = self.folders.marks.get(id).map(|m| m.iter().cloned().collect()).unwrap_or_default();
        if removing.is_empty() {
            return;
        }
        let current = self.project_folders(id);
        if let Some(live) = &self.live {
            live.run(crate::swarm::live::Command::PatchProject { project_id: id.to_string(), body: serde_json::json!({"workspaces": folders::without(&current, &removing)}) });
        }
        self.folders.marks.remove(id);
        self.folders.confirm = None;
        cx.notify();
    }

    /// The FOLDERS part of a project card: its folders with ticks, removal of
    /// the ticked ones, and the picker when it is open for this project.
    pub(super) fn render_project_folders(&self, id: &str, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let current = self.project_folders(id);
        let marked = self.folders.marks.get(id).cloned().unwrap_or_default();
        let picking = self.folders.pick.as_ref().is_some_and(|p| p.project_id == id);
        let mut head = div().flex().items_center().gap_2().px_3().pt_2().child(div().flex_1().text_size(px(10.5)).text_color(t.faint).child("FOLDERS"));
        if !marked.is_empty() {
            let n = marked.len();
            let pid = id.to_string();
            if self.folders.confirm.as_deref() == Some(id) {
                let (a, b) = (pid.clone(), pid.clone());
                head = head
                    .child(div().text_size(px(11.)).text_color(t.text).child(format!("remove {n} folder{} from this project? They stay on disk.", if n == 1 { "" } else { "s" })))
                    .child(self.button(SharedString::from(format!("folders-remove-yes-{pid}")), "remove", true).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.remove_marked(&a, cx))))
                    .child(self.button(SharedString::from(format!("folders-remove-no-{pid}")), "cancel", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        this.folders.confirm = None;
                        this.folders.marks.remove(&b);
                        cx.notify();
                    })));
            } else {
                head = head.child(self.button(SharedString::from(format!("folders-remove-{pid}")), format!("remove {n} folder{}", if n == 1 { "" } else { "s" }), false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                    this.folders.confirm = Some(pid.clone());
                    cx.notify();
                })));
            }
        }
        if !picking {
            let pid = id.to_string();
            head = head.child(self.button(SharedString::from(format!("folders-add-{pid}")), "add folders", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.open_folder_pick(&pid, cx))));
        }
        let rows = current.iter().enumerate().map(|(i, (path, role))| {
            let (pid, p) = (id.to_string(), path.clone());
            div()
                .flex()
                .items_center()
                .gap_2()
                .px_3()
                .py(px(3.))
                .child(self.tick(SharedString::from(format!("folder-mark-{id}-{i}")), path.clone(), marked.contains(path)).flex_1().min_w_0().on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                    let m = this.folders.marks.entry(pid.clone()).or_default();
                    if !m.remove(&p) {
                        m.insert(p.clone());
                    }
                    this.folders.confirm = None;
                    cx.notify();
                })))
                .child(div().text_size(px(10.5)).text_color(t.faint).child(role.replace('_', " ")))
        });
        div().flex().flex_col().child(head).child(div().flex().flex_col().pb_1().children(rows)).when(picking, |d| d.child(self.render_folder_pick(&current, cx)))
    }

    fn render_folder_pick(&self, current: &[(String, String)], cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let hover = t.hover;
        let Some(pick) = &self.folders.pick else { return div().into_any_element() };
        let listing = pick.listing.as_ref().and_then(|l| l.as_ref().ok());
        let here = listing.map(|l| l.path.clone()).unwrap_or_default();
        let parent = listing.and_then(|l| l.parent.clone());
        let local = self.folders_local();

        let mut nav = div().flex().items_center().gap_1().child(div().flex_1().min_w_0().truncate().text_size(px(11.)).text_color(t.text).child(if here.is_empty() { "…".to_string() } else { here.clone() }));
        if let Some(up) = parent {
            nav = nav.child(self.button("folders-up", "up", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.folder_go(up.clone(), cx))));
        }
        nav = nav.child(self.button("folders-home", "home", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
            let home = if local { folders::local_home().display().to_string() } else { String::new() };
            this.folder_go(home, cx);
        })));

        let body: Div = match &pick.listing {
            None => div().text_size(px(11.)).text_color(t.faint).child("Reading…"),
            Some(Err(e)) => div().text_size(px(11.)).text_color(t.err).child(e.clone()),
            Some(Ok(l)) if l.entries.is_empty() => div().text_size(px(11.)).text_color(t.faint).child(if local { "No folders here." } else { "No folders here that Swarm can see." }),
            Some(Ok(l)) => div().flex().flex_col().children(l.entries.iter().enumerate().map(|(i, e)| {
                let inside = current.iter().any(|(p, _)| *p == e.path);
                let on = pick.selected.contains(&e.path);
                let (sel, open) = (e.path.clone(), e.path.clone());
                let badge = |text: &'static str, c| div().px_1().rounded(px(t.radius)).bg(alpha(c, 0.14)).text_size(px(10.)).text_color(c).child(text);
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .py(px(2.))
                    .child(if inside {
                        div().w(px(14.)).text_size(px(11.)).text_color(t.faint).child("✓").into_any_element()
                    } else {
                        div()
                            .id(SharedString::from(format!("folder-pick-{i}")))
                            .w(px(14.))
                            .cursor_pointer()
                            .text_size(px(11.))
                            .text_color(if on { t.accent } else { t.faint })
                            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                if let Some(p) = &mut this.folders.pick {
                                    if !p.selected.remove(&sel) {
                                        p.selected.insert(sel.clone());
                                    }
                                }
                                cx.notify();
                            }))
                            .child(if on { "☑" } else { "☐" })
                            .into_any_element()
                    })
                    .child(
                        div()
                            .id(SharedString::from(format!("folder-open-{i}")))
                            .flex_1()
                            .min_w_0()
                            .truncate()
                            .px_1()
                            .rounded(px(t.radius))
                            .cursor_pointer()
                            .hover(move |s| s.bg(hover))
                            .text_size(px(11.5))
                            .text_color(if inside { t.faint } else { t.text })
                            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.folder_go(open.clone(), cx)))
                            .child(format!("{}/", e.name)),
                    )
                    .when_some(e.guide, |d, g| d.child(badge(g, t.accent)))
                    .when(e.git && e.guide.is_none(), |d| d.child(badge("git", t.muted)))
                    .when(inside, |d| d.child(div().text_size(px(10.)).text_color(t.faint).child("in project")))
            })),
        };

        let n = pick.selected.len();
        let mut foot = div().flex().items_center().gap_1().pt_1().child(div().flex_1().text_size(px(11.)).text_color(t.muted).child(if n == 0 { "Tick folders to add; open one to look inside.".to_string() } else { format!("{n} selected") }));
        foot = if pick.adding {
            foot.child(div().text_size(px(11.)).text_color(t.info).child("adding…"))
        } else if n > 0 {
            foot.child(self.button("folders-add-go", format!("add {n} folder{}", if n == 1 { "" } else { "s" }), true).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.add_picked(cx))))
        } else {
            foot.child(self.off_button("add"))
        };
        foot = foot.child(self.button("folders-pick-close", "close", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
            this.folders.pick = None;
            cx.notify();
        })));

        let mut result: Vec<Div> = Vec::new();
        match &pick.result {
            Some(Err(e)) => result.push(div().text_size(px(11.)).text_color(t.err).child(format!("Swarm did not add them: {e}"))),
            Some(Ok(o)) => {
                if !o.added.is_empty() {
                    result.push(div().text_size(px(11.)).text_color(t.ok).child(format!("Added {}. Swarm updates project.md from them.", o.added.join(", "))));
                }
                for note in &o.notes {
                    result.push(div().text_size(px(11.)).text_color(t.muted).child(note.clone()));
                }
                if !o.invisible.is_empty() {
                    result.push(div().text_size(px(11.)).text_color(t.warn).child("Swarm cannot read these yet. Run this in a terminal, then add them again:"));
                    result.push(div().px_2().py_1().rounded(px(t.radius)).bg(t.panel).text_size(px(11.)).text_color(t.text).child(folders::visible_command(&o.invisible)));
                }
                if !o.need_first_commit.is_empty() {
                    result.push(div().text_size(px(11.)).text_color(t.warn).child(format!(
                        "Not added: {} {} files and no first commit yet; start a new project with it to pick what the first commit holds.",
                        o.need_first_commit.join(", "),
                        if o.need_first_commit.len() == 1 { "has" } else { "have" }
                    )));
                }
            }
            None => {}
        }

        div()
            .mx_3()
            .mb_2()
            .p_2()
            .flex()
            .flex_col()
            .gap_1()
            .rounded(px(t.radius))
            .border_1()
            .border_color(t.border)
            .bg(t.panel)
            .child(nav)
            .child(div().id("folder-pick-list").max_h(px(300.)).overflow_y_scroll().child(body))
            .children(result)
            .child(foot)
            .into_any_element()
    }
}
