package runtimeconfig

import (
	"strings"
	"testing"
)

func withDWSForTag(value string) string {
	return strings.Replace(validJSON(), `"runtime": {`, `"runtime": {"use_dws_for_tag":`+value+`,`, 1)
}

func TestParseStrictReadsUseDWSForTag(t *testing.T) {
	if mustParseConfig(t, validJSON()).Runtime.UseDWSForTag {
		t.Fatal("absent key selected the SDK")
	}
	if !mustParseConfig(t, withDWSForTag(`true`)).Runtime.UseDWSForTag {
		t.Fatal("true did not select the SDK")
	}
	if _, err := ParseStrict([]byte(withDWSForTag(`"yes"`)), true); err == nil {
		t.Fatal("non-boolean accepted")
	}
}

// The switch follows publications live and survives a rejected one.
func TestDiamondServiceFollowsUseDWSForTag(t *testing.T) {
	client := &fakeDiamondClient{content: withDWSForTag(`true`)}
	service, err := newDiamondService(nil, true, func() (diamondClient, error) { return client, nil })
	if err != nil {
		t.Fatalf("newDiamondService: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	if !service.UseDWSForTag() {
		t.Fatal("loaded document did not select the SDK")
	}
	client.onChange(withDWSForTag(`1`))
	if !service.UseDWSForTag() {
		t.Fatal("rejected publication dropped the switch")
	}
	client.onChange(validJSON())
	if service.UseDWSForTag() {
		t.Fatal("removing the key kept the SDK")
	}
	var missing *Service
	if missing.UseDWSForTag() {
		t.Fatal("nil service selected the SDK")
	}
}
