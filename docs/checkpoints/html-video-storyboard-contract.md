# HTML storyboard → Video Studio contract

`swarm.storyboard/v1` is the canonical pre-production handoff for a self-contained HTML storyboard. It supplements, rather than replaces, `swarm.capture/v1`.

## Author

Embed exactly one `#swarm-storyboard-manifest` JSON script in the same HTML as the capture manifest/runtime. Each storyboard section uses a stable `id`, references one exact capture state with `capture_state_id`, and declares `title`, `duration_ms`, `creative_direction`, non-empty `filming_requirements`, and `production_state` (`pending` or `ready`). Optional narration and on-screen text stay descriptive until represented by typed timeline objects.

```json
{
  "version": "swarm.storyboard/v1",
  "sections": [
    {
      "id": "intro",
      "capture_state_id": "opening",
      "title": "Opening",
      "duration_ms": 2500,
      "creative_direction": "Locked wide shot, slow push",
      "filming_requirements": ["Record presenter in landscape", "Hold final pose for two seconds"],
      "production_state": "pending"
    }
  ]
}
```

## Export and import

1. Publish the complete HTML as one managed artifact.
2. Call `manage_artifact export_html_stills` with the complete exact source reference. The trusted renderer exports one 1920×1080 PNG per capture state and returns `storyboard_handoff` with exact source and PNG lineage.
3. Create an empty Video Studio base revision with `manage_video create_project`.
4. In the same workflow, call `manage_video import_storyboard` with the exact source reference and every returned `state_id`/`reference` export. Do not reconstruct `plan.parts` manually.
5. The import creates one pending initial proposal. Video Studio displays each exported PNG as the section placeholder, its filming requirements, and its pending/ready state.

## Review and replacement

The user may accept the storyboard structure while pending sections remain clearly marked. Pending sections are not final footage and block final rendering. Later media replaces one stable section at a time with `plan.kind=revision` and the original part `id`. The durable plan retains the storyboard source, capture state, exported still, and filming guide while the replacement becomes production-ready. Unselected parts remain unchanged.

Section-level feedback must carry the exact project/revision/proposal, stable part ID, storyboard source, storyboard still, capture state, production state, filming requirements, and current visible artifact. This remains one VideoProject authority; there is no parallel storyboard timeline or store.
