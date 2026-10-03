package handler

import "testing"

func TestEmployeeModelRefsHideHostUUIDs(t *testing.T) {
	id := "6d1b7912-3a90-4d34-acea-8e27405aab35"
	for _, tc := range []struct{ in, want string }{
		{"plan:" + id, "plan"},
		{"run:" + id, "run"},
		{"employee_task_link:" + id + "/blocked_by/" + id, "employee_task_link"},
		{id, "ref"},
		{"", ""},
		{"governor", "governor"},
	} {
		if got := employeeModelRefKind(tc.in); got != tc.want {
			t.Errorf("kind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := employeeModelScrubIDs("member:" + id); got != "member:id" {
		t.Errorf("scrub = %q", got)
	}
	if got := employeeModelScrubIDs(id + "/msgZq+u7w=="); got != "id/msgZq+u7w==" {
		t.Errorf("scrub source ref = %q", got)
	}
	if got := employeeModelScrubIDs("dingtalk:44675729:uid:507523443"); got != "dingtalk:44675729:uid:507523443" {
		t.Errorf("scrub changed a non-UUID ref: %q", got)
	}
}
