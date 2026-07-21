package dingtalk

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestHistoricalStreamCredentialsDoNotRequireRobotCode(t *testing.T) {
	raw, err := json.Marshal(dingtalkInstallConfig{
		AppID:              "legacy-stream-app",
		AppSecretEncrypted: base64.StdEncoding.EncodeToString([]byte("legacy-secret")),
	})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := decodeChannelCredentials(raw, plaintextDecrypter)
	if err != nil {
		t.Fatalf("decode historical Stream credentials: %v", err)
	}
	if creds.ClientID != "legacy-stream-app" || creds.ClientSecret != "legacy-secret" || creds.RobotCode != "" {
		t.Fatalf("historical Stream credentials changed: %#v", creds)
	}
}

func TestHistoricalInstallationWithoutTransportRemainsManagedStream(t *testing.T) {
	inst, err := installationFromRow(db.ChannelInstallation{
		Status: "active",
		Config: []byte(`{"app_id":"legacy-stream-app"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Status != "active" || inst.TransportMode != TransportModeStream || !inst.ConnectionManaged {
		t.Fatalf("historical installation changed semantics: %#v", inst)
	}
}

func TestTransportControlsOnlyConnectionOwnership(t *testing.T) {
	for _, test := range []struct {
		mode    TransportMode
		managed bool
	}{
		{mode: TransportModeStream, managed: true},
		{mode: TransportModeHTTPCallback, managed: false},
	} {
		config, err := encodeInstallConfig(Installation{
			ClientID: "app-1", RobotCode: "robot-1", TransportMode: test.mode,
		})
		if err != nil {
			t.Fatal(err)
		}
		var decoded dingtalkInstallConfig
		if err := json.Unmarshal(config, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.ConnectionManaged == nil || *decoded.ConnectionManaged != test.managed {
			t.Fatalf("mode %s connection_managed = %v, want %v", test.mode, decoded.ConnectionManaged, test.managed)
		}
	}
}
