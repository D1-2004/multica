package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/pkg/dshtrajectory"
)

// Upload uses only the task token, never the shared daemon identity. Redirects
// cannot move this private artifact or its authentication to another endpoint.
func (c *Client) uploadDSHTrajectory(ctx context.Context, taskID, workspaceID, token string, data []byte) error {
	if !strings.HasPrefix(token, "mat_") {
		return fmt.Errorf("native trajectory requires a task token")
	}
	doc, err := dshtrajectory.Parse(data)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/api/tasks/"+taskID+"/dsh-trajectory", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid native trajectory upload request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Workspace-ID", workspaceID)
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("X-DSH-Session-ID", doc.Scope.SessionID)
	req.Header.Set("X-Content-SHA256", fmt.Sprintf("%x", sha256.Sum256(data)))
	client := *c.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("native trajectory upload transport failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("native trajectory upload returned HTTP %d", resp.StatusCode)
	}
	return nil
}
