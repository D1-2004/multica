package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/modelregistry"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

func (h *Handler) ConfigureGlobalModels(pool *pgxpool.Pool) {
	key, err := secretbox.LoadKey("MULTICA_MODEL_PROVIDER_SECRET_KEY")
	var box *secretbox.Box
	if err == nil {
		box, _ = secretbox.New(key)
	}
	h.Models = &modelregistry.Registry{Pool: pool, Box: box, Defaults: func() modelregistry.Snapshot {
		c := h.currentConfig()
		models := append([]string{}, c.FCE2B.LLMModels...)
		if len(models) == 0 {
			models = append(models, c.LLMDefaultModel)
		}
		refs := []modelregistry.Ref{}
		for _, m := range models {
			if m != "" {
				refs = append(refs, modelregistry.Ref{Provider: "mass", Model: m})
			}
		}
		def := modelregistry.Ref{Provider: "mass", Model: c.LLMDefaultModel}
		if len(refs) > 0 {
			def = refs[0]
		}
		coord := c.LLMDefaultModel
		if h.InboundCoordinator != nil {
			coord = h.InboundCoordinator.CurrentModel()
		}
		if coord == "" {
			coord = "qwen3.8-max"
		}
		found := false
		for _, m := range models {
			if m == coord {
				found = true
			}
		}
		if !found {
			models = append(models, coord)
		}
		return modelregistry.Snapshot{Config: modelregistry.Config{Providers: []modelregistry.Provider{{ID: "mass", Name: "Diamond", BaseURL: c.LLMBaseURL, Models: models, Enabled: true, Builtin: true, HasKey: c.LLMAPIKey != ""}}, AgentModels: refs, DefaultModel: def, Coordinator: []modelregistry.Ref{{Provider: "mass", Model: coord}}, DiamondFallback: true}, Keys: map[string]string{"mass": c.LLMAPIKey}}
	}}
}
func (h *Handler) DeveloperCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"developer": h.canPublishFCE2BStable(r)})
}
func (h *Handler) GetGlobalModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireFCE2BStablePublisher(w, r); !ok {
		return
	}
	s, e := h.Models.Load(r.Context())
	if e != nil {
		writeError(w, 503, "model configuration unavailable")
		return
	}
	writeJSON(w, 200, s.Config)
}
func (h *Handler) SaveGlobalModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireFCE2BStablePublisher(w, r); !ok {
		return
	}
	var c modelregistry.Config
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&c); e != nil {
		writeError(w, 400, "invalid model configuration")
		return
	}
	s, e := h.Models.Save(r.Context(), c, requestUserID(r))
	if e != nil {
		status := 400
		if errors.Is(e, modelregistry.ErrConflict) {
			status = 409
		}
		writeError(w, status, e.Error())
		return
	}
	writeJSON(w, 200, s.Config)
}
func (h *Handler) DiscoverProviderModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireFCE2BStablePublisher(w, r); !ok {
		return
	}
	var p modelregistry.Provider
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&p); e != nil {
		writeError(w, 400, "invalid provider")
		return
	}
	if e := modelregistry.ValidateURL(p.BaseURL); e != nil {
		writeError(w, 400, e.Error())
		return
	}
	if p.APIKey == "" {
		s, e := h.Models.Load(r.Context())
		if e != nil {
			writeError(w, 503, "model configuration unavailable")
			return
		}
		for _, existing := range s.Config.Providers {
			if existing.ID == p.ID && strings.TrimRight(existing.BaseURL, "/") == strings.TrimRight(p.BaseURL, "/") {
				p.APIKey = s.Keys[p.ID]
			}
		}
		if p.APIKey == "" {
			writeError(w, 400, "API key required for this provider URL")
			return
		}
	}
	timeout := 15 * time.Second
	if modelregistry.BailianAPIOrigin(p.BaseURL) != "" {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(p.BaseURL, "/")+"/models", nil)
	if e != nil {
		writeError(w, 400, "invalid provider")
		return
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	resp, e := modelregistry.HTTPClient().Do(req)
	if e != nil {
		writeError(w, 502, "model discovery connection failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		writeError(w, 502, "model discovery rejected by provider")
		return
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&body); e != nil {
		writeError(w, 502, "invalid model catalog")
		return
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, v := range body.Data {
		if v.ID != "" && !seen[v.ID] {
			ids = append(ids, v.ID)
			seen[v.ID] = true
		}
	}
	if modelregistry.BailianAPIOrigin(p.BaseURL) != "" {
		result, err := modelregistry.DiscoverBailian(ctx, p, ids, modelregistry.HTTPClient())
		if err != nil {
			writeError(w, 502, err.Error())
			return
		}
		writeJSON(w, 200, result)
		return
	}
	writeJSON(w, 200, map[string]any{"models": ids})
}

// Runtime credentials authorize only their own cloud runtime and the global agent catalog.
func (h *Handler) ProxyRuntimeModel(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.requireDaemonRuntimeAccess(w, r, chi.URLParam(r, "runtimeId"))
	if !ok {
		return
	}
	task, ok := h.requireDaemonTaskAccess(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	if task.RuntimeID != rt.ID || (r.Method != "GET" && task.Status != "running") {
		writeError(w, 403, "active task bound to runtime required")
		return
	}
	daemon := middleware.DaemonIDFromContext(r.Context())
	if daemon == "" || daemon != rt.DaemonID.String {
		writeError(w, 403, "runtime daemon credential required")
		return
	}
	if _, ok = h.configuredCloudSandboxModelCatalog(rt); !ok {
		writeError(w, 403, "managed cloud runtime required")
		return
	}
	agent, loadErr := h.Queries.GetAgent(r.Context(), task.AgentID)
	if loadErr != nil {
		writeError(w, 503, "agent configuration unavailable")
		return
	}
	s, e := h.Models.TaskSnapshot(r.Context(), uuidToString(task.ID), agent.Model.String)
	if e != nil {
		writeError(w, 503, "model configuration unavailable")
		return
	}
	suffix := chi.URLParam(r, "modelPath")
	if suffix == "" {
		suffix = chi.URLParam(r, "*")
	}
	if suffix == "models" && r.Method == "GET" {
		items := []map[string]string{}
		for _, m := range s.Config.AgentModels {
			items = append(items, map[string]string{"id": m.String(), "object": "model"})
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": items})
		return
	}
	if suffix != "chat/completions" && suffix != "responses" {
		writeError(w, 404, "unsupported model endpoint")
		return
	}
	var body map[string]json.RawMessage
	if e = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&body); e != nil {
		writeError(w, 400, "invalid model request")
		return
	}
	var model string
	if e = json.Unmarshal(body["model"], &model); e != nil {
		writeError(w, 400, "model is required")
		return
	}
	ref, e := s.AgentRef(model)
	if e != nil {
		writeError(w, 400, e.Error())
		return
	}
	p, key, e := s.Resolve(ref)
	if e != nil {
		writeError(w, 400, e.Error())
		return
	}
	body["model"], _ = json.Marshal(ref.Model)
	raw, _ := json.Marshal(body)
	req, e := http.NewRequestWithContext(r.Context(), "POST", strings.TrimRight(p.BaseURL, "/")+"/"+suffix, bytes.NewReader(raw))
	if e != nil {
		writeError(w, 502, "invalid model endpoint")
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, e := modelregistry.HTTPClient().Do(req)
	if e != nil {
		writeError(w, 502, "model provider connection failed")
		return
	}
	defer resp.Body.Close()
	slog.InfoContext(r.Context(), "runtime model request", "event", "runtime_model_request", "task_id", uuidToString(task.ID), "provider", ref.Provider, "model", ref.Model, "configuration_revision", s.Config.Revision, "status", resp.StatusCode)
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 32<<10)
	for {
		n, e := resp.Body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if e != nil {
			return
		}
	}
}
func (h *Handler) RestoreGlobalModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireFCE2BStablePublisher(w, r); !ok {
		return
	}
	var req struct {
		Revision int64 `json:"revision"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req) != nil {
		writeError(w, 400, "invalid revision")
		return
	}
	s, e := h.Models.Restore(r.Context(), req.Revision, requestUserID(r))
	if e != nil {
		status := 400
		if errors.Is(e, modelregistry.ErrConflict) {
			status = 409
		}
		writeError(w, status, e.Error())
		return
	}
	writeJSON(w, 200, s.Config)
}

func (h *Handler) TestProviderModel(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireFCE2BStablePublisher(w, r); !ok {
		return
	}
	var input struct {
		Provider modelregistry.Provider `json:"provider"`
		Model    string                 `json:"model"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&input) != nil || input.Model == "" {
		writeError(w, 400, "provider and model are required")
		return
	}
	p := input.Provider
	if e := modelregistry.ValidateURL(p.BaseURL); e != nil {
		writeError(w, 400, e.Error())
		return
	}
	if p.APIKey == "" {
		s, e := h.Models.Load(r.Context())
		if e != nil {
			writeError(w, 503, "model configuration unavailable")
			return
		}
		for _, v := range s.Config.Providers {
			if v.ID == p.ID && strings.TrimRight(v.BaseURL, "/") == strings.TrimRight(p.BaseURL, "/") {
				p.APIKey = s.Keys[p.ID]
			}
		}
	}
	if p.APIKey == "" {
		writeError(w, 400, "API key required for this URL")
		return
	}
	body := map[string]any{"model": input.Model, "messages": []map[string]string{{"role": "user", "content": "Call finish with value 2. This is a connectivity probe; no tools will be executed."}}, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "finish", "description": "Return an integer", "parameters": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer"}}, "required": []string{"value"}}}}}, "tool_choice": "required", "enable_thinking": false, "reasoning_effort": "none", "max_completion_tokens": 256}
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(p.BaseURL, "/")+"/chat/completions", bytes.NewReader(raw))
	if e != nil {
		writeError(w, 400, "invalid endpoint")
		return
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("Content-Type", "application/json")
	before := time.Now()
	resp, e := modelregistry.HTTPClient().Do(req)
	if e != nil {
		writeError(w, 502, "model probe connection failed")
		return
	}
	defer resp.Body.Close()
	var result struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	valid := false
	if resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result) == nil && len(result.Choices) == 1 && len(result.Choices[0].Message.ToolCalls) == 1 {
		call := result.Choices[0].Message.ToolCalls[0]
		var args struct {
			Value int `json:"value"`
		}
		valid = call.Function.Name == "finish" && json.Unmarshal([]byte(call.Function.Arguments), &args) == nil && args.Value == 2
	}
	writeJSON(w, 200, map[string]any{"valid": valid, "status": resp.StatusCode, "elapsed_ms": time.Since(before).Milliseconds()})
}
