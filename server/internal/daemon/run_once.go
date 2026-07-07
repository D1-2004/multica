package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type RunOnceOptions struct {
	RuntimeID   string
	DaemonToken string
	Provider    string
	RuntimeName string
}

func (d *Daemon) RunOnce(ctx context.Context, opts RunOnceOptions) error {
	opts.RuntimeID = strings.TrimSpace(opts.RuntimeID)
	opts.DaemonToken = strings.TrimSpace(opts.DaemonToken)
	opts.Provider = strings.TrimSpace(opts.Provider)
	opts.RuntimeName = strings.TrimSpace(opts.RuntimeName)
	if opts.RuntimeID == "" {
		return fmt.Errorf("runtime id is required")
	}
	if opts.DaemonToken == "" {
		return fmt.Errorf("daemon token is required")
	}
	if opts.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	if opts.RuntimeName == "" {
		opts.RuntimeName = opts.Provider
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.cancelFunc = cancel
	d.rootCtx = runCtx
	d.client.SetToken(opts.DaemonToken)

	healthLn, err := d.listenHealth()
	if err != nil {
		return err
	}
	go d.serveHealth(runCtx, healthLn, time.Now())
	d.ready.Store(true)

	d.seedRunOnceRuntime(opts.RuntimeID, opts.RuntimeName, opts.Provider)
	task, err := d.client.ClaimTask(runCtx, opts.RuntimeID)
	if err != nil {
		return fmt.Errorf("claim task: %w", err)
	}
	if task == nil {
		d.logger.Info("run-once claimed no task", "runtime_id", opts.RuntimeID)
		return nil
	}
	if task.WorkspaceID == "" {
		return fmt.Errorf("claimed task has no workspace_id")
	}

	d.seedRunOnceTaskWorkspace(*task, opts.RuntimeID, opts.RuntimeName, opts.Provider)
	taskTarget := task.IssueID
	if taskTarget == "" && task.ChatSessionID != "" {
		taskTarget = "chat:" + shortID(task.ChatSessionID)
	}
	d.logger.Info("run-once task received", "task", shortID(task.ID), "target", taskTarget)
	d.activeTasks.Add(1)
	defer d.activeTasks.Add(-1)
	d.handleTask(runCtx, *task, 0)
	d.waitBackgroundSyncs()
	return nil
}

func (d *Daemon) seedRunOnceRuntime(runtimeID, runtimeName, provider string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runtimeIndex == nil {
		d.runtimeIndex = make(map[string]Runtime)
	}
	d.runtimeIndex[runtimeID] = Runtime{
		ID:       runtimeID,
		Name:     runtimeName,
		Provider: provider,
		Status:   "online",
	}
}

func (d *Daemon) seedRunOnceTaskWorkspace(task Task, runtimeID, runtimeName, provider string) {
	d.mu.Lock()
	if d.workspaces == nil {
		d.workspaces = make(map[string]*workspaceState)
	}
	ws, ok := d.workspaces[task.WorkspaceID]
	if !ok {
		ws = newWorkspaceState(task.WorkspaceID, []string{runtimeID}, "", task.Repos, nil)
		d.workspaces[task.WorkspaceID] = ws
	} else {
		ws.runtimeIDs = appendMissingRuntimeID(ws.runtimeIDs, runtimeID)
		if ws.allowedRepoURLs == nil {
			ws.allowedRepoURLs = repoAllowlist(task.Repos)
		}
		for _, repo := range task.Repos {
			if repo.URL != "" {
				ws.allowedRepoURLs[repo.URL] = struct{}{}
			}
		}
	}
	if d.runtimeIndex == nil {
		d.runtimeIndex = make(map[string]Runtime)
	}
	d.runtimeIndex[runtimeID] = Runtime{
		ID:       runtimeID,
		Name:     runtimeName,
		Provider: provider,
		Status:   "online",
	}
	d.mu.Unlock()

	d.registerTaskRepos(task.WorkspaceID, task.ID, task.Repos)
}

func appendMissingRuntimeID(ids []string, id string) []string {
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}
