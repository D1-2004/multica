package featureflags

import (
	"context"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"testing"
)

func TestSemanticaEnvironmentDefault(t *testing.T) {
	for _, key := range []string{"AONE_ENV_TYPE", "ENV_TYPE", "GO_ENV", "APP_ENV"} {
		t.Setenv(key, "")
	}
	for _, v := range []struct {
		env  string
		want bool
	}{{"pre", true}, {"prepub", true}, {"staging", true}, {"production", false}, {"", false}, {"garbage", false}} {
		t.Setenv("AONE_ENV_TYPE", v.env)
		if got := SemanticaMCPRelayEnabled(context.Background(), nil); got != v.want {
			t.Errorf("%q: %v", v.env, got)
		}
	}
	t.Setenv("AONE_ENV_TYPE", "prod")
	t.Setenv("APP_ENV", "pre")
	if SemanticaMCPRelayEnabled(context.Background(), nil) {
		t.Fatal("lower priority environment overrode prod")
	}
}

type semanticaFlagProvider struct{ enabled bool }

func (p *semanticaFlagProvider) Name() string { return "test" }
func (p *semanticaFlagProvider) Lookup(context.Context, string) (featureflag.Decision, bool) {
	return featureflag.Decision{Enabled: p.enabled}, true
}
func TestSemanticaLiveDisable(t *testing.T) {
	t.Setenv("AONE_ENV_TYPE", "pre")
	p := &semanticaFlagProvider{true}
	s := featureflag.NewService(p)
	if !SemanticaMCPRelayEnabled(context.Background(), s) {
		t.Fatal("not enabled")
	}
	p.enabled = false
	if SemanticaMCPRelayEnabled(context.Background(), s) {
		t.Fatal("disable not applied")
	}
}
