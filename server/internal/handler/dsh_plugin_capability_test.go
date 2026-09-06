package handler

import (
	"strconv"
	"testing"
	"time"
)

// The signature in the query IS the credential for this route, so every way of
// getting it wrong has to fail closed. A gap here would expose a workspace's
// plugin packages to anyone who can guess a UUID.
func TestVerifyDshPluginCapabilityFailsClosed(t *testing.T) {
	const pluginID = "11111111-1111-4111-8111-111111111111"
	now := time.Unix(1_700_000_000, 0)
	exp := now.Add(time.Hour).Unix()
	sig := signDshPluginCapability(pluginID, exp)
	valid := verifyDshPluginCapability

	if !valid(pluginID, strconv.FormatInt(exp, 10), sig, now) {
		t.Fatal("a freshly minted capability must verify")
	}

	cases := []struct {
		name   string
		id     string
		rawExp string
		rawSig string
		at     time.Time
	}{
		{"empty id", "", strconv.FormatInt(exp, 10), sig, now},
		{"empty expiry", pluginID, "", sig, now},
		{"empty signature", pluginID, strconv.FormatInt(exp, 10), "", now},
		{"unparseable expiry", pluginID, "not-a-number", sig, now},
		{"elapsed expiry", pluginID, strconv.FormatInt(exp, 10), sig, time.Unix(exp+1, 0)},
		{"malformed signature", pluginID, strconv.FormatInt(exp, 10), "zzzz", now},
		// Minted for a different plugin: the id is inside the signed message,
		// so a link for one package cannot be replayed against another.
		{"signature for another plugin", "22222222-2222-4222-8222-222222222222", strconv.FormatInt(exp, 10), sig, now},
		// Extending the expiry must invalidate rather than extend, because the
		// claimed expiry is part of the signed message.
		{"extended expiry", pluginID, strconv.FormatInt(exp+3600, 10), sig, now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if valid(tc.id, tc.rawExp, tc.rawSig, tc.at) {
				t.Error("a capability that should be refused verified")
			}
		})
	}
}

func TestSignDshPluginCapabilityIsUnambiguous(t *testing.T) {
	// The fields are joined with a separator that cannot occur in a UUID or a
	// decimal timestamp, so no two different (id, exp) pairs can produce the
	// same signed message.
	a := signDshPluginCapability("aaaa|1", 2)
	b := signDshPluginCapability("aaaa", 12)
	if a == b {
		t.Fatal("two different capabilities produced the same signature")
	}
}
