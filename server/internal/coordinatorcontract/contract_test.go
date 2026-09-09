package coordinatorcontract

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestContractBoundToExactInstructions(t *testing.T) {
	authored, err := Parse([]byte(`{"version":1,"scope":"产品问答","must_delegate":["查证"],"constraints":["只起草"],"clarify_when":[],"source_instructions_sha256":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}`))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := Bind(authored, "完整岗位\n只起草")
	if err != nil {
		t.Fatal(err)
	}
	if authored.SourceInstructionsSHA256 != strings.Repeat("f", 64) || bound.SourceInstructionsSHA256 == strings.Repeat("f", 64) {
		t.Fatal("Bind must copy and replace the untrusted hash")
	}
	if _, state := Resolve(Marshal(bound), "完整岗位\n只起草"); state != StateLoaded {
		t.Fatalf("state=%s", state)
	}
	if _, state := Resolve(Marshal(bound), "完整岗位\n允许发送"); state != StateStale {
		t.Fatalf("changed instructions state=%s", state)
	}
	again, _ := Parse(Marshal(bound))
	if Hash(again) == "" || Hash(again) != Hash(bound) {
		t.Fatal("canonical hash changed on roundtrip")
	}
	bound.Constraints[0] = "changed"
	if authored.Constraints[0] != "只起草" {
		t.Fatal("Bind aliased authored slices")
	}
}

func TestContractRejectsInvalidAndOversizeObjects(t *testing.T) {
	for _, raw := range []string{
		`[]`, `{"version":2,"scope":"x"}`, `{"version":1,"scope":"x","actions":["reply"]}`,
		`{"version":1,"scope":" "}`, `{"version":1,"scope":"x","constraints":[""]}`,
		`{"version":1,"scope":"x"} {"version":1}`, `{"version":1,"scope":5}`,
		`{"version":1,"scope":"` + strings.Repeat("长", MaxCharacters) + `"}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("accepted invalid contract %.90s", raw)
		}
		if _, state := Resolve([]byte(raw), ""); state != StateUnavailable {
			t.Errorf("invalid contract state=%s", state)
		}
	}
	for _, raw := range [][]byte{nil, []byte("null"), []byte(" \n ")} {
		if contract, state := Resolve(raw, ""); contract != nil || state != StateNotConfigured {
			t.Fatalf("absent contract=(%v,%s)", contract, state)
		}
	}
}

func TestBoundContractTotalBudgetIncludesJSONAndHostHash(t *testing.T) {
	base := &Contract{Version: Version, Scope: "中"}
	bound, err := Bind(base, "job")
	if err != nil {
		t.Fatal(err)
	}
	budget := MaxCharacters - utf8.RuneCount(Marshal(bound))
	base.Scope += strings.Repeat("中", budget)
	bound, err = Bind(base, "job")
	if err != nil || utf8.RuneCount(Marshal(bound)) != MaxCharacters {
		t.Fatalf("exact budget rejected: %v", err)
	}
	base.Scope += "中"
	if _, err := Bind(base, "job"); err == nil {
		t.Fatal("bound contract exceeded total budget")
	}
	if bytes.Equal(Marshal(bound), nil) {
		t.Fatal("missing serialized contract")
	}
}
