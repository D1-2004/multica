package handler

import (
	"context"
	"errors"
	"strings"

	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/modelregistry"
	openai "github.com/openai/openai-go/v3"
)

// employeeModelRoutes separates frozen selection from current provider
// authorization/credentials. Preparation never makes a model request.
type employeeModelRoutes interface {
	CoordinatorPlan(context.Context) (modelregistry.CoordinatorPlan, error)
	PrepareCoordinatorAttempt(context.Context, modelregistry.CoordinatorPlan, int) (modelregistry.CoordinatorAttempt, error)
}

func (w *EmployeeSceneWorker) modelReady(ctx context.Context) error {
	if w.ModelRoutes == nil {
		if enabled, ok := w.model.(interface{ Enabled() bool }); ok && !enabled.Enabled() {
			return errors.New("employee legacy model is not configured")
		}
		return nil
	}
	plan, err := w.ModelRoutes.CoordinatorPlan(ctx)
	if err != nil {
		return err
	}
	for index := range plan.Candidates {
		if _, err = w.ModelRoutes.PrepareCoordinatorAttempt(ctx, plan, index); err == nil {
			return nil
		}
		if !errors.Is(err, modelregistry.ErrCandidateUnavailable) {
			return err
		}
	}
	return errors.New("employee coordinator model chain is unavailable")
}

func (m *employeeJournalModel) routeSelection(request *openai.ChatCompletionNewParams) ([]employeeentry.ModelRouteSelection, error) {
	if m.routePlan == nil {
		return nil, nil
	}
	plan := m.routePlan
	if m.routes == nil || plan.Version != 1 || plan.Revision < 0 || m.candidate < 0 || m.candidate >= len(plan.Candidates) {
		return nil, errors.New("employee model route snapshot is unavailable")
	}
	ref := plan.Candidates[m.candidate]
	if strings.TrimSpace(ref.Provider) == "" || strings.TrimSpace(ref.Model) == "" {
		return nil, errors.New("employee model route has an invalid candidate")
	}
	request.Model = ref.Model
	if fields := request.ExtraFields(); fields != nil {
		copy := make(map[string]any, len(fields))
		for k, v := range fields {
			if k != "model" {
				copy[k] = v
			}
		}
		request.SetExtraFields(copy)
	}
	return []employeeentry.ModelRouteSelection{{Revision: plan.Revision, Ref: ref.String(), Candidate: m.candidate, NextCandidate: m.candidate}}, nil
}

func (m *employeeJournalModel) recordFailure(ctx context.Context, ordinal int, cause error) (*openai.ChatCompletion, error) {
	message := cause.Error()
	if message == "" {
		message = "employee model request failed"
	}
	var next []int
	var route *employeeentry.ModelRouteSelection
	if m.routePlan != nil {
		candidate := m.candidate
		if candidate+1 < len(m.routePlan.Candidates) && (errors.Is(cause, modelregistry.ErrCandidateUnavailable) || modelregistry.Retryable(cause)) {
			candidate++
		}
		next = []int{candidate}
		route = &employeeentry.ModelRouteSelection{Revision: m.routePlan.Revision, Ref: m.routePlan.Candidates[m.candidate].String(), Candidate: m.candidate, NextCandidate: candidate}
	}
	if err := m.store.SaveModelFailure(ctx, m.job, ordinal, message, next...); err != nil {
		if m.abort != nil {
			m.abort()
		}
		return nil, err
	}
	if route != nil {
		m.candidate = route.NextCandidate
	}
	return nil, &employeeentry.ModelFailure{Message: message, Route: route}
}
