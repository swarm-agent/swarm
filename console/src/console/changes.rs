//! What a finished task changed, shown with its merge buttons: the files
//! with their added and removed line counts, and a file's diff when it is
//! opened. Read from Swarm when the person opens it (never in a loop) and
//! kept per task head; a moved head is read again on the next open. Swarm
//! bounds the answer; "truncated" is shown where it says it cut.

use std::collections::HashMap;
use std::rc::Rc;

use gpui::{
    AnyElement, ClickEvent, Context, Div, Hsla, ListHorizontalSizingBehavior, SharedString, UniformListScrollHandle, div, prelude::*, px,
    uniform_list,
};

use super::{Console, alpha};
use crate::data::{Merge, Task, TaskStatus};
use crate::icons::Icon;
use crate::swarm::diff::{self, DiffFile, LineKind, TaskDiff};

/// One diff line's height.
const LINE: f32 = 16.;
/// An open file's diff shows at most this high, then scrolls.
const DIFF_HEIGHT: f32 = 320.;

/// A task's changes as read from Swarm, by task id.
#[derive(Default)]
pub(super) struct Changes {
    /// The task head the read was asked at.
    head: String,
    open: bool,
    loading: bool,
    error: Option<String>,
    read: Option<Read>,
}

struct Read {
    truncated: bool,
    files_truncated: bool,
    files: Vec<FileView>,
}

struct FileView {
    file: DiffFile,
    lines: Rc<Vec<(LineKind, SharedString)>>,
    widest: usize,
    open: bool,
    /// The file was read on its own (?path=), in full within Swarm's bound.
    whole: bool,
    loading: bool,
    error: Option<String>,
    scroll: UniformListScrollHandle,
}

impl FileView {
    fn new(mut file: DiffFile) -> FileView {
        let lines: Vec<(LineKind, SharedString)> = diff::lines(&file.diff).into_iter().map(|(k, l)| (k, l.into())).collect();
        file.diff = String::new();
        let widest = lines.iter().enumerate().max_by_key(|(_, (_, l))| l.len()).map(|(i, _)| i).unwrap_or(0);
        FileView { file, lines: Rc::new(lines), widest, open: false, whole: false, loading: false, error: None, scroll: UniformListScrollHandle::new() }
    }
}

fn read_of(d: TaskDiff) -> Read {
    Read { truncated: d.truncated, files_truncated: d.files_truncated, files: d.files.into_iter().map(FileView::new).collect() }
}

pub(super) type ChangesByTask = HashMap<String, Changes>;

impl Console {
    /// Finished work with commits to merge has changes to look at.
    fn has_changes(&self, si: usize, task: &Task) -> bool {
        self.sessions[si].live.is_some() && !task.key.is_empty() && task.status == TaskStatus::NeedsReview && matches!(task.merge, Merge::Integrate | Merge::Recover)
    }

    fn task_project(&self, si: usize) -> String {
        self.projects.get(self.sessions[si].project).map(|p| p.id.to_string()).unwrap_or_default()
    }

    /// Opens or closes a task's changes; opening reads them unless they were
    /// read at the task's current head.
    fn toggle_changes(&mut self, si: usize, ti: usize, cx: &mut Context<Self>) {
        let Some(task) = self.sessions[si].tasks.get(ti) else { return };
        let (key, head) = (task.key.to_string(), task.head.to_string());
        let project = self.task_project(si);
        let entry = self.changes.entry(key.clone()).or_default();
        entry.open = !entry.open;
        let stale = entry.read.is_none() || entry.head != head;
        if entry.open && stale && !entry.loading && !project.is_empty() {
            self.read_changes(key, project, head, cx);
        }
        cx.notify();
    }

    fn read_changes(&mut self, key: String, project: String, head: String, cx: &mut Context<Self>) {
        let Some(client) = self.live.as_ref().map(|l| l.client()) else { return };
        let entry = self.changes.entry(key.clone()).or_default();
        entry.loading = true;
        entry.error = None;
        entry.head = head.clone();
        let task = key.clone();
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { diff::read(&client, &project, &task, None) }).await;
            this.update(cx, |this, cx| {
                let Some(entry) = this.changes.get_mut(&key) else { return };
                if entry.head != head {
                    return; // a newer read was asked for
                }
                entry.loading = false;
                match result {
                    Ok(d) => entry.read = Some(read_of(d)),
                    Err(e) => entry.error = Some(e),
                }
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    /// Opens or closes one file's diff; a file Swarm cut in the list is read
    /// on its own the first time it opens.
    fn toggle_change_file(&mut self, si: usize, key: String, fi: usize, cx: &mut Context<Self>) {
        let project = self.task_project(si);
        let client = self.live.as_ref().map(|l| l.client());
        let Some(entry) = self.changes.get_mut(&key) else { return };
        let head = entry.head.clone();
        let Some(file) = entry.read.as_mut().and_then(|r| r.files.get_mut(fi)) else { return };
        file.open = !file.open;
        if file.open && file.file.truncated && !file.file.binary && !file.whole && !file.loading {
            if let Some(client) = client.filter(|_| !project.is_empty()) {
                file.loading = true;
                file.error = None;
                let path = file.file.path.clone();
                let task = key.clone();
                cx.spawn(async move |this, cx| {
                    let result = cx.background_executor().spawn(async move { diff::read(&client, &project, &task, Some(&path)) }).await;
                    this.update(cx, |this, cx| {
                        let Some(entry) = this.changes.get_mut(&key).filter(|e| e.head == head) else { return };
                        let Some(file) = entry.read.as_mut().and_then(|r| r.files.get_mut(fi)) else { return };
                        file.loading = false;
                        match result.map(|d| d.files.into_iter().next()) {
                            Ok(Some(whole)) => {
                                let open = file.open;
                                *file = FileView::new(whole);
                                file.open = open;
                                file.whole = true;
                            }
                            Ok(None) => file.error = Some("Swarm sent no text for this file.".into()),
                            Err(e) => file.error = Some(e),
                        }
                        cx.notify();
                    })
                    .ok();
                })
                .detach();
            }
        }
        cx.notify();
    }

    /// Any of this session's tasks shows its changes (the bar then grows).
    pub(super) fn changes_open(&self, si: usize) -> bool {
        self.sessions[si].tasks.iter().any(|t| self.changes.get(t.key.as_ref()).is_some_and(|c| c.open))
    }

    /// "changes ▸ 3 files +12 −4" beside a finished task's merge buttons.
    pub(super) fn changes_toggle(&self, si: usize, ti: usize, task: &Task, key: &str, cx: &mut Context<Self>) -> Option<AnyElement> {
        if !self.has_changes(si, task) {
            return None;
        }
        let t = &self.theme;
        let entry = self.changes.get(task.key.as_ref());
        let open = entry.is_some_and(|c| c.open);
        let summary = entry.filter(|c| c.head == task.head.as_ref()).and_then(|c| c.read.as_ref()).map(|r| {
            let (add, del) = r.files.iter().fold((0, 0), |(a, d), f| (a + f.file.additions, d + f.file.deletions));
            let n = r.files.len();
            (format!("{n} file{}{}", if n == 1 { "" } else { "s" }, if r.files_truncated { "+" } else { "" }), add, del)
        });
        let hover = t.hover;
        Some(
            div()
                .id(SharedString::from(format!("{key}-changes")))
                .flex()
                .flex_none()
                .items_center()
                .gap_1()
                .px_1()
                .rounded(px(t.radius))
                .cursor_pointer()
                .hover(move |s| s.bg(hover))
                .text_size(px(10.5))
                .text_color(t.muted)
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.toggle_changes(si, ti, cx)))
                .child(self.icon(if open { Icon::ChevronDown } else { Icon::ChevronRight }, 10., t.faint))
                .child("changes")
                .children(summary.map(|(files, add, del)| {
                    div()
                        .flex()
                        .gap_1()
                        .child(div().text_color(t.faint).child(files))
                        .child(div().text_color(t.ok).child(format!("+{add}")))
                        .child(div().text_color(t.err).child(format!("−{del}")))
                }))
                .into_any_element(),
        )
    }

    /// The open changes of a task: its files, and the diff of each open one.
    pub(super) fn render_changes(&self, si: usize, task: &Task, key: &str, cx: &mut Context<Self>) -> Option<Div> {
        if !self.has_changes(si, task) {
            return None;
        }
        let entry = self.changes.get(task.key.as_ref()).filter(|c| c.open)?;
        let t = &self.theme;
        let frame = div().ml(px(18.)).mt_1().pl_2().border_l_1().border_color(t.border_strong).flex().flex_col().gap(px(2.)).text_size(px(11.));
        let note = |text: String, color: Hsla| div().text_size(px(10.5)).text_color(color).child(text);
        let task_key = task.key.to_string();
        if entry.loading {
            return Some(frame.child(note("reading the changes…".into(), t.faint)));
        }
        if let Some(e) = &entry.error {
            return Some(frame.child(note(format!("Swarm could not show the changes: {e}"), t.err)));
        }
        let read = entry.read.as_ref()?;
        let mut frame = frame;
        if entry.head != task.head.as_ref() {
            frame = frame.child(note("the task moved on since this was read; close and open changes to read it again".into(), t.warn));
        }
        if read.files.is_empty() {
            return Some(frame.child(note("no files changed".into(), t.faint)));
        }
        let hover = t.hover;
        for (fi, f) in read.files.iter().enumerate() {
            let k = task_key.clone();
            let color = match f.file.letter() {
                'A' => t.ok,
                'D' => t.err,
                'R' => t.info,
                _ => t.warn,
            };
            let name = if f.file.old_path.is_empty() { f.file.path.clone() } else { format!("{} → {}", f.file.old_path, f.file.path) };
            let row = div()
                .id(SharedString::from(format!("{key}-file-{fi}")))
                .flex()
                .items_center()
                .gap_2()
                .px_1()
                .rounded(px(t.radius))
                .cursor_pointer()
                .hover(move |s| s.bg(hover))
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.toggle_change_file(si, k.clone(), fi, cx)))
                .child(self.icon(if f.open { Icon::ChevronDown } else { Icon::ChevronRight }, 10., t.faint))
                .child(div().w(px(10.)).flex_none().text_color(color).child(f.file.letter().to_string()))
                .child(div().flex_1().min_w_0().truncate().text_color(t.text).child(name))
                .when(f.file.binary, |d| d.child(div().flex_none().text_color(t.faint).child("binary")))
                .when(f.file.truncated && !f.whole, |d| d.child(div().flex_none().text_color(t.warn).child("truncated")))
                .child(div().w(px(48.)).flex_none().text_right().text_color(t.ok).child(format!("+{}", f.file.additions)))
                .child(div().w(px(48.)).flex_none().text_right().text_color(t.err).child(format!("−{}", f.file.deletions)));
            frame = frame.child(row);
            if f.open {
                frame = frame.child(self.render_file_diff(key, fi, f));
            }
        }
        if read.files_truncated {
            frame = frame.child(note("more files changed than Swarm lists here".into(), t.warn));
        } else if read.truncated && read.files.iter().any(|f| f.file.truncated && !f.whole) {
            frame = frame.child(note("Swarm cut some diffs (truncated); open a file to read it on its own".into(), t.faint));
        }
        Some(frame)
    }

    fn render_file_diff(&self, key: &str, fi: usize, f: &FileView) -> AnyElement {
        let t = &self.theme;
        let note = |text: &str, color: Hsla| div().ml(px(18.)).py(px(2.)).text_size(px(10.5)).text_color(color).child(text.to_string()).into_any_element();
        if f.file.binary {
            return note("binary file: no text to show", t.faint);
        }
        if f.loading {
            return note("reading the whole file…", t.faint);
        }
        if let Some(e) = &f.error {
            return note(&format!("Swarm could not show this file: {e}"), t.err);
        }
        if f.lines.is_empty() {
            return note(if f.file.truncated { "truncated: Swarm sent no text for this file" } else { "no text changes (a rename or a mode change)" }, t.faint);
        }
        let lines = f.lines.clone();
        let (added, removed, hunk, context, note_color) = (t.ok, t.err, t.faint, t.muted, t.faint);
        let (add_bg, del_bg) = (alpha(t.ok, 0.08), alpha(t.err, 0.08));
        let height = (lines.len() as f32 * LINE).min(DIFF_HEIGHT) + 2.;
        let list = uniform_list(SharedString::from(format!("{key}-diff-{fi}")), lines.len(), move |range, _, _| {
            range
                .filter_map(|i| lines.get(i).cloned())
                .map(|(kind, text)| {
                    let (color, bg) = match kind {
                        LineKind::Added => (added, Some(add_bg)),
                        LineKind::Removed => (removed, Some(del_bg)),
                        LineKind::Hunk => (hunk, None),
                        LineKind::Note => (note_color, None),
                        LineKind::Context => (context, None),
                    };
                    div().h(px(LINE)).px_1().whitespace_nowrap().text_color(color).when_some(bg, |d, bg| d.bg(bg)).child(text)
                })
                .collect::<Vec<_>>()
        })
        .with_horizontal_sizing_behavior(ListHorizontalSizingBehavior::Unconstrained)
        .with_width_from_item(Some(f.widest))
        .track_scroll(f.scroll.clone())
        .h(px(height))
        .w_full();
        div()
            .ml(px(18.))
            .flex()
            .flex_col()
            .child(div().rounded(px(t.radius)).border_1().border_color(t.border).bg(t.surface).text_size(px(11.)).overflow_hidden().child(list))
            .when(f.file.truncated, |d| d.child(div().py(px(2.)).text_size(px(10.5)).text_color(t.warn).child("truncated: Swarm cut this file's diff")))
            .into_any_element()
    }
}
