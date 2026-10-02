package employeememory

import (
	"fmt"
	"strings"
)

// Portions copyright (c) 2026 Nex. Modified from GawkBot at
// 71e82a1809565281cbd0bf8185d3c125b715d934. See LICENSE.gawkbot and SOURCE_MAP.md.

type MemoryWorkflowStep string

const (
	MemoryWorkflowStepLookup  MemoryWorkflowStep = "lookup"
	MemoryWorkflowStepCapture MemoryWorkflowStep = "capture"
	MemoryWorkflowStepPromote MemoryWorkflowStep = "promote"
)

const (
	MemoryWorkflowStatusNotRequired = "not_required"
	MemoryWorkflowStatusPending     = "pending"
	MemoryWorkflowStatusSatisfied   = "satisfied"
	MemoryWorkflowStatusOverridden  = "overridden"
)

const (
	MemoryWorkflowStepStatusPending   = "pending"
	MemoryWorkflowStepStatusSatisfied = "satisfied"
)

// ContextCitation is the backend-neutral citation shape used by the context
// harness. It is intentionally broad enough for markdown and GBrain
// sources without making any one backend canonical.
type ContextCitation struct {
	Backend     string   `json:"backend,omitempty"`
	Source      string   `json:"source,omitempty"`
	SourceID    string   `json:"source_id,omitempty"`
	Path        string   `json:"path,omitempty"`
	PageID      string   `json:"page_id,omitempty"`
	ChunkID     string   `json:"chunk_id,omitempty"`
	SourceURL   string   `json:"source_url,omitempty"`
	LineStart   int      `json:"line_start,omitempty"`
	LineEnd     int      `json:"line_end,omitempty"`
	Title       string   `json:"title,omitempty"`
	Snippet     string   `json:"snippet,omitempty"`
	Score       *float64 `json:"score,omitempty"`
	Stale       *bool    `json:"stale,omitempty"`
	RetrievedAt string   `json:"retrieved_at,omitempty"`
}

type MemoryWorkflowArtifact struct {
	Backend      string `json:"backend,omitempty"`
	Source       string `json:"source,omitempty"`
	Path         string `json:"path,omitempty"`
	PageID       string `json:"page_id,omitempty"`
	PromotionID  string `json:"promotion_id,omitempty"`
	EntityKind   string `json:"entity_kind,omitempty"`
	EntitySlug   string `json:"entity_slug,omitempty"`
	PlaybookSlug string `json:"playbook_slug,omitempty"`
	Title        string `json:"title,omitempty"`
	SkipReason   string `json:"skip_reason,omitempty"`
	Snippet      string `json:"snippet,omitempty"`
	CommitSHA    string `json:"commit_sha,omitempty"`
	State        string `json:"state,omitempty"`
	RecordedAt   string `json:"recorded_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
	Missing      bool   `json:"missing,omitempty"`
}

type MemoryWorkflowStepState struct {
	SourceID    string `json:"source_id,omitempty"`
	EvidenceID  string `json:"evidence_id,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Status      string `json:"status,omitempty"`
	Actor       string `json:"actor,omitempty"`
	Query       string `json:"query,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	Count       int    `json:"count,omitempty"`
}

type MemoryWorkflowOverride struct {
	Actor     string `json:"actor"`
	Reason    string `json:"reason"`
	Timestamp string `json:"timestamp"`
}

type MemoryWorkflow struct {
	Required          bool                     `json:"required"`
	Status            string                   `json:"status,omitempty"`
	RequirementReason string                   `json:"requirement_reason,omitempty"`
	RequiredSteps     []MemoryWorkflowStep     `json:"required_steps,omitempty"`
	Lookup            MemoryWorkflowStepState  `json:"lookup,omitempty"`
	Capture           MemoryWorkflowStepState  `json:"capture,omitempty"`
	Promote           MemoryWorkflowStepState  `json:"promote,omitempty"`
	Citations         []ContextCitation        `json:"citations,omitempty"`
	Captures          []MemoryWorkflowArtifact `json:"captures,omitempty"`
	Promotions        []MemoryWorkflowArtifact `json:"promotions,omitempty"`
	Override          *MemoryWorkflowOverride  `json:"override,omitempty"`
	PartialErrors     []string                 `json:"partial_errors,omitempty"`
	CreatedAt         string                   `json:"created_at,omitempty"`
	UpdatedAt         string                   `json:"updated_at,omitempty"`
	CompletedAt       string                   `json:"completed_at,omitempty"`
}

func refreshMemoryWorkflowStepStatus(wf *MemoryWorkflow, step MemoryWorkflowStep) {
	if wf == nil {
		return
	}
	switch step {
	case MemoryWorkflowStepLookup:
		if len(wf.Citations) > 0 {
			wf.Lookup.Status = MemoryWorkflowStepStatusSatisfied
			wf.Lookup.Count = len(wf.Citations)
			return
		}
		wf.Lookup.CompletedAt = ""
		wf.Lookup.Count = 0
		if wf.Lookup.Required {
			wf.Lookup.Status = MemoryWorkflowStepStatusPending
		}
	case MemoryWorkflowStepCapture:
		count := countPresentArtifacts(wf.Captures)
		if count > 0 {
			wf.Capture.Status = MemoryWorkflowStepStatusSatisfied
			wf.Capture.Count = count
			return
		}
		if hasMissingMemoryWorkflowArtifact(wf.Captures) {
			wf.Capture.CompletedAt = ""
			wf.Capture.Status = MemoryWorkflowStepStatusPending
			wf.Capture.Count = 0
			return
		}
		if wf.Capture.CompletedAt != "" {
			wf.Capture.Status = MemoryWorkflowStepStatusSatisfied
			wf.Capture.Count = 0
			return
		}
		if wf.Capture.Required {
			wf.Capture.Status = MemoryWorkflowStepStatusPending
			wf.Capture.Count = 0
		}
	case MemoryWorkflowStepPromote:
		count := countPresentArtifacts(wf.Promotions)
		if count > 0 {
			wf.Promote.Status = MemoryWorkflowStepStatusSatisfied
			wf.Promote.Count = count
			return
		}
		if hasMissingMemoryWorkflowArtifact(wf.Promotions) {
			wf.Promote.CompletedAt = ""
			wf.Promote.Status = MemoryWorkflowStepStatusPending
			wf.Promote.Count = 0
			return
		}
		if wf.Promote.CompletedAt != "" {
			wf.Promote.Status = MemoryWorkflowStepStatusSatisfied
			wf.Promote.Count = 0
			return
		}
		if wf.Promote.Required {
			wf.Promote.Status = MemoryWorkflowStepStatusPending
			wf.Promote.Count = 0
		}
	}
}

func countPresentArtifacts(artifacts []MemoryWorkflowArtifact) int {
	count := 0
	for _, artifact := range artifacts {
		if !artifact.Missing {
			count++
		}
	}
	return count
}

func hasMissingMemoryWorkflowArtifact(artifacts []MemoryWorkflowArtifact) bool {
	for _, artifact := range artifacts {
		if artifact.Missing {
			return true
		}
	}
	return false
}

func memoryWorkflowStepSatisfied(step MemoryWorkflowStepState) bool {
	return step.Status == MemoryWorkflowStepStatusSatisfied
}

func recordMemoryWorkflowLookup(wf *MemoryWorkflow, actor, query string, citations []ContextCitation, timestamp string) bool {
	if wf == nil {
		return false
	}
	changed := false
	stepChanged := false
	actor = strings.TrimSpace(actor)
	query = strings.TrimSpace(query)
	if wf.Lookup.Actor != actor {
		wf.Lookup.Actor = actor
		stepChanged = true
	}
	if wf.Lookup.Query != query {
		wf.Lookup.Query = query
		stepChanged = true
	}
	if len(citations) > 0 && wf.Lookup.CompletedAt == "" {
		wf.Lookup.CompletedAt = timestamp
		stepChanged = true
	}
	for _, citation := range citations {
		normalized := normalizeContextCitation(citation, timestamp)
		if appendContextCitation(&wf.Citations, normalized) {
			stepChanged = true
		}
	}
	if stepChanged && wf.Lookup.UpdatedAt != timestamp {
		wf.Lookup.UpdatedAt = timestamp
	}
	changed = stepChanged
	changed = refreshMemoryWorkflowStatus(wf, timestamp) || changed
	if changed {
		wf.UpdatedAt = timestamp
	}
	return changed
}

func recordMemoryWorkflowCapture(wf *MemoryWorkflow, actor string, artifact MemoryWorkflowArtifact, timestamp string) bool {
	return recordMemoryWorkflowArtifact(wf, actor, artifact, timestamp, MemoryWorkflowStepCapture)
}

func recordMemoryWorkflowPromotion(wf *MemoryWorkflow, actor string, artifact MemoryWorkflowArtifact, timestamp string) bool {
	return recordMemoryWorkflowArtifact(wf, actor, artifact, timestamp, MemoryWorkflowStepPromote)
}

func recordMemoryWorkflowArtifact(wf *MemoryWorkflow, actor string, artifact MemoryWorkflowArtifact, timestamp string, step MemoryWorkflowStep) bool {
	if wf == nil {
		return false
	}
	artifact = normalizeMemoryWorkflowArtifact(artifact, timestamp)
	if memoryWorkflowArtifactKey(artifact) == "" {
		return false
	}
	changed := false
	stepChanged := false
	switch step {
	case MemoryWorkflowStepCapture:
		if appendMemoryWorkflowArtifact(&wf.Captures, artifact) {
			stepChanged = true
		}
		if wf.Capture.Actor != strings.TrimSpace(actor) {
			wf.Capture.Actor = strings.TrimSpace(actor)
			stepChanged = true
		}
		if wf.Capture.CompletedAt == "" {
			wf.Capture.CompletedAt = timestamp
			stepChanged = true
		}
		if stepChanged && wf.Capture.UpdatedAt != timestamp {
			wf.Capture.UpdatedAt = timestamp
		}
	case MemoryWorkflowStepPromote:
		if appendMemoryWorkflowArtifact(&wf.Promotions, artifact) {
			stepChanged = true
		}
		if wf.Promote.Actor != strings.TrimSpace(actor) {
			wf.Promote.Actor = strings.TrimSpace(actor)
			stepChanged = true
		}
		if wf.Promote.CompletedAt == "" {
			wf.Promote.CompletedAt = timestamp
			stepChanged = true
		}
		if stepChanged && wf.Promote.UpdatedAt != timestamp {
			wf.Promote.UpdatedAt = timestamp
		}
	}
	changed = stepChanged
	changed = refreshMemoryWorkflowStatus(wf, timestamp) || changed
	if changed {
		wf.UpdatedAt = timestamp
	}
	return changed
}

func appendContextCitation(citations *[]ContextCitation, citation ContextCitation) bool {
	if !contextCitationHasEvidence(citation) {
		return false
	}
	for i := range *citations {
		if contextCitationKey((*citations)[i]) == contextCitationKey(citation) {
			merged := mergeContextCitation((*citations)[i], citation)
			changed := !contextCitationEqual((*citations)[i], merged)
			(*citations)[i] = merged
			return changed
		}
	}
	*citations = append(*citations, citation)
	return true
}

func contextCitationHasEvidence(citation ContextCitation) bool {
	return strings.TrimSpace(citation.Source) != "" ||
		strings.TrimSpace(citation.SourceID) != "" ||
		strings.TrimSpace(citation.Path) != "" ||
		strings.TrimSpace(citation.PageID) != "" ||
		strings.TrimSpace(citation.ChunkID) != "" ||
		strings.TrimSpace(citation.SourceURL) != "" ||
		strings.TrimSpace(citation.Title) != "" ||
		strings.TrimSpace(citation.Snippet) != "" ||
		citation.LineStart > 0 ||
		citation.LineEnd > 0
}

func appendMemoryWorkflowArtifact(artifacts *[]MemoryWorkflowArtifact, artifact MemoryWorkflowArtifact) bool {
	if memoryWorkflowArtifactKey(artifact) == "" {
		return false
	}
	for i := range *artifacts {
		if memoryWorkflowArtifactKey((*artifacts)[i]) == memoryWorkflowArtifactKey(artifact) {
			merged := mergeMemoryWorkflowArtifact((*artifacts)[i], artifact)
			changed := (*artifacts)[i] != merged
			(*artifacts)[i] = merged
			return changed
		}
	}
	*artifacts = append(*artifacts, artifact)
	return true
}

func contextCitationEqual(a, b ContextCitation) bool {
	if a.Backend != b.Backend || a.Source != b.Source || a.SourceID != b.SourceID ||
		a.Path != b.Path || a.PageID != b.PageID || a.ChunkID != b.ChunkID ||
		a.SourceURL != b.SourceURL || a.LineStart != b.LineStart || a.LineEnd != b.LineEnd ||
		a.Title != b.Title || a.Snippet != b.Snippet || a.RetrievedAt != b.RetrievedAt {
		return false
	}
	if (a.Score == nil) != (b.Score == nil) || (a.Stale == nil) != (b.Stale == nil) {
		return false
	}
	if a.Score != nil && b.Score != nil && *a.Score != *b.Score {
		return false
	}
	if a.Stale != nil && b.Stale != nil && *a.Stale != *b.Stale {
		return false
	}
	return true
}

func normalizeContextCitation(citation ContextCitation, timestamp string) ContextCitation {
	citation.Backend = strings.TrimSpace(citation.Backend)
	citation.Source = strings.TrimSpace(citation.Source)
	citation.SourceID = strings.TrimSpace(citation.SourceID)
	citation.Path = strings.TrimSpace(citation.Path)
	citation.PageID = strings.TrimSpace(citation.PageID)
	citation.ChunkID = strings.TrimSpace(citation.ChunkID)
	citation.SourceURL = strings.TrimSpace(citation.SourceURL)
	citation.Title = strings.TrimSpace(citation.Title)
	citation.Snippet = strings.TrimSpace(citation.Snippet)
	if citation.RetrievedAt == "" {
		citation.RetrievedAt = timestamp
	}
	return citation
}

func normalizeMemoryWorkflowArtifact(artifact MemoryWorkflowArtifact, timestamp string) MemoryWorkflowArtifact {
	artifact.Backend = strings.TrimSpace(artifact.Backend)
	artifact.Source = strings.TrimSpace(artifact.Source)
	artifact.Path = strings.TrimSpace(artifact.Path)
	artifact.PageID = strings.TrimSpace(artifact.PageID)
	artifact.PromotionID = strings.TrimSpace(artifact.PromotionID)
	artifact.EntityKind = strings.TrimSpace(artifact.EntityKind)
	artifact.EntitySlug = strings.TrimSpace(artifact.EntitySlug)
	artifact.PlaybookSlug = strings.TrimSpace(artifact.PlaybookSlug)
	artifact.Title = strings.TrimSpace(artifact.Title)
	artifact.SkipReason = strings.TrimSpace(artifact.SkipReason)
	artifact.Snippet = strings.TrimSpace(artifact.Snippet)
	artifact.CommitSHA = strings.TrimSpace(artifact.CommitSHA)
	artifact.State = strings.TrimSpace(artifact.State)
	if artifact.RecordedAt == "" {
		artifact.RecordedAt = timestamp
	}
	if artifact.UpdatedAt == "" {
		artifact.UpdatedAt = timestamp
	}
	return artifact
}

func contextCitationKey(citation ContextCitation) string {
	parts := []string{
		citation.Backend,
		citation.Source,
		citation.SourceID,
		citation.Path,
		citation.PageID,
		citation.ChunkID,
		citation.SourceURL,
	}
	if citation.LineStart > 0 {
		parts = append(parts, fmt.Sprintf("%d", citation.LineStart))
	}
	if citation.LineEnd > 0 {
		parts = append(parts, fmt.Sprintf("%d", citation.LineEnd))
	}
	key := strings.Trim(strings.Join(parts, "|"), "|")
	if key == "" {
		key = strings.TrimSpace(citation.Title + "|" + citation.Snippet)
	}
	return key
}

func memoryWorkflowArtifactKey(artifact MemoryWorkflowArtifact) string {
	parts := []string{
		artifact.Backend,
		artifact.Source,
		artifact.Path,
		artifact.PageID,
		artifact.PromotionID,
		artifact.EntityKind,
		artifact.EntitySlug,
		artifact.PlaybookSlug,
		artifact.SkipReason,
	}
	return strings.Trim(strings.Join(parts, "|"), "|")
}

func mergeContextCitation(existing, incoming ContextCitation) ContextCitation {
	if existing.Backend == "" {
		existing.Backend = incoming.Backend
	}
	if existing.Source == "" {
		existing.Source = incoming.Source
	}
	if existing.SourceID == "" {
		existing.SourceID = incoming.SourceID
	}
	if existing.Path == "" {
		existing.Path = incoming.Path
	}
	if existing.PageID == "" {
		existing.PageID = incoming.PageID
	}
	if existing.ChunkID == "" {
		existing.ChunkID = incoming.ChunkID
	}
	if existing.SourceURL == "" {
		existing.SourceURL = incoming.SourceURL
	}
	if existing.LineStart == 0 {
		existing.LineStart = incoming.LineStart
	}
	if existing.LineEnd == 0 {
		existing.LineEnd = incoming.LineEnd
	}
	if existing.Title == "" {
		existing.Title = incoming.Title
	}
	if existing.Snippet == "" {
		existing.Snippet = incoming.Snippet
	}
	if existing.Score == nil {
		existing.Score = incoming.Score
	}
	if existing.Stale == nil {
		existing.Stale = incoming.Stale
	}
	if existing.RetrievedAt == "" {
		existing.RetrievedAt = incoming.RetrievedAt
	}
	return existing
}

func mergeMemoryWorkflowArtifact(existing, incoming MemoryWorkflowArtifact) MemoryWorkflowArtifact {
	before := existing
	if existing.Backend == "" {
		existing.Backend = incoming.Backend
	}
	if existing.Source == "" {
		existing.Source = incoming.Source
	}
	if existing.Path == "" {
		existing.Path = incoming.Path
	}
	if existing.PageID == "" {
		existing.PageID = incoming.PageID
	}
	if existing.PromotionID == "" {
		existing.PromotionID = incoming.PromotionID
	}
	if existing.EntityKind == "" {
		existing.EntityKind = incoming.EntityKind
	}
	if existing.EntitySlug == "" {
		existing.EntitySlug = incoming.EntitySlug
	}
	if existing.PlaybookSlug == "" {
		existing.PlaybookSlug = incoming.PlaybookSlug
	}
	if existing.Title == "" {
		existing.Title = incoming.Title
	}
	if incoming.SkipReason != "" && incoming.SkipReason != existing.SkipReason {
		existing.SkipReason = incoming.SkipReason
		existing.Title = incoming.Title
	}
	if existing.Snippet == "" {
		existing.Snippet = incoming.Snippet
	}
	if existing.CommitSHA == "" {
		existing.CommitSHA = incoming.CommitSHA
	}
	if incoming.State != "" {
		existing.State = incoming.State
	}
	if existing.RecordedAt == "" {
		existing.RecordedAt = incoming.RecordedAt
	}
	contentChanged := existing.Backend != before.Backend ||
		existing.Source != before.Source ||
		existing.Path != before.Path ||
		existing.PageID != before.PageID ||
		existing.PromotionID != before.PromotionID ||
		existing.EntityKind != before.EntityKind ||
		existing.EntitySlug != before.EntitySlug ||
		existing.PlaybookSlug != before.PlaybookSlug ||
		existing.Title != before.Title ||
		existing.SkipReason != before.SkipReason ||
		existing.Snippet != before.Snippet ||
		existing.CommitSHA != before.CommitSHA ||
		existing.State != before.State ||
		existing.RecordedAt != before.RecordedAt ||
		existing.Missing != incoming.Missing
	if incoming.UpdatedAt != "" && (existing.UpdatedAt == "" || contentChanged) {
		existing.UpdatedAt = incoming.UpdatedAt
	}
	existing.Missing = incoming.Missing
	return existing
}

// refreshMemoryWorkflowStatus records progress without importing GawkBot's
// mandatory task-completion gate. EmployeeLoop steps are always informational.
func refreshMemoryWorkflowStatus(wf *MemoryWorkflow, timestamp string) bool {
	if wf == nil {
		return false
	}
	oldStatus, oldLookup, oldCapture, oldPromote := wf.Status, wf.Lookup, wf.Capture, wf.Promote
	refreshMemoryWorkflowStepStatus(wf, MemoryWorkflowStepLookup)
	refreshMemoryWorkflowStepStatus(wf, MemoryWorkflowStepCapture)
	refreshMemoryWorkflowStepStatus(wf, MemoryWorkflowStepPromote)
	wf.Required = false
	wf.Status = MemoryWorkflowStatusNotRequired
	wf.CompletedAt = ""
	return oldStatus != wf.Status || oldLookup != wf.Lookup || oldCapture != wf.Capture || oldPromote != wf.Promote
}
