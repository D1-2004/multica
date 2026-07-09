package service

const dwsAgentSkillContent = `---
name: multica-dws
description: Use DWS CLI for DingTalk Workspace tasks; smoke check with dws auth status and dws contact user get-self using --format json.
allowed-tools: Bash(dws *)
---

# DingTalk Workspace CLI

Use the ` + "`dws`" + ` CLI for DingTalk Workspace data and actions. The sandbox may already contain an imported DWS login profile for this agent.

Before any DWS operation, check the command shape with ` + "`dws <path> --help`" + ` if the path or flags are uncertain. Every data command must include ` + "`--format json`" + `.

For current-user smoke checks, use:

` + "```bash" + `
dws auth status --format json
dws contact user get-self --format json
` + "```" + `

If DWS auth is missing, expired, or a command returns ` + "`unknown command`" + ` / ` + "`unknown flag`" + `, report the exact situation and the relevant error output. Do not claim success without a successful DWS command result.
`

// DWSAgentSkill returns the runtime-specific DWS instructions injected into
// FC/E2B Hermes agents that have a DWS profile or select a DWS-capable template.
func DWSAgentSkill() AgentSkillData {
	return AgentSkillData{
		Name:        "multica-dws",
		Description: "Use DWS CLI for DingTalk Workspace tasks; smoke check with dws auth status and dws contact user get-self using --format json.",
		Content:     dwsAgentSkillContent,
	}
}
