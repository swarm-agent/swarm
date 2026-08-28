#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
GO_LIB="${SCRIPT_DIR}/lib-go.sh"
PNPM_LIB="${SCRIPT_DIR}/lib-pnpm.sh"
cd "${ROOT_DIR}"

usage() {
  cat <<'USAGE'
Usage: bash scripts/run-critical-tests.sh [fast|deep|agents|all]

Runs the curated, atlas-driven critical test manifest.
  fast    Build-blocking security and authority tests; hermetic and bounded.
  deep    Durability, recovery, Git, artifact, media, and video boundary tests.
  agents  Agent identity, tool authority, delegation, scheduling, lineage, and Desktop child-state tests.
  all     Run fast followed by deep and agents.

The manifest uses explicit test functions and files. Update this script and the
Swarm atlas together when a covered critical invariant or its evidence changes.
USAGE
}

suite="${1:-all}"
case "${suite}" in
  fast|deep|agents|all) ;;
  -h|--help)
    usage
    exit 0
    ;;
  *)
    echo "unknown critical test suite: ${suite}" >&2
    usage >&2
    exit 2
    ;;
esac

if [[ ! -f "${GO_LIB}" ]]; then
  echo "missing Go resolver script at ${GO_LIB}" >&2
  exit 1
fi
# shellcheck disable=SC1091
source "${GO_LIB}"
swarm_require_go "${ROOT_DIR}"

CACHE_ROOT="${GO_CACHE_ROOT:-${ROOT_DIR}/.cache/go}"
GOCACHE_DIR="${GOCACHE_DIR:-${CACHE_ROOT}/build}"
GOMODCACHE_DIR="${GOMODCACHE_DIR:-${CACHE_ROOT}/mod}"
GOPATH_DIR="${GOPATH_DIR:-${CACHE_ROOT}/path}"
mkdir -p "${GOCACHE_DIR}" "${GOMODCACHE_DIR}" "${GOPATH_DIR}"

run_go() {
  GOCACHE="${GOCACHE_DIR}" \
  GOMODCACHE="${GOMODCACHE_DIR}" \
  GOPATH="${GOPATH_DIR}" \
  GOTOOLCHAIN="${GOTOOLCHAIN}" \
  "${GO_BIN}" "$@"
}

run_root_tests() {
  local pattern="$1"
  run_go test -count=1 -timeout=30s ./pkg/startupconfig ./pkg/storagecontract -run "${pattern}"
}

run_swarmd_tests() {
  local timeout="$1"
  local packages="$2"
  local pattern="$3"
  (
    cd "${ROOT_DIR}/swarmd"
    # shellcheck disable=SC2086
    run_go test -count=1 -timeout="${timeout}" ${packages} -run "${pattern}"
  )
}

run_web_critical_tests() {
  if [[ ! -d "${ROOT_DIR}/web/node_modules" ]]; then
    echo "[critical/fast] FAIL: web/node_modules is missing; run 'cd web && corepack pnpm install --frozen-lockfile'" >&2
    return 1
  fi
  if [[ ! -f "${PNPM_LIB}" ]]; then
    echo "missing pnpm resolver script at ${PNPM_LIB}" >&2
    return 1
  fi
  # shellcheck disable=SC1091
  source "${PNPM_LIB}"
  (
    cd "${ROOT_DIR}/web"
    swarm_pnpm run test:critical
  )
}

run_fast() {
  echo "[critical/fast] root safety defaults and private storage"
  run_root_tests 'Test(ResolveRoots|ResolveRoot|ValidateRoot|Overrides|JoinRejects|WritePreservesIdentity)'

  echo "[critical/fast] system-agent and tool authority"
  run_swarmd_tests 30s \
    './internal/agent' \
    '^Test(BuiltinSystemAgentRegistryIsCompleteAndUnique|CoderIdentityDoesNotAcceptRetiredCloneNames|SystemAgentRegistryRejectsMissingDuplicateAndInvalidDefinitions|SystemAgentSnapshotReconciliationPreservesDynamicContextAndModels|CompiledRouterIsDistinctAndToolFree|SystemSubagentsDoNotEnableRecursiveTask|AITaskPreparerUsesCanonicalParentModelAndReadOnlyTools|ManagedArtifactToolIsRestrictedToSwarmAndDesigner)$'

  echo "[critical/fast] workspace, discovery, artifact, action, and video containment"
  run_swarmd_tests 45s \
    './internal/workspace ./internal/discovery ./internal/artifact ./internal/action ./internal/videoproject' \
    'Test(ResolvePath|ScopeForPath|CreateFolderForPrincipal|ScanScope|ArtifactMaterializationTraversal|ResolveActionEntrypoint|AssembleActionArguments|RunnerSnapshotsAreExactScopeBound|VideoprojectSecurityRejections)'

  echo "[critical/fast] auth, origin, privacy, and signed cursor failures"
  run_swarmd_tests 60s \
    './internal/api' \
    'Test(DesktopBoundary|ProtectedCreateAPIsRequire|ProtectedCreateAPIsReject|ProtectedCreateAPIsSucceed|DesktopSessionBootstrapFails|DesktopSessionBootstrapIssues|DesktopSessionRejects|XSwarmTokenPrincipal|ExtractAttachToken|PanicRecovery|V3SyncCursor|V3RealtimeRejects)'

  echo "[critical/fast] Desktop V3 durable state and realtime repair"
  run_web_critical_tests
}

run_agent_web_tests() {
  if [[ ! -d "${ROOT_DIR}/web/node_modules" ]]; then
    echo "[critical/agents] FAIL: web/node_modules is missing; run 'cd web && corepack pnpm install --frozen-lockfile'" >&2
    return 1
  fi
  if [[ ! -f "${PNPM_LIB}" ]]; then
    echo "missing pnpm resolver script at ${PNPM_LIB}" >&2
    return 1
  fi
  # shellcheck disable=SC1091
  source "${PNPM_LIB}"
  (
    cd "${ROOT_DIR}/web"
    swarm_pnpm run test:agents
  )
}

run_agents() {
  echo "[critical/agents] runtime agent authority and launch trust boundary"
  run_swarmd_tests 60s \
    './internal/run' \
    '^Test(ResolveAgentToolContractRestoresManageSessionsOnlyForSwarmPrimary|ResolveAgentToolContractInheritsOnlyAccountPolicy|ResolveAgentToolContractForcesTaskOffForEverySubagent|ResolveAgentToolContractFailsClosedWhenSavedContractMissing|TaskDelegationPromptSanitizesInheritedContextAndFramesItUntrusted|ParseTaskCallArgumentsRejectsLaunchTimeTrustFields)$'

  echo "[critical/agents] Task Program and Iteration Swarm scheduling, recovery, and lineage"
  run_swarmd_tests 90s \
    './internal/run' \
    '^Test(ParseTaskProgramValidatesCompleteStagedContract|ParseTaskProgramRejectsDesignerAndSplitCoderWorkspaceTargets|ParseTaskProgramRejectsInvalidGraphAndScopesBeforeLaunch|TaskProgramSchedulerUnlocksOnlyIntegratedDependencies|TaskProgramStructuredBlockerPreservesExactRepairState|TaskProgramBlockedJobIsTerminalAndNeverRescheduled|TaskProgramFinderHandoffHydratesDependentCoderWithVerificationWarning|TaskProgramFinderHandoffLookupFailsClosedForMissingV3Message|TaskProgramManagedArtifactValidationRejectsLineageMismatch|ParseTaskProgramStartWithoutDefinitionSelectsApprovedCheckpointProgram|ParseTaskProgramLifecycleRejectsAllExistingProgramContinuation|ParseTaskSwarmIdeaRepeatsExactQuestionWithoutRouterFields|ParseTaskSwarmRejectsFinderExplicitLaunchesAndTrustFields|ParseTaskSwarmImageBuildsDirectRouterHydratedItems|ParseTaskSwarmDesignerRejectsWorkspaceOutput|ValidateTaskSwarmHydrationFailsClosed|IdeaProfileIsCompiledToolFreeAndProtected)$'

  echo "[critical/agents] subagent capacity, approval, and parent-only delegation"
  run_swarmd_tests 60s \
    './internal/permission' \
    '^Test(ReservationAutomaticBudgetCountsWavesRegardlessOfChildCount|ReservationActiveChildLimitAsksForPerCallAndAggregateOverflow|ReservationUsesSeparateRegularAndSwarmLimits|ReservationAsksThroughCanonicalPermissionFlow|ReservationPreservesDirectModeAndAbsoluteSafetyDenials|ReservationDeniesDelegationFromChildSession|FailedSubagentWaveReleasesConcurrencyWithoutRefundingBudget|ReserveSubagentProgramCountsOneInvocationAndOnlyReadyCapacity)$'

  echo "[critical/agents] durable Task Program and child-rotation recovery"
  run_swarmd_tests 90s \
    './internal/store/pebble' \
    '^Test(TaskProgramStoreScopesCreationAndStatusToParentSession|TaskProgramStoreRevisionGuardsAndIdempotentTransitions|TaskProgramStoreParallelIntegrationDependentFixerAndTerminalCompletion|TaskProgramStoreReconstructsBlockedDirtyRecoveryAfterReopen|DelegatedChildRotationPersistsTargetedHandoffAcrossReopen|DelegatedChildRotationTransfersLeaseWithoutMutatingWorktree|DelegatedChildRotationRejectsIncompleteHandoffAndPreservesManagedArtifactIdentity|DelegatedChildRotationConcurrentMutationIsSingleWinnerAndIdempotent)$'

  echo "[critical/agents] Desktop child state, lineage, realtime subscriptions, and tool ordering"
  run_agent_web_tests
}

run_deep() {
  echo "[critical/deep] V3 atomic mutation, outbox, replay, restart, and concurrency"
  run_swarmd_tests 120s \
    './internal/store/pebble' \
    '^Test(ApplyV3SessionMutationAtomicCreateAndReplayIdempotency|ApplyV3SessionMutationExpectedLastEventSeqConflicts|ApplyV3SessionMutationRealtimeOutboxIsAtomicAndOrdered|ApplyV3SessionMutationConcurrentIdempotentAppendAllocatesOneSeq|ApplyV3SessionMutationConcurrentDistinctAppendsAllocateContiguousSeq|V3SessionMutationStoresSurviveRestart|ReplayV3SessionEventsSupportsCursorLimitAndDetectsGaps|V3RealtimeOutboxAuthScopeIndexReturnsOnlyAuthorizedRowsInEndpointOrder|V3RealtimeOutboxSessionSeqIndexDoesNotMissRowsPastOldGlobalScanCap)$'

  echo "[critical/deep] executor idempotency"
  run_swarmd_tests 90s \
    './internal/api' \
    'TestSessionV3Executor'

  echo "[critical/deep] delegated Git integration isolation"
  run_swarmd_tests 120s \
    './internal/worktree' \
    '^Test(ResolveTaskBaseRejectsDirtyParentBeforeAllocation|TaskCommitDescendsFromRecordedBase|VerifyTaskIntegrationWorkspaceRejectsPathOutsideManagedRepository|ApplyTaskIntegrationRejectsConcurrentRepositoryOwner|PrepareAndApplyTaskIntegrationIsDeterministic|PrepareTaskIntegrationPreflightsCompleteStackAndLeavesParentUnchangedOnConflict|PrepareTaskIntegrationRejectsStaleParentHead|EnsureWorktreeParentUsesPrivatePermissions|AllocateTaskWorkspaceMaterializesOnlyOwnedScopeAndContext)$'

  echo "[critical/deep] managed artifact Git integrity and bounds"
  run_swarmd_tests 90s \
    './internal/artifactgit' \
    'Test(HistoricalForkMergeLocksCASRestartAndBundle|GenesisRetryRequiresExactTreeAndTransactionsAreImmutable|MoveMaterializeDeleteAndBounds|RejectsHostileIDs)'
}

case "${suite}" in
  fast)
    run_fast
    ;;
  deep)
    run_deep
    ;;
  agents)
    run_agents
    ;;
  all)
    run_fast
    run_deep
    run_agents
    ;;
esac

echo "[critical/${suite}] PASS"
