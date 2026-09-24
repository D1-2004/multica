package dshhost

import "testing"

func TestACSErrorCodeKeepsOnlyTheMachineCode(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"ram rpc error", `{"RequestId":"r","HostId":"ram.aliyuncs.com","Code":"NoPermission","Message":"You are not authorized to do this action. Resource: acs:ram:*:1:role/x"}`, "NoPermission"},
		{"dotted code", `{"Code":"EntityNotExist.Role","Message":"The role not exists: x."}`, "EntityNotExist.Role"},
		{"lower-case envelope", `{"success":false,"code":"InvalidParameter","message":"volume name"}`, "InvalidParameter"},
		{"numeric code", `{"code":400,"message":"bad request"}`, ""},
		{"code with spaces", `{"Code":"not a code"}`, ""},
		{"not json", `<html>503</html>`, ""},
		{"empty", ``, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := acsErrorCode([]byte(tc.body)); got != tc.want {
				t.Fatalf("acsErrorCode(%s) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}
