package employeememory

// Portions copyright (c) 2026 Nex. Modified from GawkBot at
// 71e82a1809565281cbd0bf8185d3c125b715d934. See LICENSE.gawkbot and SOURCE_MAP.md.

import (
	"strings"
	"time"
)

type LearningType string

const (
	LearningTypePattern      LearningType = "pattern"
	LearningTypePitfall      LearningType = "pitfall"
	LearningTypePreference   LearningType = "preference"
	LearningTypeArchitecture LearningType = "architecture"
	LearningTypeTool         LearningType = "tool"
	LearningTypeOperational  LearningType = "operational"
)

func ValidLearningTypes() []LearningType {
	return []LearningType{
		LearningTypePattern,
		LearningTypePitfall,
		LearningTypePreference,
		LearningTypeArchitecture,
		LearningTypeTool,
		LearningTypeOperational,
	}
}

type LearningSource string

const (
	LearningSourceUserStated LearningSource = "user-stated"
	LearningSourceObserved   LearningSource = "observed"
	LearningSourceInferred   LearningSource = "inferred"
	LearningSourceExecution  LearningSource = "execution"
	LearningSourceSynthesis  LearningSource = "synthesis"
	LearningSourceCrossBot   LearningSource = "cross-agent"
	LearningSourceCrossModel LearningSource = "cross-model"
)

func ValidLearningSources() []LearningSource {
	return []LearningSource{
		LearningSourceUserStated,
		LearningSourceObserved,
		LearningSourceInferred,
		LearningSourceExecution,
		LearningSourceSynthesis,
		LearningSourceCrossBot,
		LearningSourceCrossModel,
	}
}

type LearningRecord struct {
	Workflow     *MemoryWorkflow `json:"workflow,omitempty"`
	ID           string          `json:"id"`
	SourceID     string          `json:"source_id"`
	EvidenceID   string          `json:"evidence_id"`
	Type         LearningType    `json:"type"`
	Key          string          `json:"key"`
	Insight      string          `json:"insight"`
	Confidence   int             `json:"confidence"`
	Source       LearningSource  `json:"source"`
	Trusted      bool            `json:"trusted"`
	Scope        string          `json:"scope"`
	PlaybookSlug string          `json:"playbook_slug,omitempty"`
	ExecutionID  string          `json:"execution_id,omitempty"`
	TaskID       string          `json:"task_id,omitempty"`
	Files        []string        `json:"files,omitempty"`
	Entities     []string        `json:"entities,omitempty"`
	CreatedBy    string          `json:"created_by"`
	CreatedAt    time.Time       `json:"created_at"`
	Supersedes   string          `json:"supersedes,omitempty"`
}

func dedupeLearnings(records []LearningRecord) []LearningRecord {
	byKey := make(map[string]LearningRecord, len(records))
	for _, rec := range records {
		key := rec.Scope + "|" + string(rec.Type) + "|" + rec.Key
		existing, ok := byKey[key]
		if !ok || rec.CreatedAt.After(existing.CreatedAt) || (rec.CreatedAt.Equal(existing.CreatedAt) && rec.ID > existing.ID) {
			byKey[key] = rec
		}
	}
	out := make([]LearningRecord, 0, len(byKey))
	for _, rec := range byKey {
		out = append(out, rec)
	}
	return out
}

func effectiveLearningConfidence(rec LearningRecord, now time.Time) int {
	if rec.Trusted || rec.CreatedAt.IsZero() {
		return rec.Confidence
	}
	ageDays := int(now.Sub(rec.CreatedAt).Hours() / 24)
	if ageDays <= 0 {
		return rec.Confidence
	}
	decay := ageDays / 30
	effective := rec.Confidence - decay
	if effective < 1 {
		return 1
	}
	return effective
}

func learningMatchesQuery(rec LearningRecord, query string) bool {
	fields := []string{
		rec.Key,
		rec.Insight,
		rec.Scope,
		string(rec.Type),
		string(rec.Source),
		rec.PlaybookSlug,
		rec.TaskID,
	}
	fields = append(fields, rec.Files...)
	fields = append(fields, rec.Entities...)
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), query) {
			return true
		}
	}
	return false
}

func containsInstructionLikeLearning(insight string) bool {
	lower := strings.ToLower(insight)
	bad := []string{
		"ignore previous instructions",
		"ignore all previous",
		"you are now",
		"always output no findings",
		"skip security",
		"skip review",
		"skip checks",
		"override:",
		"system:",
		"assistant:",
		"user:",
		"do not report",
		"do not flag",
		"do not mention",
		"approve all",
		"approve every",
	}
	for _, phrase := range bad {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

const (
	MaxLearningInsightLen = 4000
	MaxLearningKeyLen     = 80
	MaxLearningLimit      = 100
)

type LearningSearchResult struct {
	LearningRecord
	EffectiveConfidence int `json:"effective_confidence"`
}
