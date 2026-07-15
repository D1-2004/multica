package service

const dwsAgentSkillContent = `---
name: multica-dws
description: Use DWS CLI for DingTalk Workspace tasks; smoke check with dws auth status and dws contact user get-self using --format json.
allowed-tools: Bash(dws *)
---

# DingTalk Workspace CLI

Use the ` + "`dws`" + ` CLI for DingTalk Workspace data and actions. When the Agent has a DingTalk account identity bound, Multica injects that identity into the current chat sandbox before the task starts.

Before any DWS operation, check the command shape with ` + "`dws <path> --help`" + ` if the path or flags are uncertain. Every data command must include ` + "`--format json`" + `.

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
		Description: "Use DWS CLI for DingTalk Workspace tasks; smoke check with dws auth status and dws contact user get-self using --format json.",
		Content:     dwsAgentSkillContent,
	}
}
