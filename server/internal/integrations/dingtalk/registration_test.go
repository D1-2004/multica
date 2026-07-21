package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeRegistrationServer is an httptest stand-in for oapi.dingtalk.com's
// /app/registration/* endpoints. Each field, when set, overrides the
// default happy-path response for that phase.
type fakeRegistrationServer struct {
	t *testing.T

	initStatus  int
	initBody    any
	beginStatus int
	beginBody   any
	pollStatus  int
	pollBody    any

	// captured requests for assertions
	initReqs  []map[string]any
	beginReqs []map[string]any
	pollReqs  []map[string]any
}

func (f *fakeRegistrationServer) handler() http.Handler {
	mux := http.NewServeMux()
	serve := func(w http.ResponseWriter, status int, body any) {
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}
	decode := func(r *http.Request) map[string]any {
		var m map[string]any
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			f.t.Fatalf("decode request: %v", err)
		}
		return m
	}
	mux.HandleFunc(registrationInitPath, func(w http.ResponseWriter, r *http.Request) {
		f.initReqs = append(f.initReqs, decode(r))
		body := f.initBody
		if body == nil {
			body = map[string]any{"errcode": 0, "errmsg": "ok", "nonce": "nr_test", "expires_in": 300}
		}
		serve(w, f.initStatus, body)
	})
	mux.HandleFunc(registrationBeginPath, func(w http.ResponseWriter, r *http.Request) {
		f.beginReqs = append(f.beginReqs, decode(r))
		body := f.beginBody
		if body == nil {
			body = map[string]any{
				"errcode": 0, "errmsg": "ok",
				"device_code":               "dc_test",
				"user_code":                 "MUEU-DEKR-5TN3",
				"verification_uri":          "https://open-dev.dingtalk.com/fe/app-registration",
				"verification_uri_complete": "https://open-dev.dingtalk.com/fe/app-registration?user_code=MUEU-DEKR-5TN3",
				"expires_in":                7200,
				"interval":                  5,
			}
		}
		serve(w, f.beginStatus, body)
	})
	mux.HandleFunc(registrationPollPath, func(w http.ResponseWriter, r *http.Request) {
		f.pollReqs = append(f.pollReqs, decode(r))
		body := f.pollBody
		if body == nil {
			body = map[string]any{"errcode": 0, "errmsg": "ok", "status": "WAITING"}
		}
		serve(w, f.pollStatus, body)
	})
	return mux
}

func newFakeRegistration(t *testing.T) (*fakeRegistrationServer, *RegistrationClient) {
	t.Helper()
	f := &fakeRegistrationServer{t: t}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	client := NewRegistrationClient(RegistrationConfig{BaseURL: srv.URL})
	return f, client
}

func TestRegistrationBeginHappyPath(t *testing.T) {
	f, client := newFakeRegistration(t)
	res, err := client.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if res.DeviceCode != "dc_test" {
		t.Errorf("DeviceCode = %q, want dc_test", res.DeviceCode)
	}
	if res.QRCodeURL != "https://open-dev.dingtalk.com/fe/app-registration?user_code=MUEU-DEKR-5TN3" {
		t.Errorf("QRCodeURL = %q", res.QRCodeURL)
	}
	if res.UserCode != "MUEU-DEKR-5TN3" {
		t.Errorf("UserCode = %q", res.UserCode)
	}
	if res.Interval != 5*time.Second {
		t.Errorf("Interval = %v, want 5s", res.Interval)
	}
	// The advertised 7200s window must be capped — an abandoned dialog
	// must not pin a polling goroutine for two hours.
	if res.ExpiresIn != registrationMaxPollWindow {
		t.Errorf("ExpiresIn = %v, want capped %v", res.ExpiresIn, registrationMaxPollWindow)
	}
	// Every Multica scan uses the DingTalk-allocated FDE product source.
	if len(f.initReqs) != 1 || f.initReqs[0]["source"] != "FDE_AGENT" {
		t.Errorf("init requests = %v", f.initReqs)
	}
	if len(f.beginReqs) != 1 || f.beginReqs[0]["nonce"] != "nr_test" {
		t.Errorf("begin requests = %v", f.beginReqs)
	}
	if _, ok := f.beginReqs[0]["mode"]; ok {
		t.Errorf("stream begin unexpectedly carried mode: %v", f.beginReqs[0])
	}
	if _, ok := f.beginReqs[0]["outgoing_url"]; ok {
		t.Errorf("stream begin unexpectedly carried outgoing_url: %v", f.beginReqs[0])
	}
}

func TestRegistrationBeginHTTPCallbackUsesFixedOutgoingURL(t *testing.T) {
	f := &fakeRegistrationServer{t: t}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	client := NewRegistrationClient(RegistrationConfig{
		BaseURL:     srv.URL,
		OutgoingURL: "https://gateway.example.test/robot/callback",
	})

	if _, err := client.BeginWithTransport(context.Background(), TransportModeHTTPCallback); err != nil {
		t.Fatalf("BeginWithTransport: %v", err)
	}
	if got := f.beginReqs[0]["mode"]; got != "HTTPS" {
		t.Errorf("mode = %v, want HTTPS", got)
	}
	if got := f.beginReqs[0]["outgoing_url"]; got != "https://gateway.example.test/robot/callback" {
		t.Errorf("outgoing_url = %v", got)
	}
}

func TestRegistrationBeginHTTPCallbackRejectsUnavailableURL(t *testing.T) {
	for _, raw := range []string{"", "http://gateway.example.test/callback", "://bad"} {
		t.Run(raw, func(t *testing.T) {
			client := NewRegistrationClient(RegistrationConfig{OutgoingURL: raw})
			_, err := client.BeginWithTransport(context.Background(), TransportModeHTTPCallback)
			var re *RegistrationError
			if !errors.As(err, &re) || re.Code != "http_callback_unavailable" {
				t.Fatalf("err = %v, want http_callback_unavailable", err)
			}
		})
	}
}

func TestTransportModeFromPollKeepsClientAndRobotIdentityIndependent(t *testing.T) {
	httpMode, err := transportModeFromPoll("HTTPS")
	if err != nil || httpMode != TransportModeHTTPCallback {
		t.Fatalf("HTTPS mode = %q, err=%v", httpMode, err)
	}
	streamMode, err := transportModeFromPoll("")
	if err != nil || streamMode != TransportModeStream {
		t.Fatalf("empty mode = %q, err=%v", streamMode, err)
	}
}

func TestRegistrationBeginAlwaysUsesFDEAgentSource(t *testing.T) {
	f := &fakeRegistrationServer{t: t}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	client := NewRegistrationClient(RegistrationConfig{BaseURL: srv.URL})
	if _, err := client.Begin(context.Background()); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if got := f.initReqs[0]["source"]; got != "FDE_AGENT" {
		t.Errorf("source = %q, want FDE_AGENT", got)
	}
}

func TestRegistrationBeginShortExpiryIsHonored(t *testing.T) {
	f, client := newFakeRegistration(t)
	f.beginBody = map[string]any{
		"errcode": 0, "errmsg": "ok",
		"device_code":               "dc",
		"verification_uri_complete": "https://x.example/qr",
		"expires_in":                120,
	}
	res, err := client.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if res.ExpiresIn != 2*time.Minute {
		t.Errorf("ExpiresIn = %v, want 2m", res.ExpiresIn)
	}
	if res.Interval != time.Duration(registrationDefaultPollSeconds)*time.Second {
		t.Errorf("Interval = %v, want default", res.Interval)
	}
}

func TestRegistrationBeginErrcodeIsTerminal(t *testing.T) {
	f, client := newFakeRegistration(t)
	f.initBody = map[string]any{"errcode": 88001, "errmsg": "internal error"}
	_, err := client.Begin(context.Background())
	var re *RegistrationError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RegistrationError", err)
	}
	if re.Code != "errcode_88001" || re.Description != "internal error" {
		t.Errorf("err = %+v", re)
	}
}

func TestRegistrationBeginMissingFields(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{"empty nonce", nil}, // handled below by overriding init
		{"missing device_code", map[string]any{
			"errcode": 0, "errmsg": "ok",
			"verification_uri_complete": "https://x.example/qr",
		}},
		{"missing verification_uri_complete", map[string]any{
			"errcode": 0, "errmsg": "ok",
			"device_code": "dc",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, client := newFakeRegistration(t)
			if tc.body == nil {
				f.initBody = map[string]any{"errcode": 0, "errmsg": "ok", "nonce": ""}
			} else {
				f.beginBody = tc.body
			}
			_, err := client.Begin(context.Background())
			var re *RegistrationError
			if !errors.As(err, &re) || re.Code != "invalid_response" {
				t.Fatalf("err = %v, want invalid_response", err)
			}
		})
	}
}

func TestRegistrationPollOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		body   map[string]any
		check  func(t *testing.T, res *RegistrationPollResult)
		errFmt string // expected RegistrationError code from Poll's error return; empty = no error
	}{
		{
			name: "waiting",
			body: map[string]any{"errcode": 0, "errmsg": "ok", "status": "WAITING"},
			check: func(t *testing.T, res *RegistrationPollResult) {
				if !res.Pending {
					t.Errorf("Pending = false, want true")
				}
			},
		},
		{
			name: "documented progress statuses stay pending",
			body: map[string]any{"errcode": 0, "errmsg": "ok", "status": "SELECTING"},
			check: func(t *testing.T, res *RegistrationPollResult) {
				if !res.Pending {
					t.Errorf("Pending = false, want true")
				}
			},
		},
		{
			name: "empty status tolerated as pending",
			body: map[string]any{"errcode": 0, "errmsg": "ok"},
			check: func(t *testing.T, res *RegistrationPollResult) {
				if !res.Pending {
					t.Errorf("Pending = false, want true")
				}
			},
		},
		{
			name: "success",
			body: map[string]any{
				"errcode": 0, "errmsg": "ok", "status": "SUCCESS",
				"client_id": "dingabc", "client_secret": "s3cret", "robot_code": "robotabc",
			},
			check: func(t *testing.T, res *RegistrationPollResult) {
				if res.ClientID != "dingabc" || res.ClientSecret != "s3cret" || res.RobotCode != "robotabc" {
					t.Errorf("res = %+v", res)
				}
			},
		},
		{
			name: "approving returns one-time credentials and review marker",
			body: map[string]any{
				"errcode": 0, "errmsg": "ok", "status": "APPROVING",
				"client_id": "dingreview", "client_secret": "review-secret",
				"robot_code": "robot-review", "mode": "HTTPS",
			},
			check: func(t *testing.T, res *RegistrationPollResult) {
				if !res.ApprovalPending || res.Pending {
					t.Errorf("result = %+v, want terminal approval pending", res)
				}
				if res.ClientID != "dingreview" || res.ClientSecret != "review-secret" || res.RobotCode != "robot-review" {
					t.Errorf("res = %+v", res)
				}
			},
		},
		{
			name: "fail carries fail_reason",
			body: map[string]any{
				"errcode": 0, "errmsg": "ok", "status": "FAIL",
				"fail_reason": "用户拒绝授权",
			},
			check: func(t *testing.T, res *RegistrationPollResult) {
				if res.Err == nil || res.Err.Code != "fail" || res.Err.Description != "用户拒绝授权" {
					t.Errorf("Err = %+v", res.Err)
				}
			},
		},
		{
			name: "expired",
			body: map[string]any{"errcode": 0, "errmsg": "ok", "status": "EXPIRED"},
			check: func(t *testing.T, res *RegistrationPollResult) {
				if res.Err == nil || res.Err.Code != "expired" {
					t.Errorf("Err = %+v", res.Err)
				}
			},
		},
		{
			name:   "success without credentials is a protocol error",
			body:   map[string]any{"errcode": 0, "errmsg": "ok", "status": "SUCCESS"},
			errFmt: "invalid_response",
		},
		{
			name: "success without robot code is a protocol error",
			body: map[string]any{
				"errcode": 0, "errmsg": "ok", "status": "SUCCESS",
				"client_id": "dingabc", "client_secret": "s3cret",
			},
			errFmt: "invalid_response",
		},
		{
			name: "approving without credentials is a protocol error",
			body: map[string]any{
				"errcode": 0, "errmsg": "ok", "status": "APPROVING",
				"robot_code": "robot-review",
			},
			errFmt: "invalid_response",
		},
		{
			name:   "unknown status is a protocol error",
			body:   map[string]any{"errcode": 0, "errmsg": "ok", "status": "SOMETHING_NEW"},
			errFmt: "invalid_response",
		},
		{
			name:   "errcode envelope is terminal",
			body:   map[string]any{"errcode": 500, "errmsg": "server exploded"},
			errFmt: "errcode_500",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, client := newFakeRegistration(t)
			f.pollBody = tc.body
			res, err := client.Poll(context.Background(), "dc_test")
			if tc.errFmt != "" {
				var re *RegistrationError
				if !errors.As(err, &re) || re.Code != tc.errFmt {
					t.Fatalf("err = %v, want code %s", err, tc.errFmt)
				}
				return
			}
			if err != nil {
				t.Fatalf("Poll: %v", err)
			}
			tc.check(t, res)
			if f.pollReqs[0]["device_code"] != "dc_test" {
				t.Errorf("poll request = %v", f.pollReqs[0])
			}
		})
	}

	for _, status := range []string{"SCANNED", "CREATING", "PUBLISHING"} {
		t.Run(strings.ToLower(status)+" stays pending", func(t *testing.T) {
			f, client := newFakeRegistration(t)
			f.pollBody = map[string]any{"errcode": 0, "errmsg": "ok", "status": status}
			res, err := client.Poll(context.Background(), "dc_test")
			if err != nil {
				t.Fatalf("Poll: %v", err)
			}
			if !res.Pending {
				t.Errorf("Pending = false for %s", status)
			}
		})
	}
}

func TestRegistrationPollRequiresDeviceCode(t *testing.T) {
	_, client := newFakeRegistration(t)
	_, err := client.Poll(context.Background(), "")
	var re *RegistrationError
	if !errors.As(err, &re) || re.Code != "invalid_argument" {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

func TestRegistrationUnparseableBodySurfacesHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>proxy error</html>"))
	}))
	t.Cleanup(srv.Close)
	client := NewRegistrationClient(RegistrationConfig{BaseURL: srv.URL})
	_, err := client.Poll(context.Background(), "dc")
	var re *RegistrationError
	if !errors.As(err, &re) || re.Code != "http_502" {
		t.Fatalf("err = %v, want http_502", err)
	}
}
