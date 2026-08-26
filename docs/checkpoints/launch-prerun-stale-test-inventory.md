# Launch pre-run stale-test inventory

## Purpose

This inventory separates the launch-critical end-to-end gate from an accidentally broad API test selection. It is triage evidence, not a claim that every listed test is obsolete or that every failure is a product regression.

The accidental selector was:

```text
go test ./internal/identity ./internal/api -run 'Test(Session|Desktop|Local|Auth|Protected|JWT|Onboarding)'
```

Because the expression is unanchored, it selects most `TestSessionsV3...` tests. The launch-critical onboarding lane no longer runs this selector or the complete frontend unit suite. It now builds an isolated daemon and directly proves bootstrap, protected API behavior, session issuance, restart persistence, and rebootstrap rejection.

## Baseline

- First measured run: 379 top-level tests selected, 135 top-level failures.
- Repeat inventory run: 133 top-level failures.
- The two-test variation means the broad baseline is not fully deterministic and needs isolation during later triage.
- The latest 133 failures group into six likely shared contract areas below.
- Classification is intentionally pending. A later audit must compare each test with current checked-in product contracts before updating, removing, or treating it as a regression.

## 1. Onboarding, authentication, and identity — 9

- `TestLocalTransportHandlerAllowsProtectedAPIWithoutAttachToken`
- `TestOnboardingAllowsSensitiveMetadataWithDesktopSession`
- `TestOnboardingIdentitySaveDoesNotRequireLANAdvertiseHost`
- `TestOnboardingPostBootstrapsIdentityAndIssuesSession`
- `TestOnboardingProviderCredentialVerifiesActivatesHydratesBeforeReturning`
- `TestOnboardingRedactsSensitiveMetadataWithoutAuth`
- `TestOnboardingRejectsPrimaryChildModeToggle`
- `TestProtectedCreateAPIsSucceedAfterBootstrapWithValidProductJWT`
- `TestSessionPermissionsStatusPendingBypassesHistoricalLimit`

Likely shared drift includes the canonical personal account-scope identity model, removal of implicit team creation, retired onboarding fields, principal requirements, and newer protected-create contracts.

## 2. Plan and checkpoint lifecycle — 26

- `TestSessionV3LatestPlanManageToolPayloadUsesMessageTail`
- `TestSessionV3ProviderCheckpointStartupInputUsesCanonicalBuilder`
- `TestSessionsV3CompactEndpointSlicesRepeatedCompactionFromLatestCheckpoint`
- `TestSessionsV3DirectionChangingUserMessageReactivatesPausedCheckpointForAgentRestartDecision`
- `TestSessionsV3ExecutorAutomaticCheckpointsUseSameEpochAndDistinctRuns`
- `TestSessionsV3ExecutorExitPlanModeUsesV3MutationAndRefreshesContinuationRuntime`
- `TestSessionsV3ExecutorFinalHandoffIsolatesSentinelAcrossCheckpointCycles`
- `TestSessionsV3ExecutorFinalReviewFollowupStartsCheckpointInSuccessorEpochExactlyOnce`
- `TestSessionsV3ExecutorFinalizesAutomaticLastCheckpointCompletion`
- `TestSessionsV3ExecutorFinalizesReviewCheckpointAfterProviderManagedPlanComplete`
- `TestSessionsV3ExecutorProviderManagedStartCheckpointDefersToAutomaticRun`
- `TestSessionsV3ExecutorUserContinuationResumesBlockedCheckpointBeforeAdvancing`
- `TestSessionsV3PlanEntryRejectsFlatPlanDisabledWithoutMutation`
- `TestSessionsV3PlanModeDedicatedLifecycleEndpointValidation`
- `TestSessionsV3PlanModeDedicatedLifecycleEndpointsSuccess`
- `TestSessionsV3PlanSidechatModelProfileClonesImmutableSnapshot`
- `TestSessionsV3PlanSidechatModelProjectionConsistentAcrossSyncViewAndRealtime`
- `TestSessionsV3PlanSidechatUsesImmutableSnapshotAndPreservesParentState`
- `TestSessionsV3PrimaryNextMessageUsesActiveFollowupCheckpointSameEpochContext`
- `TestSessionsV3PrimaryNextMessageWithoutActiveCheckpointSplitUsesTranscriptContext`
- `TestSessionsV3PrimaryPlanModeStartCheckpointPreflightIsPrimaryOnlyAndAtomic`
- `TestSessionsV3ProviderBaseRequestCheckpointCategoriesUseSameEpochCodexBoundary`
- `TestSessionsV3ProviderInputAfterCompactUsesCheckpointAndRuntimePayload`
- `TestSessionsV3ProviderManagedPlanManageTerminalOutcomesUsePlanSavedOutbox`
- `TestSessionsV3UserMessageRebindsRefinedCheckpointFromCompletedRun`
- `TestSessionsV3WaitingReviewMessageKeepsCanonicalDurableMessageAndProviderEpoch`

Likely shared drift includes complete structured-plan validation, active run ownership, same-epoch checkpoint continuation, final-review routing, and retired follow-up lifecycle assumptions.

## 3. Sync, realtime, and projections — 27

- `TestSessionsV3CommittedMutationPublishesGlobalEventEnvelope`
- `TestSessionsV3GlobalEnvelopePreservesSourceAndCausation`
- `TestSessionsV3ModelProfileMutationCommitsCurrentSlotAndRealtimeOutbox`
- `TestSessionsV3PrimaryHydratesAfterStoreRestart`
- `TestSessionsV3PrimaryListAndHydrateRejectCrossUserSessions`
- `TestSessionsV3PrimaryLiveStreamPublishesPermissionEventsBeforeProviderToolStart`
- `TestSessionsV3PrimaryLiveStreamPublishesToolExecutionProgressAndCommittedCompletion`
- `TestSessionsV3PrimaryParentStreamDeliversLiveChildEvents`
- `TestSessionsV3PrimaryParentStreamReplaysKnownChildEvents`
- `TestSessionsV3PrimaryRunStreamControlIsPrimaryOnly`
- `TestSessionsV3PrimaryStreamCapturesRealProviderMultiToolLoopContinuity`
- `TestSessionsV3PrimaryStreamCarriesProviderReasoningEventsAndMessage`
- `TestSessionsV3PrimaryStreamCursorErrors`
- `TestSessionsV3PrimaryStreamDesktopV3StaticGuards`
- `TestSessionsV3PrimaryStreamDisambiguatesReusedProviderToolCallIDs`
- `TestSessionsV3PrimaryStreamDoesNotRepublishReplayedMutations`
- `TestSessionsV3PrimaryStreamHandlerDoesNotUseV2RunStreamOrRuntime`
- `TestSessionsV3PrimaryStreamPublishesExecutorCommittedEventsAndReplaysThem`
- `TestSessionsV3PrimaryStreamRejectsDesktopSurfaceBeforeWebsocketUpgrade`
- `TestSessionsV3PrimaryStreamRejectsLegacyResumeInputsInStrictMode`
- `TestSessionsV3PrimaryStreamReplaysDurableEventsAfterRestart`
- `TestSessionsV3PrimaryStreamTransitionsFromReplayToLiveEvents`
- `TestSessionsV3ReconnectWorksetSessionShellOmitsSettingsOnlyMetadata`
- `TestSessionsV3SyncBootstrapDesktopIncludesPinnedSessionsOutsideRecentLimit`
- `TestSessionsV3SyncBootstrapSessionShellOmitsSettingsOnlyMetadata`
- `TestSessionsV3SyncHydrateUsesSessionPreferenceInsteadOfAgentModelFields`
- `TestSessionsV3SyncSessionShellProjectsImmutableCurrentModePreference`

Likely shared drift includes retirement of per-session streams, canonical `/v3/realtime/stream`, opaque endpoint cursors, required mutation idempotency fields, and newer projection/resource-set behavior.

## 4. Agent and model settings — 17

- `TestSessionV3OrdinaryAgentResolutionKeepsCurrentAccountToolContract`
- `TestSessionV3SystemSidechatResolutionUsesRegistryAndRejectsSpoofing`
- `TestSessionsV3ExplicitModelProfilePreferenceMatchesDurableSessionPreference`
- `TestSessionsV3ModelProfileChoiceSnapshotsSavedAndTemporaryProfiles`
- `TestSessionsV3ModelProfileMutationRejectsActiveRunWithoutChangingSnapshot`
- `TestSessionsV3ModelProfileMutationRejectsClearWithoutChangingAuthorities`
- `TestSessionsV3PreferenceIdempotencyReplayConflictAndDistinctKey`
- `TestSessionsV3PreferenceRequiresCallerIdempotencyKey`
- `TestSessionsV3PrimaryAgentSwitchUpdatesStoredProfileAndRuntime`
- `TestSessionsV3PrimaryPreferenceUsesV3PrimaryMutation`
- `TestSessionsV3PrimarySettingsPatchRejectsGenericMode`
- `TestSessionsV3PrimarySettingsPatchRejectsPreferenceWhenAgentModelLocked`
- `TestSessionsV3PrimarySettingsPatchRejectsRemovedModelFields`
- `TestSessionsV3PrimarySettingsPatchRejectsStaleProjectionSeq`
- `TestSessionsV3PrimarySettingsPatchUpdatesAgentPreferenceWithoutChangingMode`
- `TestSessionsV3RuntimeInstructionsUseManagedWorktreeAndRuleContext`
- `TestSessionsV3SystemSidechatsExecuteFromRegistryAfterStoreReopen`

Likely shared drift includes immutable session model snapshots, canonical account-scoped agent-model settings, required client request IDs, and code-owned system-agent contracts.

## 5. Session storage, access, and worktrees — 6

- `TestSessionBindingAccessUsesManagedWorktreeParentLineage`
- `TestSessionsV3PrimaryCreateIsIdempotent`
- `TestSessionsV3PrimaryMessagesCommitUserMessageAndPendingExecutorIntent`
- `TestSessionsV3PrimaryWorktreeCreateReplayDoesNotReallocate`
- `TestSessionsV3ProviderLineageMetadataPersistsAcrossModelHandoffLegacyTranscriptIgnored`
- `TestSessionsV3SearchArchivedSessionAppendReactivates`

Likely shared drift includes required execution preferences, canonical idempotency payload hashes, binding-generation authority, and archived-session mutation behavior.

## 6. Executor, providers, tools, compaction, and titles — 48

- `TestSessionsV3AssistantCompletedCarriesRunIdentity`
- `TestSessionsV3CommittedMutationReachesGlobalWebsocketSessionWildcard`
- `TestSessionsV3CompactEndpointRunsManualCompactAndResetsUsage`
- `TestSessionsV3CreateReachesGlobalWebsocketSessionWildcard`
- `TestSessionsV3ExecutorCarriesFullContinuationHistoryAcrossMultipleToolSteps`
- `TestSessionsV3ExecutorCoalescesProviderDeltas`
- `TestSessionsV3ExecutorCoalescesProviderReasoningDeltas`
- `TestSessionsV3ExecutorCompletesAfterMoreThanEightVariedToolCalls`
- `TestSessionsV3ExecutorCompletesAfterPreToolDeltaAndThreeToolCalls`
- `TestSessionsV3ExecutorContinuesAfterOverflowCompaction`
- `TestSessionsV3ExecutorContinuesAfterProviderManagedRestartTurn`
- `TestSessionsV3ExecutorDoesNotRetitleExplicitTitle`
- `TestSessionsV3ExecutorExecutesProviderToolCallsAndContinuesToFinalAnswer`
- `TestSessionsV3ExecutorFailsClosedWhenProviderReturnsToolCalls`
- `TestSessionsV3ExecutorFailsNonCompletionProviderStopReason`
- `TestSessionsV3ExecutorFailsStaleRunningRunAfterRestartWithoutResume`
- `TestSessionsV3ExecutorFlushesProviderDeltaAtSizeBoundary`
- `TestSessionsV3ExecutorPersistsFailureWhenToolCallRepeatsFiveConsecutiveTimes`
- `TestSessionsV3ExecutorPersistsInterleavedAssistantSegmentsBeforeTools`
- `TestSessionsV3ExecutorReasoningSnapshotNewlineFlushUsesIncrementalChange`
- `TestSessionsV3ExecutorReasoningSnapshotSizeFlushUsesIncrementalChange`
- `TestSessionsV3ExecutorRecordsFailurePayload`
- `TestSessionsV3ExecutorRecoversPendingRunAfterRestart`
- `TestSessionsV3ExecutorRestoresCurrentAccountSwarmManageSessions`
- `TestSessionsV3ExecutorStartsTitleBeforeAssistantCompletes`
- `TestSessionsV3ExecutorStartsTitleFromCommittedMutationHook`
- `TestSessionsV3ExecutorUpdatesDefaultTitleAfterToolShapedFirstRun`
- `TestSessionsV3ExecutorUpdatesDefaultTitleWithMemoryAgentAfterFirstRun`
- `TestSessionsV3ExecutorUpdatesDefaultTitleWithSystemPreludeAfterFirstRun`
- `TestSessionsV3ExecutorUpdatesTUITitleAfterFirstRun`
- `TestSessionsV3ExecutorUsesAutoToolChoiceWhenToolsResolved`
- `TestSessionsV3ExecutorUsesCurrentAgentToolContractWithStoredProfileSnapshot`
- `TestSessionsV3ExecutorUsesProviderFromCommittedHistory`
- `TestSessionsV3PrimaryDispatchAuthorityRecordsSpecificBlockedReasonsWithoutBlockingMessage`
- `TestSessionsV3PrimaryDuplicatePostReplayDoesNotDuplicateAssistantOutput`
- `TestSessionsV3PrimaryEventsReplayCursorAndRestart`
- `TestSessionsV3PrimaryModeSubresourceIsRetired`
- `TestSessionsV3PrimaryPostReturnsBeforeModelCompletion`
- `TestSessionsV3PrimaryProviderVerticalSlicePersistsAssistantOnce`
- `TestSessionsV3PrimaryRunStopCancelsActiveExecutorAndSuppressesLateOutput`
- `TestSessionsV3PrimaryStandalonePathsWorkWithDispatchServicesDisabled`
- `TestSessionsV3PrimaryToolFailurePersistsDurableTerminalEvent`
- `TestSessionsV3ProviderInputReplaysDurableMediaAfterContractRefresh`
- `TestSessionsV3ProviderManagedRead2000LineLatencyAndPayload`
- `TestSessionsV3ProviderUsageAccountingE2E`
- `TestSessionsV3ProviderUsageAccountingTransitionE2E`
- `TestSessionsV3RepresentativeMutationsPublishGlobalV3Envelopes`
- `TestSessionsV3TitleMutationPublishesCanonicalGlobalV3Payload`

Likely shared drift includes provider adapter setup, current tool contracts, delta persistence/coalescing, compaction dependencies, title-agent configuration, usage records, and retired websocket assumptions.

## Current launch-gate result

After narrowing the onboarding lane and fixing evidence-only harness blockers, the intended launch tests currently report:

- `onboarding`: pass; isolated daemon bootstrap, personal account scope, protected APIs, session issuance, restart persistence, and rebootstrap rejection completed.
- `desktop`: pass; the runner now reuses an installed system Chrome/Chromium binary instead of requiring a separately downloaded Playwright browser.
- `tui`: pass; final ordering evidence now comes from canonical durable messages instead of a clipboard rendering that omitted assistant rows.
- `task-routing`: pass.
- `task-program`: pass across two runtime-discovered bound repositories and both parent workspace modes; an explicit linked path remains available as an override rather than a required duplicate configuration.
- `plan-auto`: fail; both checkpoints and their subtasks complete, but the initial and checkpoint run intents remain `pending_executor`, so runtime usage and terminal-intent gates cannot pass. This remains a real launch-path issue to classify or fix.
- `provider-sync`: not run; the required expected candidate commit and remote candidate repository authority were not configured. The runner correctly refuses to invent these values.

No testbench rebuild, deployment, source commit, push, or publication was performed.

## Required next audit

For each group:

1. Read the current implementation and its canonical contract.
2. Run the smallest directly relevant test subset in isolation.
3. Label every failure `regression`, `stale-test`, `retired-contract`, or `test-harness` with evidence.
4. Fix one shared contract cluster at a time.
5. Do not weaken fail-closed behavior or restore retired APIs merely to satisfy an old assertion.
