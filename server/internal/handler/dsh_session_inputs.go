package handler

import (
	"context"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

// The existing profile reconciler restores background inputs after an app
// restart. It never creates or replaces a Host and requires no browser cookie.
func (h *Handler) refreshDSHSessionInputs(ctx context.Context, key dshhost.Key) error {
	if h.DB == nil || h.FCE2BLauncher == nil {
		return nil
	}
	rows, err := h.DB.Query(ctx, `SELECT scope_id,sandbox_id,generation FROM employee_filesystem_host
 WHERE workspace_id=$1 AND agent_id=$2 AND state='running' ORDER BY scope_id`, key.WorkspaceID, key.AgentID)
	if err != nil {
		return err
	}
	var hosts []dshhost.Host
	for rows.Next() {
		host := dshhost.Host{Key: key, State: "running"}
		if err := rows.Scan(&host.ScopeID, &host.SandboxID, &host.Generation); err != nil {
			rows.Close()
			return err
		}
		hosts = append(hosts, host)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, host := range hosts {
		if err := h.FCE2BLauncher.EnsureDSHSessionInputs(ctx, host, h.dshNativeAccessManager(), h.submitDSHNativePrompt); err != nil {
			return err
		}
	}
	return nil
}
