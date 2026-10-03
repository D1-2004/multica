package employeeentry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHostTimeRendersAsiaShanghaiWithExplicitOffset(t *testing.T) {
	at := time.Date(2026, 10, 3, 11, 34, 5, 0, time.UTC)
	wire, err := json.Marshal(struct {
		At time.Time `json:"at"`
	}{HostTime(at)})
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) != `{"at":"2026-10-03T19:34:05+08:00"}` {
		t.Fatalf("host time json = %s", wire)
	}
	if got := HostClock(at); got != "10-03 19:34" {
		t.Fatalf("clock = %q", got)
	}
	if got := HostStamp(at); got != "2026-10-03 19:34 +08:00" {
		t.Fatalf("stamp = %q", got)
	}
	if !HostTime(at).Equal(at) {
		t.Fatal("conversion changed the instant")
	}
	if !HostTime(time.Time{}).IsZero() || HostClock(time.Time{}) != "" || HostStamp(time.Time{}) != "" {
		t.Fatal("zero time must stay empty")
	}
}

// R1003 (C1-WD): history observed_at was UTC ("...T11:27:04Z"); the employee
// added the run time and told the requester "about 11:34" for 19:34 local.
func TestRecentConversationTimesCarryHostOffset(t *testing.T) {
	f, before := recentHistoryDatabase(t)
	scope, principal := f.admission.Scope, f.admission.Item.PrincipalID
	_, job := recentHistoryInput(t, f, scope, principal, "请在执行环境里跑 sleep 420", "tz-request", before.Add(-7*time.Minute))
	recentHistoryReply(t, f, scope, job, "delivered", "已派发，跑完告诉你", "tz-reply", "cid-test", before.Add(-6*time.Minute))
	got, err := f.store.RecentConversation(context.Background(), RecentConversationRequest{Scope: scope, PrincipalID: principal, Before: before})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("history = %+v", got)
	}
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Since    string `json:"since"`
		Before   string `json:"before"`
		Messages []struct {
			ObservedAt string `json:"observed_at"`
		} `json:"messages"`
	}
	if err = json.Unmarshal(wire, &raw); err != nil {
		t.Fatal(err)
	}
	stamps := []string{raw.Since, raw.Before}
	for _, message := range raw.Messages {
		stamps = append(stamps, message.ObservedAt)
	}
	for _, stamp := range stamps {
		if !strings.HasSuffix(stamp, "+08:00") {
			t.Fatalf("host timestamp without +08:00 offset: %q in %s", stamp, wire)
		}
	}
	want := before.Add(-7 * time.Minute).In(HostZone).Format("2006-01-02T15:04")
	if !strings.HasPrefix(raw.Messages[0].ObservedAt, want) {
		t.Fatalf("observed_at %q is not the local clock %q", raw.Messages[0].ObservedAt, want)
	}
	if !got.Before.Equal(before) || !got.Messages[0].At.Equal(before.Add(-7*time.Minute)) {
		t.Fatal("localization changed an instant")
	}
}
