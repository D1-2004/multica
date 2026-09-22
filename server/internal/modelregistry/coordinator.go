package modelregistry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/langfuse"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

type Route struct {
	Snapshot  Snapshot
	mu        sync.Mutex
	preferred int
	call      func(context.Context, Ref, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

func (r *Registry) CoordinatorSnapshot(ctx context.Context) (*Route, error) {
	s, e := r.Load(ctx)
	if e != nil {
		return nil, e
	}
	return &Route{Snapshot: s}, nil
}
func (r *Route) Model() string {
	if len(r.Snapshot.Config.Coordinator) == 0 {
		return ""
	}
	return r.Snapshot.Config.Coordinator[0].String()
}
func Retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var api *openai.Error
	if errors.As(err, &api) {
		switch api.StatusCode {
		case 401, 403, 404, 408, 429:
			return true
		case 400:
			return api.Code == "provider_error" || (api.Code == "invalid_parameter_error" && api.Param == "" && strings.Contains(api.Message, "model serving") && strings.Contains(api.Message, "[Invalid request parameters.]"))
		}
		return api.StatusCode >= 500
	}
	var n net.Error
	return errors.As(err, &n) || errors.Is(err, context.DeadlineExceeded)
}
func (r *Route) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	r.mu.Lock()
	start := r.preferred
	r.mu.Unlock()
	chain := r.Snapshot.Config.Coordinator
	var last error
	for i := start; i < len(chain); i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		ref := chain[i]
		p := params
		p.Model = shared.ChatModel(ref.Model)
		attemptCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		var gen *langfuse.Observation
		if t := langfuse.TraceFromContext(ctx); t != nil {
			gen = t.StartObservation(langfuse.ObservationOptions{Type: langfuse.TypeGeneration, Name: "coordinator.provider_attempt", Model: ref.String(), Input: p.Messages, Metadata: map[string]any{"provider": ref.Provider, "upstream_model": ref.Model, "configuration_revision": r.Snapshot.Config.Revision, "candidate_index": i}})
		}
		before := time.Now()
		var result *openai.ChatCompletion
		var err error
		if r.call != nil {
			result, err = r.call(attemptCtx, ref, p)
		} else {
			client, e := r.Snapshot.Client(ref)
			if e != nil {
				cancel()
				return nil, e
			}
			provider, _, _ := r.Snapshot.Resolve(ref)
			if ref.Model == "deepseek-v4.1-flash" && BailianAPIOrigin(provider.BaseURL) != "" {
				p, e = deepSeekCoordinatorParams(p)
				if e != nil {
					cancel()
					return nil, e
				}
			}
			result, err = client.Chat(attemptCtx, p)
		}
		cancel()
		if gen != nil {
			var output any
			if result != nil {
				output = result.Choices
			}
			end := langfuse.EndOptions{Output: output, Err: err}
			if result != nil {
				end.Usage = &langfuse.Usage{Input: result.Usage.PromptTokens, Output: result.Usage.CompletionTokens, Total: result.Usage.TotalTokens, CacheRead: result.Usage.PromptTokensDetails.CachedTokens}
			}
			gen.End(end)
		}
		slog.InfoContext(ctx, "coordinator provider attempt", "event", "inbound_coordinator_provider_attempt", "model", ref.String(), "revision", r.Snapshot.Config.Revision, "candidate_index", i, "elapsed_ms", time.Since(before).Milliseconds(), "succeeded", err == nil)
		if err == nil {
			r.mu.Lock()
			if i > r.preferred {
				r.preferred = i
			}
			r.mu.Unlock()
			// Attempt generations own usage; the aggregate orchestration observation must not double count it.
			copyResult := *result
			copyResult.Usage = openai.CompletionUsage{}
			return &copyResult, nil
		}
		last = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !Retryable(err) {
			return nil, err
		}
	}
	if last == nil {
		last = fmt.Errorf("coordinator model chain is empty")
	}
	return nil, last
}
