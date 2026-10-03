package employeememory

// Portions copyright (c) 2026 Nex. Modified from GawkBot at
// 71e82a1809565281cbd0bf8185d3c125b715d934. See LICENSE.gawkbot and SOURCE_MAP.md.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const taskDistillInsightDetailClip = 400

// learningKeyFromTitle produces a key that satisfies the learning store's
// ^[a-z0-9][a-z0-9_-]*$ pattern from an arbitrary task title. Titles with
// punctuation ("Fix #42: crash v2.0") previously produced invalid keys and
// the distillation silently no-opped (review HIGH finding).
func learningKeyFromTitle(title string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		isAlnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		switch {
		case isAlnum:
			b.WriteRune(r)
			lastDash = false
		case !lastDash && b.Len() > 0:
			b.WriteRune('-')
			lastDash = true
		}
	}
	key := strings.Trim(b.String(), "-")
	if key == "" {
		key = "task"
	}
	if len(key) > MaxLearningKeyLen {
		key = strings.Trim(key[:MaxLearningKeyLen], "-")
	}
	return key
}

// learningKeyForTask preserves the upstream readable prefix while using the
// full Host task-identity digest to distinguish Chinese and colliding titles.
func learningKeyForTask(title, taskID string) string {
	prefix := learningKeyFromTitle(title)
	if len(prefix) > 15 {
		prefix = strings.TrimRight(prefix[:15], "-")
	}
	digest := sha256.Sum256([]byte(taskID))
	return prefix + "-" + hex.EncodeToString(digest[:])
}

// taskDistillInsight renders the verified-outcome learning text. Pure
// string assembly, shared by the learning record and the notebook
// post-task bookend.
func taskDistillInsight(task VerifiedRun) string {
	insight := fmt.Sprintf("Verified outcome: %s.", strings.TrimSpace(task.Title))
	if details := tailClip(task.Details, taskDistillInsightDetailClip); details != "" {
		insight += " " + details
	}
	if task.Passed {
		if proof := strings.TrimSpace(task.Proof); proof != "" {
			insight += fmt.Sprintf(" Proof (%s): %s", task.ProofKind, truncate(proof, 200))
		}
	}
	return insight
}

// VerifiedRun must come from Host-owned verification results, never model arguments.
// OccurredAt is the Host work-end time of the verified execution. It is the
// reset fence: retries or late verification never refresh it.
type VerifiedRun struct {
	TaskID, ExecutionID, Title, Details, Proof, ProofKind, ActorID, EvidenceID string
	Passed                                                                     bool
	OccurredAt                                                                 time.Time
}

func tailClip(text string, limit int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[len(runes)-limit:])
	}
	return text
}
func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "..."
	}
	return text
}
