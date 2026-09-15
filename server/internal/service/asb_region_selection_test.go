package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgtype"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestASBExecutionRegionConfig(t *testing.T) {
	for _, raw := range []string{`{"asb_regions":null}`, `{"asb_regions":"cn-hangzhou"}`, `{"asb_regions":["cn-hangzhou.evil.example"]}`, `{"asb_regions":[1]}`} {
		if _, err := ParseASBExecutionRegions([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	got, err := ParseASBExecutionRegions([]byte(`{"other":true,"asb_regions":["cn-zhangjiakou","cn-hangzhou","cn-hangzhou"]}`))
	if err != nil || !reflect.DeepEqual(got, []string{"cn-hangzhou", "cn-zhangjiakou"}) {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestASBRegionSelectionNeverFailsOverOutsideSelection(t *testing.T) {
	for _, test := range []struct {
		name      string
		quota     int
		reject    bool
		missing   bool
		wantCalls int
	}{
		{name: "only Hangzhou", quota: 1, wantCalls: 1},
		{name: "Hangzhou full", quota: 0},
		{name: "Hangzhou contention", quota: 1, reject: true, wantCalls: 1},
		{name: "selected region removed", missing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := newASBRegionalTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Host != "sandbox-cn-hangzhou.aone.alibaba-inc.com" {
					t.Errorf("escaped selected region: %s", r.URL.Host)
				}
				var body asbCreateSandboxRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Metadata[asbSandboxRegionKey] != "cn-hangzhou" {
					t.Error("missing actual placement metadata")
				}
				if test.reject {
					w.WriteHeader(403)
					w.Write([]byte(`{"code":"QUOTA_EXCEEDED","message":"full"}`))
					return
				}
				w.WriteHeader(201)
				w.Write([]byte(`{"id":"sandbox-test","status":{"state":"Running"}}`))
			})
			quotas := []ASBSandboxQuota{{Region: "cn-zhangjiakou", Quota: 100}}
			if !test.missing {
				quotas = append(quotas, ASBSandboxQuota{Region: "cn-hangzhou", Quota: test.quota})
			}
			input := ASBCreateSandboxInput{AllowedRegions: []string{"cn-hangzhou"}, ImageURI: "image", TimeoutSeconds: 60, ResourceCPU: "1", ResourceMemory: "1Gi", Entrypoint: []string{"sleep infinity"}, Metadata: map[string]string{"keep": "value"}}
			_, err := createASBSandboxInAvailableRegion(context.Background(), client, pgtype.UUID{}, input, quotas)
			if calls != test.wantCalls {
				t.Fatalf("calls=%d", calls)
			}
			if test.missing {
				if err == nil || errors.Is(err, ErrASBCapacityUnavailable) {
					t.Fatalf("removed region error=%v", err)
				}
			} else if (test.quota == 0 || test.reject) != errors.Is(err, ErrASBCapacityUnavailable) {
				t.Fatalf("capacity error=%v", err)
			}
			if input.Metadata[asbSandboxRegionKey] != "" {
				t.Error("mutated shared metadata")
			}
		})
	}
}

func TestASBWarmSandboxHonorsSelectedRegions(t *testing.T) {
	for _, region := range []string{"", "cn-zhangjiakou", "cn-hangzhou"} {
		t.Run(region, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(ASBSandbox{ID: "sandbox-test", Status: ASBSandboxStatus{State: "Running"}, Metadata: map[string]string{asbNetworkPolicyFingerprintKey: "policy", asbSandboxRegionKey: region}})
			}))
			defer srv.Close()
			client, _ := NewASBClient(ASBClientConfig{BaseURL: srv.URL, APIKey: "test-key"})
			reuse, _, err := inspectReusableASBSandbox(context.Background(), client, "sandbox-test", "policy", "cn-hangzhou")
			if err != nil || reuse != (region == "cn-hangzhou") {
				t.Fatalf("reuse=%v err=%v", reuse, err)
			}
		})
	}
}
