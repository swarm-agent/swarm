package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui/v3chat"
)

func (a *App) cycleHomeModelProfile() {
	if a == nil || a.home == nil {
		return
	}
	profiles := a.homeModel.ModelProfiles
	if len(profiles) == 0 {
		a.setModelProfileStatus("no saved model profiles to cycle")
		return
	}
	currentID := strings.TrimSpace(a.homeModel.DefaultModelProfileID)
	if currentID == "" {
		currentID = strings.TrimSpace(a.homeModel.ActiveModelProfile.ProfileID)
	}
	next := 0
	for i, profile := range profiles {
		if strings.TrimSpace(profile.ProfileID) == currentID {
			next = (i + 1) % len(profiles)
			break
		}
	}
	a.selectHomeModelProfile(profiles[next].ProfileID)
}

func (a *App) selectHomeModelProfile(profileID string) {
	if a == nil || a.api == nil || a.home == nil {
		return
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		a.setModelProfileStatus("select profile failed: profile id is required")
		return
	}
	if a.route == "v3chat" && a.v3Chat != nil && a.v3Chat.Runtime() != nil && a.v3Chat.Runtime().Store() != nil {
		if _, active := v3chat.SelectActiveRun(a.v3Chat.Runtime().Store().Snapshot()); active {
			a.setModelProfileStatus("select profile failed: model profile cannot be changed during an active run")
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	profile, err := a.api.SetDefaultModelProfile(ctx, profileID)
	if err != nil {
		a.setModelProfileStatus(fmt.Sprintf("select profile failed: %v", err))
		return
	}
	state, err := a.api.ListModelProfiles(ctx)
	if err != nil {
		a.setModelProfileStatus(fmt.Sprintf("profile selected, but refresh failed: %v", err))
		a.queueReload(false)
		return
	}
	a.applyHomeModel(applyHomeModelProfiles(a.currentHomeModel(), state))
	if err := a.applySelectedProfileToV3Chat(ctx, profileID); err != nil {
		a.setModelProfileStatus(fmt.Sprintf("profile selected for new sessions, but chat update failed: %v", err))
		return
	}
	label := strings.TrimSpace(profile.Name)
	if label == "" {
		label = profile.ProfileID
	}
	a.setModelProfileStatus("profile selected: " + label)
}

func (a *App) setModelProfileStatus(status string) {
	if a == nil {
		return
	}
	if a.home != nil {
		a.home.SetStatus(status)
	}
	if a.route == "v3chat" && a.v3Chat != nil {
		a.v3Chat.SetStatus(status)
	}
}

func (a *App) applySelectedProfileToV3Chat(ctx context.Context, profileID string) error {
	if a.v3ChatDraftActive() {
		a.syncPrimedV3ChatFromHomeDraft()
		return nil
	}
	if a == nil || a.route != "v3chat" || a.v3Chat == nil || a.v3Chat.Runtime() == nil || a.v3Chat.Runtime().Store() == nil {
		return nil
	}
	sessionID := strings.TrimSpace(a.v3Chat.Runtime().Store().Snapshot().Session.ID)
	policy, err := a.api.SetSessionV3ModelProfile(ctx, sessionID, profileID)
	if err != nil {
		return err
	}
	a.v3Chat.ApplyModelProfile(policy)
	return nil
}

func (a *App) openCodexUsageModal() {
	if a == nil || a.home == nil {
		return
	}
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideAuthModal()
	a.home.HideWorkspaceModal()
	a.home.HideWorktreesModal()
	a.home.HideModelsModal()
	a.home.HideProfilesModal()
	a.home.HideCodexUsageModal()
	a.home.HideAgentsModal()
	a.home.HideVoiceModal()
	a.home.HideThemeModal()
	a.home.HideKeybindsModal()
	a.home.ShowCodexUsageModal()
}

func (a *App) refreshHomeCodexAccount() {
	if a == nil || a.home == nil {
		return
	}
	if a.api == nil {
		a.home.SetCodexUsageModalResult(client.CodexAccountUsage{}, "Codex account API is unavailable", client.CodexResetCredits{}, "Codex reset credits API is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var (
		usage      client.CodexAccountUsage
		usageErr   error
		credits    client.CodexResetCredits
		creditsErr error
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		usage, usageErr = a.api.GetCodexAccountUsage(ctx)
	}()
	go func() {
		defer wg.Done()
		credits, creditsErr = a.api.ListCodexResetCredits(ctx)
	}()
	wg.Wait()
	usageError := ""
	creditsError := ""
	if usageErr != nil {
		usageError = "usage: " + usageErr.Error()
	}
	if creditsErr != nil {
		creditsError = "reset credits: " + creditsErr.Error()
	}
	a.home.SetCodexUsageModalResult(usage, usageError, credits, creditsError)
	if usageErr == nil && creditsErr == nil {
		a.home.SetStatus(fmt.Sprintf("Codex usage refreshed: %s plan, %d reset credits", emptyFallback(strings.TrimSpace(usage.PlanType), "unknown"), credits.AvailableCount))
	} else {
		a.home.SetStatus("Codex account usage refresh failed; press r to retry or open /auth")
	}
}

func (a *App) consumeHomeCodexResetCredit(creditID, idempotencyKey string) {
	if a == nil || a.api == nil || a.home == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := a.api.ConsumeCodexResetCredit(ctx, creditID, idempotencyKey)
	if err != nil {
		message := fmt.Sprintf("Codex reset credit failed: %v; retry keeps the same redemption key", err)
		a.home.SetCodexResetResult(creditID, message, true)
		a.home.SetStatus(message)
		return
	}
	message := ""
	retryable := true
	switch out.Code {
	case "reset":
		message = fmt.Sprintf("Codex usage reset (%d windows)", out.WindowsReset)
		retryable = false
	case "already_redeemed":
		message = "Codex reset credit was already used"
		retryable = false
	case "nothing_to_reset":
		message = "No reached Codex usage window needs resetting"
	case "no_credit":
		message = "Codex reset credit is no longer available"
	}
	a.home.SetCodexResetResult(creditID, message, retryable)
	a.home.SetStatus(message)
	if !retryable {
		a.refreshHomeCodexAccount()
	}
}
