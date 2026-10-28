//! The one password prompt every root step on this computer uses: a small
//! modal over whatever view started the step, naming exactly what will run,
//! with a masked field, Run and Cancel. A wrong password says so and asks
//! again; Cancel changes nothing.
//!
//! The view that runs a root step takes an `Asker` (`asker`) and hands it to
//! `Target::run_root` on its background thread. When sudo needs a password,
//! the asker puts a request here and waits; this view shows it, and Run sends
//! the typed password straight back to that thread, which writes it to sudo's
//! stdin and wipes it (`swarm::root`). Nothing here keeps it: the field is
//! emptied as Run is pressed. While a step is under way this view looks for
//! a request ten times a second, and stops once no step holds an asker.

use std::sync::{Arc, Mutex, mpsc};
use std::time::Duration;

use gpui::{ClickEvent, Context, Entity, Focusable, Hsla, SharedString, Window, actions, div, prelude::*, px};

use crate::onboarding::input_colors;
use crate::swarm::root::{AskPassword, Password};
use crate::text_input::TextInput;
use crate::theme::Theme;

actions!(password_prompt, [Submit, Cancel]);

/// One root step waiting for the person.
struct Request {
    summary: String,
    command: String,
    retry: Option<String>,
    reply: mpsc::Sender<Option<Password>>,
}

#[derive(Default)]
struct Shared {
    slot: Mutex<Option<Request>>,
}

/// What a root step's background thread asks through. Hold it only for the
/// step: the prompt stops looking for requests once every asker is gone.
#[derive(Clone)]
pub struct Asker(Arc<Shared>);

impl AskPassword for Asker {
    fn ask(&self, summary: &str, command: &str, retry: Option<&str>) -> Option<Password> {
        let (reply, answer) = mpsc::channel();
        let request = Request { summary: summary.into(), command: command.into(), retry: retry.map(String::from), reply };
        *self.0.slot.lock().ok()? = Some(request);
        // The prompt dropped (window closed) without an answer: cancelled.
        answer.recv().ok().flatten()
    }
}

pub struct PasswordPrompt {
    theme: Theme,
    shared: Arc<Shared>,
    watching: bool,
    open: Option<Request>,
    field: Entity<TextInput>,
    focus_field: bool,
}

fn alpha(c: Hsla, a: f32) -> Hsla {
    Hsla { a, ..c }
}

impl PasswordPrompt {
    pub fn new(theme: Theme, cx: &mut Context<Self>) -> Self {
        let colors = input_colors(&theme);
        let field = cx.new(|cx| TextInput::new("Your computer password", colors, cx).secret().one_line());
        cx.observe(&field, |_, _, cx| cx.notify()).detach();
        PasswordPrompt { theme, shared: Arc::new(Shared::default()), watching: false, open: None, field, focus_field: false }
    }

    pub fn set_theme(&mut self, theme: Theme, cx: &mut Context<Self>) {
        let colors = input_colors(&theme);
        self.field.update(cx, |f, _| f.set_colors(colors));
        self.theme = theme;
        cx.notify();
    }

    /// An asker for one root step; starts looking for its requests.
    pub fn asker(&mut self, cx: &mut Context<Self>) -> Asker {
        let asker = Asker(self.shared.clone());
        if !self.watching {
            self.watching = true;
            cx.spawn(async move |this, cx| {
                loop {
                    cx.background_executor().timer(Duration::from_millis(100)).await;
                    if !this.update(cx, |this, cx| this.check(cx)).unwrap_or(false) {
                        break;
                    }
                }
            })
            .detach();
        }
        asker
    }

    /// Shows a waiting request; false once nothing can ask any more.
    fn check(&mut self, cx: &mut Context<Self>) -> bool {
        if self.open.is_none() {
            let request = self.shared.slot.lock().ok().and_then(|mut slot| slot.take());
            if let Some(request) = request {
                self.open = Some(request);
                self.focus_field = true;
                cx.notify();
            }
        }
        let waiting = self.shared.slot.lock().map(|slot| slot.is_some()).unwrap_or(false);
        let keep = self.open.is_some() || waiting || Arc::strong_count(&self.shared) > 1;
        if !keep {
            self.watching = false;
        }
        keep
    }

    fn submit(&mut self, cx: &mut Context<Self>) {
        if self.open.is_none() || self.field.read(cx).is_empty() {
            return;
        }
        let password = Password::from_string(self.field.update(cx, |f, cx| f.take_text(cx)));
        if let Some(request) = self.open.take() {
            let _ = request.reply.send(Some(password));
        }
        cx.notify();
    }

    fn cancel(&mut self, cx: &mut Context<Self>) {
        // Whatever was typed goes with the field's text, unused.
        drop(Password::from_string(self.field.update(cx, |f, cx| f.take_text(cx))));
        if let Some(request) = self.open.take() {
            let _ = request.reply.send(None);
        }
        cx.notify();
    }

    fn button(&self, id: &'static str, label: &'static str, primary: bool, enabled: bool) -> gpui::Stateful<gpui::Div> {
        let t = &self.theme;
        let (bg, fg, border) = if primary { (t.accent, t.accent_text, t.accent) } else { (alpha(t.raised, 0.0), t.muted, t.border_strong) };
        let hover = if primary { alpha(t.accent, 0.88) } else { t.hover };
        div()
            .id(id)
            .h(px(30.))
            .px_4()
            .flex()
            .items_center()
            .justify_center()
            .rounded(px(t.radius + 1.))
            .border_1()
            .border_color(border)
            .bg(bg)
            .text_color(fg)
            .text_size(px(12.))
            .when(enabled, |d| d.cursor_pointer().hover(move |s| s.bg(hover)))
            .when(!enabled, |d| d.opacity(0.45))
            .child(label)
    }
}

impl Render for PasswordPrompt {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        if self.open.is_some() && self.focus_field {
            self.focus_field = false;
            window.focus(&self.field.focus_handle(cx));
        }
        // Closed, it takes no place in the host's layout.
        let Some(open) = &self.open else { return div().absolute().into_any_element() };
        let t = &self.theme;
        let typed = !self.field.read(cx).is_empty();
        let focused = self.field.focus_handle(cx).is_focused(window);
        let card = div()
            .id("password-prompt")
            .w(px(460.))
            .flex()
            .flex_col()
            .gap_3()
            .p(px(22.))
            .rounded(px(t.radius * 2. + 4.))
            .border_1()
            .border_color(t.border_strong)
            .bg(t.panel)
            .shadow_md()
            .on_click(|_, _, cx| cx.stop_propagation())
            .child(div().text_size(px(14.)).text_color(t.text).child("Swarm needs your computer password to run:"))
            .child(div().text_size(px(12.5)).text_color(t.accent).child(open.summary.clone()))
            .child(
                div()
                    .px_2()
                    .py_1()
                    .rounded(px(t.radius))
                    .bg(t.surface)
                    .text_size(px(11.))
                    .text_color(t.faint)
                    .child(SharedString::from(open.command.clone())),
            )
            .children(open.retry.clone().map(|r| div().text_size(px(11.5)).text_color(t.err).child(r)))
            .child(
                div()
                    .h(px(34.))
                    .px_3()
                    .flex()
                    .items_center()
                    .rounded(px(t.radius + 1.))
                    .border_1()
                    .border_color(if focused { alpha(t.accent, 0.7) } else { t.border_strong })
                    .bg(t.surface)
                    .text_color(t.text)
                    .child(self.field.clone()),
            )
            .child(div().text_size(px(11.)).text_color(t.faint).child("It goes only to sudo on this computer, once; Swarm Console does not keep it."))
            .child(
                div()
                    .flex()
                    .justify_end()
                    .gap_2()
                    .child(self.button("password-cancel", "Cancel", false, true).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.cancel(cx))))
                    .child(self.button("password-run", "Run", true, typed).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.submit(cx)))),
            );
        div()
            .id("password-backdrop")
            .key_context("PasswordPrompt")
            .on_action(cx.listener(|this, _: &Submit, _, cx| this.submit(cx)))
            .on_action(cx.listener(|this, _: &Cancel, _, cx| this.cancel(cx)))
            .absolute()
            .inset_0()
            .occlude()
            .flex()
            .items_center()
            .justify_center()
            .bg(alpha(t.bg, 0.72))
            .font_family(t.mono)
            .child(card)
            .into_any_element()
    }
}
