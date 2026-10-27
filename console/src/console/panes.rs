//! Arranging panes by hand: drag a session from the sidebar, or a pane by its
//! header, onto a pane's edge to split there or onto its middle to show it
//! there; drag the divider between panes to resize them.

use gpui::{
    AnyElement, Context, DragMoveEvent, Hsla, IntoElement, Render, SharedString, Window, div, prelude::*, px,
    relative,
};

use super::{Console, Pane, Page, alpha};
use crate::layout::{Axis, Node, PaneId, Zone};

/// A session being dragged: from the sidebar, or a pane by its header.
#[derive(Clone)]
pub(super) struct Dragged {
    pub session: usize,
    pub pane: Option<usize>,
}

/// A divider being dragged: the split (child indexes from the root) and the
/// divider after child `index`.
#[derive(Clone)]
pub(super) struct Resizing {
    path: Vec<usize>,
    index: usize,
}

/// What follows the pointer while dragging.
#[derive(Clone)]
pub(super) struct Ghost {
    label: SharedString,
    font: SharedString,
    bg: Hsla,
    fg: Hsla,
    border: Hsla,
}

impl Render for Ghost {
    fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
        if self.label.is_empty() {
            return div();
        }
        div()
            .px_2()
            .py_1()
            .rounded(px(5.))
            .border_1()
            .border_color(self.border)
            .bg(self.bg)
            .text_color(self.fg)
            .font_family(self.font.clone())
            .text_size(px(12.))
            .child(self.label.clone())
    }
}

impl Console {
    pub(super) fn ghost(&self, label: impl Into<SharedString>) -> Ghost {
        let t = &self.theme;
        Ghost { label: label.into(), font: t.mono.into(), bg: t.raised, fg: t.text, border: alpha(t.accent, 0.6) }
    }

    /// A split tree: children in their shares, dividers between them.
    pub(super) fn render_node(&self, node: &Node, path: Vec<usize>, cx: &mut Context<Self>) -> AnyElement {
        let Node::Split { axis, children, sizes } = node else {
            let Node::Leaf(id) = node else { unreachable!() };
            return self.render_pane(*id, cx);
        };
        let axis = *axis;
        let own = path.clone();
        let mut row = div()
            .flex()
            .size_full()
            .map(|d| if axis == Axis::Row { d.flex_row() } else { d.flex_col() })
            .on_drag_move(cx.listener(move |this, e: &DragMoveEvent<Resizing>, _, cx| {
                let drag = e.drag(cx);
                if drag.path != own {
                    return;
                }
                let (b, p) = (e.bounds, e.event.position);
                let at = match axis {
                    Axis::Row => f32::from(p.x - b.origin.x) / f32::from(b.size.width),
                    Axis::Column => f32::from(p.y - b.origin.y) / f32::from(b.size.height),
                };
                let index = drag.index;
                this.layout.resize(&own, index, at);
                cx.notify();
            }));
        for (i, child) in children.iter().enumerate() {
            if i > 0 {
                row = row.child(self.divider(axis, path.clone(), i - 1));
            }
            let mut inner = path.clone();
            inner.push(i);
            let mut cell = div().flex().min_w_0().min_h_0().flex_basis(px(0.)).child(self.render_node(child, inner, cx));
            cell.style().flex_grow = Some(sizes[i]);
            row = row.child(cell);
        }
        row.into_any_element()
    }

    fn divider(&self, axis: Axis, path: Vec<usize>, index: usize) -> impl IntoElement {
        let hover = alpha(self.theme.accent, 0.45);
        let id = SharedString::from(format!("divider-{path:?}-{index}"));
        let ghost = self.ghost("");
        div()
            .id(id)
            .flex_none()
            .rounded(px(3.))
            .map(|d| if axis == Axis::Row { d.w(px(6.)).h_full().cursor_col_resize() } else { d.h(px(6.)).w_full().cursor_row_resize() })
            .hover(move |s| s.bg(hover))
            .on_drag(Resizing { path, index }, move |_, _, _, cx| cx.new(|_| ghost.clone()))
    }

    /// Drop targets over a pane while a session is dragged: its four edges
    /// split, its middle shows the session there.
    pub(super) fn drop_zones(&self, pid: usize, cx: &mut Context<Self>) -> impl IntoElement {
        let tint = alpha(self.theme.accent, 0.22);
        let edge = alpha(self.theme.accent, 0.8);
        let zone = |zone: Zone| {
            let base = div().absolute().id(SharedString::from(format!("drop-{pid}-{zone:?}")));
            let placed = match zone {
                Zone::Left => base.left_0().top_0().bottom_0().w(relative(0.25)),
                Zone::Right => base.right_0().top_0().bottom_0().w(relative(0.25)),
                Zone::Top => base.top_0().left(relative(0.25)).right(relative(0.25)).h(relative(0.3)),
                Zone::Bottom => base.bottom_0().left(relative(0.25)).right(relative(0.25)).h(relative(0.3)),
                Zone::Center => base.top(relative(0.3)).bottom(relative(0.3)).left(relative(0.25)).right(relative(0.25)),
            };
            placed
                .drag_over::<Dragged>(move |s, _, _, _| {
                    let s = s.bg(tint);
                    match zone {
                        Zone::Left => s.border_l_4().border_color(edge),
                        Zone::Right => s.border_r_4().border_color(edge),
                        Zone::Top => s.border_t_4().border_color(edge),
                        Zone::Bottom => s.border_b_4().border_color(edge),
                        Zone::Center => s.border_2().border_color(edge),
                    }
                })
                .on_drop(cx.listener(move |this, d: &Dragged, _, cx| this.drop_on(pid, zone, d.clone(), cx)))
        };
        div()
            .absolute()
            .inset_0()
            .child(zone(Zone::Left))
            .child(zone(Zone::Right))
            .child(zone(Zone::Top))
            .child(zone(Zone::Bottom))
            .child(zone(Zone::Center))
    }

    fn drop_on(&mut self, pid: usize, zone: Zone, d: Dragged, cx: &mut Context<Self>) {
        let target = PaneId(pid);
        match (d.pane, zone.split()) {
            // A pane dropped in another pane's middle: they swap sessions.
            (Some(from), None) => {
                let a = self.panes.iter().position(|p| p.id.0 == from);
                let b = self.panes.iter().position(|p| p.id == target);
                if let (Some(a), Some(b)) = (a, b) {
                    let (sa, sb) = (self.panes[a].session, self.panes[b].session);
                    self.panes[a].session = sb;
                    self.panes[b].session = sa;
                }
                self.active = target;
            }
            (Some(from), Some(_)) => {
                if self.layout.move_pane(PaneId(from), target, zone) {
                    self.active = PaneId(from);
                }
            }
            (None, None) => {
                if let Some(p) = self.panes.iter_mut().find(|p| p.id == target) {
                    p.session = d.session;
                }
                self.active = target;
            }
            (None, Some((axis, before))) => {
                let id = PaneId(self.next_pane);
                self.next_pane += 1;
                if self.layout.split_at(target, axis, id, before) {
                    self.panes.push(Pane { id, session: d.session, tray: false, tab: super::background::BarTab::Tasks, scroll: gpui::ScrollHandle::new() });
                    self.active = id;
                }
            }
        }
        self.page = Page::Sessions;
        cx.notify();
    }
}
