package dshhost

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type cloudTransport func(*http.Request) (*http.Response, error)

func (f cloudTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestACSRequestUsesOfficialSignatureAndNeverFollowsRedirect(t *testing.T) {
	c, err := NewACSClient("cn-beijing", func(context.Context) (CloudCredentials, error) {
		return CloudCredentials{"test-key", "test-secret", "test-session"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.http.Transport = cloudTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.Host != "nas.cn-beijing.aliyuncs.com" || r.URL.Path != "/" {
			t.Fatal("wrong endpoint")
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "ACS3-HMAC-SHA256 Credential=test-key,") || r.Header.Get("X-Acs-Security-Token") != "test-session" || r.Header.Get("X-Acs-Action") != "CreateAgenticSpace" {
			t.Fatal("missing SDK signature or STS identity")
		}
		if r.URL.Query().Get("Quota.SizeLimit") != "10737418240" || r.URL.Query().Get("ClientToken") != "intent-0" {
			t.Fatal("nested POP query or correlation was lost")
		}
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{"https://other.invalid/capture"}}, Body: io.NopCloser(strings.NewReader("provider body with test-secret")), Request: r}, nil
	})
	err = c.Call(context.Background(), CloudCall{Service: "nas", Action: "CreateAgenticSpace", Query: map[string]any{"ClientToken": "intent-0", "Quota": map[string]int64{"SizeLimit": 10737418240}}}, nil)
	if err == nil || calls != 1 || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("redirect/retry/body leak: %d %v", calls, err)
	}
}

func TestACSVolumeEnvelopeAndCancellation(t *testing.T) {
	c, _ := NewACSClient("cn-beijing", func(context.Context) (CloudCredentials, error) {
		return CloudCredentials{"test-key", "test-secret", ""}, nil
	})
	calls := 0
	c.http.Transport = cloudTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/pop/2026-05-09/volumes" || r.Method != http.MethodGet || r.URL.Query().Get("teamID") != "team" {
			t.Fatal("incorrect FC POP route")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"success":false,"message":"test-secret"}`)), Request: r}, nil
	})
	call := CloudCall{Service: "fcsandbox", Action: "ListVolumes", Query: map[string]any{"teamID": "team"}}
	if err := c.Call(context.Background(), call, nil); err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatal("accepted failure envelope", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Call(ctx, call, nil); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("cancelled request was sent", err)
	}
}
