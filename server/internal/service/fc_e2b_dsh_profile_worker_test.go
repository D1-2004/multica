package service

import (
	"errors"
	"testing"
)

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
