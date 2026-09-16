package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestASBIdentityErrorPreservesDeepAttachCause(t *testing.T) {
	for _, test := range []struct{ upstream, code string }{
		{"BIZ_SERVICE_CONTAINER_TRUST_DEVICE_REGISTER_USAGE_EXCEEDS_LIMIT", "ASB-BUC-TRUST-DEVICE-LIMIT"},
		{"zt token not found", "ASB-BUC-ZT-TOKEN-NOT-FOUND"},
	} {
		t.Run(test.code, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"code": "INTERNAL_ERROR", "message": strings.Repeat("tunnel not ready; ", 100) + test.upstream + ` bucRefreshToken="private-refresh"`})
			err := newASBHTTPError("attach_buc_identity", &http.Response{StatusCode: 400, Header: http.Header{"X-Request-Id": {"request-test"}}}, body)
			if isASBWireGuardPostAttachCheckPending(err) {
				t.Fatal("terminal cause treated as pending")
			}
			failure := ClassifyRuntimeStartError(SandboxBackendASB, newASBIdentityStartError(err, nil))
			if failure.Code != test.code || failure.Phase != "sandbox_ready" || failure.Retryable {
				t.Fatalf("failure = %+v", failure)
			}
			for _, want := range []string{test.upstream, "request-test"} {
				if !strings.Contains(failure.PublicMessage, want) {
					t.Fatalf("missing %q: %+v", want, failure)
				}
			}
			if strings.Contains(failure.PublicMessage+failure.InternalDetail, "private-refresh") {
				t.Fatal("credential leaked")
			}
		})
	}
}

func TestASBIdentityDiagnosticsRecoversLifecycleCause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sandboxes/identity-failed/diagnostics/logs" || r.URL.Query().Get("scope") != "lifecycle" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sandboxId": "identity-failed", "kind": "logs", "scope": "lifecycle", "delivery": "inline", "content": "wgclient: BIZ_SERVICE_CONTAINER_TRUST_DEVICE_REGISTER_USAGE_EXCEEDS_LIMIT token=private-refresh"})
	}))
	defer server.Close()
	l := &ASBLauncher{Client: newTestASBClient(t, server)}
	cause := &ASBHTTPError{Operation: "attach_buc_identity", StatusCode: 500, RequestID: "request-test", ErrorMessage: "wireguard tunnel not ready yet"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := l.asbIdentityStartError(ctx, "identity-failed", cause)
	if !errors.Is(err, cause) {
		t.Fatal("original cause lost")
	}
	failure := ClassifyRuntimeStartError(SandboxBackendASB, err)
	if failure.Code != "ASB-BUC-TRUST-DEVICE-LIMIT" || !strings.Contains(failure.PublicMessage, "request-test") || strings.Contains(failure.PublicMessage, "private-refresh") {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestASBIdentityDiagnosticsFailureKeepsOriginalCause(t *testing.T) {
	for _, mode := range []string{"unsupported", "url", "wrong-sandbox", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "timeout" {
					<-r.Context().Done()
					return
				}
				if mode == "unsupported" {
					w.WriteHeader(501)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"sandboxId": "another-sandbox", "kind": "logs", "scope": "lifecycle", "delivery": mode, "content": "zt token not found", "contentUrl": "https://invalid.example/secret"})
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			reason := newTestASBClient(t, server).asbIdentityDiagnosticReason(ctx, "identity-failed")
			if reason != nil {
				t.Fatalf("untrusted/unavailable diagnostics used: %+v", reason)
			}
			cause := &ASBHTTPError{Operation: "attach_buc_identity", StatusCode: 500, ErrorMessage: "wireguard tunnel not ready yet"}
			failure := ClassifyRuntimeStartError(SandboxBackendASB, newASBIdentityStartError(cause, reason))
			if !strings.Contains(failure.PublicMessage, "wireguard tunnel not ready yet") {
				t.Fatalf("lost original cause: %+v", failure)
			}
		})
	}
}

func TestASBTaskProbeErrorPreservesSafeResponseFields(t *testing.T) {
	result := &ASBExecResult{Stdout: asbBUCProbeErrorPrefix + `{"code":"401","message":"zt token not found","accessToken":"private-access"}`}
	err := asbTaskBUCProbeError(result)
	failure := ClassifyRuntimeStartError(SandboxBackendASB, newASBIdentityStartError(err, nil))
	if failure.Code != "ASB-BUC-ZT-TOKEN-NOT-FOUND" || !strings.Contains(failure.PublicMessage, "zt token not found") || strings.Contains(failure.PublicMessage, "private-access") {
		t.Fatalf("failure = %+v", failure)
	}
	for _, message := range []string{`authorization: Bearer secret-auth`, `bucRefreshToken="secret-refresh"`, `"accessToken":"secret-access"`, `\"accessToken\":\"secret-access\"`, `wgclientCredentials={"key":"secret-key"}`} {
		t.Run(message, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"code": "400", "message": "upstream rejected " + message})
			err := asbTaskBUCProbeError(&ASBExecResult{Stdout: asbBUCProbeErrorPrefix + string(body)})
			if strings.Contains(runtimeStartUserDetailFromError(err), "secret-") {
				t.Fatalf("secret leaked: %s", runtimeStartUserDetailFromError(err))
			}
		})
	}
}

func TestASBIdentityErrorReauthPreserved(t *testing.T) {
	err := newASBIdentityStartError(fmt.Errorf("wrong employee: %w", ErrEnterpriseIdentityNeedsReauth), nil)
	if !errors.Is(err, ErrEnterpriseIdentityNeedsReauth) {
		t.Fatal("reauth sentinel lost")
	}
	failure := ClassifyRuntimeStartError(SandboxBackendASB, err)
	if failure.Code != "ASB-BUC-IDENTITY-MISMATCH" {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestASBTaskProbeCommandEmitsFailureWithoutIdentityPayload(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required to verify the sandbox probe")
	}
	command := asbTaskBUCIdentityProbeCommand()
	_, script, ok := strings.Cut(command, "/opt/task-python/bin/python -c '")
	if !ok {
		t.Fatal("probe Python command missing")
	}
	script = strings.TrimSuffix(script, "'")
	for _, test := range []struct {
		name, body string
		exit       int
		want       string
	}{
		{"missing_zt", `{"success":false,"errorCode":"400","errorMsg":"zt token not found","content":{"accessToken":"private-access"}}`, 1, "zt token not found"},
		{"valid", `{"success":true,"errorCode":0,"content":{"data":{"empId":"12345","agentId":"agent-1","token":"private-access"}}}`, 0, ""},
		{"wrong_employee", `{"success":true,"errorCode":0,"content":{"data":{"empId":"54321","agentId":"agent-1"}}}`, 42, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.Command(python, "-c", script)
			cmd.Env = []string{"EXPECTED_EMP_ID=12345"}
			cmd.Stdin = strings.NewReader(test.body)
			output, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatal(err)
				}
				code = exitErr.ExitCode()
			}
			if code != test.exit {
				t.Fatalf("exit=%d output=%s", code, output)
			}
			if test.want == "" && len(output) != 0 {
				t.Fatalf("identity payload leaked: %s", output)
			}
			if test.want != "" && (!strings.Contains(string(output), test.want) || strings.Contains(string(output), "private-access")) {
				t.Fatalf("output=%s", output)
			}
		})
	}
}

func TestASBTaskProbeMissingZTCanConverge(t *testing.T) {
	var server *httptest.Server
	calls := 0
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/endpoints/44772") {
			_ = json.NewEncoder(w).Encode(map[string]any{"endpoint": server.URL + "/execd", "headers": map[string]string{"X-Sandbox-Token": testEndpointToken}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		calls++
		if calls == 1 {
			event, _ := json.Marshal(map[string]string{"type": "stdout", "text": asbBUCProbeErrorPrefix + `{"code":"400","message":"zt token not found"}` + "\n"})
			_, _ = fmt.Fprintf(w, "data: %s\ndata: {\"type\":\"error\",\"error\":{\"ename\":\"ExitCode\",\"evalue\":\"1\"}}\n", event)
		}
		_, _ = fmt.Fprintln(w, `data: {"type":"execution_complete","execution_time":1}`)
	}))
	defer server.Close()
	l := &ASBLauncher{Client: newTestASBClient(t, server)}
	if err := l.waitSandboxTaskBUCIdentityReady(context.Background(), "probe-sandbox", "12345", time.Second); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}
