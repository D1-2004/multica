package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
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

type AgentOKRKeyResultDTO struct {
	Text  string `json:"text"`
	Label string `json:"label"`
	Color string `json:"color"`
}

type AgentOKRDTO struct {
	Objective  string                 `json:"objective"`
	Label      string                 `json:"label"`
	Color      string                 `json:"color"`
	KeyResults []AgentOKRKeyResultDTO `json:"key_results"`
}

type AgentOKRResponse struct {
	OKRs []AgentOKRDTO `json:"okrs"`
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
	writeJSON(w, http.StatusOK, AgentOKRResponse{OKRs: agentOKRsFromRows(rows)})
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

	if err := queries.DeleteAgentOKRsByAgent(r.Context(), db.DeleteAgentOKRsByAgentParams{
		AgentID:     agent.ID,
		WorkspaceID: agent.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to replace agent OKRs")
		return
	}

	for objectiveIndex, entry := range normalized {
		objectiveLabel, err := queries.UpsertAgentOKRLabel(r.Context(), db.UpsertAgentOKRLabelParams{
			WorkspaceID: agent.WorkspaceID,
			Name:        agentOKRObjectivePrefix + entry.objective,
			Description: agentOKRLabelDescription,
			Color:       agentOKRObjectiveColor,
		})
		if err != nil {
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
				WorkspaceID: agent.WorkspaceID,
				Name:        agentOKRKeyResultPrefix + keyResult,
				Description: agentOKRLabelDescription,
				Color:       agentOKRKeyResultColor,
			})
			if err != nil {
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
	writeJSON(w, http.StatusOK, AgentOKRResponse{OKRs: agentOKRsFromRows(rows)})
}

func agentOKRsFromRows(rows []db.ListAgentOKRsRow) []AgentOKRDTO {
	okrs := make([]AgentOKRDTO, 0)
	byObjectiveID := map[string]int{}
	for _, row := range rows {
		if row.Kind != "objective" {
			continue
		}
		byObjectiveID[uuidToString(row.ID)] = len(okrs)
		okrs = append(okrs, AgentOKRDTO{
			Objective:  strings.TrimPrefix(row.LabelName, agentOKRObjectivePrefix),
			Label:      row.LabelName,
			Color:      row.LabelColor,
			KeyResults: []AgentOKRKeyResultDTO{},
		})
	}
	for _, row := range rows {
		if row.Kind != "key_result" || !row.ParentID.Valid {
			continue
		}
		index, ok := byObjectiveID[uuidToString(row.ParentID)]
		if !ok {
			continue
		}
		okrs[index].KeyResults = append(okrs[index].KeyResults, AgentOKRKeyResultDTO{
			Text:  strings.TrimPrefix(row.LabelName, agentOKRKeyResultPrefix),
			Label: row.LabelName,
			Color: row.LabelColor,
		})
	}
	return okrs
}

// buildAgentOKRInstructions renders the OKR section appended to an agent's
// instructions. It names the exact label strings and the exact command, because
// an agent told only "tag the issue" invents labels and the catalog fragments.
func buildAgentOKRInstructions(okrs []AgentOKRDTO) string {
	if len(okrs) == 0 {
		return ""
	}
	var section strings.Builder
	section.WriteString("## OKR Tagging\n\n")
	section.WriteString("Tag every Issue you own with the OKR labels it advances. ")
	section.WriteString("Use only the label names listed below, exactly as written — do not invent, translate, or reword them, and do not create new labels.\n\n")
	for _, okr := range okrs {
		section.WriteString("- " + okr.Label + "\n")
		for _, keyResult := range okr.KeyResults {
			section.WriteString("  - " + keyResult.Label + "\n")
		}
	}
	section.WriteString("\nAttach a label with `multica issue label add <issue> \"<label name>\"`. ")
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
	return buildAgentOKRInstructions(agentOKRsFromRows(rows))
}
