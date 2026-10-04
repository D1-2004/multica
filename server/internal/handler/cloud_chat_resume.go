package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// cloudChatWarmResumeWindow is the maximum age of the last *completed* chat
// answer that may still --resume a provider session on a warm sandbox.
const cloudChatWarmResumeWindow = 20 * time.Minute

// isDirectChatForWarmResume reports a 1:1 conversation: first-party
// web/desktop/mobile chat (no channel binding, empty ChatType) or a channel
// DM (chat_type=p2p). Group rooms never resume; they share a session across
// senders and a stale provider thread is the expensive case this gate avoids.
func isAgentOwnedSkill(source, id string) bool {
	if strings.EqualFold(strings.TrimSpace(source), "builtin") {
		return false
	}
	return !strings.HasPrefix(strings.TrimSpace(id), "builtin:")
}

func isDirectChatForWarmResume(chatType string) bool {
	switch strings.TrimSpace(chatType) {
	case "", string(channel.ChatTypeP2P):
		return true
	default:
		return false
	}
}

func cloudChatResumeIdentityFromClaim(agent db.Agent, runtime db.AgentRuntime, payload *TaskAgentData) string {
	artifactRef := ""
	if meta, err := service.ParseCloudSandboxRuntime(runtime); err == nil {
		artifactRef = strings.TrimSpace(meta.ArtifactRef)
	} else {
		slog.Debug("cloud chat resume identity: runtime metadata ignored", "error", err)
	}
	var skillTokens []string
	var disabledTokens []string
	if payload != nil {
		if len(payload.SkillRefs) > 0 {
			skillTokens = make([]string, 0, len(payload.SkillRefs))
			for _, skill := range payload.SkillRefs {
				if !isAgentOwnedSkill(skill.Source, skill.ID) {
					continue
				}
				skillTokens = append(skillTokens, skill.ID+":"+skill.Hash)
			}
		} else {
			skillTokens = make([]string, 0, len(payload.Skills))
			for _, skill := range payload.Skills {
				if !isAgentOwnedSkill(skill.Source, skill.ID) {
					continue
				}
				skillTokens = append(skillTokens, skill.ID+":"+skill.Hash)
			}
		}
		disabledTokens = make([]string, 0, len(payload.DisabledRuntimeSkills))
		for _, skill := range payload.DisabledRuntimeSkills {
			disabledTokens = append(disabledTokens, strings.Join([]string{skill.Root, skill.Key, skill.Plugin}, "/"))
		}
	}
	return cloudChatResumeIdentity(
		util.UUIDToString(runtime.ID),
		artifactRef,
		agent.Instructions,
		skillTokens,
		disabledTokens,
	)
}

func cloudChatResumeIdentity(runtimeID, artifactRef, instructions string, skillTokens, disabledTokens []string) string {
	skills := append([]string(nil), skillTokens...)
	disabled := append([]string(nil), disabledTokens...)
	sort.Strings(skills)
	sort.Strings(disabled)
	sum := sha256.New()
	fmt.Fprintf(sum, "v1\nruntime=%s\nartifact=%s\ninstructions=%s\nskills=%s\ndisabled=%s\n",
		strings.TrimSpace(runtimeID),
		strings.TrimSpace(artifactRef),
		instructions,
		strings.Join(skills, ","),
		strings.Join(disabled, ","),
	)
	return hex.EncodeToString(sum.Sum(nil))
}

func (h *Handler) cloudSandboxAttemptIsColdStart(ctx context.Context, task db.AgentTaskQueue) bool {
	attempt, err := h.Queries.GetStartingAgentTaskRuntimeStartAttemptByTask(ctx, db.GetStartingAgentTaskRuntimeStartAttemptByTaskParams{
		TaskID:    task.ID,
		RuntimeID: task.RuntimeID,
	})
	if err == pgx.ErrNoRows {
		attempt, err = h.Queries.GetLatestAgentTaskRuntimeStartAttemptByTask(ctx, db.GetLatestAgentTaskRuntimeStartAttemptByTaskParams{
			TaskID:    task.ID,
			RuntimeID: task.RuntimeID,
		})
	}
	if err != nil || !attempt.ColdStart.Valid {
		// Unknown launch shape: fail closed and keep database history as
		// the only continuity source.
		return true
	}
	return attempt.ColdStart.Bool
}

func (h *Handler) lastCompletedChatAnswerIsFresh(ctx context.Context, chatSessionID pgtype.UUID, now time.Time) bool {
	completedAt, err := h.Queries.GetLastCompletedChatTaskAt(ctx, chatSessionID)
	if err != nil || !completedAt.Valid {
		return false
	}
	return now.Sub(completedAt.Time) >= 0 && now.Sub(completedAt.Time) <= cloudChatWarmResumeWindow
}

func (h *Handler) shouldWarmResumeCloudChat(
	ctx context.Context,
	agent db.Agent,
	agentLoadErr error,
	runtime db.AgentRuntime,
	task db.AgentTaskQueue,
	session db.ChatSession,
	resp AgentTaskResponse,
	currentIdentity string,
) bool {
	// Local continuity is already guarded by the daemon's workdir checks. A
	// cloud-only opt-in must never clear a local provider session.
	if !service.IsCloudSandboxRuntime(runtime) {
		return true
	}
	if agentLoadErr != nil {
		return false
	}
	var private struct {
		Steer bool `json:"task_steer"`
	}
	if json.Unmarshal(task.Context, &private) == nil && private.Steer &&
		!task.ForceFreshSession && strings.TrimSpace(resp.PriorSessionID) != "" &&
		strings.TrimSpace(resp.PriorWorkDir) != "" && !h.cloudSandboxAttemptIsColdStart(ctx, task) {
		stored, err := h.Queries.GetChatSessionResumeIdentity(ctx, session.ID)
		// An explicit steer may resume its canceled first turn (and a group
		// turn); it still requires the same frozen agent/runtime configuration.
		if err == nil && currentIdentity != "" && stored == currentIdentity {
			return true
		}
	}
	enabled, err := h.Queries.GetAgentChatSessionResume(ctx, agent.ID)
	if err != nil || !enabled {
		return false
	}
	if !service.IsCloudSandboxRuntime(runtime) {
		return false
	}
	if task.ForceFreshSession || strings.TrimSpace(resp.PriorSessionID) == "" {
		return false
	}
	if !isDirectChatForWarmResume(resp.ChatType) {
		return false
	}
	if h.cloudSandboxAttemptIsColdStart(ctx, task) {
		return false
	}
	storedIdentity, err := h.Queries.GetChatSessionResumeIdentity(ctx, session.ID)
	if err != nil || strings.TrimSpace(storedIdentity) == "" || storedIdentity != currentIdentity {
		return false
	}
	if !h.lastCompletedChatAnswerIsFresh(ctx, session.ID, time.Now()) {
		return false
	}
	slog.Info("cloud chat warm resume eligible",
		"task_id", util.UUIDToString(task.ID),
		"chat_session_id", util.UUIDToString(session.ID),
		"prior_session_id", resp.PriorSessionID,
	)
	return true
}
