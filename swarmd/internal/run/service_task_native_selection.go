package run

import (
	"errors"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Bind only native selections to native Designer sources. The message reference
// was authenticated on admission; PrepareTurn rechecks owner/head/projection and
// target membership before granting any authoring capability.
func bindTaskNativeArtifactSelection(parsed *taskCallArguments, launches []taskLaunchSpec, selection *pebblestore.SessionArtifactSelectionReference) error {
	// Fully declared programs use their own dependency graph, not ambient chat
	// selections. Internal cohorts retain program_job_id after Program is cleared.
	if parsed.Program != nil {
		return nil
	}
	for _, launch := range launches {
		if mapString(launch.SourceArguments, "program_job_id") != "" {
			return nil
		}
	}
	if selection == nil || selection.ArtifactID == "" {
		return nil
	}
	managed := false
	for _, launch := range launches {
		managed = managed || (agentruntime.IsDesignerAgentName(launch.RequestedSubagentType) && launch.OutputMode == taskOutputModeManaged)
	}
	if !managed {
		return nil
	}
	if selection.SessionID == "" || selection.CommitOID == "" || selection.ProjectionSeq == 0 || selection.RevisionRef != "revision-"+selection.CommitOID {
		return errors.New("task native Artifact Studio selection is missing authenticated source context")
	}
	if parsed.SourceArtifact != nil || parsed.ArtifactV2Source != nil {
		return errors.New("task native Artifact Studio selection cannot use a legacy source")
	}
	// Admission leaves TargetPartIDs nil for an authenticated whole-artifact
	// selection. It is an optional intent boundary, not authentication evidence.
	var targetPartIDs []string
	if selection.TargetPartIDs != nil {
		targetPartIDs = append([]string(nil), (*selection.TargetPartIDs)...)
	}
	source := &taskArtifactV3Source{SessionID: selection.SessionID, ArtifactID: selection.ArtifactID, CommitOID: selection.CommitOID, ProjectionSeq: selection.ProjectionSeq, TargetPartIDs: targetPartIDs}
	matches := func(requested *taskArtifactV3Source) bool {
		return requested == nil || (requested.SessionID == source.SessionID && requested.ArtifactID == source.ArtifactID && requested.CommitOID == source.CommitOID && requested.ProjectionSeq == source.ProjectionSeq && (len(requested.TargetPartIDs) == 0 || equalStringSet(requested.TargetPartIDs, source.TargetPartIDs)))
	}
	if !matches(parsed.ArtifactV3Source) {
		return errors.New("task artifact_v3_source does not match the authenticated Artifact Studio selection")
	}
	// Preflight the entire wave before mutating any launch, including regular
	// launch hints that could otherwise expand a selected Part set.
	for _, launch := range launches {
		if !agentruntime.IsDesignerAgentName(launch.RequestedSubagentType) || launch.OutputMode != taskOutputModeManaged {
			continue
		}
		if launch.SourceArtifact != nil || launch.ArtifactV2Source != nil || !matches(launch.ArtifactV3Source) {
			return errors.New("task launch source does not match the authenticated native Artifact Studio selection")
		}
		targets, err := artifactV3TargetPartIDs(launch)
		if err != nil {
			return err
		}
		if len(targets) != 0 && !equalStringSet(targets, source.TargetPartIDs) {
			return errors.New("task target Parts do not match the authenticated native Artifact Studio selection")
		}
	}
	parsed.ArtifactV3Source = cloneTaskArtifactV3Source(source)
	if parsed.Swarm != nil {
		parsed.Swarm.ArtifactV3Source = cloneTaskArtifactV3Source(source)
	}
	for i := range launches {
		if agentruntime.IsDesignerAgentName(launches[i].RequestedSubagentType) && launches[i].OutputMode == taskOutputModeManaged {
			launches[i].ArtifactV3Source = cloneTaskArtifactV3Source(source)
			if launches[i].SourceArguments == nil {
				launches[i].SourceArguments = map[string]any{}
			}
			launches[i].SourceArguments["artifact_v3_source"] = cloneTaskArtifactV3Source(source)
		}
	}
	return nil
}
