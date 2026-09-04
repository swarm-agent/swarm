import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('./desktop-onboarding-gate.tsx', import.meta.url), 'utf8')
const repositorySource = await readFile(new URL('../../../workspaces/launcher/services/workspace-repository.ts', import.meta.url), 'utf8')
const assistantSource = await readFile(new URL('./workspace-onboarding-assistant.tsx', import.meta.url), 'utf8')
const onboardingAPISource = await readFile(new URL('../api.ts', import.meta.url), 'utf8')
const conversationSource = await readFile(new URL('../../chat/components/desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')

test('onboarding saves, refreshes, selects, finalizes, and navigates without definition polling', () => {
  assert.match(source, /await saveWorkspace\(\{/)
  assert.match(source, /await refreshWorkspaces\(\)/)
  assert.match(source, /const selectedResolution = await saveAndOpenReadyWorkspace\([\s\S]*?entry\.path/)
  assert.match(source, /await finishWithWorkspace\(selectedResolution, entry\.path\)/)
  assert.match(source, /waitForOnboardingReadyHold\(\)/)
  assert.match(source, /patchDesktopOnboarding\(\{ desktopOnboardingComplete: true \}\)\.then\(\(\) => reloadStatus\(\)\)/)
  assert.match(source, /await navigateToWorkspace\(resolution, fallbackPath\)/)
  assert.doesNotMatch(source, /waitForWorkspaceDefinition/)
})

test('explorer workspace additions fade directly into the stable setup view', () => {
  assert.match(source, /const STEP_TRANSITION_MS = 220/)
  assert.match(source, /transitionToSetup[\s\S]*?setPanelVisible\(false\)[\s\S]*?setWorkspaceExplorerOpen\(false\)[\s\S]*?setView\('setup'\)/)
  assert.match(source, /workspaceExplorerOpen && view === 'workspace'[\s\S]*?panelVisible \? 'opacity-100' : 'pointer-events-none opacity-0'/)

  const addFolderHandler = source.slice(
    source.indexOf('const handleSaveAndOpenFolder'),
    source.indexOf('const initializeOnboardingRepository'),
  )
  assert.doesNotMatch(addFolderHandler, /setWorkspaceExplorerOpen\(false\)/)
  assert.match(addFolderHandler, /setPendingAction\('workspace'\)[\s\S]*?transitionToSetup\(\)[\s\S]*?await saveAndOpenReadyWorkspace/)
  assert.match(addFolderHandler, /WorkspaceRepositoryPrerequisiteError[\s\S]*?setWorkspaceRepositoryState\(err\.repository\)/)
})

test('onboarding does not gate saved workspaces on definition state or show personalization copy', () => {
  assert.match(source, /const resolution = await openWorkspace\(path\)/)
  assert.match(source, /WorkspaceRepositoryPrerequisiteError[\s\S]*?setWorkspaceRepositoryState\(err\.repository\)/)
  assert.doesNotMatch(source, /definitionStatus === 'pending'/)
  assert.doesNotMatch(source, /definitionStatus === 'failed'/)
  assert.doesNotMatch(source, /workspaceDefinitionFailureMessage/)
  assert.doesNotMatch(source, /personaliz/i)
  assert.doesNotMatch(source, /Router is reading the bounded workspace context/)
  assert.match(source, /Setting up your workspace…/)
  assert.match(source, /Holding the onboarding surface steady while the workspace is confirmed\./)
  assert.match(source, /A committed Git repository is required/)
  assert.match(source, /Initialize Git repository/)
  assert.match(source, /Talk to Onboarding Swarm/)
  assert.match(source, /Fix manually and retry/)
  assert.match(source, /canUseOnboardingAssistant = status\.auth\.credentialCount > 0 && status\.auth\.activeProviders\.length > 0/)
  assert.match(source, /if \(!onboardingAssistant \|\| canUseOnboardingAssistant\) return[\s\S]*?active provider credential/)
  assert.match(source, /startWorkspaceOnboardingSession\(\{/)
  assert.match(onboardingAPISource, /\/v3\/sessions:workspace-onboarding/)
  assert.doesNotMatch(source, /postDesktopV3BackgroundRouterSessionStart/)
  assert.doesNotMatch(source, /desktopV3RoutedWorkspaceAuthority/)
  assert.match(repositorySource, /review repository setup[\s\S]*?ignore rules[\s\S]*?explicit permission/i)
  assert.doesNotMatch(source, /Git is optional|ready to use without Git|Use folder for this chat only/)
})

test('existing-file onboarding stays in its dedicated assistant and admits the workspace only after a committed repository recheck', () => {
  assert.match(source, /const assistant = \{ sessionId: launched\.sessionId, path: launched\.repository\.path \}/)
  assert.match(source, /saveWorkspaceOnboardingAssistantResume\(assistant\)[\s\S]*?setOnboardingAssistant\(assistant\)/)
  assert.match(source, /loadWorkspaceOnboardingAssistantResume\(\)/)
  assert.match(source, /applyDesktopV3RoutedStartResponse\(\{/)
  assert.match(assistantSource, /Onboarding Swarm/)
  assert.match(assistantSource, /Check repository and return to workspace onboarding/)
  assert.match(assistantSource, /Check repository and finish onboarding/)
  assert.match(assistantSource, /DesktopV3ExistingConversationPane/)
  assert.match(conversationSource, /DesktopPermissionModal/)
  assert.match(conversationSource, /onResolve=\{handleResolvePermission\}/)
  assert.match(assistantSource, /DesktopV3RuntimeProvider initialPreferredSessionId=\{sessionId\}/)
  assert.match(assistantSource, /requestAnimationFrame[\s\S]*?selectAndHydrateDesktopV3Session\(sessionId\)/)
  assert.match(source, /const saveAndOpenReadyWorkspace[\s\S]*?await saveWorkspace\([\s\S]*?await refreshWorkspaces\(\)[\s\S]*?return openWorkspace\(path\)/)
  const recheckHandler = source.slice(source.indexOf('const recheckOnboardingRepository'), source.indexOf('const openWorkspaceExplorer'))
  assert.match(recheckHandler, /saveAndOpenReadyWorkspace\([\s\S]*?assistant\.path[\s\S]*?await finishWithWorkspace/)
  assert.match(recheckHandler, /WorkspaceRepositoryPrerequisiteError[\s\S]*?saveWorkspaceOnboardingAssistantResume\(null\)[\s\S]*?Repository is not ready yet/)
  assert.doesNotMatch(assistantSource, /patchDesktopOnboarding|desktopOnboardingComplete/)
})

test('assistant start and recheck failures remain recoverable without a false onboarding transition', () => {
  assert.match(source, /error\.message.*Fix the folder manually or retry\./)
  assert.match(source, /Could not verify and add this repository\. Retry after the first commit exists\./)
  assert.match(assistantSource, /Use Check and return, then reopen Onboarding Swarm to retry\./)
  const recheckFailureHandler = source.slice(source.indexOf('const recheckOnboardingRepository'), source.indexOf('const openWorkspaceExplorer'))
  assert.match(recheckFailureHandler, /await finishWithWorkspace[\s\S]*?saveWorkspaceOnboardingAssistantResume\(null\)[\s\S]*?setOnboardingAssistant\(null\)/)
  assert.match(recheckFailureHandler, /catch \(error\)[\s\S]*?setClosing\(false\)[\s\S]*?transitionToStep\('workspace'\)/)
  const genericFailure = recheckFailureHandler.slice(recheckFailureHandler.lastIndexOf('} else {'), recheckFailureHandler.indexOf('} finally {'))
  assert.match(genericFailure, /setRepositoryRecheckError/)
  assert.doesNotMatch(genericFailure, /setOnboardingAssistant\(null\)|saveWorkspaceOnboardingAssistantResume\(null\)/)
  assert.doesNotMatch(recheckFailureHandler.slice(recheckFailureHandler.indexOf('catch')), /finalizeOnboarding|onComplete/)
})
