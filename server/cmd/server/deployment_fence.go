package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/deploymentfence"
)

type deploymentFenceTransitionRequest struct {
	State  deploymentfence.State `json:"state"`
	Reason string                `json:"reason"`
}

func deploymentFenceStatusHandler(token string, service *deploymentfence.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeDeploymentFenceRequest(w, r, token) {
			return
		}
		status, err := service.Status(r.Context())
		if err != nil {
			http.Error(w, "read deployment fence status", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func deploymentFenceTransitionHandler(token string, service *deploymentfence.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeDeploymentFenceRequest(w, r, token) {
			return
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
		decoder.DisallowUnknownFields()
		var request deploymentFenceTransitionRequest
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid deployment fence request", http.StatusBadRequest)
			return
		}
		if err := ensureJSONEOF(decoder); err != nil {
			http.Error(w, "invalid deployment fence request", http.StatusBadRequest)
			return
		}
		updated, err := service.Transition(
			r.Context(),
			request.State,
			request.Reason,
			"aone-prepub-operator",
		)
		if err != nil {
			var transitionErr *deploymentfence.TransitionError
			if errors.As(err, &transitionErr) {
				http.Error(w, transitionErr.Error(), http.StatusConflict)
				return
			}
			http.Error(w, "update deployment fence", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

func authorizeDeploymentFenceRequest(w http.ResponseWriter, r *http.Request, token string) bool {
	token = strings.TrimSpace(token)
	if token != "" {
		if hasBearerToken(r, token) {
			return true
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="deployment-fence"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if isDirectLoopbackRequest(r) {
		return true
	}
	http.NotFound(w, r)
	return false
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}
