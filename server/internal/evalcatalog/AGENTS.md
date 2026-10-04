# Evaluation catalog

Read `docs/evals/CONTRIBUTING.md` before modifying definitions.

- Edit `p0-golden.json` and `office-scenarios.json`; they drive the single-page view.
- Preserve scenario/case IDs. A fixed incident becomes a retained regression case.
- Every case needs roles, an observable verification target, a verification method
  and repository source references. Existing definitions never imply executed pass.
- Keep P0-to-scenario/case references valid and exact. Do not claim full coverage
  of a scenario when only selected cases are referenced.
- Run `make eval-check` before submitting. No live DWS/model call belongs in this check.
- Keep the UI limited to golden sets and expandable scenarios. Do not add raw
  Markdown readers, runtime state, action buttons or implementation metadata.
