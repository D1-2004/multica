package service

const dwsAgentSkillDescription = "Use DWS CLI for DingTalk Workspace tasks. Send messages with current-user identity by default using dws chat message send; use dws chat message send-by-bot only when the user explicitly requests bot identity."

const dwsAgentSkillContent = `---
name: multica-dws
description: Use DWS CLI for DingTalk Workspace tasks. Send messages with current-user identity by default using dws chat message send; use dws chat message send-by-bot only when the user explicitly requests bot identity.
allowed-tools: Bash(dws *)
---

# DingTalk Workspace CLI

Use the ` + "`dws`" + ` CLI for DingTalk Workspace data and actions. When the Agent has a DingTalk account identity bound, Multica injects that identity into the current chat sandbox before the task starts.

Before any DWS operation, check the command shape with ` + "`dws <path> --help`" + ` if the path or flags are uncertain. Every data command must include ` + "`--format json`" + `.

## Message sender identity

Treat the sender identity as a user-visible product choice for every request to send, forward, reply to, or share a DingTalk message:

- Default to the current-user identity and use ` + "`dws chat message send`" + `. This default applies to both group chats and direct messages.
- Only use bot identity when the user explicitly asks to send as a bot or robot; then use ` + "`dws chat message send-by-bot`" + `.
- A group-chat target, an existing robot in the conversation, or the availability of a robot code does not imply bot identity. Do not search for or choose a robot unless bot identity is required by the user's request.
- Do not switch to bot identity because current-user sending fails, is denied, or appears less convenient. Report the exact current-user error instead.
- If the requested capability is unavailable through current-user sending but is available through a bot, explain that the visible sender would change and obtain explicit confirmation before sending.

Message sending is an external side effect. Never send a real message as a test or probe; confirm the target and content before executing the chosen send command.

For current-user smoke checks, use:

` + "```bash" + `
dws auth status --format json
dws contact user get-self --format json
` + "```" + `

If DWS authentication is missing, tell the user to bind a DingTalk account in this Agent's integrations. Do not start an interactive login and do not look for or import a historical DWS profile. If authentication is expired, or a command returns ` + "`unknown command`" + ` / ` + "`unknown flag`" + `, report the exact situation and the relevant error output. Do not claim success without a successful DWS command result.
`

// DWSAgentSkill returns the runtime-specific DWS instructions injected into
// every FC/E2B Hermes agent whose runtime exposes the DWS capability.
func DWSAgentSkill() AgentSkillData {
	return AgentSkillData{
		Name:        "multica-dws",
		Description: dwsAgentSkillDescription,
		Content:     dwsAgentSkillContent,
	}
}
