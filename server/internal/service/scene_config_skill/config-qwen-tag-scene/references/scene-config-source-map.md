# config-qwen-tag-scene source map

| Fact | Source |
| --- | --- |
| The MCP server is mounted only for a task with a current group or 1:1 scene, at `/api/scene-config/mcp/{sct_ token}` | `server/internal/handler/scene_config_mcp.go` (`sceneConfigMCPRoute`), `server/internal/handler/runner_mcp.go` (`injectRunnerMCP`) |
| The scene token binds task, agent, workspace and scene; every call also needs the same task's task token and re-resolves the scene | `server/internal/auth/scene_token.go`, `server/internal/handler/scene_config_mcp.go` (`sceneConfigMCPTarget`) |
| Tools take no scene argument | `server/internal/handler/scene_config_mcp.go` (tool definitions) |
| Routine runs are read-only | `server/internal/handler/scene_config_mcp.go` (`routine_run_read_only`), `server/internal/service/scene_routine.go` (`IsSceneRoutineContext`) |
| Routines: dedupe, pause kept, 15-minute floor, Asia/Shanghai default, start/end notices | `server/internal/handler/scene_routines.go` |
| Remote MCP servers only; reserved names refused | `server/internal/contextcap/mcp_config.go` (`NormalizeRemoteMCPConfig`), `server/internal/handler/context_capabilities_task.go` (`reservedMCPServerName`) |
| Groups and 1:1 chats add, re-point, switch and delete remote servers; omitted fields keep stored values; URLs shown masked; notices name who asked and the address | `server/internal/handler/scene_config_mcp.go` (`sceneConfigMCPUpsert`, `maskMCPServerURL`, `sceneConfigNoticeText`) |
| Routine runs issue no configuration link | `server/internal/handler/scene_config_mcp.go` (`sceneConfigWriteTools`), `server/internal/handler/multica_mcp_context_config.go` (`createContextConfigLink`) |
| Chat runs keep the 15-minute minimum (`routine_run_too_soon`) | `server/internal/handler/scene_config_mcp.go` (`sceneConfigRoutineRun`) |
| A routine never carries a personal layer | `server/internal/contextcap/scope.go` (`routineScope`), `server/internal/handler/scene_routines.go` (`RoutineRuntimeContext`) |
| Only offered skills and connectors can be switched | `server/internal/contextcap/store.go` (`UpsertBinding`) |
| Account connection goes through a configuration link, replied as a Markdown link to its `dingtalk_url` | `server/internal/handler/context_config_link_mint.go`, `server/internal/service/inboundcoord/config_link.go` (`ConfigLinkDeepLink`), `server/internal/handler/scene_config_mcp.go` (`scene_connect_link` description) |
| The skill is injected at claim and resolved by the same check | `server/internal/handler/daemon.go` (claim and `ResolveTaskSkillBundles`), `server/internal/service/scene_config_skill.go` |
