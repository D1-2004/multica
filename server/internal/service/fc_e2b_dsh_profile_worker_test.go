package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDSHProfileReconciliationTimeoutDoesNotExhaustStartupBudget(t *testing.T) {
	for _, ctxErr := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, err := range []error{ctxErr, errors.New("installation receipt unavailable"), errors.Join(errDSHHostWaiting, errDSHHostStartup)} {
			if dshProfileApplyConsumesAttempt(ctxErr, err, 5*time.Minute) {
				t.Fatal("unconfirmed work consumed a startup attempt")
			}
		}
	}
	if !dshProfileApplyConsumesAttempt(context.DeadlineExceeded, context.DeadlineExceeded, 15*time.Minute) {
		t.Fatal("persistent timeouts must remain bounded")
	}
	if dshProfileApplyConsumesAttempt(nil, errDSHHostWaiting, time.Hour) {
		t.Fatal("expected lifecycle wait consumed a startup attempt")
	}
	if !dshProfileApplyConsumesAttempt(nil, errors.Join(errDSHHostWaiting, errDSHHostStartup), 0) ||
		!dshProfileApplyConsumesAttempt(nil, errors.New("confirmed configuration error"), 0) {
		t.Fatal("confirmed failures must remain bounded")
	}
}

func TestDSHProfileFailureKeepsFirstBoundary(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errors.Join(errDSHNativeSync, errors.New("package over import limit")), "native_sync_failed"},
		{errors.Join(errDSHHostWaiting, errDSHHostStartup), "host_start_failed"},
		{errors.New("database unavailable"), "profile_apply_failed"},
	} {
		if got := dshProfileApplyError(tc.err); got != tc.want {
			t.Errorf("got %s, want %s", got, tc.want)
		}
	}
}
