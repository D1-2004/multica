package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/connectorconfig"
)

func TestConnectorAppViewOmitsSecrets(t *testing.T) {
	const secret = "super-secret-client-value"
	view := appView(connectorconfig.App{
		ID: "app", Provider: "github", ClientID: "Iv1.public", SecretHint: "••••alue",
		SecretCiphertext: []byte(secret), CallbackMode: connectorconfig.CallbackProductionForward,
		Instances: []connectorconfig.InstanceRecord{{
			Instance:        connectorconfig.Instance{ID: "inst", Label: "work", Status: connectorconfig.StatusActive, Enabled: true},
			TokenCiphertext: []byte(secret), TokenHint: "••••ken1",
		}},
	}, true)
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "ciphertext") {
		t.Fatalf("response leaked secret material: %s", raw)
	}
	if !strings.Contains(string(raw), `"client_secret_set":true`) || !strings.Contains(string(raw), `"token_set":true`) {
		t.Fatalf("missing configured flags: %s", raw)
	}
}

func TestGitHubOAuthRedirectOrigin(t *testing.T) {
	home := "https://pre.example.test"
	prod := "https://example.test"
	if got := githubOAuthRedirectOrigin(home, prod, connectorconfig.CallbackSelf); got != home {
		t.Fatalf("self = %s", got)
	}
	if got := githubOAuthRedirectOrigin(home, prod, connectorconfig.CallbackProductionForward); got != prod {
		t.Fatalf("forward = %s", got)
	}
	if got := githubOAuthRedirectOrigin(home, prod, ""); got != prod {
		t.Fatalf("default = %s", got)
	}
}
