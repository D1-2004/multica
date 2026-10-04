package handler

// Outlook has no public user-delegated Microsoft Graph MCP server. The
// catalog entry still carries an https URL on an allowed host so the
// connector row matches every other official app, but discovery and relay
// calls never request it. These helpers call Graph with the account token
// and return the same JSON-RPC results the relay already understands.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

const outlookCatalogSlug = "outlook"

// outlookGraphOrigin is the Microsoft Graph origin. Tests point it at an
// httptest server. The catalog MCP URL is never requested.
var outlookGraphOrigin = "https://graph.microsoft.com"

const (
	outlookToolTextLimit = 4000
	outlookListDefault   = 5
	outlookListMax       = 10
)

type outlookToolSpec struct {
	Name        string
	Description string
	HasTop      bool
}

func outlookToolSpecs() []outlookToolSpec {
	return []outlookToolSpec{
		{Name: "whoami", Description: "Read the signed-in Outlook account: display name, mail and user principal name.", HasTop: false},
		{Name: "list_messages", Description: "List recent messages in the signed-in mailbox. Read only.", HasTop: true},
		{Name: "list_events", Description: "List events on the signed-in calendar. Read only.", HasTop: true},
		{Name: "list_contacts", Description: "List contacts of the signed-in account. Read only.", HasTop: true},
	}
}

func outlookToolByName(name string) (outlookToolSpec, bool) {
	for _, spec := range outlookToolSpecs() {
		if spec.Name == name {
			return spec, true
		}
	}
	return outlookToolSpec{}, false
}

// outlookDiscoveredTools is the static tool list stored for an Outlook
// connector. Every tool is read-only, so it is pinned without write_enabled.
func outlookDiscoveredTools() []discoveredConnectorTool {
	specs := outlookToolSpecs()
	out := make([]discoveredConnectorTool, 0, len(specs))
	for _, spec := range specs {
		out = append(out, discoveredConnectorTool{Name: spec.Name, ReadOnly: true})
	}
	return out
}

func outlookToolsListJSON() json.RawMessage {
	tools := make([]map[string]any, 0, len(outlookToolSpecs()))
	for _, spec := range outlookToolSpecs() {
		properties := map[string]any{}
		if spec.HasTop {
			properties["top"] = map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     outlookListMax,
				"description": "How many items to return, from 1 to 10. Defaults to 5.",
			}
		}
		tools = append(tools, map[string]any{
			"name":        spec.Name,
			"description": spec.Description,
			"inputSchema": map[string]any{"type": "object", "properties": properties},
			"annotations": map[string]any{"readOnlyHint": true},
		})
	}
	raw, err := json.Marshal(map[string]any{"tools": tools})
	if err != nil {
		return json.RawMessage(`{"tools":[]}`)
	}
	return raw
}

// callOutlookGraph serves one Outlook JSON-RPC call. tools/list needs no
// token. tools/call retries once after Graph answers 401.
func callOutlookGraph(ctx context.Context, client *remotemcp.ExternalClient, method string, params map[string]any, refresh func(rejected string) (string, error)) (json.RawMessage, error) {
	if method == "tools/list" {
		return outlookToolsListJSON(), nil
	}
	if method != "tools/call" {
		return nil, errors.New("unsupported outlook method")
	}
	if client == nil {
		return nil, errors.New("outlook client is not configured")
	}
	if refresh == nil {
		return nil, errors.New("outlook token is unavailable")
	}
	token, err := refresh("")
	if err != nil {
		return nil, err
	}
	raw, status, callErr := outlookToolResult(ctx, client, token, params)
	if status == http.StatusUnauthorized {
		next, refreshErr := refresh(token)
		if refreshErr != nil {
			return nil, refreshErr
		}
		raw, status, callErr = outlookToolResult(ctx, client, next, params)
		if status == http.StatusUnauthorized {
			return outlookToolError("Outlook rejected the account token; reconnect Outlook"), nil
		}
	}
	if callErr != nil {
		return nil, callErr
	}
	return raw, nil
}

func (h *Handler) callOutlookConnector(ctx context.Context, c *internalConnector, method string, params map[string]any) (json.RawMessage, error) {
	app, ok := catalogApp(c.CatalogSlug)
	if !ok {
		return nil, errCatalogAppUnknown
	}
	raw, err := callOutlookGraph(ctx, catalogExternalClient(app), method, params, func(rejected string) (string, error) {
		return h.freshConnectorToken(ctx, c, rejected)
	})
	if err != nil {
		return nil, catalogUpstreamError(err)
	}
	return raw, nil
}

// outlookToolResult calls one Graph tool. A 401 is returned so the caller
// can refresh the token. Other failures become a tool error that does not
// include the token or the provider body.
func outlookToolResult(ctx context.Context, client *remotemcp.ExternalClient, token string, params map[string]any) (json.RawMessage, int, error) {
	name, _ := params["name"].(string)
	spec, ok := outlookToolByName(strings.TrimSpace(name))
	if !ok {
		return outlookToolError("unknown Outlook tool"), 0, nil
	}
	top := outlookTop(outlookArguments(params))
	rawURL, err := outlookGraphRequestURL(outlookGraphOrigin, spec.Name, top)
	if err != nil {
		return outlookToolError("Outlook request failed"), 0, nil
	}
	var body json.RawMessage
	if err := client.GetJSON(ctx, rawURL, bearerHeader(token), &body); err != nil {
		var oauthErr *remotemcp.OAuthError
		if errors.As(err, &oauthErr) && oauthErr.StatusCode == http.StatusUnauthorized {
			return nil, http.StatusUnauthorized, err
		}
		return outlookToolError("Outlook request failed"), 0, nil
	}
	text, ok := outlookProject(spec.Name, body, top)
	if !ok {
		return outlookToolError("Outlook returned an unexpected response"), 0, nil
	}
	return outlookToolText(text), 0, nil
}

func outlookArguments(params map[string]any) map[string]any {
	if params == nil {
		return nil
	}
	switch raw := params["arguments"].(type) {
	case map[string]any:
		return raw
	case json.RawMessage:
		var args map[string]any
		if len(raw) == 0 || json.Unmarshal(raw, &args) != nil {
			return nil
		}
		return args
	default:
		return nil
	}
}

func outlookTop(args map[string]any) int {
	if args == nil {
		return outlookListDefault
	}
	raw, ok := args["top"]
	if !ok || raw == nil {
		return outlookListDefault
	}
	var n int
	switch value := raw.(type) {
	case float64:
		n = int(value)
	case int:
		n = value
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return outlookListDefault
		}
		n = int(parsed)
	default:
		return outlookListDefault
	}
	if n < 1 {
		return outlookListDefault
	}
	if n > outlookListMax {
		return outlookListMax
	}
	return n
}

func outlookGraphRequestURL(origin, tool string, top int) (string, error) {
	base, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil {
		return "", errors.New("outlook graph origin is not https")
	}
	query := url.Values{}
	switch tool {
	case "whoami":
		base.Path = "/v1.0/me"
		query.Set("$select", "id,displayName,mail,userPrincipalName")
	case "list_messages":
		base.Path = "/v1.0/me/messages"
		query.Set("$top", strconv.Itoa(top))
		query.Set("$select", "subject,from,receivedDateTime,isRead")
	case "list_events":
		base.Path = "/v1.0/me/events"
		query.Set("$top", strconv.Itoa(top))
		query.Set("$select", "subject,start,end,location,isAllDay")
	case "list_contacts":
		base.Path = "/v1.0/me/contacts"
		query.Set("$top", strconv.Itoa(top))
		query.Set("$select", "displayName,emailAddresses,mobilePhone")
	default:
		return "", errors.New("unknown outlook tool")
	}
	base.RawQuery = query.Encode()
	base.Fragment = ""
	return base.String(), nil
}

func outlookProject(tool string, body json.RawMessage, top int) (string, bool) {
	switch tool {
	case "whoami":
		var me struct {
			ID                string `json:"id"`
			DisplayName       string `json:"displayName"`
			Mail              string `json:"mail"`
			UserPrincipalName string `json:"userPrincipalName"`
		}
		if json.Unmarshal(body, &me) != nil {
			return "", false
		}
		raw, err := json.Marshal(me)
		return string(raw), err == nil
	case "list_messages":
		var page struct {
			Value []struct {
				Subject          string `json:"subject"`
				ReceivedDateTime string `json:"receivedDateTime"`
				IsRead           bool   `json:"isRead"`
				From             struct {
					EmailAddress struct {
						Name    string `json:"name"`
						Address string `json:"address"`
					} `json:"emailAddress"`
				} `json:"from"`
			} `json:"value"`
		}
		if json.Unmarshal(body, &page) != nil || page.Value == nil {
			return "", false
		}
		if len(page.Value) > top {
			page.Value = page.Value[:top]
		}
		raw, err := json.Marshal(page.Value)
		return string(raw), err == nil
	case "list_events":
		var page struct {
			Value []struct {
				Subject  string `json:"subject"`
				IsAllDay bool   `json:"isAllDay"`
				Start    struct {
					DateTime string `json:"dateTime"`
					TimeZone string `json:"timeZone"`
				} `json:"start"`
				End struct {
					DateTime string `json:"dateTime"`
					TimeZone string `json:"timeZone"`
				} `json:"end"`
				Location struct {
					DisplayName string `json:"displayName"`
				} `json:"location"`
			} `json:"value"`
		}
		if json.Unmarshal(body, &page) != nil || page.Value == nil {
			return "", false
		}
		if len(page.Value) > top {
			page.Value = page.Value[:top]
		}
		raw, err := json.Marshal(page.Value)
		return string(raw), err == nil
	case "list_contacts":
		var page struct {
			Value []struct {
				DisplayName    string `json:"displayName"`
				MobilePhone    string `json:"mobilePhone"`
				EmailAddresses []struct {
					Name    string `json:"name"`
					Address string `json:"address"`
				} `json:"emailAddresses"`
			} `json:"value"`
		}
		if json.Unmarshal(body, &page) != nil || page.Value == nil {
			return "", false
		}
		if len(page.Value) > top {
			page.Value = page.Value[:top]
		}
		raw, err := json.Marshal(page.Value)
		return string(raw), err == nil
	default:
		return "", false
	}
}

func outlookToolText(text string) json.RawMessage {
	raw, err := json.Marshal(multicaMCPToolResult{Content: []multicaMCPContent{{Type: "text", Text: outlookTruncate(text, outlookToolTextLimit)}}})
	if err != nil {
		return outlookToolError("Outlook request failed")
	}
	return raw
}

func outlookToolError(message string) json.RawMessage {
	raw, err := json.Marshal(multicaMCPToolResult{IsError: true, Content: []multicaMCPContent{{Type: "text", Text: message}}})
	if err != nil {
		return json.RawMessage(`{"content":[{"type":"text","text":"Outlook request failed"}],"isError":true}`)
	}
	return raw
}

func outlookTruncate(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max])
}
