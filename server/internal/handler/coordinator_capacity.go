package handler

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	// sceneCapacityStaleAfter stops a non-running task from holding a slot
	// forever. The runtime sweeper owns real task failure with liveness
	// signals (cmd/server/runtime_sweeper.go); this only decides whether a row
	// still counts as occupying its delegator while the sweeper has not acted.
	sceneCapacityStaleAfter = 2 * time.Hour
	// sceneCapacityWaitDeadline bounds how long one judged window waits for a
	// free slot. Capacity is politeness, not a safety invariant: after this the
	// window runs anyway rather than repeating a silent 5-second park forever.
	sceneCapacityWaitDeadline = 10 * time.Minute
)

// sceneCapacityRejectReason is the admission reason for a scene that is full
// for this delegator. Reasons are persisted on parked jobs, so the matcher
// below must keep recognising the pre-upgrade wording during a rollout.
// The pre-upgrade prefix is kept verbatim so a replica still running the old
// binary recognises a job this one parked. TestSceneCapacityReasonMatchesLimit
// fails if the limit ever stops matching the wording.
func sceneCapacityRejectReason() string {
	return "scene already has two in-flight matters for this delegator"
}

func isSceneCapacityReason(reason string) bool {
	return strings.Contains(reason, "in-flight matters")
}

type sceneCapacityWaiverKey struct{}

// withSceneCapacityWaiver marks one job run as past its capacity deadline.
// It travels in the worker's context, which no public wire request can supply.
func withSceneCapacityWaiver(ctx context.Context) context.Context {
	return context.WithValue(ctx, sceneCapacityWaiverKey{}, true)
}

func sceneCapacityWaived(ctx context.Context) bool {
	waived, _ := ctx.Value(sceneCapacityWaiverKey{}).(bool)
	return waived
}

// coordinatorCapacityWaitExpired reports that this job has been waiting for
// scene capacity past the deadline. Only the capacity reason qualifies: the
// same-issue busy park protects against a duplicate execution of one matter
// and must never be waived by waiting.
func coordinatorCapacityWaitExpired(job db.InboundCoordinatorJob) bool {
	if !job.LastError.Valid || !isSceneCapacityReason(job.LastError.String) {
		return false
	}
	if !job.CreatedAt.Valid {
		return false
	}
	return time.Since(job.CreatedAt.Time) >= sceneCapacityWaitDeadline
}

// sceneDelegator identifies whose budget a window spends. Keys are the same
// canonical person key and aliases assoc binds to a matter (task_person), so
// capacity follows the person across uid / staffId / openDingTalkId.
type sceneDelegator struct {
	// SceneID is the dispatch's Agent work scene; capacity counts the open
	// matters linked to that scene node.
	SceneID string
	Keys    []string
	// resolved is true once Keys is the alias closure from the database, so
	// two utterances of one person group onto one budget even when each
	// message carries a different identifier.
	resolved bool
}

func dispatchSceneDelegator(command DispatchCommand) sceneDelegator {
	ids := dispatchAssocIDs(command)
	d := sceneDelegator{SceneID: dispatchSceneID(command)}
	if key := strings.TrimSpace(ids.PersonID); key != "" {
		d.Keys = append(d.Keys, key)
	}
	for _, alias := range ids.PersonAliases {
		if alias = strings.TrimSpace(alias); alias != "" {
			d.Keys = append(d.Keys, alias)
		}
	}
	return d
}

// resolveSceneDelegator expands the window's identifiers to every key that can
// address this person on an existing edge. Grouping without it would give one
// person two budgets when two utterances carry different identifiers.
func resolveSceneDelegator(ctx context.Context, h *Handler, workspaceID, agentID pgtype.UUID, d sceneDelegator) sceneDelegator {
	if d.resolved || len(d.Keys) == 0 || h == nil || h.Queries == nil {
		return d
	}
	keys, err := h.Queries.ResolveAssocPersonKeys(ctx, db.ResolveAssocPersonKeysParams{
		PersonKeys: d.Keys, WorkspaceID: workspaceID, AgentID: agentID,
	})
	if err != nil || len(keys) == 0 {
		// Keep the trusted inbound identifiers; they are still this person.
		return d
	}
	d.Keys, d.resolved = keys, true
	return d
}

// key groups needs within one window. The resolved closure is sorted, so the
// same person yields the same group key whichever identifier they arrived with.
func (d sceneDelegator) key() string {
	if len(d.Keys) == 0 {
		return ""
	}
	if d.resolved {
		return d.Keys[0]
	}
	sorted := append([]string{}, d.Keys...)
	sort.Strings(sorted)
	return sorted[0]
}

// pendingWindowDelegators lists whose work this job still has to start. A
// collected window keeps its first speaker as the command sender, so judging
// capacity by that sender alone would make one busy person hold back the
// unstarted items of everyone else in the same window.
func pendingWindowDelegators(job db.InboundCoordinatorJob, command DispatchCommand) []sceneDelegator {
	plan, err := coordinatorJobCheckpoint(job.Command)
	if err != nil || plan == nil || plan.Action != inboundcoord.ActionIssue || len(plan.Items) == 0 {
		return []sceneDelegator{dispatchSceneDelegator(command)}
	}
	var out []sceneDelegator
	seen := map[string]struct{}{}
	for _, item := range plan.Items {
		if planItemCompleted(*plan, item.ActionKey) {
			continue
		}
		d := dispatchSceneDelegator(windowItemCommand(command, item))
		if _, ok := seen[d.key()]; ok {
			continue
		}
		seen[d.key()] = struct{}{}
		out = append(out, d)
	}
	if len(out) == 0 {
		return []sceneDelegator{dispatchSceneDelegator(command)}
	}
	return out
}

// sceneDelegatorNeed is how many new matters one delegator asked for in the
// current window. A window normally has one speaker; a collected group window
// can carry several, and each spends only their own slots.
type sceneDelegatorNeed struct {
	delegator sceneDelegator
	needed    int
}

func addSceneDelegatorNeed(needs []sceneDelegatorNeed, d sceneDelegator) []sceneDelegatorNeed {
	for i := range needs {
		if needs[i].delegator.key() == d.key() {
			needs[i].needed++
			return needs
		}
	}
	return append(needs, sceneDelegatorNeed{delegator: d, needed: 1})
}

// sceneCapacitySlots reports how many new matters this delegator may start in
// this scene right now. An unattributed sender falls back to the scene-wide
// count: a missing identity must not mint unlimited capacity.
func sceneCapacitySlots(ctx context.Context, h *Handler, workspaceID, agentID pgtype.UUID, d sceneDelegator) int {
	limit := inboundcoord.SceneDelegatorMaxInFlightMatters
	if sceneCapacityWaived(ctx) {
		return limit
	}
	if h == nil || h.Queries == nil || strings.TrimSpace(d.SceneID) == "" {
		return limit
	}
	var (
		active int64
		err    error
	)
	if len(d.Keys) == 0 {
		active, err = h.Queries.CountActiveTasksForConversation(ctx, db.CountActiveTasksForConversationParams{
			WorkspaceID:    workspaceID,
			AgentID:        agentID,
			SceneID:        d.SceneID,
			StaleAfterSecs: sceneCapacityStaleAfter.Seconds(),
		})
	} else {
		active, err = h.Queries.CountActiveDelegatorTasksForConversation(ctx, db.CountActiveDelegatorTasksForConversationParams{
			WorkspaceID:    workspaceID,
			AgentID:        agentID,
			SceneID:        d.SceneID,
			StaleAfterSecs: sceneCapacityStaleAfter.Seconds(),
			PersonKeys:     d.Keys,
		})
	}
	if err != nil {
		// An unreadable count must not silently stop the employee from working.
		return limit
	}
	if left := limit - int(active); left > 0 {
		return left
	}
	return 0
}
