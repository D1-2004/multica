package handler

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/evalcatalog"
	"github.com/multica-ai/multica/server/internal/evalreport"
	"github.com/multica-ai/multica/server/internal/util"
)

const maxEvalReportBytes = 2 << 20

// EvalReportReplicaMarker includes immutable ingestion and workspace cleanup.
const EvalReportReplicaMarker = "[eval-report:1]"

// RequireEvalReportReplicas prevents new rows until every live node can clean them.
func RequireEvalReportReplicas(ready func(context.Context) (bool, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			if ready == nil {
				writeError(w, http.StatusServiceUnavailable, "evaluation report replicas are not ready")
				return
			}
			compatible, err := ready(r.Context())
			if err != nil || !compatible {
				writeError(w, http.StatusServiceUnavailable, "evaluation report replicas are not ready")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireEvalReportHumanActor rejects service principals before workspace resolution.
func RequireEvalReportHumanActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Actor-Source") != "" {
			writeError(w, http.StatusForbidden, "eval reports require human authentication")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) evalReportStore() *evalreport.Store {
	return evalreport.NewStore(h.DB, h.TxStarter)
}

func evalReportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, evalreport.ErrNotFound):
		writeError(w, http.StatusNotFound, "evaluation report or workspace not found")
	case errors.Is(err, evalreport.ErrConflict):
		writeError(w, http.StatusConflict, "run_id already has a different immutable report")
	default:
		writeError(w, http.StatusServiceUnavailable, "evaluation report storage is unavailable")
	}
}

func evalReportReceipt(record evalreport.Record) map[string]any {
	return map[string]any{
		"id": record.ID, "workspace_id": record.WorkspaceID,
		"submitted_by": record.SubmittedBy, "received_at": record.ReceivedAt,
		"content_sha256": record.ContentSHA256, "definition_sha256": record.DefinitionSHA256,
		"summary": record.Summary,
	}
}

// SubmitEvalReport receives immutable local results, without verifying execution.
func (h *Handler) SubmitEvalReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Header.Get("X-Actor-Source") != "" {
		writeError(w, http.StatusForbidden, "eval reports require human authentication")
		return
	}
	workspaceID := workspaceIDFromURL(r, "id")
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEvalReportBytes))
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			writeError(w, http.StatusRequestEntityTooLarge, "evaluation report exceeds 2 MiB")
		} else {
			writeError(w, http.StatusBadRequest, "cannot read evaluation report")
		}
		return
	}
	submission, err := evalreport.Decode(body)
	if err != nil {
		if errors.Is(err, evalreport.ErrInvalid) {
			writeError(w, http.StatusBadRequest, err.Error())
		} else {
			evalReportError(w, err)
		}
		return
	}
	record, replayed, err := h.evalReportStore().Submit(r.Context(), workspaceID, userID, submission)
	if err != nil {
		evalReportError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, evalReportReceipt(record))
}

func (h *Handler) GetEvalReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Header.Get("X-Actor-Source") != "" {
		writeError(w, http.StatusForbidden, "eval reports require human authentication")
		return
	}
	workspaceID := workspaceIDFromURL(r, "id")
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	reportID := chi.URLParam(r, "reportId")
	if _, ok := parseUUIDOrBadRequest(w, reportID, "report_id"); !ok {
		return
	}
	record, err := h.evalReportStore().Get(r.Context(), workspaceID, userID, reportID)
	if err != nil {
		evalReportError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, record)
}

func (h *Handler) ListEvalReports(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Header.Get("X-Actor-Source") != "" {
		writeError(w, http.StatusForbidden, "eval reports require human authentication")
		return
	}
	workspaceID := workspaceIDFromURL(r, "id")
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	limit := 25
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 50 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 50")
			return
		}
		limit = parsed
	}
	before := r.URL.Query().Get("before_id")
	if before != "" {
		if _, ok := parseUUIDOrBadRequest(w, before, "before_id"); !ok {
			return
		}
	}
	records, more, err := h.evalReportStore().List(r.Context(), workspaceID, userID, before, limit)
	if err != nil {
		evalReportError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := evalReportReceipt(record)
		item["workspace_name"] = record.WorkspaceName
		item["run_id"] = record.Submission.RunID
		item["title"] = record.Submission.Title
		item["execution_kind"] = record.Submission.ExecutionKind
		item["environment"] = record.Submission.Environment
		item["target_revision"] = record.Submission.TargetRevision
		item["runner"] = record.Submission.Runner
		item["started_at"] = record.Submission.StartedAt
		item["finished_at"] = record.Submission.FinishedAt
		item["catalog"] = record.Submission.Catalog
		items = append(items, item)
	}
	var next *string
	if more && len(records) > 0 {
		value := records[len(records)-1].ID
		next = &value
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": more, "next_before_id": next})
}

// EvalReportPage loads only currently member-visible report metadata or one detail.
func (h *Handler) EvalReportPage(r *http.Request) (evalcatalog.ReportPage, error) {
	var page evalcatalog.ReportPage
	userID := requestUserID(r)
	if _, err := util.ParseUUID(userID); err != nil {
		return page, evalreport.ErrNotFound
	}
	if r.Header.Get("X-Actor-Source") != "" {
		return page, evalreport.ErrNotFound
	}
	store := h.evalReportStore()
	if reportID := r.URL.Query().Get("report"); reportID != "" {
		workspaceID := r.URL.Query().Get("workspace_id")
		if _, err := util.ParseUUID(reportID); err != nil {
			return page, evalreport.ErrNotFound
		}
		if _, err := util.ParseUUID(workspaceID); err != nil {
			return page, evalreport.ErrNotFound
		}
		record, err := store.Get(r.Context(), workspaceID, userID, reportID)
		if err != nil {
			return page, err
		}
		page.Detail = &record
		return page, nil
	}
	records, err := store.ListVisible(r.Context(), userID, 25)
	if err != nil {
		return page, err
	}
	page.Reports = records
	return page, nil
}
