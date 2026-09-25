package runtimeconfig

import (
	"errors"
	"testing"
)

const testSDKRollout = `{"enabled":true,"workspace_ids":["d4f9ceed-d114-4312-bb30-dd791aee039b"],"agent_ids":["79dde6f4-30e2-44cc-acd4-8c3421221406"],"percent":0}`

func TestDiamondServiceLoadsAndUpdatesFCE2BSDKRollout(t *testing.T) {
	client := &fakeDiamondClient{content: validJSON(), rolloutContent: testSDKRollout}
	service, err := newDiamondService(nil, true, func() (diamondClient, error) { return client, nil })
	if err != nil {
		t.Fatalf("newDiamondService: %v", err)
	}
	loaded := service.FCE2BSDKRollout()
	if !loaded.Present || !loaded.Rollout.Enabled || loaded.Generation != 1 || len(loaded.Rollout.AgentIDs) != 1 || client.rolloutOnChange == nil {
		t.Fatalf("loaded = %#v, listener=%v", loaded, client.rolloutOnChange != nil)
	}

	client.rolloutOnChange(`{"enabled":false}`)
	if next := service.FCE2BSDKRollout(); !next.Present || next.Rollout.Enabled || next.Generation != 2 {
		t.Fatalf("switched off = %#v", next)
	}
	// An invalid publication keeps the last valid rollout.
	client.rolloutOnChange(`{"enabled":true,"agent_ids":["not-a-uuid"]}`)
	if kept := service.FCE2BSDKRollout(); kept.Generation != 2 || kept.Rollout.Enabled {
		t.Fatalf("invalid update replaced the snapshot: %#v", kept)
	}
	// Removing the document hands control back to the caller's fallback.
	client.rolloutOnChange("")
	if removed := service.FCE2BSDKRollout(); removed.Present || removed.Generation != 3 {
		t.Fatalf("removed = %#v", removed)
	}
	if err := service.Close(); err != nil || !client.rolloutCancelled {
		t.Fatalf("close = %v, rollout listener cancelled = %v", err, client.rolloutCancelled)
	}
}

func TestDiamondServiceStartsWithoutFCE2BSDKRollout(t *testing.T) {
	cases := map[string]*fakeDiamondClient{
		"document absent":  {content: validJSON()},
		"fetch failed":     {content: validJSON(), rolloutGetErr: errors.New("timeout")},
		"document invalid": {content: validJSON(), rolloutContent: `{"enabled":true,"percent":500}`},
		"listener failed":  {content: validJSON(), rolloutListenErr: errors.New("listen refused")},
		// A loaded rollout without a listener could never be switched off.
		"loaded but listener failed": {content: validJSON(), rolloutContent: testSDKRollout, rolloutListenErr: errors.New("listen refused")},
	}
	for name, client := range cases {
		service, err := newDiamondService(nil, true, func() (diamondClient, error) { return client, nil })
		if err != nil {
			t.Fatalf("%s: startup must not depend on the rollout document: %v", name, err)
		}
		if snapshot := service.FCE2BSDKRollout(); snapshot.Present {
			t.Fatalf("%s: snapshot = %#v, want the caller fallback", name, snapshot)
		}
		_ = service.Close()
	}
}
