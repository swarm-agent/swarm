//! A session pane in a window of its own: "Open in new window" in a pane's
//! header.
//!
//! The window does not copy the session. Its view holds the same `Console`
//! entity as the main window and draws that pane with `render_pane`, so both
//! windows read one set of records over one live connection, and a click or
//! a message in either acts through the same code. GPUI redraws a window
//! when an entity it read while drawing changes, and the observer below
//! makes that explicit.
//!
//! The pane leaves the main window's layout by the same path as closing it
//! (popping out the only pane leaves the empty state) but stays in
//! `Console::panes`, so its message box and live following stay. When the
//! window closes, the pane goes too. Closing it does not end the app or touch
//! the main window.

use gpui::{
    App, AppContext, Bounds, Context, Entity, Focusable, IntoElement, Render, SharedString, Subscription, TitlebarOptions, Window, WindowBounds, WindowOptions, div, prelude::*,
    px, size,
};

use super::{ClosePane, Console, CopySelection};
use crate::layout::{Axis, PaneId};

/// The OS window title for a session.
fn window_title(session: &str) -> SharedString {
    format!("Swarm Console — {session}").into()
}

/// The root view of a popped-out pane's window.
pub(super) struct PopOut {
    console: Entity<Console>,
    pane: PaneId,
    /// The title the window was last given.
    title: SharedString,
    _redraw: Subscription,
}

impl PopOut {
    fn new(console: Entity<Console>, pane: PaneId, title: SharedString, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let redraw = cx.observe(&console, |_, _, cx| cx.notify());
        // The window closing (its title bar, or the pane's X) releases this
        // view; the pane goes with it.
        cx.on_release(move |this: &mut PopOut, cx: &mut App| {
            let console = this.console.clone();
            console.update(cx, |c, cx| c.forget_popped(pane, cx));
        })
        .detach();
        if let Some(input) = console.read(cx).composers.get(&pane.0).cloned() {
            window.focus(&input.focus_handle(cx));
        }
        PopOut { console, pane, title, _redraw: redraw }
    }
}

impl Render for PopOut {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let pane = self.pane;
        let (title, bg, fg, font, body) = self.console.update(cx, |c, cx| {
            let title = c.panes.iter().find(|p| p.id == pane).and_then(|p| c.sessions.get(p.session)).map(|s| window_title(&s.title));
            (title, c.theme.bg, c.theme.text, c.theme.mono, c.render_pane(pane, cx))
        });
        // The session's title can change (a new session gets its name).
        if let Some(title) = title.filter(|t| *t != self.title) {
            window.set_window_title(&title);
            self.title = title;
        }
        div()
            .key_context("Console")
            .size_full()
            .flex()
            .p(px(6.))
            .bg(bg)
            .text_color(fg)
            .font_family(font)
            .text_size(px(12.5))
            .on_action(cx.listener(|this, _: &CopySelection, _, cx| {
                let console = this.console.clone();
                console.update(cx, |c, cx| c.copy_selection(cx));
            }))
            // ctrl-w closes this window, as the pane's X does.
            .on_action(|_: &ClosePane, window: &mut Window, _: &mut App| window.remove_window())
            .child(body)
    }
}

/// Opens the window for pane `id` (already out of the main layout).
fn open_window(console: Entity<Console>, id: PaneId, title: SharedString, cx: &mut App) {
    let bounds = Bounds::centered(None, size(px(820.), px(860.)), cx);
    let options = WindowOptions {
        window_bounds: Some(WindowBounds::Windowed(bounds)),
        titlebar: Some(TitlebarOptions { title: Some(title.clone()), ..Default::default() }),
        app_id: Some("swarm-console".into()),
        ..Default::default()
    };
    let root = console.clone();
    let opened = cx.open_window(options, move |window, cx| cx.new(|cx| PopOut::new(root, id, title, window, cx)));
    if let Err(e) = opened {
        // No window: the pane comes back to the main window, and the status
        // bar says why.
        console.update(cx, |c, cx| {
            c.layout.place(id, None, Axis::Row, false);
            c.notice = Some((format!("Could not open a new window: {e}").into(), true));
            cx.notify();
        });
    }
}

impl Console {
    /// Moves pane `id` of the main window into a window of its own.
    pub(super) fn pop_out(&mut self, id: PaneId, cx: &mut Context<Self>) {
        let Some(si) = self.panes.iter().find(|p| p.id == id).map(|p| p.session) else { return };
        // Out of the layout as a close takes it; the pane record stays.
        if !self.layout.close(id) {
            return;
        }
        self.pending_focus = true;
        let title = self.sessions.get(si).map(|s| window_title(&s.title)).unwrap_or_else(|| window_title(""));
        let console = cx.entity();
        // The new window reads the console as it opens, so it opens once
        // this update is over.
        cx.defer(move |cx| open_window(console, id, title, cx));
        cx.notify();
    }

    /// A popped-out pane's window closed: the pane is gone. A pane of the
    /// main window is never removed here.
    fn forget_popped(&mut self, id: PaneId, cx: &mut Context<Self>) {
        if self.layout.contains(id) {
            return;
        }
        self.panes.retain(|p| p.id != id);
        cx.notify();
    }
}
