# Evaluation catalog

Read `docs/evals/CONTRIBUTING.md` before modifying definitions.

- Edit `spec.json`, `p0-golden.json` and `office-scenarios.json`; they drive
  QwenTag SPEC & EVALS. Categories organize display, never change case ownership.
- Preserve scenario/case IDs. A fixed incident becomes a retained regression case.
- Every case needs roles, an observable verification target, a verification method
  and repository source references. Existing definitions never imply executed pass.
- Keep P0-to-scenario/case references valid and exact. Do not claim full coverage
  of a scenario when only selected cases are referenced.
- Run `make eval-check` before submitting. No live DWS/model call belongs in this check.
- SPEC states employee requirements and links them to scenario IDs; it never
  claims execution or acceptance. Keep sources and stable IDs in repository data.
- Keep the UI limited to SPEC, golden sets and expandable scenarios. Do not add raw
  Markdown readers, runtime state, action buttons or implementation metadata.
