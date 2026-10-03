package employeeverification

import (
	"reflect"
	"strings"
	"testing"
)

func evidenceWith(name string, data []byte) runEvidence {
	a := Artifact{AttachmentID: "att-1", TaskID: "task", RunID: "run", QueueTaskID: "queue", GoalRevision: 1, Filename: name, SHA256: sha256Hex(data), State: "ready"}
	return runEvidence{RunID: "run", TaskID: "task", QueueTaskID: "queue", GoalRevision: 1, Artifacts: []Artifact{a}, Bytes: map[string][]byte{"att-1": data}}
}

func mustCheck(t *testing.T, c Check) Check {
	t.Helper()
	out, _, err := NormalizeChecks([]Check{c}, "")
	if err != nil {
		t.Fatal(err)
	}
	return out[0]
}

func TestArtifactContentsCheckerIsDeterministicOverBytes(t *testing.T) {
	table := []byte("\xef\xbb\xbf区域,金额\r\n\"华东, 上海\",12\r\n华北,8\r\n\r\n")
	cases := []struct {
		check   Check
		data    []byte
		outcome Outcome
		detail  string
	}{
		{Check{Kind: KindArtifactContents, File: "s.csv", DataRows: intPtr(2), Columns: []string{"金额", "区域"}}, table, OutcomePassed, "2 data rows"},
		{Check{Kind: KindArtifactContents, File: "s.csv", DataRows: intPtr(3)}, table, OutcomeFailed, "has 2 data rows, expected 3"},
		{Check{Kind: KindArtifactContents, File: "s.csv", Columns: []string{"利润"}}, table, OutcomeFailed, "lacks columns"},
		{Check{Kind: KindArtifactContents, File: "s.tsv", DataRows: intPtr(1)}, []byte("a\tb\n1\t2\n"), OutcomePassed, "1 data rows"},
		{Check{Kind: KindArtifactContents, File: "n.md", Contains: []string{"合计", "部门"}}, []byte("部门合计"), OutcomePassed, "contains"},
		{Check{Kind: KindArtifactContents, File: "n.md", Contains: []string{"合计"}}, []byte("小计"), OutcomeFailed, "does not contain"},
		{Check{Kind: KindArtifactContents, File: "n.md"}, []byte{}, OutcomeFailed, "is empty"},
		{Check{Kind: KindArtifactContents, File: "n.md", SHA256: sha256Hex([]byte("x"))}, []byte("y"), OutcomeFailed, "sha256 is"},
		{Check{Kind: KindExecutionOutput, File: "sum.txt", Expect: "5050"}, []byte(" 5050.0\n"), OutcomePassed, "= 5050"},
		{Check{Kind: KindExecutionOutput, File: "sum.txt", Expect: "5050"}, []byte("5049"), OutcomeFailed, "is 5049, expected 5050"},
		{Check{Kind: KindExecutionOutput, File: "r.json", Field: "total", Expect: "5050"}, []byte(`{"total": 5050, "note": "PASS"}`), OutcomePassed, "field total"},
		{Check{Kind: KindExecutionOutput, File: "r.json", Field: "status", Expect: "ok"}, []byte(`{"status":"failed"}`), OutcomeFailed, "is failed"},
	}
	for _, tc := range cases {
		c := mustCheck(t, tc.check)
		obs := evaluate(c, evidenceWith(c.File, tc.data))
		if len(obs) != 1 || obs[0].Outcome != tc.outcome || !strings.Contains(obs[0].Detail, tc.detail) {
			t.Errorf("%+v on %q: %+v", tc.check, tc.data, obs)
		}
		again := evaluate(c, evidenceWith(c.File, tc.data))
		if !reflect.DeepEqual(again, obs) {
			t.Errorf("non-deterministic: %+v vs %+v", obs[0], again[0])
		}
	}
}

func TestCheckerFailsClosedOnIntegrityAndUnsettledEvidence(t *testing.T) {
	c := mustCheck(t, Check{Kind: KindArtifactContents, File: "n.md"})
	e := evidenceWith("n.md", []byte("x"))
	e.Bytes["att-1"] = []byte("tampered")
	if obs := evaluate(c, e); obs[0].Outcome != OutcomeFailed || !strings.Contains(obs[0].Detail, "do not match the recorded sha256") {
		t.Fatalf("tampered bytes %+v", obs)
	}
	e = evidenceWith("n.md", []byte("x"))
	e.Artifacts[0].State = "pending"
	if obs := evaluate(c, e); obs[0].Outcome != OutcomeUnknown {
		t.Fatalf("pending upload %+v", obs)
	}
	command := mustCheck(t, Check{Kind: KindExecutionOutput, Command: "go test ./..."})
	if obs := evaluate(command, runEvidence{RunID: "run"}); obs[0].Outcome != OutcomeUnknown || !strings.Contains(obs[0].Detail, "not proof") {
		t.Fatalf("command without Host executor %+v", obs)
	}
	if obs := evaluate(Check{ID: "chk_x", Kind: "url"}, runEvidence{RunID: "run"}); obs[0].Outcome != OutcomeFailed {
		t.Fatalf("unknown kind %+v", obs)
	}
}

func TestDeliveryReceiptStates(t *testing.T) {
	c := mustCheck(t, Check{Kind: KindDeliveryReceipt, Delivery: "origin_reply"})
	for state, want := range map[string]Outcome{"delivered": OutcomePassed, "failed": OutcomeFailed, "cancelled": OutcomeFailed, "provider_accepted": OutcomeUnknown, "pending": OutcomeUnknown, "silent": OutcomeUnknown} {
		obs := evaluate(c, runEvidence{RunID: "run", Deliveries: []Delivery{{ActionID: "a1", State: state, MessageID: "m1"}}})
		if obs[0].Outcome != want {
			t.Errorf("%s -> %s", state, obs[0].Outcome)
		}
	}
	if obs := evaluate(c, runEvidence{RunID: "run", Deliveries: []Delivery{{ActionID: "a1", State: "delivered"}}}); obs[0].Outcome != OutcomeUnknown {
		t.Fatalf("delivered without a provider message id %+v", obs)
	}
}

func TestGateFoldsRequiredChecksOnly(t *testing.T) {
	content := mustCheck(t, Check{Kind: KindArtifactContents, File: "n.md"})
	delivery := mustCheck(t, Check{Kind: KindDeliveryReceipt, Delivery: "origin_reply"})
	optional := mustCheck(t, Check{Kind: KindArtifactContents, File: "extra.md", Optional: true})
	optional.Optional = true
	spec := Spec{State: SpecActive, Revision: 1, Checks: []Check{content, delivery, optional}}
	rec := func(c Check, o Outcome) Record { return Record{CheckID: c.ID, CheckKind: c.Kind, Outcome: o} }
	if g := gateFor(spec, []Record{rec(content, OutcomePassed), rec(delivery, OutcomeFailed), rec(delivery, OutcomePassed), rec(optional, OutcomeFailed)}); g.Status != GatePassed || !g.Correct {
		t.Fatalf("delivery retry or optional failure blocked: %+v", g)
	}
	if g := gateFor(spec, []Record{rec(content, OutcomePassed), rec(content, OutcomeFailed), rec(delivery, OutcomePassed)}); g.Status != GateFailed || g.Correct {
		t.Fatalf("one failing copy must fail content: %+v", g)
	}
	if g := gateFor(spec, []Record{rec(content, OutcomePassed)}); g.Status != GatePending || len(g.Pending) != 1 {
		t.Fatalf("missing delivery: %+v", g)
	}
	if g := gateFor(Spec{State: SpecProposed, Checks: spec.Checks}, nil); g.Status != GateNone {
		t.Fatalf("proposed spec gated: %+v", g)
	}
}

func TestAbsenceVerdictOnlyYieldsToRealEvidence(t *testing.T) {
	absent := Record{EvidenceRef: "run-artifacts:r", Outcome: OutcomeFailed}
	pass := Record{EvidenceRef: "dws-message-file:m/f", Outcome: OutcomePassed}
	wrong := Record{EvidenceRef: "artifact:a", Outcome: OutcomeFailed}
	for _, tc := range []struct {
		records []Record
		want    Outcome
	}{
		{[]Record{absent}, OutcomeFailed},
		{[]Record{absent, pass}, OutcomePassed},
		{[]Record{absent, pass, wrong}, OutcomeFailed},
		{[]Record{{EvidenceRef: "run-artifacts:r/delivered", Outcome: OutcomeUnknown}, absent}, OutcomeFailed},
	} {
		if got := checkStatus(KindArtifactContents, tc.records); got != tc.want {
			t.Errorf("%+v -> %s, want %s", tc.records, got, tc.want)
		}
	}
}
