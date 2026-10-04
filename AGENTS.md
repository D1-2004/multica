# Repository instructions

Shared instructions for contributors and coding agents. Read the root
[CLAUDE.md](CLAUDE.md) for architecture, coding rules and operational invariants.
Do not duplicate those rules in this file.

Before implementation, define the acceptance criteria, relevant end-to-end
scenarios, test environment and bounded delivery workflow in
[docs/development-delivery.md](docs/development-delivery.md). Use the existing
task/Plan to record them; a small change does not need extra planning documents.

Load additional contracts only for the affected work:

| Work | Authority |
| --- | --- |
| Mobile | [apps/mobile/CLAUDE.md](apps/mobile/CLAUDE.md) |
| Employee / Tag | [docs/employee-delivery-workflow.md](docs/employee-delivery-workflow.md) |
| Coordinator policy | [docs/inbound-coordinator-loop.md](docs/inbound-coordinator-loop.md), `server/internal/service/inboundcoord/AGENTS.md` |
| Provider event admission | [docs/event-scene-router.md](docs/event-scene-router.md) |
| Scene identity | [docs/agent-scene.md](docs/agent-scene.md) |
| Aone / Runtime / trace operations | The relevant `.agents/skills/*/SKILL.md` |

Use `Makefile`, `package.json` and `pnpm-workspace.yaml` for commands. Local
memories, developer-specific modes, checkout paths, branches and CLI profiles are
not shared project policy. Resolve targets from the current task and environment.

Report implementation, runtime verification and acceptance separately. At a
delivery boundary, provide verified results, unresolved failures and a concise
handoff; do not silently extend the task or describe untested behavior as passed.
