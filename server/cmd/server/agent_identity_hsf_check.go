package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
)

const agentIdentityHSFCheckTTLSeconds = 900

type agentIdentityContextCreator interface {
	CreateContext(context.Context, agentidentityhsf.CreateContextRequest) (agentidentityhsf.CreateContextResult, error)
}

type agentIdentityHSFCheckRequest struct {
	UID        string `json:"uid"`
	OrgID      string `json:"org_id"`
	RequestID  string `json:"request_id,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
	RuntimeID  string `json:"runtime_id,omitempty"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

type agentIdentityHSFCheckResponse struct {
	Success              bool   `json:"success"`
	RequestID            string `json:"request_id,omitempty"`
	ContextID            string `json:"context_id,omitempty"`
	ExpiresAt            int64  `json:"expires_at,omitempty"`
	ContextTokenReceived bool   `json:"context_token_received"`
	Error                string `json:"error,omitempty"`
	ErrorCode            string `json:"error_code,omitempty"`
	Field                string `json:"field,omitempty"`
}

// agentIdentityHSFCheckHandler exercises the complete Multica -> daprd -> HSF
// call after deployment. It intentionally discards the short-lived ContextToken
// and returns only proof that the service produced one.
func agentIdentityHSFCheckHandler(token string, client agentIdentityContextCreator) http.HandlerFunc {
	token = strings.TrimSpace(token)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if token != "" {
			if !hasBearerToken(r, token) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="agent-identity-hsf-check"`)
				writeJSON(w, http.StatusUnauthorized, agentIdentityHSFCheckResponse{Error: "unauthorized"})
				return
			}
		} else if !isDirectLoopbackRequest(r) {
			http.NotFound(w, r)
			return
		}

		var input agentIdentityHSFCheckRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, agentIdentityHSFCheckResponse{Error: "invalid_json"})
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, agentIdentityHSFCheckResponse{Error: "invalid_json"})
			return
		}

		requestID := strings.TrimSpace(input.RequestID)
		if requestID == "" {
			requestID = "multica-hsf-check-" + uuid.NewString()
		}
		taskID := strings.TrimSpace(input.TaskID)
		if taskID == "" {
			taskID = requestID
		}
		agentID := strings.TrimSpace(input.AgentID)
		if agentID == "" {
			agentID = "multica-hsf-check"
		}
		runtimeID := strings.TrimSpace(input.RuntimeID)
		if runtimeID == "" {
			runtimeID = "aone-" + uuid.NewString()
		}
		ttlSeconds := input.TTLSeconds
		if ttlSeconds == 0 {
			ttlSeconds = agentIdentityHSFCheckTTLSeconds
		}

		result, err := client.CreateContext(r.Context(), agentidentityhsf.CreateContextRequest{
			RequestID:   requestID,
			TaskID:      taskID,
			AgentID:     agentID,
			RuntimeType: "AONE_SERVICE",
			RuntimeID:   runtimeID,
			Reason:      "Multica deployment HSF connectivity check",
			Source: map[string]string{
				"app":      strings.TrimSpace(os.Getenv("APP_NAME")),
				"endpoint": "internal-agent-identity-hsf-check",
			},
			UID:        input.UID,
			OrgID:      input.OrgID,
			TTLSeconds: ttlSeconds,
		})
		if err != nil {
			var validationErr *agentidentityhsf.ValidationError
			if errors.As(err, &validationErr) {
				writeJSON(w, http.StatusBadRequest, agentIdentityHSFCheckResponse{
					Success: false,
					Error:   "invalid_request",
					Field:   validationErr.Field,
				})
				return
			}
			var serviceErr *agentidentityhsf.ServiceError
			if errors.As(err, &serviceErr) {
				writeJSON(w, http.StatusBadGateway, agentIdentityHSFCheckResponse{
					Success:   false,
					RequestID: requestID,
					Error:     "agent_identity_rejected",
					ErrorCode: serviceErr.Code,
				})
				return
			}
			// Do not attach the downstream error: transport stacks are outside our
			// control and may echo request or response data.
			slog.Warn("Agent Identity HSF diagnostic failed", "request_id", requestID)
			writeJSON(w, http.StatusBadGateway, agentIdentityHSFCheckResponse{
				Success:   false,
				RequestID: requestID,
				Error:     "hsf_call_failed",
			})
			return
		}

		tokenReceived := strings.TrimSpace(result.ContextToken) != ""
		result.ContextToken = ""
		writeJSON(w, http.StatusOK, agentIdentityHSFCheckResponse{
			Success:              true,
			RequestID:            requestID,
			ContextID:            result.ContextID,
			ExpiresAt:            result.ExpiresAt,
			ContextTokenReceived: tokenReceived,
		})
	}
}
