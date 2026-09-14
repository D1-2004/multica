package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const validDSHTrajectory = `{"type":"session","version":0,"id":"ses_test-1","createdAt":1720000000000,"delegationDepth":0}
{"type":"turn/start","seq":0,"time":1720000000001,"data":{"turn":1}}
{"type":"user/message","seq":1,"time":1720000000002,"data":{"content":[{"type":"text","text":"hello"}],"source":{"kind":"user"}},"surfaceOp":"append"}
{"type":"turn/end","seq":2,"time":1720000000003,"data":{"turn":1,"reason":{"kind":"completed"}}}
`

func TestDSHUploadBindingRejectsOtherRequestsAndWholeManagedSessions(t *testing.T) {
	const scoped = `{"type":"multica/task-trajectory","version":1,"sessionId":"session","requestId":"mine","firstSeq":10,"lastSeq":12}
{"type":"session","version":3,"id":"session","createdAt":1,"isSeeded":false}
{"type":"turn/start","seq":10,"time":2,"data":{"turn":9}}
{"type":"user/message","seq":11,"time":3,"data":{"source":{"kind":"user","rpcId":"mine"}}}
{"type":"turn/end","seq":12,"time":4,"data":{"turn":9,"reason":{"kind":"completed"}}}
`
	for _, item := range []struct {
		session, request string
		bound, valid     bool
	}{
		{"session", "mine", true, true}, {"session", "other", true, false}, {"other", "mine", true, false}, {"session", "mine", false, false},
	} {
		if err := validateDSHUploadBinding([]byte(scoped), item.session, item.request, item.bound); (err == nil) != item.valid {
			t.Fatalf("binding accepted=%t expected=%t", err == nil, item.valid)
		}
	}
	if err := validateDSHUploadBinding([]byte(validDSHTrajectory), "session", "mine", true); err == nil {
		t.Fatal("managed task accepted whole Session export")
	}
	if err := validateDSHUploadBinding([]byte(validDSHTrajectory), "", "", false); err != nil {
		t.Fatal("legacy trajectory rejected", err)
	}
	if header, count, err := validateDSHTrajectory([]byte(scoped), "session"); err != nil || header.Version != 3 || count != 3 {
		t.Fatalf("scoped artifact rejected: %v", err)
	}
}

func TestValidateDSHTrajectoryAcceptsNativeJSONL(t *testing.T) {
	header, count, err := validateDSHTrajectory([]byte(validDSHTrajectory), "ses_test-1")
	if err != nil {
		t.Fatal(err)
	}
	if header.Type != "session" || header.Version != 0 || count != 3 {
		t.Fatalf("header=%+v event_count=%d", header, count)
	}
}

func TestDSHTrajectoryObjectEncryptionRoundTripAndRandomizesCiphertext(t *testing.T) {
	plain := []byte(validDSHTrajectory)
	first, firstKey, err := sealDSHTrajectory(plain)
	if err != nil {
		t.Fatal(err)
	}
	second, secondKey, err := sealDSHTrajectory(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, plain) || bytes.Equal(first, second) || bytes.Equal(firstKey, secondKey) {
		t.Fatal("trajectory encryption did not produce independent protected objects")
	}
	opened, err := openDSHTrajectory(first, firstKey, dshTrajectoryEncryptionScheme)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, plain) {
		t.Fatal("trajectory encryption round trip changed the native JSONL")
	}
	if _, err := openDSHTrajectory(first, secondKey, dshTrajectoryEncryptionScheme); err == nil {
		t.Fatal("trajectory ciphertext opened with the wrong data key")
	}
}

func TestUploadDSHTrajectoryRequiresMatchingTaskTokenBeforeStorageAccess(t *testing.T) {
	const taskID = "11111111-1111-1111-1111-111111111111"
	handler := &Handler{}

	request := withURLParam(httptest.NewRequest(http.MethodPut, "/api/tasks/"+taskID+"/dsh-trajectory", nil), "taskId", taskID)
	response := httptest.NewRecorder()
	handler.UploadDSHTrajectory(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("human upload status = %d, want 403", response.Code)
	}

	request = withURLParam(httptest.NewRequest(http.MethodPut, "/api/tasks/"+taskID+"/dsh-trajectory", nil), "taskId", taskID)
	request.Header.Set("X-Actor-Source", "task_token")
	request.Header.Set("X-Task-ID", "22222222-2222-2222-2222-222222222222")
	response = httptest.NewRecorder()
	handler.UploadDSHTrajectory(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-task upload status = %d, want 403", response.Code)
	}
}

func TestValidateDSHTrajectoryRejectsSessionMismatchAndSequenceGap(t *testing.T) {
	if _, _, err := validateDSHTrajectory([]byte(validDSHTrajectory), "ses_other"); err == nil || !strings.Contains(err.Error(), "session id") {
		t.Fatalf("session mismatch error = %v", err)
	}
	withGap := strings.Replace(validDSHTrajectory, `"seq":1`, `"seq":7`, 1)
	if _, _, err := validateDSHTrajectory([]byte(withGap), "ses_test-1"); err == nil || !strings.Contains(err.Error(), "sequence") {
		t.Fatalf("sequence gap error = %v", err)
	}
}

func TestValidateDSHTrajectoryRejectsMalformedTail(t *testing.T) {
	if _, _, err := validateDSHTrajectory([]byte(validDSHTrajectory+`{"type":`), "ses_test-1"); err == nil {
		t.Fatal("expected malformed tail to be rejected")
	}
}

func TestValidateDSHTrajectoryRequiresNativeHeaderFieldsAndObjectData(t *testing.T) {
	for _, field := range []string{`"version":0,`, `"createdAt":1720000000000,`, `"delegationDepth":0`} {
		withoutField := strings.Replace(validDSHTrajectory, field, "", 1)
		if _, _, err := validateDSHTrajectory([]byte(withoutField), "ses_test-1"); err == nil {
			t.Fatalf("expected missing %s to be rejected", field)
		}
	}
	withNullData := strings.Replace(validDSHTrajectory, `"data":{"turn":1}`, `"data":null`, 1)
	if _, _, err := validateDSHTrajectory([]byte(withNullData), "ses_test-1"); err == nil {
		t.Fatal("expected non-object event data to be rejected")
	}
	futureVersion := strings.Replace(validDSHTrajectory, `"version":0`, `"version":1`, 1)
	if _, _, err := validateDSHTrajectory([]byte(futureVersion), "ses_test-1"); err == nil {
		t.Fatal("expected an unsupported future format version to be rejected")
	}
	childSession := strings.Replace(validDSHTrajectory, `"delegationDepth":0`, `"parentSession":"ses_parent","origin":"subagent","delegationDepth":1`, 1)
	if _, _, err := validateDSHTrajectory([]byte(childSession), "ses_test-1"); err == nil {
		t.Fatal("expected a subagent trajectory to be rejected as the task root")
	}
	unsafeTimestamp := strings.Replace(validDSHTrajectory, `"time":1720000000001`, `"time":9007199254740992`, 1)
	if _, _, err := validateDSHTrajectory([]byte(unsafeTimestamp), "ses_test-1"); err == nil {
		t.Fatal("expected a timestamp outside JavaScript's exact integer range to be rejected")
	}
	withoutJSONLBoundary := strings.Replace(validDSHTrajectory, "}\n{\"type\":\"turn/start\"", `} {"type":"turn/start"`, 1)
	if _, _, err := validateDSHTrajectory([]byte(withoutJSONLBoundary), "ses_test-1"); err == nil {
		t.Fatal("expected multiple records on one physical line to be rejected")
	}
}

func TestValidateDSHTrajectoryNativeV3Root(t *testing.T) {
	ledger := strings.Replace(validDSHTrajectory, `"version":0`, `"version":3,"isSeeded":false`, 1)
	header, count, err := validateDSHTrajectory([]byte(ledger), "ses_test-1")
	if err != nil || header.Version != 3 || count != 3 {
		t.Fatalf("native v3 root rejected: header=%+v count=%d err=%v", header, count, err)
	}
	withoutDepth := strings.Replace(ledger, `,"delegationDepth":0`, "", 1)
	if header, _, err := validateDSHTrajectory([]byte(withoutDepth), "ses_test-1"); err != nil || header.DelegationDepth != 0 {
		t.Fatalf("official root without optional depth rejected: %v", err)
	}
	for name, invalid := range map[string]string{
		"null depth":           strings.Replace(ledger, `"delegationDepth":0`, `"delegationDepth":null`, 1),
		"missing seed flag":    strings.Replace(ledger, `"isSeeded":false,`, "", 1),
		"null seed flag":       strings.Replace(ledger, `"isSeeded":false`, `"isSeeded":null`, 1),
		"seeded root":          strings.Replace(ledger, `"isSeeded":false`, `"isSeeded":true`, 1),
		"future format":        strings.Replace(ledger, `"version":3`, `"version":4`, 1),
		"nonzero event origin": strings.Replace(ledger, `"seq":0`, `"seq":100`, 1),
		"child":                strings.Replace(ledger, `"delegationDepth":0`, `"delegationDepth":1,"parentSession":"ses_parent","origin":"subagent"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := validateDSHTrajectory([]byte(invalid), "ses_test-1"); err == nil {
				t.Fatal("unsupported or incomplete native ledger was accepted")
			}
		})
	}
}
