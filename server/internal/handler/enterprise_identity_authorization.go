package handler

import (
	"net/url"
	"strings"
)

const enterpriseIdentityAuthorizationInstruction = `## BUC Identity Authorization

If an a1 command or an nw-aliwork-cli command reports a BUC login or authorization failure (including IdentityAuthFailed, HTTP 401, BUC SSO ticket expired, not authenticated, not logged in, login required, authorization required, or needs reauthorization), do not keep retrying and never ask the user for a password, ticket, token, or cookie.

The final user-visible response must include the following Chinese text without omitting or shortening the URL:
BUC 身份需要授权或重新授权，请由当前 Agent 所有者或工作区 owner/admin 打开：%s
打开后会自动为当前 Agent 发起授权。如果阿里钉收到“集团账号权限助手”的消息，请完成本次所有“前往授权”，然后重试原请求。

Keep the complete URL visible in both Multica and DingTalk replies.`

func buildEnterpriseIdentityAuthorizationURL(appURL, workspaceSlug, agentID string) string {
	appURL = strings.TrimSpace(appURL)
	workspaceSlug = strings.TrimSpace(workspaceSlug)
	agentID = strings.TrimSpace(agentID)
	if appURL == "" || workspaceSlug == "" || agentID == "" {
		return ""
	}

	base, err := url.Parse(appURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		return ""
	}
	base.RawQuery = ""
	base.Fragment = ""
	joined, err := url.JoinPath(base.String(), workspaceSlug, "agents", agentID)
	if err != nil {
		return ""
	}
	target, err := url.Parse(joined)
	if err != nil {
		return ""
	}
	query := target.Query()
	query.Set("view", "identity")
	query.Set("enterprise_identity", "authorize")
	target.RawQuery = query.Encode()

	loginPath, err := url.JoinPath(base.String(), "login")
	if err != nil {
		return ""
	}
	login, err := url.Parse(loginPath)
	if err != nil {
		return ""
	}
	loginQuery := login.Query()
	loginQuery.Set("next", target.RequestURI())
	login.RawQuery = loginQuery.Encode()
	return login.String()
}

func applyEnterpriseIdentityAuthorizationInstruction(response *AgentTaskResponse, authorizationURL string) {
	if response == nil || strings.TrimSpace(authorizationURL) == "" {
		return
	}
	instruction := strings.Replace(
		enterpriseIdentityAuthorizationInstruction,
		"%s",
		authorizationURL,
		1,
	)
	existing := strings.TrimSpace(response.Instruction)
	if existing == "" {
		response.Instruction = instruction
		return
	}
	response.Instruction = existing + "\n\n" + instruction
}
