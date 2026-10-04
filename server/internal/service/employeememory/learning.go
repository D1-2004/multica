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
	// Scene-shared memory types. They are validated only on write, so an older
	// binary that reads them simply displays the stored value.
	LearningTypeFact     LearningType = "fact"
	LearningTypeDecision LearningType = "decision"
	// LearningTypeOpenItem is proposed only by the Host flush writer.
	LearningTypeOpenItem LearningType = "open_item"
)

func ValidLearningTypes() []LearningType {
	return []LearningType{
		LearningTypePattern,
		LearningTypePitfall,
		LearningTypePreference,
		LearningTypeArchitecture,
		LearningTypeTool,
		LearningTypeOperational,
		LearningTypeFact,
		LearningTypeDecision,
		LearningTypeOpenItem,
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
	// EvidenceOccurredAt is a Host timestamp used only by ordered private observations.
	EvidenceOccurredAt time.Time       `json:"evidence_occurred_at,omitempty,omitzero"`
	Workflow           *MemoryWorkflow `json:"workflow,omitempty"`
	ID                 string          `json:"id"`
	SourceID           string          `json:"source_id"`
	EvidenceID         string          `json:"evidence_id"`
	Type               LearningType    `json:"type"`
	Key                string          `json:"key"`
	Insight            string          `json:"insight"`
	Confidence         int             `json:"confidence"`
	Source             LearningSource  `json:"source"`
	Trusted            bool            `json:"trusted"`
	Scope              string          `json:"scope"`
	PlaybookSlug       string          `json:"playbook_slug,omitempty"`
	ExecutionID        string          `json:"execution_id,omitempty"`
	TaskID             string          `json:"task_id,omitempty"`
	Files              []string        `json:"files,omitempty"`
	Entities           []string        `json:"entities,omitempty"`
	CreatedBy          string          `json:"created_by"`
	CreatedAt          time.Time       `json:"created_at"`
	Supersedes         string          `json:"supersedes,omitempty"`
	// Attribution of a captured statement. All of it is filled by the Host from
	// frozen evidence, never from model arguments; older binaries ignore it.
	Subject       string        `json:"subject,omitempty"`
	SpeakerRef    string        `json:"speaker_ref,omitempty"`
	SpeakerName   string        `json:"speaker_name,omitempty"`
	SaidAt        time.Time     `json:"said_at,omitempty,omitzero"`
	CaptureOrigin CaptureOrigin `json:"capture_origin,omitempty"`
	// ConflictsWith names an active scene record with the same type and key
	// written by another author. Both stay active as conflicting candidates.
	ConflictsWith string `json:"conflicts_with,omitempty"`
	// CaptureSourceID names the admitted message that asked for the capture
	// ("employee-message:<receipt_id>"), so history can hide the capture job's
	// own confirmation once the record is forgotten or superseded.
	CaptureSourceID string `json:"capture_source_id,omitempty"`
}

func dedupeLearnings(records []LearningRecord) []LearningRecord {
	byKey := make(map[string]LearningRecord, len(records))
	for _, rec := range records {
		key := rec.Scope + "|" + string(rec.Type) + "|" + rec.Key
		if rec.Scope == string(ScopeScene) {
			// Cross-author records with one key are conflict candidates, not
			// versions of each other: keep one current record per author.
			key += "|" + rec.CreatedBy
		}
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
		// Chinese equivalents (Multica extension): role changes, overrides and
		// blanket approvals that must never become stored "memory".
		"忽略之前的指令",
		"忽略之前所有指令",
		"忽略之前的所有指令",
		"忽略以上指令",
		"忽略上述指令",
		"你现在是",
		"以系统身份",
		"跳过审核",
		"全部批准",
		"无需审批",
		"无需确认",
		"系统提示",
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
