# Composable Agent Dispatch Implementation Plan

> **For Codex:** Execute this plan task-by-task with tests written before production changes.

**Goal:** Make Agent Dispatch V2 independently select issue/chat persistence, trusted execution identity, prompt construction, and DWS/robot-SDK outbound behavior from the dispatch command.

**Architecture:** Parse and validate the existing wire command into an internal execution plan. `surface` selects only the issue or chat materializer; authenticated endpoint context supplies the Multica authorization principal while DingTalk sender data remains message attribution/context; the prompt builder derives runtime and workflow instructions from source event data plus outbound mode; outbound mode controls whether Multica's channel engine may emit robot-side typing and replies.

**Tech Stack:** Go, net/http, pgx, existing channel engine, existing Dispatch Command V2 DTOs.

**Boundary:** No Dispatch Command wire-format change, no database schema change, and no compatibility fallback. Existing direct DingTalk channel ingress keeps sender-binding behavior; only authenticated Agent Dispatch V2 ingress supplies a trusted identity override.

---

### Task 1: Freeze the composable behavior with tests

**Files:**
- Modify: `server/internal/integrations/channel/engine/router_test.go`
- Modify: `server/internal/handler/agent_dispatch_v2_test.go`

- [x] Add a channel-engine test proving a trusted dispatch identity bypasses sender binding and becomes the session/message/task principal.
- [x] Add a channel-engine test proving DWS-owned outbound suppresses Multica typing and robot replies without suppressing task creation.
- [x] Add prompt/task-field matrix tests proving DWS and robot SDK behavior is independent of issue/chat surface.
- [x] Run the focused tests and confirm they fail for the missing behavior.

### Task 2: Add channel-engine execution options

**Files:**
- Modify: `server/internal/integrations/channel/engine/router.go`

- [x] Add an options-aware `HandleResult` entry point while retaining the current default entry point for direct channel adapters.
- [x] Allow a validated trusted identity override to skip the channel sender-binding resolver.
- [x] Allow the caller to suppress server-owned typing/replies when outbound is owned by DWS.
- [x] Run channel-engine tests and confirm the new behavior passes without changing existing defaults.

### Task 3: Build and apply the Agent Dispatch execution plan

**Files:**
- Create: `server/internal/handler/agent_dispatch_v2_plan.go`
- Create: `server/internal/handler/agent_dispatch_v2_plan_test.go`
- Modify: `server/internal/handler/agent_dispatch_v2_handler.go`
- Modify: `server/internal/handler/agent_dispatch_v2.go`

- [x] Build a plan from validated command fields: surface materializer, authenticated principal, outbound owner, and generated prompt.
- [x] Route chat dispatch through the channel engine with the authenticated endpoint actor as principal and no implicit DingTalk sender binding.
- [x] Keep DingTalk sender/conversation/message data in the inbound message and structured task context for attribution, isolation, and prompt construction.
- [x] Apply generated runtime/workflow instructions for every valid issue/chat and DWS/robot-SDK combination instead of hard-coded pairings.
- [x] Keep issue continuation and chat continuation responses returning the latest materialized target.

### Task 4: Document semantics and verify

**Files:**
- Create: `docs/agent-dispatch-v2-execution-contract.md`

- [x] Document the independent surface, identity, prompt, and outbound responsibilities.
- [x] Append an audit history entry explaining the change and its cause.
- [x] Run focused tests for handler and channel engine.
- [x] Run related Go package tests and a compile/build check; report any environment-only gaps explicitly.
- [x] Run `git diff --check` and inspect the final diff for unrelated changes.
