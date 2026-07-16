package main

import "testing"

func TestDingTalkStreamConnectionTarget(t *testing.T) {
	tests := []struct {
		name            string
		value           string
		wantTarget      int
		wantCoordinated bool
		wantErr         bool
	}{
		{name: "safe rollout default", value: "", wantTarget: 0, wantCoordinated: false},
		{name: "explicit single", value: "1", wantTarget: 1, wantCoordinated: true},
		{name: "dual ready", value: "2", wantTarget: 2, wantCoordinated: true},
		{name: "zero rejected", value: "0", wantErr: true},
		{name: "more than dual rejected", value: "3", wantErr: true},
		{name: "non numeric rejected", value: "dual", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MULTICA_DINGTALK_STREAM_CONNECTION_TARGET", tc.value)
			target, coordinated, err := dingTalkStreamConnectionTarget()
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && (target != tc.wantTarget || coordinated != tc.wantCoordinated) {
				t.Fatalf("target/coordinated = %d/%v, want %d/%v", target, coordinated, tc.wantTarget, tc.wantCoordinated)
			}
		})
	}
}

func TestDingTalkStreamCoordinatorRequiresRedisOnlyWhenExplicit(t *testing.T) {
	t.Setenv("MULTICA_DINGTALK_STREAM_CONNECTION_TARGET", "")
	coordinator, target, coordinated, err := dingTalkStreamCoordinatorFromEnv(nil)
	if err != nil || coordinator != nil || target != 0 || coordinated {
		t.Fatalf("legacy config = coordinator=%v target=%d coordinated=%v err=%v", coordinator, target, coordinated, err)
	}

	t.Setenv("MULTICA_DINGTALK_STREAM_CONNECTION_TARGET", "1")
	if _, _, _, err := dingTalkStreamCoordinatorFromEnv(nil); err == nil {
		t.Fatal("explicit Redis coordination without Redis must fail startup configuration")
	}
}
