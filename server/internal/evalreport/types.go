// Package evalreport validates and stores immutable locally reported evaluation runs.
package evalreport

import (
	"errors"
	"time"
)

const MaxSubmissionBytes = 2 << 20

var (
	ErrInvalid     = errors.New("invalid evaluation report")
	ErrConflict    = errors.New("evaluation run already has different content")
	ErrNotFound    = errors.New("evaluation report or workspace is not visible")
	ErrUnavailable = errors.New("evaluation report storage unavailable")
)

// ValidationError only includes controlled field names and reasons, never input values.
type ValidationError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func (e *ValidationError) Error() string {
	return "invalid evaluation report: " + e.Field + ": " + e.Reason
}
func (e *ValidationError) Unwrap() error { return ErrInvalid }

type Runner struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Catalog struct {
	Revision string `json:"revision"`
	Dirty    bool   `json:"dirty"`
	SHA256   string `json:"sha256"`
}

type CaseDefinition struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Kind       string   `json:"kind"`
	ScenarioID string   `json:"scenario_id"`
	Roles      []string `json:"roles"`
	Verifies   []string `json:"verifies"`
	Method     []string `json:"method"`
}

type Evidence struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
}

type CaseResult struct {
	CaseID   string     `json:"case_id"`
	Status   string     `json:"status"`
	Summary  string     `json:"summary"`
	Evidence []Evidence `json:"evidence"`
}

type Submission struct {
	SchemaVersion  int              `json:"schema_version"`
	RunID          string           `json:"run_id"`
	Title          string           `json:"title"`
	ExecutionKind  string           `json:"execution_kind"`
	Environment    string           `json:"environment"`
	TargetRevision string           `json:"target_revision"`
	Runner         Runner           `json:"runner"`
	StartedAt      time.Time        `json:"started_at"`
	FinishedAt     time.Time        `json:"finished_at"`
	Catalog        Catalog          `json:"catalog"`
	SelectedCases  []CaseDefinition `json:"selected_cases"`
	Results        []CaseResult     `json:"results"`
}

// Summary is derived by the server; client totals or acceptance claims are not inputs.
type Summary struct {
	Total       int     `json:"total"`
	Pass        int     `json:"pass"`
	Fail        int     `json:"fail"`
	Blocked     int     `json:"blocked"`
	Incomplete  int     `json:"incomplete"`
	Skipped     int     `json:"skipped"`
	PassRate    float64 `json:"pass_rate"`
	Conclusion  string  `json:"conclusion"`
	P0Selected  int     `json:"p0_selected"`
	P0Total     int     `json:"p0_total"`
	RealE2EPass int     `json:"real_e2e_pass"`
}

type Record struct {
	ID               string     `json:"id"`
	WorkspaceID      string     `json:"workspace_id"`
	WorkspaceName    string     `json:"workspace_name"`
	SubmittedBy      string     `json:"submitted_by"`
	ReceivedAt       time.Time  `json:"received_at"`
	ContentSHA256    string     `json:"content_sha256"`
	DefinitionSHA256 string     `json:"definition_sha256"`
	Submission       Submission `json:"submission"`
	Summary          Summary    `json:"summary"`
}
