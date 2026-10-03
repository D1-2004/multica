package handler

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/employeedirectory"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// EmployeeLoop directory facts (docs/plans 12-memory-design W6, M6): the
// agent's own address book entry (supervisor, department, title) and the
// member roster of its group scenes, read by the Host as the agent's DWS
// identity with zero model calls. Every replica runs the loop; the
// PostgreSQL leases in employeedirectory let one replica refresh a given
// agent or scene, at most daily. Wakes read the stored facts only (two
// indexed lookups) and ask for a background refresh when they are missing
// or stale, so a wake never waits on DingTalk.

const (
	employeeDirectoryTick          = 10 * time.Minute
	employeeDirectoryProfileBudget = 15 * time.Second
	employeeDirectoryRosterBudget  = 30 * time.Second
	employeeDirectoryRosterActive  = 72 * time.Hour
	// employeeDirectoryLazyEvery bounds how often one replica asks for the
	// same key's refresh from wakes (the lease decides whether it runs).
	employeeDirectoryLazyEvery = 10 * time.Minute
	employeeDirectoryLazyMax   = 4
)

// employeeDirectoryRefresher is nil when the directory or the database is
// unavailable.
func (h *Handler) employeeDirectoryRefresher() *employeedirectory.Refresher {
	if h == nil || h.EmployeeDirectory == nil {
		return nil
	}
	database, ok := employeeEntryDB(h)
	if !ok {
		return nil
	}
	var trace *langfuse.Client
	if h.EmployeeSceneWorker != nil {
		trace = h.EmployeeSceneWorker.Langfuse
	}
	return &employeedirectory.Refresher{
		Store:     employeedirectory.NewStore(database),
		Directory: h.EmployeeDirectory,
		Schedule:  employeedirectory.DefaultSchedule,
		// A staffId the roster proved is the same fact the native sender
		// path keeps (dws_open_identity_staff).
		Remember: func(ctx context.Context, orgID, viewerUID, openDingTalkID, staffID string) error {
			return contextcap.RememberOpenIDStaff(ctx, database, orgID, viewerUID, openDingTalkID, staffID)
		},
		Observe: func(rep employeedirectory.Report) { observeEmployeeDirectoryRefresh(trace, rep) },
	}
}

// observeEmployeeDirectoryRefresh logs and traces a refresh, metadata only:
// no names, titles or departments.
func observeEmployeeDirectoryRefresh(trace *langfuse.Client, rep employeedirectory.Report) {
	attrs := []any{"event", "employee_directory_refresh", "kind", rep.Kind, "workspace_id", rep.WorkspaceID, "agent_id", rep.AgentID,
		"scene_id", rep.SceneID, "outcome", rep.Outcome, "error_code", rep.ErrorCode, "elapsed_ms", rep.Elapsed.Milliseconds()}
	if rep.Kind == "roster" {
		attrs = append(attrs, "total", rep.Total, "stored", rep.Stored, "same_org", rep.SameOrg, "other_org", rep.Other,
			"unknown_org", rep.Unknown, "searches", rep.Searches, "facts_read", rep.FactsRead)
	} else {
		attrs = append(attrs, "supervisor", rep.Supervisor, "department", rep.Department, "title", rep.Title)
	}
	if rep.Outcome == employeedirectory.OutcomeFailed {
		slog.Warn("employee directory refresh failed", attrs...)
	} else {
		slog.Info("employee directory refresh", attrs...)
	}
	if trace == nil {
		return
	}
	name := "employee_agent_profile_refresh"
	session := ""
	if rep.Kind == "roster" {
		name, session = "employee_scene_roster_refresh", rep.SceneID
	}
	metadata := map[string]any{"outcome": rep.Outcome, "error_code": rep.ErrorCode, "elapsed_ms": rep.Elapsed.Milliseconds(),
		"workspace_id": rep.WorkspaceID, "agent_id": rep.AgentID, "tenant_org_id": rep.TenantOrgID, "model_calls": 0}
	if rep.Kind == "roster" {
		metadata["scene_id"], metadata["total"], metadata["stored"], metadata["same_org"] = rep.SceneID, rep.Total, rep.Stored, rep.SameOrg
		metadata["other_org"], metadata["unknown_org"], metadata["searches"], metadata["facts_read"] = rep.Other, rep.Unknown, rep.Searches, rep.FactsRead
	} else {
		metadata["supervisor"], metadata["department"], metadata["title"] = rep.Supervisor, rep.Department, rep.Title
	}
	started := time.Now().Add(-rep.Elapsed)
	t := trace.StartTrace(context.Background(), langfuse.TraceOptions{Name: name, SessionID: session, StartTime: started,
		Tags: []string{"employee_memory", "agent-" + rep.AgentID}, Metadata: metadata})
	level := langfuse.LevelDefault
	if rep.Outcome == employeedirectory.OutcomeFailed {
		level = langfuse.LevelWarning
	}
	t.End(langfuse.EndOptions{Level: level, StatusMessage: rep.ErrorCode, Output: map[string]any{"outcome": rep.Outcome}})
}

// RunEmployeeDirectoryRefresh refreshes due agent profiles and the rosters
// of recently active group scenes until ctx ends. It is a no-op without a
// directory.
func (h *Handler) RunEmployeeDirectoryRefresh(ctx context.Context) {
	if h.employeeDirectoryRefresher() == nil {
		return
	}
	// Replicas start together on a deploy; spread their first scans.
	delay := time.Minute + time.Duration(rand.Int64N(int64(time.Minute)))
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = employeeDirectoryTick
		h.refreshDueEmployeeDirectory(ctx)
	}
}

func (h *Handler) refreshDueEmployeeDirectory(ctx context.Context) {
	r := h.employeeDirectoryRefresher()
	if r == nil {
		return
	}
	scanCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	profiles, err := r.Store.DueProfileTargets(scanCtx, r.Schedule, 10)
	cancel()
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("employee directory profile scan failed", "event", "employee_directory_scan_failed", "kind", "profile", "error", err)
	}
	for _, target := range profiles {
		runCtx, cancel := context.WithTimeout(ctx, employeeDirectoryProfileBudget)
		if _, err := r.RefreshProfile(runCtx, target); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("employee directory profile refresh failed", "event", "employee_directory_refresh_error", "agent_id", target.AgentID, "error", err)
		}
		cancel()
	}
	scanCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
	rosters, err := r.Store.DueRosterTargets(scanCtx, r.Schedule, employeeDirectoryRosterActive, 5)
	cancel()
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("employee directory roster scan failed", "event", "employee_directory_scan_failed", "kind", "roster", "error", err)
	}
	for _, target := range rosters {
		runCtx, cancel := context.WithTimeout(ctx, employeeDirectoryRosterBudget)
		if _, err := r.RefreshRoster(runCtx, target); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("employee directory roster refresh failed", "event", "employee_directory_refresh_error", "agent_id", target.AgentID,
				"scene_id", target.SceneID, "error", err)
		}
		cancel()
	}
}

// employeeDirectoryRequest names one wake's directory context.
type employeeDirectoryRequest struct {
	WorkspaceID string
	AgentID     string
	TenantOrgID string
	// Scene is the wake's fenced scene (employeeSceneFence).
	Scene db.AgentScene
	// DWSUID is the wake's execution identity
	// (command.ExternalIdentity.DWS.UID); facts read as another identity
	// are never used.
	DWSUID string
	// Prioritize lists requester refs of the current window; members among
	// them are listed first. Non-members are ignored.
	Prioritize []string
}

// employeeDirectoryContext is the Host directory context of one wake, to be
// frozen into a new snapshot by the memory input hook.
type employeeDirectoryContext struct {
	// SelfFacts is the SELF PROFILE line of the agent's own address book
	// entry (supervisor, department, title), employeedirectory.SelfFactsUnread
	// when nothing usable is stored.
	SelfFacts string
	// GroupMembers is the GROUP MEMBERS Host fact block of a group scene; ""
	// outside group scenes and before the first roster refresh.
	GroupMembers string
	// ProfileStatus is loaded or unread; RosterStatus is loaded, none,
	// not_group or unavailable.
	ProfileStatus string
	RosterStatus  string
}

// Metadata is trace metadata of the context, without names or facts.
func (c employeeDirectoryContext) Metadata() map[string]any {
	return map[string]any{"directory_profile": c.ProfileStatus, "directory_roster": c.RosterStatus,
		"directory_self_bytes": len(c.SelfFacts), "directory_members_bytes": len(c.GroupMembers)}
}

// employeeDirectoryFacts reads the wake's directory context from PostgreSQL
// and asks for a background refresh when it is missing or stale. It never
// calls DingTalk itself and never fails the wake: errors leave a block out.
func (h *Handler) employeeDirectoryFacts(ctx context.Context, req employeeDirectoryRequest) employeeDirectoryContext {
	out := employeeDirectoryContext{SelfFacts: employeedirectory.SelfFactsUnread, ProfileStatus: "unread", RosterStatus: "not_group"}
	database, ok := employeeEntryDB(h)
	if !ok {
		out.RosterStatus = "unavailable"
		return out
	}
	store := employeedirectory.NewStore(database)
	uid := strings.TrimSpace(req.DWSUID)
	profile, found, err := store.ReadProfile(ctx, req.WorkspaceID, req.AgentID, req.TenantOrgID)
	if err == nil {
		out.SelfFacts = employeedirectory.RenderSelfFacts(profile, found, uid)
		if out.SelfFacts != employeedirectory.SelfFactsUnread {
			out.ProfileStatus = "loaded"
		}
	}
	if uid != "" && (err == nil && (!found || profile.DWSUID != uid || stale(profile.RefreshedAt))) {
		h.refreshEmployeeDirectoryLater("profile:"+req.WorkspaceID+":"+req.AgentID+":"+req.TenantOrgID+":"+uid, func(ctx context.Context, r *employeedirectory.Refresher) {
			_, _ = r.RefreshProfile(ctx, employeedirectory.ProfileTarget{WorkspaceID: req.WorkspaceID, AgentID: req.AgentID, TenantOrgID: req.TenantOrgID, DWSUID: uid})
		})
	}

	// A roster exists only for a group scene of this agent in this tenant.
	if req.Scene.SceneKind != scene.KindGroup {
		return out
	}
	sceneID := util.UUIDToString(req.Scene.ID)
	if util.UUIDToString(req.Scene.WorkspaceID) != req.WorkspaceID || util.UUIDToString(req.Scene.AgentID) != req.AgentID ||
		scene.CheckTenant(req.Scene, req.TenantOrgID) != nil {
		out.RosterStatus = "unavailable"
		return out
	}
	roster, found, err := store.ReadRoster(ctx, req.WorkspaceID, req.AgentID, sceneID)
	switch {
	case err != nil:
		out.RosterStatus = "unavailable"
	case found && roster.TenantOrgID == req.TenantOrgID && roster.DWSUID == uid:
		out.GroupMembers = employeedirectory.RenderGroupMembers(roster, employeedirectory.RenderOptions{Prioritize: req.Prioritize})
		out.RosterStatus = "loaded"
	default:
		out.RosterStatus = "none"
	}
	conversationID := strings.TrimSpace(req.Scene.ExternalSceneID)
	if uid != "" && conversationID != "" && err == nil && (out.RosterStatus != "loaded" || stale(roster.RefreshedAt)) {
		target := employeedirectory.RosterTarget{WorkspaceID: req.WorkspaceID, AgentID: req.AgentID, SceneID: sceneID,
			TenantOrgID: req.TenantOrgID, DWSUID: uid, ConversationID: conversationID}
		h.refreshEmployeeDirectoryLater("roster:"+req.WorkspaceID+":"+req.AgentID+":"+sceneID+":"+uid, func(ctx context.Context, r *employeedirectory.Refresher) {
			_, _ = r.RefreshRoster(ctx, target)
		})
	}
	return out
}

func stale(refreshed time.Time) bool {
	return refreshed.IsZero() || time.Since(refreshed) >= employeedirectory.DefaultSchedule.MinAge
}

// employeeDirectoryLazy bounds wake-triggered refreshes on this replica.
var employeeDirectoryLazy = struct {
	mu      sync.Mutex
	last    map[string]time.Time
	running int
	wg      sync.WaitGroup
}{last: map[string]time.Time{}}

// refreshEmployeeDirectoryLater runs refresh in the background, at most once
// per key per employeeDirectoryLazyEvery and at most employeeDirectoryLazyMax
// at a time on this replica; the lease decides whether it reads anything.
func (h *Handler) refreshEmployeeDirectoryLater(key string, refresh func(context.Context, *employeedirectory.Refresher)) {
	r := h.employeeDirectoryRefresher()
	if r == nil {
		return
	}
	lazy := &employeeDirectoryLazy
	lazy.mu.Lock()
	if last, ok := lazy.last[key]; (ok && time.Since(last) < employeeDirectoryLazyEvery) || lazy.running >= employeeDirectoryLazyMax {
		lazy.mu.Unlock()
		return
	}
	lazy.last[key] = time.Now()
	for k, at := range lazy.last {
		if time.Since(at) >= employeeDirectoryLazyEvery {
			delete(lazy.last, k)
		}
	}
	lazy.running++
	lazy.wg.Add(1)
	lazy.mu.Unlock()
	go func() {
		defer func() {
			lazy.mu.Lock()
			lazy.running--
			lazy.mu.Unlock()
			lazy.wg.Done()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), employeeDirectoryRosterBudget)
		defer cancel()
		refresh(ctx, r)
	}()
}
