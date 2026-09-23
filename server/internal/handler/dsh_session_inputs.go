package handler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/service"
)

// Restore inputs independently of slow plugin builds and Host creation. Each
// application replica keeps a transport so a rolling restart does not rely on
// the replica which last won the profile reconciliation claim.
func (h *Handler) RunDSHSessionInputWorker(ctx context.Context) {
	if h.DB == nil || h.FCE2BLauncher == nil {
		return
	}
	// Wait after completion; an overdue ticker must not trigger a new scan immediately.
	next := 0
	for {
		next = h.refreshRunningDSHSessionInputs(ctx, next)
		timer := time.NewTimer(15 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
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
	// Renew every live transport even when only a bounded subset needs CLI probes.
	due := make([]bool, len(hosts))
	for i, host := range hosts {
		due[i] = h.FCE2BLauncher.DSHSessionInputsNeedProbe(host)
	}
	var group sync.WaitGroup
	permits := make(chan struct{}, 2)
	scheduled := 0
	nextIndex := 0
	for i := 0; i < len(hosts); i++ {
		index := (next + i) % len(hosts)
		host := hosts[index]
		if scheduled >= 8 {
			nextIndex = index
			break
		}
		if !due[index] {
			continue
		}
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
		scheduled++
		group.Add(1)
		go func() {
			defer group.Done()
			defer func() { <-permits }()
			// No Host creation/restart or browser grant is involved.
			if err := h.FCE2BLauncher.EnsureDSHSessionInputs(ctx, host, h.dshNativeAccessManager(), h.submitDSHNativePrompt); err != nil && ctx.Err() == nil && !errors.Is(err, service.ErrDSHInputProbeDeferred) {
				slog.Warn("DSH background session input connection is not ready", "agent_id", host.AgentID, "workspace_id", host.WorkspaceID, "generation", host.Generation, "scope_id", host.ScopeID, "sandbox_id", host.SandboxID, "error_code", service.DSHSessionInputFailureCode(err))
			}
		}()
	}
	group.Wait()
	return nextIndex
}
