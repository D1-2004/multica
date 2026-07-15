package agentmessagerouter

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
)

func TestDispatchCredentialFixedFixture(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	keyring, err := ParseDispatchKeyring("v1:"+base64.RawStdEncoding.EncodeToString(key), "v1")
	if err != nil {
		t.Fatalf("ParseDispatchKeyring: %v", err)
	}
	endpointID, err := keyring.GenerateEndpointID(bytes.NewReader([]byte{
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
	}))
	if err != nil {
		t.Fatalf("GenerateEndpointID: %v", err)
	}
	if endpointID != "v1_AAECAwQFBgcICQoLDA0ODw" {
		t.Fatalf("endpointID = %q", endpointID)
	}

	secret, err := keyring.DeriveDeliverySecret(endpointID)
	if err != nil {
		t.Fatalf("DeriveDeliverySecret: %v", err)
	}
	if secret != "D2MoCmEssqskHVWrSIoWWphTrnP6O8mgvrGvvdEthZ8" {
		t.Fatalf("secret = %q", secret)
	}
	if !keyring.VerifyDeliverySecret(endpointID, secret) {
		t.Fatal("fixed fixture secret did not verify")
	}
	if keyring.VerifyDeliverySecret(endpointID, secret+"x") {
		t.Fatal("modified secret unexpectedly verified")
	}
	crossProjectSecret, err := keyring.DeriveDeliverySecret("v1_mqN7AZx5h96UXK2L_5X9_w")
	if err != nil {
		t.Fatalf("derive Router fixture: %v", err)
	}
	if crossProjectSecret != "pEeIt_zWCxX49ucBJrRyF3aWxApicrHnC2WULexyMW8" {
		t.Fatalf("Router fixture secret = %q", crossProjectSecret)
	}
}

func TestDispatchCredentialFailsClosedForInvalidConfigAndEndpoint(t *testing.T) {
	validKey := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	tests := []struct {
		name      string
		config    string
		currentID string
	}{
		{name: "empty config", currentID: "v1"},
		{name: "missing current key", config: "v1:" + validKey, currentID: "v2"},
		{name: "short key", config: "v1:" + base64.RawStdEncoding.EncodeToString(make([]byte, 31)), currentID: "v1"},
		{name: "duplicate key", config: "v1:" + validKey + ",v1:" + validKey, currentID: "v1"},
		{name: "router-incompatible dotted key id", config: "v1.1:" + validKey, currentID: "v1.1"},
		{name: "router-incompatible base64url key", config: "v1:" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, 32)), currentID: "v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseDispatchKeyring(tt.config, tt.currentID); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}

	keyring, err := ParseDispatchKeyring("v1:"+validKey, "v1")
	if err != nil {
		t.Fatalf("ParseDispatchKeyring: %v", err)
	}
	for _, endpointID := range []string{
		"v2_AAECAwQFBgcICQoLDA0ODw",
		"v1_not-base64!",
		"v1_AAECAw",
		"v1_",
		"missing-version-separator",
	} {
		if _, err := keyring.DeriveDeliverySecret(endpointID); err == nil {
			t.Fatalf("expected endpoint %q to fail closed", endpointID)
		}
		if keyring.VerifyDeliverySecret(endpointID, "anything") {
			t.Fatalf("endpoint %q unexpectedly verified", endpointID)
		}
	}
}

func TestDispatchCredentialMetricsDoNotExposeUnknownKeyIDs(t *testing.T) {
	validKey := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	keyring, err := ParseDispatchKeyring("v1:"+validKey, "v1")
	if err != nil {
		t.Fatalf("ParseDispatchKeyring: %v", err)
	}
	businessMetrics := obsmetrics.NewBusinessMetrics()
	keyring.SetMetrics(businessMetrics)

	if _, err := keyring.DeriveDeliverySecret("v1_AAECAwQFBgcICQoLDA0ODw"); err != nil {
		t.Fatalf("derive known key: %v", err)
	}
	if _, err := keyring.DeriveDeliverySecret("attacker_AAECAwQFBgcICQoLDA0ODw"); err == nil {
		t.Fatal("expected unknown key to fail")
	}
	if _, err := keyring.DeriveDeliverySecret("not-an-endpoint"); err == nil {
		t.Fatal("expected malformed endpoint to fail")
	}

	family := obsmetrics.GatherForTest(t, businessMetrics)["dispatch_credential_derive_total"]
	if family == nil {
		t.Fatal("dispatch credential metric family is missing")
	}
	labels := make(map[string]int)
	for _, metric := range family.GetMetric() {
		keyID := ""
		for _, label := range metric.GetLabel() {
			if label.GetName() == "key_id" {
				keyID = label.GetValue()
			}
		}
		labels[keyID]++
	}
	if labels["v1"] != 1 || labels["unknown"] != 1 || labels["invalid"] != 1 {
		t.Fatalf("key_id labels = %#v", labels)
	}
	if labels["attacker"] != 0 {
		t.Fatalf("attacker-controlled key_id was exposed: %#v", labels)
	}
}

func TestBuildDispatchURLCanonicalizesHTTPSOrigin(t *testing.T) {
	endpointID := "v1_AAECAwQFBgcICQoLDA0ODw"
	got, err := BuildDispatchURL("https://MULTICA.Example.COM:443/", endpointID)
	if err != nil {
		t.Fatalf("BuildDispatchURL: %v", err)
	}
	want := "https://multica.example.com/api/webhooks/agent-dispatch/" + endpointID
	if got != want {
		t.Fatalf("dispatch url = %q, want %q", got, want)
	}
}

func TestBuildDispatchURLRejectsNonOriginOrInsecureValues(t *testing.T) {
	endpointID := "v1_AAECAwQFBgcICQoLDA0ODw"
	for _, publicOrigin := range []string{
		"http://multica.example.com",
		"https://user@multica.example.com",
		"https://multica.example.com/base",
		"https://multica.example.com?query=value",
		"https://multica.example.com#fragment",
		"https://multica.example.com/api/%2e%2e",
		"https://multica.example.com:8443",
		"multica.example.com",
		"",
	} {
		t.Run(strings.ReplaceAll(publicOrigin, "/", "_"), func(t *testing.T) {
			if _, err := BuildDispatchURL(publicOrigin, endpointID); err == nil {
				t.Fatalf("expected public origin %q to fail", publicOrigin)
			}
		})
	}
	if _, err := BuildDispatchURL("https://multica.example.com", "not-an-endpoint"); err == nil {
		t.Fatal("expected malformed endpoint id to fail")
	}
}
