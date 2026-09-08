package service

import "github.com/multica-ai/multica/server/pkg/protocol"

const dwsAgentSkillDescription = "Use DWS CLI for DingTalk Workspace tasks. Send messages with current-user identity by default using dws chat message send; use dws chat message send-by-bot only when the user explicitly requests bot identity."

const dwsAgentSkillPrefix = `---
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
`

const dwsAgentLegacyMessagePolicy = `- Current-user sends must include ` + "`--ai-tag=false`" + ` so the message has no AI badge. You are sending as a colleague.
- After a real reply exists in the conversation, remove processing/complete emotions from the inbound message that triggered this run (` + "`dws chat +messages-remove-text-emotion`" + ` / ` + "`dws chat +messages-remove-emoji`" + ` for 处理中, 已完成, 思考中, 🤔思考中). Never add 已完成 as a stamp.
`

const dwsAgentManagedMessagePolicy = `- The Multica DWS wrapper applies this employee's AI sender label setting before each current-user send. Use the normal send or reply command without choosing ` + "`--ai-tag`" + ` yourself. Do not bypass the wrapper or invoke another DWS executable to change the label.
- When the task uses platform-managed message responses, Multica and the Router own processing reactions and their cleanup. Do not add, replace, or remove those platform reactions. Business reactions explicitly requested by the user remain ordinary DWS actions. For other tasks, follow the inbound task instruction for processing-reaction handling.
- A send task ID means accepted, not delivered. Keep the original idempotency key and query the existing send status after an uncertain result instead of issuing another send. Report delivery only when DWS confirms it.
`

const dwsAgentSkillSuffix = `- Only use bot identity when the user explicitly asks to send as a bot or robot; then use ` + "`dws chat message send-by-bot`" + `.
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

const dwsAgentSkillContent = dwsAgentSkillPrefix + dwsAgentLegacyMessagePolicy + dwsAgentSkillSuffix
const dwsAgentPolicySkillContent = dwsAgentSkillPrefix + dwsAgentManagedMessagePolicy + dwsAgentSkillSuffix

// DWSAgentSkill returns the runtime-specific DWS instructions injected into
// every FC/E2B Hermes agent whose runtime exposes the DWS capability.
func DWSAgentSkill() AgentSkillData {
	return DWSAgentSkillForPolicy(nil)
}

// DWSAgentSkillForPolicy has only legacy and policy-aware content variants.
// Per-task values stay on the claim wire and never churn the skill bundle hash.
func DWSAgentSkillForPolicy(policy *protocol.DingTalkMessagePolicy) AgentSkillData {
	content := dwsAgentSkillContent
	if policy != nil {
		content = dwsAgentPolicySkillContent
	}
	return AgentSkillData{
		Name:        "multica-dws",
		Description: dwsAgentSkillDescription,
		Content:     content,
	}
}
