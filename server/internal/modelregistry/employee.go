package modelregistry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/pkg/llm"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

// CoordinatorPlan freezes routing identity only. Provider URLs and credentials
// are deliberately absent; each real request must prepare its candidate anew.
type CoordinatorPlan struct {
	Version    int   `json:"version"`
	Revision   int64 `json:"revision"`
	Candidates []Ref `json:"candidate_refs"`
}

// ErrCandidateUnavailable identifies a frozen candidate whose current provider,
// catalog, credentials or URL no longer permit an attempt. It grants no fallback
// by itself: the caller owns its durable cursor and request budget.
var ErrCandidateUnavailable = errors.New("coordinator candidate is unavailable")

type CoordinatorAttempt interface {
	Ref() Ref
	ConfigurationRevision() int64
	Chat(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

func (r *Registry) CoordinatorPlan(ctx context.Context) (CoordinatorPlan, error) {
	if r == nil || r.Pool == nil || r.Defaults == nil {
		return CoordinatorPlan{}, errors.New("coordinator model registry is unavailable")
	}
	route, err := r.CoordinatorSnapshot(ctx)
	if err != nil {
		return CoordinatorPlan{}, err
	}
	if len(route.Snapshot.Config.Coordinator) == 0 {
		return CoordinatorPlan{}, errors.New("coordinator model chain is empty")
	}
	return CoordinatorPlan{Version: 1, Revision: route.Snapshot.Config.Revision, Candidates: append([]Ref(nil), route.Snapshot.Config.Coordinator...)}, nil
}

// PrepareCoordinatorAttempt checks authorization and binds one atomically loaded
// URL/key revision immediately before I/O. Current chain edits do not reorder a
// frozen plan; disabling its provider/model still revokes future attempts.
func (r *Registry) PrepareCoordinatorAttempt(ctx context.Context, plan CoordinatorPlan, index int) (CoordinatorAttempt, error) {
	if plan.Version != 1 {
		return nil, errors.New("unsupported coordinator plan version")
	}
	if index < 0 || index >= len(plan.Candidates) {
		return nil, errors.New("coordinator candidate index is outside the frozen plan")
	}
	if r == nil || r.Pool == nil || r.Defaults == nil {
		return nil, errors.New("coordinator model registry is unavailable")
	}
	current, err := r.Load(ctx)
	if err != nil {
		return nil, err
	}
	ref := plan.Candidates[index]
	provider, key, err := current.Resolve(ref)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrCandidateUnavailable, ref.String())
	}
	// An empty key must not fall through to ambient OPENAI_API_KEY in the SDK.
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("%w: credentials for %s", ErrCandidateUnavailable, ref.String())
	}
	if err = ValidateURL(provider.BaseURL); err != nil {
		return nil, fmt.Errorf("%w: provider URL for %s", ErrCandidateUnavailable, ref.String())
	}
	client := HTTPClient()
	if r.coordinatorTransport != nil {
		client.Transport = r.coordinatorTransport
	}
	return &preparedCoordinatorAttempt{ref: ref, revision: current.Config.Revision, client: llm.New(llm.Config{BaseURL: provider.BaseURL, APIKey: key, DefaultModel: ref.Model, MaxRetries: -1, HTTPClient: client})}, nil
}

type preparedCoordinatorAttempt struct {
	ref      Ref
	revision int64
	client   *llm.Client
}

func (a *preparedCoordinatorAttempt) Ref() Ref                     { return a.ref }
func (a *preparedCoordinatorAttempt) ConfigurationRevision() int64 { return a.revision }

// Chat performs one SDK request with no route fallback or nested generation.
// The caller owns deadlines and tracing; provider usage is preserved verbatim.
func (a *preparedCoordinatorAttempt) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	params.Model = shared.ChatModel(a.ref.Model)
	if fields := params.ExtraFields(); fields != nil {
		// ExtraFields can override typed fields. Copy it so enforcing the frozen
		// model cannot mutate the caller's journal request or its other parameters.
		copied := make(map[string]any, len(fields))
		for key, value := range fields {
			copied[key] = value
		}
		delete(copied, "model")
		params.SetExtraFields(copied)
	}
	return a.client.Chat(ctx, params)
}
