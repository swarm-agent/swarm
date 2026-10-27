//! The permissions chip in the composer's footer. What agents may use (this
//! device's files and browser, starting tasks, linked machines) is the
//! account's rules in Swarm, the same for every conversation; the chip opens
//! the Permissions page where they are read and changed.

use gpui::{AnyElement, ClickEvent, Context, SharedString, div, prelude::*, px};

use super::{Console, Page};
use crate::icons::Icon;

impl Console {
    /// The chip: a shield and "permissions"; a click opens the page.
    pub(super) fn permissions_chip(&self, pid: usize, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let hover = t.hover;
        div()
            .id(SharedString::from(format!("permissions-chip-{pid}")))
            .flex()
            .flex_none()
            .items_center()
            .gap_1()
            .px_1()
            .rounded(px(t.radius))
            .cursor_pointer()
            .hover(move |s| s.bg(hover))
            .text_color(t.muted)
            .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.go(Page::Permissions, cx)))
            .child(self.icon(Icon::Shield, 11., t.muted))
            .child("permissions")
            .into_any_element()
    }
}
