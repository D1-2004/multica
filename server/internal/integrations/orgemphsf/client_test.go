package orgemphsf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	dapr "github.com/dapr/go-sdk/client"
)

type fakeBindingInvoker struct {
	request *dapr.InvokeBindingRequest
	event   *dapr.BindingEvent
	err     error
	closed  bool
}

func (f *fakeBindingInvoker) InvokeBinding(_ context.Context, request *dapr.InvokeBindingRequest) (*dapr.BindingEvent, error) {
	f.request = request
	return f.event, f.err
}

func (f *fakeBindingInvoker) Close() {
	f.closed = true
}

func newTestClient(invoker *fakeBindingInvoker) *Client {
	return &Client{
		address:     "127.0.0.1:50001",
		bindingName: defaultBindingName,
		appName:     "dt-fde-multica",
		timeout:     defaultTimeout,
		dial: func(context.Context, string) (bindingInvoker, error) {
			return invoker, nil
		},
	}
}

func TestDecodeCorpIDMatchesJavaCorpIDUtils(t *testing.T) {
	orgID, err := DecodeCorpID("ding8196cd9a2b2405da24f2f5cc6abecb85")
	if err != nil {
		t.Fatalf("DecodeCorpID: %v", err)
	}
	if orgID != "439446171" {
		t.Fatalf("orgID = %q, want 439446171", orgID)
	}
}

func TestDecodeCorpIDRejectsMalformedValues(t *testing.T) {
	for _, corpID := range []string{
		"",
		"8196cd9a2b2405da24f2f5cc6abecb85",
		"dingnot-hex",
		"ding8196cd9a2b2405da",
	} {
		if _, err := DecodeCorpID(corpID); err == nil {
			t.Fatalf("DecodeCorpID(%q) unexpectedly succeeded", corpID)
		}
	}
}

func TestResolveEmployeeByCorpIDDecodesLocallyThenInvokesOrgEmpService(t *testing.T) {
	invoker := &fakeBindingInvoker{event: &dapr.BindingEvent{Data: []byte(`{
		"success":true,
		"result":{"uid":24710833,"orgId":439446171,"staffId":"Staff-A_106201"}
	}`)}}
	employee, err := newTestClient(invoker).ResolveEmployeeByCorpID(
		context.Background(),
		"ding8196cd9a2b2405da24f2f5cc6abecb85",
		"Staff-A_106201",
	)
	if err != nil {
		t.Fatalf("ResolveEmployeeByCorpID: %v", err)
	}
	if employee.UID != "24710833" || employee.OrgID != "439446171" || employee.StaffID != "Staff-A_106201" {
		t.Fatalf("unexpected employee: %#v", employee)
	}
	if got := invoker.request.Metadata["rpc-interface-name"]; got != serviceInterface {
		t.Fatalf("rpc-interface-name = %q, want %q", got, serviceInterface)
	}
}

func TestGetEmployeeByStaffIDInvokesOrgEmpService(t *testing.T) {
	invoker := &fakeBindingInvoker{event: &dapr.BindingEvent{Data: []byte(`{
		"success":true,
		"result":{"uid":24710833,"orgId":439446171,"staffId":"Staff-A_106201"}
	}`)}}
	client := newTestClient(invoker)

	employee, err := client.GetEmployeeByStaffID(context.Background(), "439446171", "Staff-A_106201")
	if err != nil {
		t.Fatalf("GetEmployeeByStaffID: %v", err)
	}
	if employee.UID != "24710833" || employee.OrgID != "439446171" || employee.StaffID != "Staff-A_106201" {
		t.Fatalf("unexpected employee: %#v", employee)
	}
	if !invoker.closed || invoker.request == nil {
		t.Fatal("Dapr binding was not invoked and closed")
	}
	wantMetadata := map[string]string{
		"rpc-interface-name": serviceInterface,
		"rpc-version":        serviceVersion,
		"rpc-group":          serviceGroup,
		"rpc-method-name":    methodName,
		// Keep this literal independent from parameterTypes: Dapr HSF splits
		// multiple generic parameter types on commas. A semicolon makes the
		// consumer see one type for the two request arguments.
		"rpc-method-parameter-types": "java.lang.Long,java.lang.String",
		"serialization-type":         "application/json",
		"rpc-generic":                "true",
		"rpc-timeout":                "10000",
		"appName":                    "dt-fde-multica",
	}
	for key, want := range wantMetadata {
		if got := invoker.request.Metadata[key]; got != want {
			t.Errorf("metadata[%q] = %q, want %q", key, got, want)
		}
	}
	var args []any
	decoder := json.NewDecoder(strings.NewReader(string(invoker.request.Data)))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(args) != 2 || args[0].(json.Number).String() != "439446171" || args[1] != "Staff-A_106201" {
		t.Fatalf("unexpected request arguments: %#v", args)
	}
}

func TestGetEmployeeByStaffIDAcceptsStringEncodedLongs(t *testing.T) {
	invoker := &fakeBindingInvoker{event: &dapr.BindingEvent{Data: []byte(`{
		"success":true,
		"result":{"uid":"24710833","orgId":"439446171","staffId":"106201"}
	}`)}}
	employee, err := newTestClient(invoker).GetEmployeeByStaffID(context.Background(), "439446171", "106201")
	if err != nil || employee.UID != "24710833" {
		t.Fatalf("employee=%#v err=%v", employee, err)
	}
}

func TestGetEmployeeByStaffIDRejectsInvalidInputBeforeDial(t *testing.T) {
	for _, tc := range []struct {
		name    string
		orgID   string
		staffID string
		field   string
	}{
		{name: "invalid org", orgID: "not-an-org", staffID: "Staff-A_106201", field: "org_id"},
		{name: "empty staff", orgID: "439446171", staffID: "", field: "staff_id"},
		{name: "blank staff", orgID: "439446171", staffID: "   ", field: "staff_id"},
		{name: "oversized staff", orgID: "439446171", staffID: strings.Repeat("a", maxStaffIDBytes+1), field: "staff_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialed := false
			client := &Client{dial: func(context.Context, string) (bindingInvoker, error) {
				dialed = true
				return nil, errors.New("unexpected dial")
			}}
			_, err := client.GetEmployeeByStaffID(context.Background(), tc.orgID, tc.staffID)
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) || validationErr.Field != tc.field {
				t.Fatalf("error = %#v, want field %q", err, tc.field)
			}
			if dialed {
				t.Fatal("invalid input reached Dapr")
			}
		})
	}
}

func TestResolveEmployeeByCorpIDRejectsMismatchedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		responseID  string
		responseOrg string
	}{
		{name: "different staff", responseID: "Staff-A_106202", responseOrg: "439446171"},
		{name: "staff case differs", responseID: "staff-A_106201", responseOrg: "439446171"},
		{name: "different org", responseID: "Staff-A_106201", responseOrg: "439446172"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invoker := &fakeBindingInvoker{event: &dapr.BindingEvent{Data: []byte(fmt.Sprintf(`{
				"success":true,
				"result":{"uid":24710833,"orgId":%s,"staffId":%q}
			}`, tc.responseOrg, tc.responseID))}}
			_, err := newTestClient(invoker).ResolveEmployeeByCorpID(
				context.Background(),
				"ding8196cd9a2b2405da24f2f5cc6abecb85",
				"Staff-A_106201",
			)
			if err == nil || !strings.Contains(err.Error(), "mismatched identity") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestGetEmployeeByStaffIDReturnsSafeServiceError(t *testing.T) {
	invoker := &fakeBindingInvoker{event: &dapr.BindingEvent{Data: []byte(`{
		"success":false,
		"errorCode":"ACCESS_DENIED",
		"errorMessage":"do-not-expose"
	}`)}}
	_, err := newTestClient(invoker).GetEmployeeByStaffID(context.Background(), "439446171", "106201")
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Code != "ACCESS_DENIED" {
		t.Fatalf("error = %#v", err)
	}
	if strings.Contains(err.Error(), "do-not-expose") {
		t.Fatalf("service detail leaked: %v", err)
	}
}

func TestGetEmployeeByStaffIDRejectsIncompleteResponse(t *testing.T) {
	for _, staffID := range []string{"", "   ", strings.Repeat("a", maxStaffIDBytes+1)} {
		invoker := &fakeBindingInvoker{event: &dapr.BindingEvent{Data: []byte(fmt.Sprintf(`{
			"success":true,
			"result":{"uid":24710833,"orgId":439446171,"staffId":%q}
		}`, staffID))}}
		_, err := newTestClient(invoker).GetEmployeeByStaffID(context.Background(), "439446171", "Staff-A_106201")
		if err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("staffID length=%d error=%v", len(staffID), err)
		}
	}
}
