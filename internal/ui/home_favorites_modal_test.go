package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

// Purpose: the Favorites modal must queue the user's explicit scope, reject
// chat scopes without a durable chat, and remain open until the app confirms
// success. HomePage.handleProfilesModalKey/Mouse and QueueSelectModelProfileScope
// own this boundary; direct input tests are the narrowest observable UI layer.
func TestFavoritesModalQueuesExplicitScopesAndKeepsFailureVisible(t *testing.T) {
	page := NewHomePage(model.HomeModel{ModelProfiles: []client.ModelProfile{{ProfileID: "favorite", Name: "Favorite"}}})
	page.ShowProfilesModal()
	page.handleProfilesModalKey(tcell.NewEventKey(tcell.KeyRune, '1', tcell.ModNone))
	if page.profilesModal.Scope != FavoriteScopeDefault {
		t.Fatal("chat scope accepted without a chat")
	}
	page.SetFavoritesChatAvailable(true)
	for i, scope := range []FavoriteScope{FavoriteScopeChat, FavoriteScopeDefault, FavoriteScopeDefaultAndChat} {
		page.handleProfilesModalKey(tcell.NewEventKey(tcell.KeyRune, rune('1'+i), tcell.ModNone))
		page.handleProfilesModalKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
		action, ok := page.PopHomeAction()
		if !ok || action.ModelProfileID != "favorite" || action.FavoriteScope != scope {
			t.Fatalf("queued action = %#v, %v, want scope %q", action, ok, scope)
		}
		if !page.ProfilesModalVisible() {
			t.Fatal("modal closed before confirmed API success")
		}
	}
	page.SetFavoritesStatus("default saved, but this chat was not changed")
	if page.profilesModal.Status != "default saved, but this chat was not changed" {
		t.Fatal("partial failure hidden")
	}
	page.HideProfilesModal()
	page.ShowProfilesModal()
	if page.profilesModal.ChatAvailable || page.profilesModal.Scope != FavoriteScopeDefault {
		t.Fatal("new modal retained old chat context")
	}
}

// Purpose: mouse scope controls must match keyboard scope controls even on a
// narrow terminal. drawProfilesModal produces the click targets consumed by
// handleProfilesModalMouse; a tcell simulation screen exercises that boundary.
func TestFavoritesModalMouseScopesOnNarrowTerminal(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(48, 20)
	page := NewHomePage(model.HomeModel{ModelProfiles: []client.ModelProfile{{ProfileID: "favorite"}}})
	page.ShowProfilesModal()
	page.SetFavoritesChatAvailable(true)
	page.drawProfilesModal(screen)
	for _, target := range page.profilesModalTargets {
		if target.Action != "favorite-scope" {
			continue
		}
		if target.Rect.X < 0 || target.Rect.X+target.Rect.W > 48 {
			t.Fatalf("scope target overflows: %#v", target)
		}
		page.handleProfilesModalMouse(tcell.NewEventMouse(target.Rect.X, target.Rect.Y, tcell.Button1, tcell.ModNone))
		want := []FavoriteScope{FavoriteScopeChat, FavoriteScopeDefault, FavoriteScopeDefaultAndChat}[target.Index]
		if page.profilesModal.Scope != want {
			t.Fatalf("mouse selected %q, want %q", page.profilesModal.Scope, want)
		}
	}
}
