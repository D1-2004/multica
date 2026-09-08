package inboundcoord

import "testing"

func TestCurrentAdvancesIssue(t *testing.T) {
	t.Parallel()
	if !CurrentAdvancesIssue("就约线上，token=E1247-loop-W2B", "约 dxxh 开会 token=E1247-loop-W2", "") {
		t.Fatal("W2B must continue the W2 meeting issue")
	}
	if CurrentAdvancesIssue("帮我订下周去上海的高铁，token=E1247-loop-W5", "订高铁 token=E0003-loop-W5", "") {
		t.Fatal("this-round 订票 must not comment onto last-round 高铁")
	}
	if CurrentAdvancesIssue("帮我订下周去上海的高铁，token=E1247-loop-W5", "问 dxxh 排期 token=E1247-loop-W3A", "") {
		t.Fatal("W5 must not comment onto W3A")
	}
	if !CurrentAdvancesIssue("帮我订下周去上海的高铁，token=E1247-loop-W5", "订高铁 token=E1247-loop-W5", "") {
		t.Fatal("same-token follow-up may continue")
	}
	if !CurrentAdvancesIssue("就约线上", "约 dxxh 明天开会", "") {
		t.Fatal("short confirmation without a token still continues")
	}
}
