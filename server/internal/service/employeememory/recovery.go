package employeememory

// Portions copyright (c) 2026 Nex. Modified from GawkBot at
// 71e82a1809565281cbd0bf8185d3c125b715d934. See LICENSE.gawkbot and SOURCE_MAP.md.

import (
	"fmt"
	"strings"
)

type SessionRecovery struct {
	Focus      string
	NextSteps  []string
	Highlights []string
}

func BuildSessionRecovery(sessionMode, directBot string, tasks []RecoveryTask, requests []RecoveryRequest, recent []RecoveryMessage) SessionRecovery {
	return buildSessionRecovery(sessionMode, directBot, tasks, requests, recent)
}

func buildSessionRecovery(sessionMode, directBot string, tasks []RecoveryTask, requests []RecoveryRequest, recent []RecoveryMessage) SessionRecovery {
	recovery := SessionRecovery{}

	if req, ok := firstPendingBlockingRecoveryRequest(requests); ok {
		recovery.Focus = summarizeRequest(req)
		recovery.NextSteps = append(recovery.NextSteps, "Answer the blocking human request before moving more work.")
	}
	if recovery.Focus == "" {
		if task, ok := firstRunningTask(tasks); ok {
			recovery.Focus = summarizeTask(task)
		}
	}
	if recovery.Focus == "" {
		if sessionMode == "dm" {
			recovery.Focus = fmt.Sprintf("Stay focused on the direct session with @%s.", directBot)
		} else {
			recovery.Focus = "No blocking work detected. Scan the latest channel activity before speaking."
		}
	}

	for _, task := range tasks {
		if !runtimeTaskIsRunning(task) {
			continue
		}
		if runtimeTaskUsesIsolation(task) && strings.TrimSpace(task.WorktreePath) != "" {
			recovery.NextSteps = appendUnique(recovery.NextSteps, fmt.Sprintf("Use working_directory %s for local file and bash tools.", task.WorktreePath))
		}
		if strings.TrimSpace(task.ReviewState) != "" && task.ReviewState != "not_required" && task.ReviewState != "approved" {
			recovery.NextSteps = appendUnique(recovery.NextSteps, fmt.Sprintf("Review flow is active on %s (%s).", task.ID, task.ReviewState))
		}
		if task.Blocked {
			recovery.NextSteps = appendUnique(recovery.NextSteps, fmt.Sprintf("%s is blocked; check dependencies before continuing.", task.ID))
		}
	}

	recovery.Highlights = append(recovery.Highlights, recentHighlights(recent)...)

	return recovery
}

func runtimeRequestIsOpen(req RecoveryRequest) bool {
	status := strings.ToLower(strings.TrimSpace(req.Status))
	return status == "" || status == "pending" || status == "open" || status == "draft"
}

func firstPendingBlockingRecoveryRequest(requests []RecoveryRequest) (RecoveryRequest, bool) {
	for _, req := range requests {
		status := strings.ToLower(strings.TrimSpace(req.Status))
		if status != "" && status != "pending" && status != "open" {
			continue
		}
		if req.Blocking || req.Required {
			return req, true
		}
	}
	return RecoveryRequest{}, false
}

func firstRunningTask(tasks []RecoveryTask) (RecoveryTask, bool) {
	for _, task := range tasks {
		if runtimeTaskIsRunning(task) {
			return task, true
		}
	}
	return RecoveryTask{}, false
}

func summarizeRequest(req RecoveryRequest) string {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(req.Question)
	}
	if title == "" {
		title = "Human decision pending"
	}
	if req.From != "" {
		return fmt.Sprintf("%s from @%s.", title, req.From)
	}
	return title + "."
}

func summarizeTask(task RecoveryTask) string {
	text := strings.TrimSpace(task.Title)
	if text == "" {
		text = task.ID
	}
	if task.Owner != "" {
		text += " owned by @" + task.Owner
	}
	if stage := strings.TrimSpace(task.PipelineStage); stage != "" {
		text += " at stage " + stage
	}
	return text + "."
}

func recentHighlights(recent []RecoveryMessage) []string {
	highlights := make([]string, 0, 3)
	for _, msg := range recent {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			content = strings.TrimSpace(msg.Title)
		}
		if content == "" {
			continue
		}
		highlights = append(highlights, fmt.Sprintf("@%s: %s", msg.From, truncateRecoveryText(content, 120)))
		if len(highlights) == 3 {
			break
		}
	}
	return highlights
}

func truncateRecoveryText(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "..."
}

func appendUnique(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

// Recovery inputs are snapshots authorized for the same exact memory scope.
type RecoveryTask struct {
	ID, Title, Owner, PipelineStage, Status, WorktreePath, ReviewState string
	Blocked, Isolated                                                  bool
}
type RecoveryRequest struct {
	Title, Question, From, Status string
	Blocking, Required            bool
}
type RecoveryMessage struct{ Content, Title, From string }

func runtimeTaskIsRunning(task RecoveryTask) bool {
	return task.Status == "running" || task.Status == "in_progress"
}
func runtimeTaskUsesIsolation(task RecoveryTask) bool { return task.Isolated }
