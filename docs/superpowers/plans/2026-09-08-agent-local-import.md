# Local and Git Agent package creation

> **For agentic workers:** Execute this plan in this task. Verify focused behavior before proceeding.

**Goal:** Create an Agent from a local ZIP or Git repository through the same validated bundle and transactional creation flow, and ship a complete uploadable example.

**Architecture:** Acquisition produces an immutable `agentsource.Bundle`. Both sources use schema validation, persisted actor-bound previews, runtime selection, configuration resolution, exclusive workspace skills and one create transaction. Git retains commit provenance and permission rechecks; local imports retain package provenance. External identities and credentials require explicit destination choices and never migrate by copying platform IDs.

**Tech stack:** Go, PostgreSQL/sqlc, React, TypeScript, Zod, Vitest.

## Work items

- [x] Add regression tests for ZIP/Git bundle equivalence and full v2 configuration creation.
- [x] Preserve the complete validated manifest in Bundle; support v2 compilation from either source.
- [x] Persist local previews and receipts; converge confirmations on the existing transactional materializer.
- [x] Apply portable configuration, OKRs and A2A policy; handle resource and secret references explicitly.
- [x] Add local import route, upload/preview/runtime/confirmation UI and shared API contracts.
- [x] Update Builder instructions and creation skill to explain schema, package layout and confirmation boundaries.
- [x] Author a complete test Agent with enabled/disabled skills, supporting scripts and reference files.
- [ ] Run isolated database/API tests, frontend checks and upload verification; report Builder's actual skill set and chain.

## Validation

Use the isolated `multica_agent_source_915f` database only. Cover malformed archives/manifests, repeated and foreign confirmations, configuration equivalence and skill exclusivity. Do not execute scripts from uploaded packages. Do not format code automatically. Append protocol history to maintenance documentation.

## Local verification

- Isolated PostgreSQL handler tests passed: ZIP/Git creation parity, explicit secrets/bindings, idempotency, exclusive skills, complete example upload/export, v2 publication/stale preview, stable A2A keys/manual client preservation, source and export regression checks.
- Web typecheck, core API/path tests (19), views creation/publication/i18n tests (18), schema tests (17), builtin skill conformance, backend build, npm lint and git diff --check passed. Lint retained existing warnings.
- sqlc v1.31.1 generated the new query code using the isolated database schema. Whole-repository generation is blocked by the pre-existing migration 271 referencing task_completion_outbox before that relation is created; unrelated generated models were not overwritten.
- Browser upload acceptance follows the preprod deployment and is tracked separately from these local checks.
