package employeeentry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestModelRouteJournalRetainsSelectionAndNextCandidate(t *testing.T) {
	f := database(t)
	admit(t, f)
	j := claim(t, f)
	ctx := context.Background()
	request := json.RawMessage(`{"model":"shared-model","messages":[{"role":"user","content":"work"}]}`)
	first := ModelRouteSelection{Revision: 21, Ref: "primary/shared-model", Candidate: 0, NextCandidate: 0}
	if _, err := f.store.BeginModel(ctx, j, 0, request, first); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveModelFailure(ctx, j, 0, "provider timed out", 1); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.BeginModel(ctx, j, 0, request, first)
	var failure *ModelFailure
	if !errors.As(err, &failure) || failure.Route == nil || failure.Route.NextCandidate != 1 || failure.Route.Ref != first.Ref {
		t.Fatal("failure lost durable fallback selection", failure, err)
	}
	second := ModelRouteSelection{Revision: 21, Ref: "fallback/shared-model", Candidate: 1, NextCandidate: 1}
	if _, err := f.store.BeginModel(ctx, j, 0, request, second); !errors.Is(err, ErrConflict) {
		t.Fatal("same upstream model hid provider identity drift", err)
	}
	if _, err := f.store.BeginModel(ctx, j, 0, request); !errors.Is(err, ErrConflict) {
		t.Fatal("legacy reader ignored routed identity", err)
	}
	if _, err := f.store.BeginModel(ctx, j, 1, request, second); err != nil {
		t.Fatal(err)
	}
	response := json.RawMessage(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`)
	if err := f.store.SaveModel(ctx, j, 1, response); err != nil {
		t.Fatal(err)
	}
	if got, err := f.store.BeginModel(ctx, j, 1, request, second); err != nil || string(got) != string(response) {
		var same bool
		if e := f.pool.QueryRow(ctx, `SELECT $1::jsonb=$2::jsonb`, got, response).Scan(&same); e != nil || !same || err != nil {
			t.Fatal("successful candidate response changed", string(got), err, e)
		}
	}
	var attempts int
	if err := f.pool.QueryRow(ctx, `SELECT model_attempts FROM employee_scene_job WHERE id=$1::uuid`, j.ID).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatal("route replay consumed attempts", attempts, err)
	}
}

func TestModelRouteFailureCannotRewriteSettledSelection(t *testing.T) {
	f := database(t)
	admit(t, f)
	j := claim(t, f)
	ctx := context.Background()
	selection := ModelRouteSelection{Revision: 21, Ref: "primary/model", Candidate: 0, NextCandidate: 0}
	if _, err := f.store.BeginModel(ctx, j, 0, json.RawMessage(`{"model":"model"}`), selection); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveModelFailure(ctx, j, 0, "failed", 2); !errors.Is(err, ErrInvalid) {
		t.Fatal("failure jumped outside the next candidate", err)
	}
	if err := f.store.SaveModelFailure(ctx, j, 0, "failed", 1); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveModelFailure(ctx, j, 0, "failed", 0); !errors.Is(err, ErrConflict) {
		t.Fatal("failure replay changed the fallback cursor", err)
	}
}
