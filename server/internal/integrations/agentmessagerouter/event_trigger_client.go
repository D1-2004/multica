package agentmessagerouter

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

func (c *Client) UpdateEventTrigger(ctx context.Context, sourceID, agentID string, enabled bool, revision int64) error {
	body, err := json.Marshal(map[string]any{"agentId": agentID, "enabled": enabled, "revision": revision})
	if err != nil {
		return err
	}
	pinned, err := c.responsePolicyClient()
	if err != nil {
		return err
	}
	resp, err := pinned.do(ctx, http.MethodPatch, "/api/subscriptions/"+url.PathEscape(sourceID)+"/event-trigger", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeRouterHTTPError(resp.Body, resp.StatusCode)
	}
	result, err := decodeRouterResponse[struct {
		Enabled  bool  `json:"enabled"`
		Revision int64 `json:"revision"`
	}](resp.Body)
	if err != nil {
		return err
	}
	if result.Enabled != enabled || result.Revision != revision {
		return ErrRouterInvalidResponse
	}
	return nil
}
