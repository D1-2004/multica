package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Agent OKRs exist to make an agent's tagging deterministic. Every objective and
// key result is materialized as a real workspace label, and the label catalog is
// injected into the agent's instructions, so the agent picks from a fixed set
// instead of inventing a tag per run.
//
// The labels are created in the 'issue' namespace deliberately. The 'agent'
// namespace exists but cannot be attached to an issue — both the attach query
// and the handler reject a namespace mismatch — and tagging issues is the point.
const (
	maxAgentOKRObjectives    = 10
	maxAgentOKRKeyResults    = 10
	maxAgentOKRTextLength    = 120
	agentOKRObjectivePrefix  = "O: "
	agentOKRKeyResultPrefix  = "KR: "
	agentOKRObjectiveColor   = "#6366f1"
	agentOKRKeyResultColor   = "#0ea5e9"
	agentOKRLabelDescription = "Multica Agent OKR"
)

// AgentOKRSpend is what one objective/label combination has cost so far,
// rolled up from every task on every Issue carrying its label. Collaboration
// is intentional: callers that need self-vs-collaborator split use the label
// usage task rows and their executor agent ids.
type AgentOKRSpend struct {
	TotalTokens       int64 `json:"total_tokens"`
	TotalCostUSDTicks int64 `json:"total_cost_usd_ticks"`
	UncostedTokens    int64 `json:"uncosted_tokens"`
	TaskCount         int64 `json:"task_count"`
	// UnpricedTaskCount is how many of those tasks ran on a model with no
	// price attached. The cost is a floor when this is non-zero, and the UI
	// says so rather than presenting a number that is quietly incomplete.
	UnpricedTaskCount int64 `json:"unpriced_task_count"`
}

type AgentOKRKeyResultDTO struct {
	ID       string         `json:"id"`
	LabelID  string         `json:"label_id"`
	Position int32          `json:"position"`
	Text     string         `json:"text"`
	Label    string         `json:"label"`
	Color    string         `json:"color"`
	Spend    *AgentOKRSpend `json:"spend,omitempty"`
}

type AgentOKRDTO struct {
	ID        string `json:"id"`
	LabelID   string `json:"label_id"`
	Position  int32  `json:"position"`
	Objective string `json:"objective"`
	Label     string `json:"label"`
	Color     string `json:"color"`
	// Spend is this objective's own label only. It is not the sum of its key
	// results: an Issue may carry the objective label, a key result label, or
	// both, so adding them would double-count the overlap.
	Spend      *AgentOKRSpend         `json:"spend,omitempty"`
	KeyResults []AgentOKRKeyResultDTO `json:"key_results"`
}

type AgentOKRResponse struct {
	OKRs           []AgentOKRDTO `json:"okrs"`
	UsageAvailable bool          `json:"usage_available"`
}

// SetAgentOKRsRequest replaces the whole set. Partial edits are not offered:
// OKRs are read as a list, and a rewrite keeps ordering and the O -> KR
// grouping unambiguous without a reconciliation protocol.
type SetAgentOKRsRequest struct {
	OKRs []struct {
		Objective  string   `json:"objective"`
		KeyResults []string `json:"key_results"`
	} `json:"okrs"`
}

func (h *Handler) ListAgentOKRs(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListAgentOKRs(r.Context(), db.ListAgentOKRsParams{
		AgentID:     agent.ID,
		WorkspaceID: agent.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent OKRs")
		return
	}
	spend, usageAvailable := h.agentOKRSpendFor(r.Context(), agent.ID, agent.WorkspaceID)
	writeJSON(w, http.StatusOK, AgentOKRResponse{
		OKRs:           agentOKRsFromRows(rows, spend, usageAvailable),
		UsageAvailable: usageAvailable,
	})
}

func (h *Handler) SetAgentOKRs(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	var request SetAgentOKRsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(request.OKRs) > maxAgentOKRObjectives {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d objectives are supported", maxAgentOKRObjectives))
		return
	}

	type normalizedOKR struct {
		objective  string
		keyResults []string
	}
	normalized := make([]normalizedOKR, 0, len(request.OKRs))
	seenLabels := make(map[string]struct{})
	for _, entry := range request.OKRs {
		objective := strings.TrimSpace(entry.Objective)
		if objective == "" {
			writeError(w, http.StatusBadRequest, "each objective must have text")
			return
		}
		if utf8.RuneCountInString(objective) > maxAgentOKRTextLength {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("objective must be %d characters or fewer", maxAgentOKRTextLength))
			return
		}
		objectiveLabelKey := strings.ToLower(agentOKRObjectivePrefix + objective)
		if _, duplicate := seenLabels[objectiveLabelKey]; duplicate {
			writeError(w, http.StatusBadRequest, "objectives must be unique within an agent")
			return
		}
		seenLabels[objectiveLabelKey] = struct{}{}
		if len(entry.KeyResults) > maxAgentOKRKeyResults {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d key results per objective", maxAgentOKRKeyResults))
			return
		}
		keyResults := make([]string, 0, len(entry.KeyResults))
		for _, raw := range entry.KeyResults {
			keyResult := strings.TrimSpace(raw)
			if keyResult == "" {
				continue
			}
			if utf8.RuneCountInString(keyResult) > maxAgentOKRTextLength {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("key result must be %d characters or fewer", maxAgentOKRTextLength))
				return
			}
			keyResultLabelKey := strings.ToLower(agentOKRKeyResultPrefix + keyResult)
			if _, duplicate := seenLabels[keyResultLabelKey]; duplicate {
				writeError(w, http.StatusBadRequest, "key results must be unique within an agent")
				return
			}
			seenLabels[keyResultLabelKey] = struct{}{}
			keyResults = append(keyResults, keyResult)
		}
		normalized = append(normalized, normalizedOKR{objective: objective, keyResults: keyResults})
	}

	// Label creation and the rewrite commit together: a partially written OKR
	// set would leave the agent tagging against labels that do not match what
	// the settings page shows.
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := h.Queries.WithTx(tx)
	reusableLabelIDs, err := queries.ListAgentOKRLabelIDs(r.Context(), db.ListAgentOKRLabelIDsParams{
		AgentID:     agent.ID,
		WorkspaceID: agent.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load current Agent OKR labels")
		return
	}

	if err := queries.DeleteAgentOKRsByAgent(r.Context(), db.DeleteAgentOKRsByAgentParams{
		AgentID:     agent.ID,
		WorkspaceID: agent.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to replace agent OKRs")
		return
	}

	for objectiveIndex, entry := range normalized {
		objectiveLabel, err := queries.UpsertAgentOKRLabel(r.Context(), db.UpsertAgentOKRLabelParams{
			WorkspaceID:      agent.WorkspaceID,
			Name:             agentOKRObjectivePrefix + entry.objective,
			Description:      agentOKRLabelDescription,
			Color:            agentOKRObjectiveColor,
			ReusableLabelIds: reusableLabelIDs,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) || isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "objective label name is already used outside this Agent's current OKR set")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to create objective label")
			return
		}
		objectiveRow, err := queries.CreateAgentOKR(r.Context(), db.CreateAgentOKRParams{
			WorkspaceID: agent.WorkspaceID,
			AgentID:     agent.ID,
			Kind:        "objective",
			LabelID:     objectiveLabel.ID,
			Position:    int32(objectiveIndex),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save objective")
			return
		}
		for keyResultIndex, keyResult := range entry.keyResults {
			keyResultLabel, err := queries.UpsertAgentOKRLabel(r.Context(), db.UpsertAgentOKRLabelParams{
				WorkspaceID:      agent.WorkspaceID,
				Name:             agentOKRKeyResultPrefix + keyResult,
				Description:      agentOKRLabelDescription,
				Color:            agentOKRKeyResultColor,
				ReusableLabelIds: reusableLabelIDs,
			})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) || isUniqueViolation(err) {
					writeError(w, http.StatusConflict, "key-result label name is already used outside this Agent's current OKR set")
					return
				}
				writeError(w, http.StatusInternalServerError, "failed to create key result label")
				return
			}
			if _, err := queries.CreateAgentOKR(r.Context(), db.CreateAgentOKRParams{
				WorkspaceID: agent.WorkspaceID,
				AgentID:     agent.ID,
				Kind:        "key_result",
				ParentID:    objectiveRow.ID,
				LabelID:     keyResultLabel.ID,
				Position:    int32(keyResultIndex),
			}); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to save key result")
				return
			}
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent OKRs")
		return
	}

	rows, err := h.Queries.ListAgentOKRs(r.Context(), db.ListAgentOKRsParams{
		AgentID:     agent.ID,
		WorkspaceID: agent.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload agent OKRs")
		return
	}
	spend, usageAvailable := h.agentOKRSpendFor(r.Context(), agent.ID, agent.WorkspaceID)
	writeJSON(w, http.StatusOK, AgentOKRResponse{
		OKRs:           agentOKRsFromRows(rows, spend, usageAvailable),
		UsageAvailable: usageAvailable,
	})
}

func agentOKRsFromRows(rows []db.ListAgentOKRsRow, spend map[string]AgentOKRSpend, usageAvailable bool) []AgentOKRDTO {
	okrs := make([]AgentOKRDTO, 0)
	byObjectiveID := map[string]int{}
	for _, row := range rows {
		if row.Kind != "objective" {
			continue
		}
		byObjectiveID[uuidToString(row.ID)] = len(okrs)
		entry := AgentOKRDTO{
			ID:         uuidToString(row.ID),
			LabelID:    uuidToString(row.LabelID),
			Position:   row.Position,
			Objective:  strings.TrimPrefix(row.LabelName, agentOKRObjectivePrefix),
			Label:      row.LabelName,
			Color:      row.LabelColor,
			KeyResults: []AgentOKRKeyResultDTO{},
		}
		if usageAvailable {
			value := spend[uuidToString(row.LabelID)]
			entry.Spend = &value
		}
		okrs = append(okrs, entry)
	}
	for _, row := range rows {
		if row.Kind != "key_result" || !row.ParentID.Valid {
			continue
		}
		index, ok := byObjectiveID[uuidToString(row.ParentID)]
		if !ok {
			continue
		}
		entry := AgentOKRKeyResultDTO{
			ID:       uuidToString(row.ID),
			LabelID:  uuidToString(row.LabelID),
			Position: row.Position,
			Text:     strings.TrimPrefix(row.LabelName, agentOKRKeyResultPrefix),
			Label:    row.LabelName,
			Color:    row.LabelColor,
		}
		if usageAvailable {
			value := spend[uuidToString(row.LabelID)]
			entry.Spend = &value
		}
		okrs[index].KeyResults = append(okrs[index].KeyResults, entry)
	}
	return okrs
}

// agentOKRSpendFor rolls up spend per OKR label. A failure is not fatal to the
// page: the objectives still render, with spend omitted rather than the whole
// request failing over a reporting number.
func (h *Handler) agentOKRSpendFor(ctx context.Context, agentID, workspaceID pgtype.UUID) (map[string]AgentOKRSpend, bool) {
	pricingJSON, err := h.currentConfig().ModelPricing.SQLJSON()
	if err != nil {
		slog.Error("encode model pricing for Agent OKR usage", "agent_id", uuidToString(agentID), "error", err)
		return nil, false
	}
	rows, err := h.Queries.ListAgentOKRUsage(ctx, db.ListAgentOKRUsageParams{
		AgentID:      agentID,
		WorkspaceID:  workspaceID,
		ModelPricing: pricingJSON,
	})
	if err != nil {
		slog.Warn("ListAgentOKRUsage failed", "agent_id", uuidToString(agentID), "error", err)
		return nil, false
	}
	spend := make(map[string]AgentOKRSpend, len(rows))
	for _, row := range rows {
		spend[uuidToString(row.LabelID)] = AgentOKRSpend{
			TotalTokens:       row.TotalTokens,
			TotalCostUSDTicks: row.TotalCostUsdTicks,
			UncostedTokens:    row.UncostedTokens,
			TaskCount:         row.TaskCount,
			UnpricedTaskCount: row.UnpricedTaskCount,
		}
	}
	return spend, true
}

// buildAgentOKRInstructions renders the objectives section appended to an
// agent's instructions.
//
// Objectives lead. These are what the agent is being measured on, and they
// belong in the prompt for the same reason a person's goals belong in their
// brief: they change what work is worth doing and how a judgment call gets
// made. Framing the whole section as a tagging vocabulary — which the first
// version did — reduced a goal to a label picker and told the model nothing
// about why the goal existed.
//
// Tagging is a consequence, so it comes second and only exists because the
// objectives above it do. It still names the exact label strings and the exact
// command: an agent told only "tag the issue" invents labels and the catalog
// fragments.
//
// Returns empty when there are no objectives. Nothing is injected in that case
// — not a heading, not an empty list. An agent with no objectives should read
// a prompt that never mentions them.
func buildAgentOKRInstructions(okrs []AgentOKRDTO) string {
	if len(okrs) == 0 {
		return ""
	}
	var section strings.Builder
	section.WriteString("## Objectives\n\n")
	section.WriteString("These are the objectives you are accountable for. ")
	section.WriteString("Let them inform what you prioritize, what you push back on, and how you judge whether a piece of work is worth doing. ")
	section.WriteString("They do not override your Agent Identity or any explicit instruction in a task — when they conflict, the task wins and you say so.\n\n")

	for _, okr := range okrs {
		section.WriteString("- **" + okr.Objective + "**\n")
		for _, keyResult := range okr.KeyResults {
			section.WriteString("  - " + keyResult.Text + "\n")
		}
	}

	section.WriteString("\n### Tagging\n\n")
	section.WriteString("When work you own advances one of the objectives above, tag its Issue so the contribution is attributable. ")
	section.WriteString("Use only these label names, exactly as written — do not invent, translate, or reword them, and do not create new labels:\n\n")
	for _, okr := range okrs {
		section.WriteString("- `" + okr.Label + "`\n")
		for _, keyResult := range okr.KeyResults {
			section.WriteString("  - `" + keyResult.Label + "`\n")
		}
	}
	section.WriteString("\nAttach one with `multica issue label add <issue> \"<label name>\"`. ")
	section.WriteString("Attach the objective label plus every key result the work actually advances. ")
	section.WriteString("If none of them apply, attach nothing and say so in your result rather than forcing an unrelated tag.")
	return section.String()
}

// agentOKRInstructionsFor loads and renders an agent's OKR section. A failure
// is not fatal to the claim: the agent runs without OKR tagging rather than not
// running at all.
func (h *Handler) agentOKRInstructionsFor(ctx context.Context, agentID, workspaceID pgtype.UUID) string {
	rows, err := h.Queries.ListAgentOKRs(ctx, db.ListAgentOKRsParams{
		AgentID:     agentID,
		WorkspaceID: workspaceID,
	})
	if err != nil || len(rows) == 0 {
		return ""
	}
	return buildAgentOKRInstructions(agentOKRsFromRows(rows, nil, false))
}
