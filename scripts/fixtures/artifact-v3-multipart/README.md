# Artifact V3 multipart fixture

This browser artifact is deliberately coupled across named parts:

- `hero` contains the `Choose Team` trigger.
- `pricing` renders data from `src/plans.js` through shared logic in `src/app.js`.
- both use `--accent`, `--accent-contrast`, and card rules from `styles/theme.css`.

The follow-up request used by `artifact-v3-multipart-e2e.mjs` targets `pricing`, but requires a project-wide correction: rename the `team` plan to `studio`, update its visible copy and price, keep the Hero CTA selecting it, and change the shared accent while preserving readable contrast. A correct turn must therefore edit `src/plans.js`, `src/app.js` or the Hero trigger in `index.html`, and `styles/theme.css`; no isolated "pricing part bytes" replacement can satisfy the interaction and pixel checks.

The runner also generates an independent 96-part project with the same `swarm.artifact/v3` manifest shape. The fixture is generated under the run-provided `TMPDIR` and is not persisted here.
