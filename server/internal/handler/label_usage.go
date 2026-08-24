package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// LabelUsageSummaryResponse preserves the same authoritative-cost split as
// issue and dashboard usage: provider-reported cost is summed as-is, while
// tokens from rows without a provider cost remain explicit for estimation by
// the client. A task is priced only when it has at least one usage row and all
// of its rows carry authoritative cost.
type LabelUsageSummaryResponse struct {
	TotalTokens       int64 `json:"total_tokens"`
	TotalCostUSDTicks int64 `json:"total_cost_usd_ticks"`
	UncostedTokens    int64 `json:"uncosted_tokens"`
	TaskCount         int64 `json:"task_count"`
	PricedTaskCount   int64 `json:"priced_task_count"`
	UnpricedTaskCount int64 `json:"unpriced_task_count"`
}

type LabelUsageDailyResponse struct {
	Date              string `json:"date"`
	TotalTokens       int64  `json:"total_tokens"`
	TotalCostUSDTicks int64  `json:"total_cost_usd_ticks"`
	UncostedTokens    int64  `json:"uncosted_tokens"`
	TaskCount         int64  `json:"task_count"`
	PricedTaskCount   int64  `json:"priced_task_count"`
	UnpricedTaskCount int64  `json:"unpriced_task_count"`
}

type LabelUsageBreakdownResponse struct {
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	TotalTokens       int64  `json:"total_tokens"`
	TotalCostUSDTicks int64  `json:"total_cost_usd_ticks"`
	UncostedTokens    int64  `json:"uncosted_tokens"`
	TaskCount         int64  `json:"task_count"`
	UnpricedTaskCount int64  `json:"unpriced_task_count"`
}

type LabelUsageTaskResponse struct {
	TaskID            string          `json:"task_id"`
	IssueID           string          `json:"issue_id"`
	IssueIdentifier   string          `json:"issue_identifier"`
	IssueTitle        string          `json:"issue_title"`
	Status            string          `json:"status"`
	Provider          string          `json:"provider"`
	Model             string          `json:"model"`
	TotalTokens       int64           `json:"total_tokens"`
	TotalCostUSDTicks int64           `json:"total_cost_usd_ticks"`
	UncostedTokens    int64           `json:"uncosted_tokens"`
	HasUsage          bool            `json:"has_usage"`
	IsPriced          bool            `json:"is_priced"`
	CreatedAt         string          `json:"created_at"`
	CompletedAt       *string         `json:"completed_at,omitempty"`
	ActivityAt        string          `json:"activity_at"`
	UsageBreakdown    json.RawMessage `json:"usage_breakdown"`
}

type LabelUsagePaginationResponse struct {
	Page       int   `json:"page"`
	PageSize   int   `json:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int64 `json:"total_pages"`
}

type LabelUsageDetailResponse struct {
	Label      LabelResponse                 `json:"label"`
	Period     string                        `json:"period"`
	Timezone   string                        `json:"timezone"`
	Sort       string                        `json:"sort"`
	Direction  string                        `json:"direction"`
	Summary    LabelUsageSummaryResponse     `json:"summary"`
	Daily      []LabelUsageDailyResponse     `json:"daily"`
	Breakdown  []LabelUsageBreakdownResponse `json:"breakdown"`
	Tasks      []LabelUsageTaskResponse      `json:"tasks"`
	Pagination LabelUsagePaginationResponse  `json:"pagination"`
}

type labelUsageQuery struct {
	period    string
	since     pgtype.Timestamptz
	timezone  string
	sort      string
	direction string
	page      int
	pageSize  int
}

func (h *Handler) parseLabelUsageQuery(w http.ResponseWriter, r *http.Request) (labelUsageQuery, bool) {
	if rawTZ := strings.TrimSpace(r.URL.Query().Get("tz")); rawTZ != "" {
		if loc, err := time.LoadLocation(rawTZ); err != nil || loc == nil {
			writeError(w, http.StatusBadRequest, "tz must be a valid IANA timezone")
			return labelUsageQuery{}, false
		}
	}
	q := labelUsageQuery{
		period:    strings.TrimSpace(r.URL.Query().Get("period")),
		timezone:  h.resolveViewingTZ(r),
		sort:      strings.TrimSpace(r.URL.Query().Get("sort")),
		direction: strings.TrimSpace(r.URL.Query().Get("direction")),
		page:      1,
		pageSize:  25,
	}
	if q.period == "" {
		q.period = "all"
	}
	if q.sort == "" {
		q.sort = "cost"
	}
	if q.direction == "" {
		q.direction = "desc"
	}

	days := 0
	switch q.period {
	case "7d":
		days = 7
	case "30d":
		days = 30
	case "90d":
		days = 90
	case "all":
	default:
		writeError(w, http.StatusBadRequest, "period must be 7d, 30d, 90d, or all")
		return labelUsageQuery{}, false
	}
	if days > 0 {
		loc, err := time.LoadLocation(q.timezone)
		if err != nil {
			loc = time.UTC
			q.timezone = "UTC"
		}
		q.since = pgtype.Timestamptz{
			Time:  sinceFromDays(time.Now(), days-1, loc),
			Valid: true,
		}
	}

	switch q.sort {
	case "cost", "tokens", "recent":
	default:
		writeError(w, http.StatusBadRequest, "sort must be cost, tokens, or recent")
		return labelUsageQuery{}, false
	}
	if q.direction != "asc" && q.direction != "desc" {
		writeError(w, http.StatusBadRequest, "direction must be asc or desc")
		return labelUsageQuery{}, false
	}

	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		page, err := strconv.Atoi(raw)
		if err != nil || page < 1 {
			writeError(w, http.StatusBadRequest, "page must be a positive integer")
			return labelUsageQuery{}, false
		}
		q.page = page
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("page_size")); raw != "" {
		pageSize, err := strconv.Atoi(raw)
		if err != nil || pageSize < 1 || pageSize > 100 {
			writeError(w, http.StatusBadRequest, "page_size must be between 1 and 100")
			return labelUsageQuery{}, false
		}
		q.pageSize = pageSize
	}
	if int64(q.page-1)*int64(q.pageSize) > math.MaxInt32 {
		writeError(w, http.StatusBadRequest, "page is too large")
		return labelUsageQuery{}, false
	}
	return q, true
}

// GetLabelUsage returns the usage detail for the issues currently attached to
// one issue-scoped label. Agent and Skill labels deliberately have no usage
// surface: their assignments do not define issue/task ownership.
func (h *Handler) GetLabelUsage(w http.ResponseWriter, r *http.Request) {
	labelID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "label id")
	if !ok {
		return
	}
	workspaceIDString := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceIDString); !ok {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, workspaceIDString, "workspace id")
	if !ok {
		return
	}
	label, err := h.Queries.GetLabel(r.Context(), db.GetLabelParams{
		ID: labelID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "label not found")
			return
		}
		slog.Warn("GetLabel for usage failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get label usage")
		return
	}
	if label.ResourceType != "issue" {
		writeError(w, http.StatusNotFound, "issue label not found")
		return
	}

	query, ok := h.parseLabelUsageQuery(w, r)
	if !ok {
		return
	}
	commonSummaryParams := db.GetIssueLabelUsageSummaryParams{
		LabelID: labelID, WorkspaceID: workspaceID, Since: query.since,
	}
	summaryRow, err := h.Queries.GetIssueLabelUsageSummary(r.Context(), commonSummaryParams)
	if err != nil {
		slog.Warn("GetIssueLabelUsageSummary failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get label usage")
		return
	}
	dailyRows, err := h.Queries.ListIssueLabelUsageDaily(r.Context(), db.ListIssueLabelUsageDailyParams{
		Tz: query.timezone, LabelID: labelID, WorkspaceID: workspaceID, Since: query.since,
	})
	if err != nil {
		slog.Warn("ListIssueLabelUsageDaily failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get label usage")
		return
	}
	breakdownRows, err := h.Queries.ListIssueLabelUsageBreakdown(r.Context(), db.ListIssueLabelUsageBreakdownParams{
		Since: query.since, LabelID: labelID, WorkspaceID: workspaceID,
	})
	if err != nil {
		slog.Warn("ListIssueLabelUsageBreakdown failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get label usage")
		return
	}
	offset := (query.page - 1) * query.pageSize
	taskRows, err := h.Queries.ListIssueLabelUsageTasks(r.Context(), db.ListIssueLabelUsageTasksParams{
		Sort: query.sort, Direction: query.direction,
		Offset: int32(offset), PageSize: int32(query.pageSize),
		LabelID: labelID, WorkspaceID: workspaceID, Since: query.since,
	})
	if err != nil {
		slog.Warn("ListIssueLabelUsageTasks failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get label usage")
		return
	}

	summary := LabelUsageSummaryResponse{
		TotalTokens:       summaryRow.TotalTokens,
		TotalCostUSDTicks: summaryRow.TotalCostUsdTicks,
		UncostedTokens:    summaryRow.UncostedTokens,
		TaskCount:         summaryRow.TaskCount,
		PricedTaskCount:   summaryRow.PricedTaskCount,
		UnpricedTaskCount: summaryRow.UnpricedTaskCount,
	}
	daily := make([]LabelUsageDailyResponse, len(dailyRows))
	for i, row := range dailyRows {
		daily[i] = LabelUsageDailyResponse{
			Date:              row.Date.Time.Format("2006-01-02"),
			TotalTokens:       row.TotalTokens,
			TotalCostUSDTicks: row.TotalCostUsdTicks,
			UncostedTokens:    row.UncostedTokens,
			TaskCount:         row.TaskCount,
			PricedTaskCount:   row.PricedTaskCount,
			UnpricedTaskCount: row.UnpricedTaskCount,
		}
	}
	breakdown := make([]LabelUsageBreakdownResponse, len(breakdownRows))
	for i, row := range breakdownRows {
		breakdown[i] = LabelUsageBreakdownResponse{
			Provider:          row.Provider,
			Model:             row.Model,
			TotalTokens:       row.TotalTokens,
			TotalCostUSDTicks: row.TotalCostUsdTicks,
			UncostedTokens:    row.UncostedTokens,
			TaskCount:         row.TaskCount,
			UnpricedTaskCount: row.UnpricedTaskCount,
		}
	}
	tasks := make([]LabelUsageTaskResponse, len(taskRows))
	for i, row := range taskRows {
		usageBreakdown := json.RawMessage(row.UsageBreakdown)
		if !json.Valid(usageBreakdown) {
			slog.Warn("invalid label task usage breakdown", append(logger.RequestAttrs(r), "task_id", uuidToString(row.TaskID))...)
			writeError(w, http.StatusInternalServerError, "failed to get label usage")
			return
		}
		tasks[i] = LabelUsageTaskResponse{
			TaskID:            uuidToString(row.TaskID),
			IssueID:           uuidToString(row.IssueID),
			IssueIdentifier:   row.IssueIdentifier,
			IssueTitle:        row.IssueTitle,
			Status:            row.Status,
			Provider:          row.Provider,
			Model:             row.Model,
			TotalTokens:       row.TotalTokens,
			TotalCostUSDTicks: row.TotalCostUsdTicks,
			UncostedTokens:    row.UncostedTokens,
			HasUsage:          row.HasUsage,
			IsPriced:          row.IsPriced,
			CreatedAt:         timestampToString(row.CreatedAt),
			CompletedAt:       timestampToPtr(row.CompletedAt),
			ActivityAt:        timestampToString(row.ActivityAt),
			UsageBreakdown:    usageBreakdown,
		}
	}
	totalPages := int64(0)
	if summary.TaskCount > 0 {
		totalPages = (summary.TaskCount + int64(query.pageSize) - 1) / int64(query.pageSize)
	}
	writeJSON(w, http.StatusOK, LabelUsageDetailResponse{
		Label:      labelToResponse(label),
		Period:     query.period,
		Timezone:   query.timezone,
		Sort:       query.sort,
		Direction:  query.direction,
		Summary:    summary,
		Daily:      daily,
		Breakdown:  breakdown,
		Tasks:      tasks,
		Pagination: LabelUsagePaginationResponse{Page: query.page, PageSize: query.pageSize, Total: summary.TaskCount, TotalPages: totalPages},
	})
}
