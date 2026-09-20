package handler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/dshhost"
)

// Restore inputs independently of slow plugin builds and Host creation. Each
// application replica keeps a transport so a rolling restart does not rely on
// the replica which last won the profile reconciliation claim.
func (h *Handler) RunDSHSessionInputWorker(ctx context.Context) {
	if h.DB == nil || h.FCE2BLauncher == nil {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	next := 0
	for {
		next = h.refreshRunningDSHSessionInputs(ctx, next)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) refreshRunningDSHSessionInputs(parent context.Context, next int) int {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	rows, err := h.DB.Query(ctx, `SELECT h.workspace_id,h.agent_id,h.scope_id,h.sandbox_id,h.generation FROM employee_filesystem_host h
 JOIN agent a ON a.workspace_id=h.workspace_id AND a.id=h.agent_id
 JOIN agent_runtime r ON r.id=a.runtime_id AND r.workspace_id=a.workspace_id
 WHERE h.state='running' AND a.archived_at IS NULL AND a.runtime_mode='cloud' AND r.provider='dsh'
 AND CASE WHEN r.metadata->>'kind'='fc-e2b' THEN 'aliyun_fc' ELSE r.metadata->>'sandbox_backend' END='aliyun_fc'
 ORDER BY h.workspace_id,h.agent_id,h.scope_id`)
	if err != nil {
		return next
	}
	var hosts []dshhost.Host
	for rows.Next() {
		host := dshhost.Host{State: "running"}
		if err = rows.Scan(&host.WorkspaceID, &host.AgentID, &host.ScopeID, &host.SandboxID, &host.Generation); err != nil {
			break
		}
		hosts = append(hosts, host)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return next
	}
	var group sync.WaitGroup
	permits := make(chan struct{}, 4)
	for i := 0; i < len(hosts); i++ {
		index := (next + i) % len(hosts)
		host := hosts[index]
		if ctx.Err() != nil {
			group.Wait()
			return index
		}
		select {
		case permits <- struct{}{}:
		case <-ctx.Done():
			group.Wait()
			return index
		}
		group.Add(1)
		go func() {
			defer group.Done()
			defer func() { <-permits }()
			// No Host creation/restart or browser grant is involved.
			if err := h.FCE2BLauncher.EnsureDSHSessionInputs(ctx, host, h.dshNativeAccessManager(), h.submitDSHNativePrompt); err != nil && ctx.Err() == nil {
				slog.Warn("DSH background session input connection is not ready", "agent_id", host.AgentID, "workspace_id", host.WorkspaceID)
			}
		}()
	}
	group.Wait()
	return 0
}
