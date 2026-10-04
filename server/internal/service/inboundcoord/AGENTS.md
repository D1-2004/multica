# Coordinator changes

Read the root `CLAUDE.md` and `docs/development-delivery.md`, then `docs/inbound-coordinator-loop.md` before changing this package. Define the relevant E2E acceptance, environment and delivery boundary in the current task/Plan. The current behavior contract also applies to Coordinator-related handler, assoc, scenememory, dispatch, callback and trace changes.

Policy text lives only in `policy/*.md`; register its version, obligations, selection conditions and effect dependencies in `policy/registry.json`. Preserve source provenance in `docs/plans/2026-09-07-coordinator-progressive-context-inventory.json`. Do not append an unregistered monolithic system prompt or restore superseded incident workarounds from an old Plan.

For each behavior change, preserve its historical obligation and add a minimal contrast case. Update the relevant tool schema, error hints, context projection, Host checks, current contract and evidence status together. Required semantic rules must be visible before a side-effecting plan can commit. Unknown context is not empty and phrase matching is not authorization.

Run `python3 scripts/check-coordinator-policy.py` from the repository root and the affected package checks. Report structural checks, scripted Host checks, model replay and real delivery evidence separately. A source mapping or a passing string check is not behavioral certification. See the current contract for the full maintenance procedure.
